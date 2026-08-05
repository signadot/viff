package trigger

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/signadot/tony-format/go-tony/ir"
	"github.com/signadot/verse/entity"
)

// Deref: reading a value OUT of the payload that fired an action, by kpath.
//
// An action could always NAME a value ($stage, $result) — but only inside a spawn's string
// fields, and only as text, because launch.Binding is map[string]string built with
// fmt.Sprint over the payload's top-level fields. Two consequences followed. A nested value
// was unreachable at all ($verse.review.done had no spelling), and a task's output could
// parameterize the next PROMPT but could never become a fact the next `when` matches on:
// the loop closed through state transitions the charter wrote by hand, never through what a
// task actually found.
//
// Expanding a `patch` closes it. An action can lift a value out of the payload that fired it
// and write it onto the subject, TYPED, where the match grammar can see it. That is the same
// vocabulary pairing the rest of the model uses — a Match reads, a Patch writes — with the
// reference as the third member, and it is why this is a deref rather than a template
// language: it names a value, it does not compute one.
//
// The reference grammar is tony's, not verse's. "$result.findings[0].severity" resolves by
// handing "result.findings[0].severity" to ir.Node.GetKPath, the same kpath the engine
// already uses to address an action inside its trigger (review.seq[2].onFailed). verse's
// long-standing "$name" spelling IS that kpath at depth one, so this does not add a grammar;
// it stops truncating the one already there at the first segment.

// refSource says where an action's $references read their fields from. There are exactly two
// answers, and which one applies is a property of WHY the action is running.
type refSource int

const (
	// fromSubject — a trigger firing. The change that dispatched it is a doorbell and may
	// name an entirely different entity (a CI status, a note, a task), whose fields have
	// no meaning on the subject being acted on. So references read the SUBJECT's own payload.
	//
	// This is the half of "sever the action from the cause" that a charter can see: a trigger
	// used to bind $stage from whatever changed, which read correctly only while the subject
	// and the cause happened to be the same entity.
	fromSubject refSource = iota
	// fromCause — a completion hook, or a sequence element one drives. The action is
	// downstream of a TASK, and $result / $err / $data are exactly the facts it exists to
	// read. The engine synthesizes that payload (Engine.taskEvent) rather than reading the
	// store, because the mirror may not have written the terminal facts yet — so this is not
	// merely a different entity to read, it is a payload only the engine has.
	fromCause
)

// refRoot is what a reference resolves against: the fields of the payload named by src, plus
// $subject.
//
// It is built by node rather than through entity.ToObj because a round-trip through Go
// values does not preserve tony's numbers — an int 3 comes back a float64, and the store's
// predicates are type-exact, so `patch: {n: $count}` would write a 3.0 that `value: {n: 3}`
// then fails to match. Deep-copying the fields keeps both the type and the original
// document's parent linkage intact.
//
// $subject is set LAST, the same rule bindingFor states: it is the one reserved reference,
// and a payload field that happens to be called "subject" must not shadow it.
func refRoot(payload entity.Payload, subject *ir.Node) entity.Payload {
	fields := map[string]*ir.Node{}
	for k, v := range entity.Fields(payload) { // nil for a scalar payload — no named parts
		fields[k] = v.DeepCopy()
	}
	fields[refSubject] = subject
	return ir.FromMap(fields)
}

// The reserved reference and its parts. $subject is a RECORD, and the parts fall in two
// groups that the rest of the model treats differently:
//
//	ref system kind id   IDENTITY — which entity this firing is about
//	value commit         the FIRING — what was true, and when
//
// The bare `$subject` resolves to `ref` (see deref), so every charter written before the record
// existed means exactly what it meant: the address, as a string.
const (
	refSubject     = "subject"
	subjPartRef    = "ref"
	subjPartSystem = "system"
	subjPartKind   = "kind"
	subjPartID     = "id"
	// value is the subject's payload AT THE COMMIT THAT CAUSED THE FIRING, not at the moment
	// the action runs. Those differ by however long a gate took to answer, and reading late
	// would let an approval that took a day act on a state nobody evaluated — the model's rule
	// is that a firing is settled at one snapshot, and this is that rule reaching references.
	subjPartValue = "value"
	// commit is the revision of that same causing commit.
	subjPartCommit = "commit"
)

// subjectNode assembles the record $subject resolves to. value and commit are omitted when the
// firing did not ask for them, which is what keeps a charter that never mentions them from
// paying for a snapshot read.
func subjectNode(subj entity.Ref, value entity.Payload, commit int64) *ir.Node {
	fields := map[string]*ir.Node{
		subjPartRef:    ir.FromString(subj.String()),
		subjPartSystem: ir.FromString(subj.System),
		subjPartKind:   ir.FromString(subj.Kind),
		subjPartID:     ir.FromString(subj.ID),
	}
	if value != nil {
		fields[subjPartValue] = value.DeepCopy()
	}
	if commit > 0 {
		fields[subjPartCommit] = ir.FromInt(commit)
	}
	return ir.FromMap(fields)
}

// subjectWants is what a firing needs about its subject beyond identity. Identity is free —
// three segments already in hand — while `value` may cost a read at a revision, which the store
// answers by replaying its log to that commit (entity/logdstore.go). So it is asked for rather
// than always supplied.
type subjectWants struct{ value, commit bool }

// wantsOf reports which costly parts a document references.
func wantsOf(p entity.Payload) subjectWants {
	var w subjectWants
	eachRef(p, func(ref string) {
		switch {
		case ref == refSubject+"."+subjPartCommit:
			w.commit = true
		case subjectPart(ref) == subjPartValue:
			w.value = true
		}
	})
	return w
}

// subjectPart reports which part of $subject a reference names, or "" when it is not a subject
// reference at all. `subject.value.stage` and `subject.value[0]` both name `value`: what follows
// is a path INTO the part, not another part.
func subjectPart(ref string) string {
	rest, ok := strings.CutPrefix(ref, refSubject+".")
	if !ok {
		return ""
	}
	if i := strings.IndexAny(rest, ".["); i >= 0 {
		return rest[:i]
	}
	return rest
}

// eachRef calls fn for every $reference in a document, using the same token rule the expander
// does (refAt) — so what is counted here is exactly what would be expanded, and a literal
// dollar ("$5") is neither.
func eachRef(n entity.Payload, fn func(string)) {
	if n == nil {
		return
	}
	switch n.Type {
	case ir.ObjectType, ir.ArrayType:
		for _, v := range n.Values {
			eachRef(v, fn)
		}
	case ir.StringType:
		s := n.String
		for i := 0; i < len(s); i++ {
			if s[i] != '$' {
				continue
			}
			if l := refAt(s[i:]); l > 0 {
				fn(s[i+1 : i+l])
				i += l - 1
			}
		}
	}
}

// refAt reports the length of the reference token at the start of s, or 0 when what follows
// '$' is not one. A charter may hold a literal dollar ("$5", "100$", a lone "$"), and the
// patch it sits in was never expanded before this change — so anything that is not a
// well-formed reference is left exactly as written rather than becoming an error.
func refAt(s string) int {
	if len(s) < 2 || s[0] != '$' || !identStart(s[1]) {
		return 0
	}
	i := 1
	for i < len(s) && identPart(s[i]) {
		i++
	}
	for i < len(s) {
		switch {
		case s[i] == '.' && i+1 < len(s) && identStart(s[i+1]):
			i += 2
			for i < len(s) && identPart(s[i]) {
				i++
			}
		case s[i] == '[':
			j := i + 1
			for j < len(s) && s[j] >= '0' && s[j] <= '9' {
				j++
			}
			if j == i+1 || j >= len(s) || s[j] != ']' {
				return i // "[" that is not an index ends the reference
			}
			i = j + 1
		default:
			return i
		}
	}
	return i
}

func identStart(c byte) bool {
	return c == '_' || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z')
}

func identPart(c byte) bool { return identStart(c) || (c >= '0' && c <= '9') }

// deref resolves one reference (without its '$') against the root.
//
// Absent and null are DIFFERENT answers, deliberately: GetKPath returns a nil node for a
// path that is not there and a null node for a field written null, and the store records
// that difference too (a match spells the three cases apart — see docs/trigger-model.md).
// So an absent reference is an error and a null one resolves to null.
func deref(root entity.Payload, ref string) (entity.Payload, error) {
	// The bare `$subject` IS the address: it resolved to the ref string long before $subject
	// was a record, and every charter written since means that. Mapping it onto `.ref` here
	// keeps one spelling for one thing rather than making the old form a special case of the
	// record that authors have to know about.
	if ref == refSubject {
		ref = refSubject + "." + subjPartRef
	}
	n, err := root.GetKPath(ref)
	if err != nil {
		return nil, fmt.Errorf("$%s: %w", ref, err)
	}
	if n == nil {
		return nil, fmt.Errorf("$%s is not present in the payload that fired this action", ref)
	}
	return n, nil
}

// expandString expands the references in one patch VALUE.
//
// Two forms, and the difference is the whole point of doing this at the node level: a value
// that is exactly one reference resolves to that reference's NODE, so a number stays a
// number and an object stays an object; a value with a reference embedded in text is
// interpolated, and is a string because that is what it was written as.
func expandString(s string, root entity.Payload) (entity.Payload, error) {
	if !strings.Contains(s, "$") {
		return ir.FromString(s), nil
	}
	if n := refAt(s); n == len(s) {
		v, err := deref(root, s[1:])
		if err != nil {
			return nil, err
		}
		return v.DeepCopy(), nil
	}
	var b strings.Builder
	for i := 0; i < len(s); {
		if s[i] != '$' {
			b.WriteByte(s[i])
			i++
			continue
		}
		n := refAt(s[i:])
		if n == 0 {
			b.WriteByte(s[i])
			i++
			continue
		}
		ref := s[i+1 : i+n]
		v, err := deref(root, ref)
		if err != nil {
			return nil, err
		}
		txt, ok := scalarText(v)
		if !ok {
			return nil, fmt.Errorf("$%s is %v, which has no text form: reference it on its own "+
				"to write the whole value, rather than inside a string", ref, v.Type)
		}
		b.WriteString(txt)
		i += n
	}
	return ir.FromString(b.String()), nil
}

// scalarText renders a leaf for interpolation. A composite has no text form here on purpose:
// inventing one (JSON? tony?) would make `"saw $result"` mean something different depending
// on what the task happened to return, and the whole-value form already says it exactly.
func scalarText(n entity.Payload) (string, bool) {
	switch n.Type {
	case ir.StringType:
		return n.String, true
	case ir.BoolType:
		return strconv.FormatBool(n.Bool), true
	case ir.NumberType:
		switch {
		case n.Int64 != nil:
			return strconv.FormatInt(*n.Int64, 10), true
		case n.Float64 != nil:
			return strconv.FormatFloat(*n.Float64, 'g', -1, 64), true
		}
		return n.Number, n.Number != ""
	case ir.NullType:
		return "", true
	}
	return "", false
}

// expandPatch returns the patch with every reference in a string value resolved against the
// root, or an error naming the first reference that does not resolve.
//
// It walks a DEEP COPY and replaces leaves in place, rather than rebuilding the document.
// A patch is not a plain map: it carries the mergeop tags (`!delete`, `!replace`) that give
// it its write semantics, and field order besides. Rebuilding would mean re-deriving both,
// and a dropped tag turns a deletion into a write of null.
func expandPatch(p entity.Payload, root entity.Payload, read readFn) (entity.Payload, error) {
	if p == nil {
		return nil, nil
	}
	cp := p.DeepCopy()
	if err := expandInto(cp, root, read); err != nil {
		return nil, err
	}
	return cp, nil
}

// readFn answers a path read. It is a parameter rather than a method on the engine because two
// callers legitimately have none: expanding a read's own ADDRESS (a read inside a read would
// make how many entities a firing touches a question only the store could answer), and any
// caller that has no firing behind it. A nil reader reports the tag rather than ignoring it —
// silently writing a tagged node into an entity is exactly the shape of failure this whole
// package refuses elsewhere.
type readFn func(node *ir.Node, root entity.Payload) (entity.Payload, error)

// expandInto rewrites string leaves in place. Only VALUES are expanded: a reference in a key
// would make the patch's shape depend on the payload, and the shape is what a reader of the
// charter is entitled to know without running it.
func expandInto(n entity.Payload, root entity.Payload, read readFn) error {
	if n == nil {
		return nil
	}
	// A path read replaces itself with what it names. Checked before the type switch because the
	// tag rides on the ADDRESS — an object or a string — and either would otherwise be walked
	// as ordinary content.
	if pathKind(n) != "" {
		if read == nil {
			return fmt.Errorf("!%s is not available here: it reads at the commit that caused a "+
				"firing, so it needs one", pathKind(n))
		}
		v, err := read(n, root)
		if err != nil {
			return err
		}
		parent, field, index := n.Parent, n.ParentField, n.ParentIndex
		v.DeepCopy().CloneTo(n)
		n.Parent, n.ParentField, n.ParentIndex = parent, field, index
		return nil
	}
	switch n.Type {
	case ir.ObjectType, ir.ArrayType:
		for _, v := range n.Values {
			if err := expandInto(v, root, read); err != nil {
				return err
			}
		}
		return nil
	case ir.StringType:
		v, err := expandString(n.String, root)
		if err != nil {
			return err
		}
		// CloneTo copies content but clears the destination's parent linkage, so restore it:
		// the node stays where it is in the document, only its value changes.
		parent, field, index := n.Parent, n.ParentField, n.ParentIndex
		v.CloneTo(n)
		n.Parent, n.ParentField, n.ParentIndex = parent, field, index
		return nil
	}
	return nil
}

// subjectFor resolves the $subject record for one firing, reading only what the action asked
// for.
//
// The costly part is `value`, and where it comes from is the whole point: the payload AS OF THE
// CAUSING COMMIT. When the event IS about this subject that payload is already in hand (ev.To)
// and costs nothing — which is the common case, one changed entity waking a rule about itself.
// A fan-out is where it matters: the members that did NOT change would otherwise be read at
// whatever the store says now, so a firing about a set would mix one member's state at C with
// another's at some later commit. That is the inconsistency Store.AsOf exists to remove.
func (e *Engine) subjectFor(oc occasion, subj entity.Ref, w subjectWants) (*ir.Node, error) {
	var value entity.Payload
	var commit int64
	if w.value {
		v, err := e.subjectValueAt(oc, subj)
		if err != nil {
			return nil, err
		}
		value = v
	}
	if w.commit {
		if oc.commit.Rev == 0 {
			return nil, fmt.Errorf("$%s.%s: this firing has no causing commit — a completion "+
				"hook is woken by a task finishing rather than by a write, which is why its "+
				"chain starts again (see causal.go). There is no revision to name",
				refSubject, subjPartCommit)
		}
		commit = oc.commit.Rev
	}
	return subjectNode(subj, value, commit), nil
}

// subjectValueAt reads the subject's payload at the commit that caused the firing.
func (e *Engine) subjectValueAt(oc occasion, subj entity.Ref) (entity.Payload, error) {
	// Free when the causing commit changed this subject, which is the ordinary rule about the
	// entity that changed: the payload at C is already in the commit.
	for _, ch := range oc.commit.Changes {
		if ch.Ref == subj && ch.To != nil {
			return ch.To, nil
		}
	}
	if oc.commit.Rev == 0 {
		return nil, fmt.Errorf("$%s.%s: this firing has no causing commit to read at — a "+
			"completion hook is woken by a task finishing rather than by a write, so there "+
			"is no revision at which to pin the subject's state",
			refSubject, subjPartValue)
	}
	// A fan-out member the commit did not touch. THIS is the read that was silently at "now"
	// before: one firing about a set would mix one member's state at C with another's at
	// whatever the store had reached by the time the action ran.
	snap, err := e.store.AsOf(oc.commit.Rev)
	if err != nil {
		return nil, fmt.Errorf("$%s.%s: reading %s at commit %d: %w",
			refSubject, subjPartValue, subj, oc.commit.Rev, err)
	}
	ent, ok, err := snap.Get(subj)
	if err != nil {
		return nil, fmt.Errorf("$%s.%s: reading %s at commit %d: %w",
			refSubject, subjPartValue, subj, oc.commit.Rev, err)
	}
	if !ok {
		// Absent at C is a real state, not a failure: a named subject may be written by the
		// very action about to fire. Null, so `$subject.value` resolves and a path into it
		// does not.
		return ir.Null(), nil
	}
	return ent.Payload, nil
}

// refPayload answers what an action's $references read, for one subject.
//
// fromCause hands back the cause's payload — a hook's task, which the engine may hold
// facts about that the store does not yet (Engine.taskEvent).
//
// fromSubject reads the SUBJECT. When the subject IS the entity that changed, the event
// already carries its whole level state as of the dispatching commit, so that is used
// directly: exact, and free. When they differ — a trigger whose cause is a CI status and
// whose subject is a PR — the subject is read from the store. That read is CURRENT rather
// than as-of-the-commit, which is the one place this is looser than the model wants; it
// tightens for free once a firing carries the snapshot it was evaluated against, and doing it
// here would mean a whole-verse read per subject.
func (e *Engine) refPayload(ev entity.Event, subj entity.Ref, src refSource) entity.Payload {
	if src == fromCause {
		return ev.To
	}
	if ev.Ref == subj {
		return ev.To
	}
	if ent, ok, err := e.store.Get(subj); err == nil && ok {
		return ent.Payload
	}
	return nil
}
