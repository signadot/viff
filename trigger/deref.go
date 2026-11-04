package trigger

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/expr-lang/expr/ast"
	"github.com/expr-lang/expr/parser"
	"github.com/signadot/tony-format/go-tony/ir"
	"github.com/signadot/verse/entity"
	"github.com/signadot/verse/exprs"
	"github.com/signadot/verse/launch"
)

// Reading a value OUT of what fired an action, and writing it.
//
// A charter names a value with go-tony's expressions, everywhere a firing reads one: a spawn's
// strings and a gate's pose (launch.Expand), and the values of a patch and a derive, and a path
// read's address (expandPatch). One grammar, evaluated against one environment per firing, so a
// prompt, a pose and the patch beside them cannot disagree about what they saw.
//
// In a document a firing WRITES, the difference between the two spellings is the type. A value
// that is one `.[…]` and nothing else is the expression's value — a number stays a number and an
// object an object — which is what lets a task's output become a fact the next `when` matches
// on: a match is exact about type, and `3` written as `"3"` is a different fact. A `$[…]` inside
// text is interpolated, and the value is a string because it was written as one.
//
// The ENVIRONMENT is the fields of the payload the action reads, by name (refPayload), and
// `subject` (subjectFor). A completion hook's payload is its task's record, with every fact of it
// present (taskFacts), as its spawn has it.

// refSource says whose fields an action's expressions read by name. There are exactly two
// answers, and which one applies is a property of WHY the action is running.
type refSource int

const (
	// fromSubject — a trigger firing. The change that dispatched it is a doorbell and may
	// name an entirely different entity (a CI status, a note, a task), whose fields have
	// no meaning on the subject being acted on. So the names read the SUBJECT's own payload.
	fromSubject refSource = iota
	// fromCause — a completion hook, or a sequence element one drives. The action is
	// downstream of a TASK, and `result`, `err` and `data` are exactly the facts it exists to
	// read. The engine synthesizes that payload (Engine.taskEvent) rather than reading the
	// store, because the mirror may not have written the terminal facts yet — so this is not
	// merely a different entity to read, it is a payload only the engine has.
	fromCause
)

// refEnv is what an action's expressions are evaluated against: the fields of the payload named
// by src, and `subject` as subjectFor resolved it. A hook's task has every fact present
// (taskFacts), so `.[data]` on a task that gave no document is null rather than absent.
//
// `subject` is set LAST, the same rule bindingFor states: it is the one reserved name, and a
// payload field that happens to be called "subject" must not shadow it.
func refEnv(payload entity.Payload, subject map[string]any, src refSource) launch.Binding {
	b := launch.Binding{}
	for k, v := range entity.Fields(payload) { // nil for a scalar payload — no named parts
		b[k] = anyOf(v)
	}
	if src == fromCause {
		taskFacts(b)
	}
	b[refSubject] = subject
	return b
}

// The reserved name and its parts. `subject` is a RECORD, and the parts fall in two groups that
// the rest of the model treats differently:
//
//	ref name             IDENTITY — which node this firing is about, and the key it stands at
//	value commit         the FIRING — what was true, and when
const (
	refSubject  = "subject"
	subjPartRef = "ref"
	// name is the last segment of the subject's path: the key it stands at in its parent.
	subjPartName = "name"
	// value is the subject's payload AT THE COMMIT THAT CAUSED THE FIRING, not at the moment
	// the action runs. Those differ by however long a gate took to answer, and reading late
	// would let an approval that took a day act on a state nobody evaluated — the model's rule
	// is that a firing is settled at one snapshot, and this is that rule reaching expressions.
	subjPartValue = "value"
	// commit is the revision of that same causing commit. It is in the record only where an
	// expression names it, `subject.commit`: the record whole is `{ref, name, value}`, so writing
	// it does not stamp the subject anew on every firing.
	subjPartCommit = "commit"
)

// subjectRecord assembles the record `subject` is bound to. value and commit are omitted when
// the firing did not ask for them, which is what keeps a charter that never mentions them from
// paying for a snapshot read.
func subjectRecord(subj entity.Ref, value entity.Payload, commit int64) map[string]any {
	rec := map[string]any{
		subjPartRef:  subj.String(),
		subjPartName: subj.Name(),
	}
	if value != nil {
		rec[subjPartValue] = anyOf(value)
	}
	if commit > 0 {
		rec[subjPartCommit] = int(commit)
	}
	return rec
}

// subjectWants is what a firing needs about its subject beyond identity. Identity is free —
// the path is already in hand — while `value` may cost a read at a revision, which the store
// answers by replaying its log to that commit (entity/logdstore.go). So it is asked for rather
// than always supplied.
type subjectWants struct{ value, commit bool }

// or merges two sets of wants: what either half of a construct reaches for. A fan-out has
// three documents — what to walk, how to address an element, what to write — and they are
// resolved against environments built at different moments, so their wants are asked
// separately and combined rather than computed once over a node nobody wrote.
func (w subjectWants) or(o subjectWants) subjectWants {
	return subjectWants{value: w.value || o.value, commit: w.commit || o.commit}
}

// wantsOf reports which costly parts of `subject` a document's expressions read (exprs.Reads):
// `value` where one reads subject other than as `subject.ref` or `subject.name` — `subject`
// whole and `$env` among them — and `commit` where one names it. An expression that does not
// parse reads nothing; it is refused where it is evaluated.
func wantsOf(p entity.Payload) subjectWants {
	var w subjectWants
	eachText(p, func(s string) {
		reads, err := exprs.ReadsIn(s)
		if err != nil {
			return
		}
		for _, r := range reads {
			switch {
			case r[0] == exprs.Env:
				w.value = true
			case r[0] != refSubject:
			case len(r) == 1:
				w.value = true
			case r[1] == subjPartCommit:
				w.commit = true
			case r[1] != subjPartRef && r[1] != subjPartName:
				w.value = true
			}
		}
	})
	return w
}

// eachText calls fn for every string VALUE in a document, past comment wrappers: what the
// expansion evaluates (expandInto), and so what is asked about before it runs. Keys are not
// values and are never expanded.
func eachText(n entity.Payload, fn func(string)) {
	if n == nil {
		return
	}
	switch n.Type {
	// CommentType for the reason expandInto walks it: a head comment wraps the value under it,
	// and a walk that stops at the wrapper does not see what it holds. Here that made the
	// derive content check — "a derive may read its subject and never the occasion of the
	// firing" — pass a document carrying `subject.commit`, as long as somebody had written a
	// comment above it. The check exists to rule out an unbounded cascade across a level; a
	// comment must not be what turns it off.
	case ir.ObjectType, ir.ArrayType, ir.CommentType:
		for _, v := range n.Values {
			eachText(v, fn)
		}
	case ir.StringType:
		fn(n.String)
	}
}

// scalarText renders a leaf as the text of an address segment. A composite has none, and
// neither has null: a key is a name, and `null` is not one (keyText).
func scalarText(n entity.Payload) (string, bool) {
	// PAST THE COMMENT WRAPPER, for the reason the walks around it do it: a head comment makes
	// the value under it a wrapper node, and asking a wrapper for its type answers CommentType —
	// so a scalar somebody wrote a note above rendered as "no text at all".
	n = ir.Uncomment(n)
	if n == nil {
		return "", false
	}
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

// expandPatch returns the document with every expression in a string value evaluated against
// env, and every path read answered by read, or the first error.
//
// It walks a DEEP COPY and replaces leaves in place, rather than rebuilding the document.
// A patch is not a plain map: it carries the mergeop tags (`!delete`, `!replace`) that give
// it its write semantics, and field order besides. Rebuilding would mean re-deriving both,
// and a dropped tag turns a deletion into a write of null.
func expandPatch(p entity.Payload, env launch.Binding, read readFn) (entity.Payload, error) {
	if p == nil {
		return nil, nil
	}
	cp := p.DeepCopy()
	if err := expandInto(cp, env, read); err != nil {
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
type readFn func(node *ir.Node, env launch.Binding) (entity.Payload, error)

// expandInto rewrites string leaves in place. Only VALUES are expanded: an expression in a key
// would make the patch's shape depend on the payload, and the shape is what a reader of the
// charter is entitled to know without running it.
func expandInto(n entity.Payload, env launch.Binding, read readFn) error {
	if n == nil {
		return nil
	}
	if pathKind(n) != "" {
		return answerRead(n, env, read)
	}
	switch n.Type {
	// A COMMENT IS A WRAPPER, and walking past it is not optional: a head comment above a list
	// element makes that element (or the list) a CommentType node holding the real one, and a
	// walk that switches on Object/Array alone stops there. Everything under a comment was then
	// invisible to the expansion — so a `!get-path` written under one was left in the document,
	// handed to tony.Match as an operator it does not know, and the match failed. Silently: a
	// predicate that cannot be evaluated is a non-match, so the rule simply never selected.
	//
	// That is a comment CHANGING BEHAVIOUR, in a codebase whose charters are more comment than
	// rule. It cost a live drive of the merge train and a long hunt (git-issue
	// 5dfazxf5h12krxvtg9n0): the same predicate fired in a test with no comments in it and never
	// fired in the file it was written in.
	case ir.ObjectType, ir.ArrayType, ir.CommentType:
		for _, v := range n.Values {
			if err := expandInto(v, env, read); err != nil {
				return err
			}
		}
		return nil
	case ir.StringType:
		if !exprs.Templated(n.String) {
			return nil
		}
		v, err := launch.ExpandValue(n.String, env)
		if err != nil {
			return err
		}
		// CloneTo copies content but clears the destination's parent linkage, so restore it:
		// the node stays where it is in the document, only its value changes.
		//
		// AND ITS TAG SURVIVES. CloneTo copies the source's tag over the destination's, and an
		// expanded value carries none — so `tmp: !delete .[x]` would otherwise hand back a write
		// of x, silently turning a deletion into its opposite. The tag belongs to the node's
		// place in the document, not to the value being substituted into it.
		//
		// None, because the environment holds values (eval.ToAny), not nodes: a tag or a comment
		// on what the expression reads is not carried, and a number held only as text arrives as
		// a string. The reference says so (docs/reference/action.md, Expressions).
		parent, field, index, tag := n.Parent, n.ParentField, n.ParentIndex, n.Tag
		v.CloneTo(n)
		n.Parent, n.ParentField, n.ParentIndex, n.Tag = parent, field, index, tag
		return nil
	}
	return nil
}

// answerRead replaces a path read with what it names. Checked before the type switch because
// the tag rides on the ADDRESS — an object or a string — and either would otherwise be walked as
// ordinary content. What a read brings in is not expanded again: it is the store's data, and the
// walk has already passed it.
func answerRead(n *ir.Node, env launch.Binding, read readFn) error {
	if read == nil {
		return fmt.Errorf("!%s is not available here: it reads at the commit that caused a "+
			"firing, so it needs one", pathKind(n))
	}
	v, err := read(n, env)
	if err != nil {
		return err
	}
	parent, field, index := n.Parent, n.ParentField, n.ParentIndex
	v.DeepCopy().CloneTo(n)
	n.Parent, n.ParentField, n.ParentIndex = parent, field, index
	return nil
}

// answerReads replaces every path read in a document with what it names, and touches nothing
// else: a CONDITION's predicate, whose strings are a pattern's (a `!let`'s `.[name]` is tony's
// to bind, when the match runs) and are not a firing's expressions. A read's own address is
// expanded, against env.
func answerReads(n entity.Payload, env launch.Binding, read readFn) error {
	if n == nil {
		return nil
	}
	if pathKind(n) != "" {
		return answerRead(n, env, read)
	}
	switch n.Type {
	case ir.ObjectType, ir.ArrayType, ir.CommentType:
		for _, v := range n.Values {
			if err := answerReads(v, env, read); err != nil {
				return err
			}
		}
	}
	return nil
}

// THE OLD SPELLING. Before a patch's and a derive's values were expressions, a reference was
// `$name`, a kpath read by verse: `$subject.value.sha`. Left as it is, that text is now text,
// and a patch would write the six words onto the subject where it meant the sha. A prompt can
// carry `$HOME` to an agent as written; a fact cannot carry a reference nobody resolved. So it is
// refused at install, by name, with the spelling to write (legacyRef).

// legacyRef is the first old-spelling reference in a document's string values, outside the
// expressions it holds — `$[$env["pr-number"]]` is not one — or "" for none.
func legacyRef(n entity.Payload) string {
	found := ""
	eachText(n, func(s string) {
		if found != "" {
			return
		}
		last := 0
		for _, sp := range exprs.Expressions(s) {
			if r := legacyIn(s[last:sp.Start]); r != "" {
				found = r
				return
			}
			last = sp.End
		}
		found = legacyIn(s[last:])
	})
	return found
}

// legacyIn is the first `$name` in text, with the steps the old spelling took after it
// (`.field`, `[0]`), without its `$`; "" for none. A `$` before anything but a name is text:
// `$5`, `100$`, a lone `$`, and `$$`.
func legacyIn(s string) string {
	for i := 0; i < len(s); i++ {
		if s[i] != '$' {
			continue
		}
		if i+1 < len(s) && s[i+1] == '$' {
			i++
			continue
		}
		if l := refAt(s[i:]); l > 0 {
			return s[i+1 : i+l]
		}
	}
	return ""
}

// legacyRefusal says what to write instead of the old spelling of ref, found at where.
func legacyRefusal(where, ref string) error {
	return fmt.Errorf("%s carries $%s, the old spelling of a reference. A value is an expression "+
		"now: write .[%s] for the value itself, typed, or $[%s] inside text. Text that means a "+
		"literal dollar is written as an expression too: '$[\"$%s\"]'",
		where, ref, ref, ref, ref)
}

// legacyAddress is the refusal of the old spelling in a read's address written as a string,
// nil for none. The general refusal (legacyRefusal) says to write `.[ref]`, which a dotted address
// refuses in its turn (dottedExpressions); this one says what a dotted address takes: the
// expression in a quoted segment, or the address as a list.
func legacyAddress(where string, doc entity.Payload) error {
	return eachPathRead(doc, func(n *ir.Node) error {
		s, ok := addressText(n)
		if !ok {
			return nil
		}
		ref := legacyRef(ir.FromString(s))
		if ref == "" {
			return nil
		}
		return fmt.Errorf("%s: !%s %q carries $%s, the old spelling of a reference. In a dotted "+
			"address the expression goes in a quoted segment, `%s`, or the address is written as a "+
			"list with the expression as a segment of its own, `.[%s]`", where, pathKind(n), s, ref,
			quotedRespelling(s, ref), ref)
	})
}

// quotedRespelling is s with its first `$ref` written as `$[ref]` and the segment holding it
// quoted: `git.repo.$subject.value.head` is `git.repo."$[subject.value.head]"`.
func quotedRespelling(s, ref string) string {
	tok := "$" + ref
	start := strings.Index(s, tok)
	if start < 0 {
		return s
	}
	end := start + len(tok)
	segStart := strings.LastIndex(s[:start], ".") + 1
	if segStart < len(s) && (s[segStart] == '"' || s[segStart] == '\'') {
		// Quoted already: the expression is all it needs.
		return s[:start] + "$[" + ref + "]" + s[end:]
	}
	segEnd := len(s)
	if i := strings.Index(s[end:], "."); i >= 0 {
		segEnd = end + i
	}
	return s[:segStart] + `"` + s[segStart:start] + "$[" + ref + "]" + s[end:segEnd] + `"` + s[segEnd:]
}

// expressionRefusal is what is wrong with the first expression in doc that is refused at
// install, nil for none. where names the document, `action.patch`.
//
// AN EXPRESSION THAT DOES NOT PARSE is refused here rather than at the firing, where it would
// fail every firing of the rule, and before that, a firing's question of what the expressions
// read of the subject (wantsOf) would have gone unasked.
//
// A WHOLE VALUE THAT IS A MAP OR A LIST LITERAL, `.[{findings: data.findings}]`, is refused for
// what it does with absence. A plain path that is not there refuses the firing, and that is the
// rule a value written into the store is held to. Inside a larger expression the same path is
// null, and a literal is a larger expression: the map would be written with `findings: null`,
// a fact nobody stated. In tony the map is a map already, and each value its own expression,
// `{findings: .[data.findings]}`, so each field is held to the rule. A literal deeper in the
// expression, `map(data.items, {{id: #.id}})`, builds what tony cannot, and stands.
func expressionRefusal(where string, doc entity.Payload) error {
	var err error
	eachText(doc, func(s string) {
		if err != nil {
			return
		}
		for _, sp := range exprs.Expressions(s) {
			tree, perr := parser.Parse(sp.Expr)
			if perr != nil {
				err = fmt.Errorf("%s: %q does not parse: %w", where, s, perr)
				return
			}
			if !exprs.Whole(s) {
				continue
			}
			if tony, ok := tonyLiteral(tree.Node); ok {
				err = fmt.Errorf("%s: %q builds a %s inside an expression, where a field that "+
					"is not there is null rather than a refusal. Write it in tony, each value its "+
					"own expression: %s", where, s, literalKind(tree.Node), tony)
				return
			}
		}
	})
	return err
}

// tonyLiteral is a map or a list literal respelled as tony, each value its own `.[…]`; false
// for anything else. A key that is computed has no tony spelling, and the respelling is a
// pattern for one.
func tonyLiteral(n ast.Node) (string, bool) {
	switch x := n.(type) {
	case *ast.MapNode:
		parts := make([]string, len(x.Pairs))
		for i, pn := range x.Pairs {
			pair := pn.(*ast.PairNode)
			k, ok := pair.Key.(*ast.StringNode)
			if !ok {
				return "{name: .[expression], …}", true
			}
			key := k.Value
			if !plainKey(key) {
				key = strconv.Quote(key)
			}
			parts[i] = key + ": .[" + pair.Value.String() + "]"
		}
		return "{" + strings.Join(parts, ", ") + "}", true
	case *ast.ArrayNode:
		parts := make([]string, len(x.Nodes))
		for i, el := range x.Nodes {
			parts[i] = ".[" + el.String() + "]"
		}
		return "[" + strings.Join(parts, ", ") + "]", true
	}
	return "", false
}

func literalKind(n ast.Node) string {
	if _, ok := n.(*ast.MapNode); ok {
		return "map"
	}
	return "list"
}

// plainKey is whether a map key reads as itself unquoted in tony.
func plainKey(k string) bool {
	if k == "" || !identStart(k[0]) {
		return false
	}
	for i := 1; i < len(k); i++ {
		if !identPart(k[i]) && k[i] != '-' {
			return false
		}
	}
	return true
}

// refAt reports the length of the old spelling's reference token at the start of s, or 0 when
// what follows '$' is not one: a name, then `.name` and `[digits]` steps.
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
				return i
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

// subjectFor resolves the `subject` record for one firing, reading only what the action asked
// for.
//
// The costly part is `value`, and where it comes from is the whole point: the payload AS OF THE
// CAUSING COMMIT. When the event IS about this subject that payload is already in hand (ev.To)
// and costs nothing — which is the common case, one changed entity waking a rule about itself.
// A fan-out is where it matters: the members that did NOT change would otherwise be read at
// whatever the store says now, so a firing about a set would mix one member's state at C with
// another's at some later commit. That is the inconsistency Store.AsOf exists to remove.
func (e *Engine) subjectFor(oc occasion, subj entity.Ref, w subjectWants) (map[string]any, error) {
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
			return nil, fmt.Errorf("%s.%s: this firing has no causing commit, so there is no "+
				"revision to name. A completion hook reads at the head, the verse as it is when "+
				"its task ends, and names that", refSubject, subjPartCommit)
		}
		commit = oc.commit.Rev
	}
	return subjectRecord(subj, value, commit), nil
}

// subjectValueAt reads the subject's payload at the occasion's commit: the one that caused the
// firing, and for a completion hook the head, the verse as it is when the task ends (applyHook).
func (e *Engine) subjectValueAt(oc occasion, subj entity.Ref) (entity.Payload, error) {
	// Free when the causing commit changed this subject, which is the ordinary rule about the
	// entity that changed: the payload at C is already in the commit.
	for _, ch := range oc.commit.Changes {
		if ch.Ref == subj && ch.To != nil {
			return ch.To, nil
		}
	}
	if oc.commit.Rev == 0 {
		return nil, fmt.Errorf("%s.%s: this firing has no causing commit to read at, so there "+
			"is no revision at which to pin the subject's state. A completion hook reads at "+
			"the head, the verse as it is when its task ends", refSubject, subjPartValue)
	}
	// A fan-out member the commit did not touch, read at C: one firing about a set would
	// otherwise mix one member's state at C with another's at whatever the store had reached by
	// the time the action ran.
	//
	// THE FIRING'S OWN SNAPSHOT, which is what makes a fan-out affordable. Taken here, it was a
	// snapshot PER MEMBER — twenty members, twenty reads of one world, each one a log replay
	// costing seconds on a store of any age. It is also the same world the rule's other reads
	// see, and as the rule's owner rather than as the engine, which is what a reference is
	// supposed to resolve through.
	snap, err := oc.snapshot(e)
	if err != nil {
		return nil, fmt.Errorf("%s.%s: reading %s at commit %d: %w",
			refSubject, subjPartValue, subj, oc.commit.Rev, err)
	}
	ent, ok, err := snap.Get(subj)
	if err != nil {
		return nil, fmt.Errorf("%s.%s: reading %s at commit %d: %w",
			refSubject, subjPartValue, subj, oc.commit.Rev, err)
	}
	if !ok {
		// Absent at C is a real state, not a failure: a named subject may be written by the
		// very action about to fire. Null, so `subject.value` resolves and a path into it
		// does not.
		return ir.Null(), nil
	}
	return ent.Payload, nil
}

// refPayload answers whose fields an action's expressions read by name, for one subject.
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
