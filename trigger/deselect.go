package trigger

import (
	"sort"
	"strings"

	tony "github.com/signadot/tony-format/go-tony"
	"github.com/signadot/tony-format/go-tony/ir"
	"github.com/signadot/tony-format/go-tony/mergeop"
	"github.com/signadot/verse/entity"
)

// Deselection: whether a subject can still match the predicate that selected it, once the
// action that selected it has run. It is the relation under both latch checks — checkLatch,
// which refuses a spawn that cannot stop spawning, and adviseDenyLatch, which names a gate
// whose refusal leaves the question to be asked again.
//
// It is a question about SATISFIABILITY — are the predicate and the action's post-state unsat
// — and it needs no solver, because of one fact: at fire time the predicate was TRUE, so the
// only thing that can falsify it is a path the action WRITES. Everything else is a linear
// three-valued fold over the predicate's own structure, with tony.Match as the leaf primitive.
//
// It replaced an intersection of TOP-LEVEL KEYS: the field names of the subject's payload
// predicate against the field names of the action's patches. That model is only correct when
// both sides happen to be flat objects, and a payload is any tony value — so it was wrong in
// every other shape, in both directions. A nested predicate against a sibling write passed
// (`{labels: {urgent: true}}` with `patch: {labels: {seen: true}}` writes the key `labels`, and
// `labels.urgent` is untouched, so it spawned at every commit forever). A whole-value replace
// was refused, having no top-level keys to offer, despite being the most total latch there is.
// A root operator yielded its TAG NAME as if it were a field, so `value: !and […]` was refused
// unless the action wrote a field literally called `and`. And writing the same value back
// passed, which is a latch for a store write — rewriting a value raises no commit — and not for
// a SPAWN, which is what the check is about.
//
// AN UNTOUCHED PATH IS UNKNOWN HERE, not "still true". The tempting step is that the predicate
// held at fire time, so a leaf nobody wrote holds still — but that is only sound in a positive
// position. Under a `!not` it manufactures a refutation: `value: !not {stage: open}` with an
// action that touches nothing at all would fold to "definitely false" and be waved through to
// spin forever. What the firing tells us is that the predicate held as a WHOLE, and a disjunct
// that failed then fails still. Unknown costs nothing that matters, because a conjunction is
// false as soon as one conjunct is, and that is the shape every real latch has. The whole-
// predicate form of the same argument is sound and is used — it is latchUntouched.
//
// The check is not the fold. Undecided is a PASS (see latch): a refusal is a contract, and the
// values this cannot see through are ordinary ones — `patch: {stage: $result.status}` may well
// latch. What it refuses is what it can see: a rule that writes nothing its subject reads, and
// one whose write leaves the predicate satisfied.

// tri is Kleene's three-valued truth: what a predicate comes out as when the document it is
// asked of is only partly known. triMaybe is not a soft false — it is the refusal to decide,
// and every caller here acts on a definite answer only.
type tri int

const (
	triMaybe tri = iota
	triTrue
	triFalse
)

func (t tri) and(u tri) tri {
	switch {
	case t == triFalse || u == triFalse:
		return triFalse
	case t == triTrue && u == triTrue:
		return triTrue
	}
	return triMaybe
}

func (t tri) or(u tri) tri {
	switch {
	case t == triTrue || u == triTrue:
		return triTrue
	case t == triFalse && u == triFalse:
		return triFalse
	}
	return triMaybe
}

func (t tri) not() tri {
	switch t {
	case triTrue:
		return triFalse
	case triFalse:
		return triTrue
	}
	return triMaybe
}

func triOf(b bool) tri {
	if b {
		return triTrue
	}
	return triFalse
}

// post is what is known about the subject's payload after an action has run — tony.Patch
// carried out over knowledge rather than over a document, since the document is whatever the
// subject happened to hold and the check never sees it.
//
// The six kinds are the six things an action can leave behind at a path. Two of them say
// nothing about the VALUE and differ only in whether anything happened at all — which is the
// difference between "this rule has nothing to latch on" and "it does something here and this
// cannot say what", and those earn different answers.
type post struct {
	kind   postKind
	node   *ir.Node         // postKnown: exactly this
	fields map[string]*post // postObject: what the patch named
	rest   *post            // postObject: every field it did not name
}

type postKind int

const (
	postUnwritten postKind = iota // nothing was written here: whatever the subject held
	postTouched                   // written at or above here, by something this does not model
	postPresent                   // written and present, and what landed is not knowable here
	postAbsent                    // deleted, or never there to begin with
	postKnown                     // written as a literal
	postObject                    // merged into: an object, with these fields and that rest
)

var (
	unwritten = &post{kind: postUnwritten}
	touched   = &post{kind: postTouched}
	opaque    = &post{kind: postPresent}
	gone      = &post{kind: postAbsent}
)

// field is what is known about one field of a state.
func (p *post) field(name string) *post {
	switch p.kind {
	case postObject:
		if f, ok := p.fields[name]; ok {
			return f
		}
		return p.rest
	case postKnown:
		// A literal write settles its whole subtree, so a field of one is known too —
		// including that it is not there.
		if p.node == nil || p.node.Type != ir.ObjectType {
			return gone
		}
		if v := ir.Get(p.node, name); v != nil {
			return &post{kind: postKnown, node: v}
		}
		return gone
	case postAbsent:
		return gone
	case postPresent, postTouched:
		// A value landed here, so what is under it landed too — whatever it is.
		return touched
	}
	return unwritten
}

// apply carries a patch out over a state: the abstract half of tony.Patch, and it follows that
// function's reading of a node rather than a second one of its own.
func apply(in *post, p entity.Payload) *post {
	if p == nil {
		return in
	}
	// A path read resolves to a value at fire time, and an unresolved one FAILS the firing
	// rather than being dropped — so if the action applied at all, something landed here.
	// Checked first because the tag rides on an object, which the literal branch below would
	// otherwise merge as if `system` and `kind` were fields of the subject.
	if pathKind(p) != "" {
		return opaque
	}
	if _, tag, _, child, err := mergeop.SplitChild(p); err != nil || tag != "" {
		switch {
		case err != nil:
			return touched
		case tag == "delete":
			return gone
		case tag == "replace":
			// `!replace {from, to}` asserts what was there and writes `to`. The assertion can
			// fail the firing; if it does not, the value is exactly `to`.
			if to := ir.Get(child, "to"); to != nil {
				return literal(to)
			}
		}
		// Any other operator: it wrote here, and this does not model what.
		return touched
	}
	switch p.Type {
	case ir.ObjectType:
		// An object patch MERGES: the fields it names take its values, and the rest keep
		// whatever they had — except over a document that was not an object, where objPatchY
		// keeps nothing. Either way the result IS an object, which is itself worth knowing:
		// it decides a predicate that asks for a scalar.
		out := &post{kind: postObject, fields: make(map[string]*post, len(p.Fields)), rest: in.merged()}
		for i, key := range p.Fields {
			if key == nil || key.Type != ir.StringType {
				return touched // a null key is an embed, and this does not model one
			}
			out.fields[key.String] = apply(in.field(key.String), p.Values[i])
		}
		return out
	}
	return literal(p)
}

// literal is what a non-object patch node leaves behind. A scalar replaces outright, so it is
// known exactly — unless it carries a $reference, which resolves to a value only the firing
// has. An array does not replace outright (tony patches arrays element-wise and appends the
// rest), so it is reported as present rather than as itself.
func literal(p *ir.Node) *post {
	if p.Type == ir.ArrayType || hasRef(p) {
		return opaque
	}
	return &post{kind: postKnown, node: p}
}

// merged is what a field an object patch does NOT name is worth afterwards.
func (p *post) merged() *post {
	switch p.kind {
	case postObject:
		return p.rest
	case postAbsent:
		return gone
	case postPresent, postTouched:
		return touched
	case postKnown:
		// Merging an object over something that is not one keeps none of it (objPatchY reads
		// no fields off a scalar), and postKnown only ever holds a scalar — see literal.
		if p.node == nil || p.node.Type != ir.ObjectType {
			return gone
		}
	}
	return unwritten
}

// hasRef reports whether a node carries a $reference anywhere, by the same token rule the
// expander uses — so what counts here is exactly what would be substituted.
func hasRef(n *ir.Node) bool {
	found := false
	eachRef(n, func(string) { found = true })
	return found
}

// eval asks a predicate of a state: does the subject still match?
//
// It is tony.Match's own walk, three-valued where the state does not say. Anything it does not
// interpret answers triMaybe, which is what keeps a check built on it from claiming a latch it
// cannot see.
func eval(s *post, pat *ir.Node) tri {
	if pat == nil {
		return triTrue // no predicate constrains nothing, so everything satisfies it
	}
	switch s.kind {
	case postAbsent:
		// matchObj fails on a field the document does not have, whatever the pattern says
		// about it — a `!not` included, which is why absence is decided here rather than by
		// descending into the pattern.
		return triFalse
	case postKnown:
		ok, err := tony.Match(s.node, pat)
		if err != nil {
			return triMaybe
		}
		return triOf(ok)
	}
	_, tag, _, child, err := mergeop.SplitChild(pat)
	if err != nil {
		return triMaybe
	}
	switch tag {
	case "and":
		if child.Type == ir.ArrayType {
			out := triTrue
			for _, v := range child.Values {
				out = out.and(eval(s, v))
			}
			return out
		}
		return eval(s, child)
	case "or":
		if child.Type == ir.ArrayType {
			out := triFalse
			for _, v := range child.Values {
				out = out.or(eval(s, v))
			}
			return out
		}
		// The object form of `!or` is a disjunction over the fields the DOCUMENT has, and
		// which fields it has is the thing least known here.
		return triMaybe
	case "not":
		return eval(s, child).not()
	case "":
	default:
		// An operator this does not interpret. It is decided when the state is known — the
		// postKnown case above hands it to tony.Match — and undecided otherwise.
		return triMaybe
	}
	if pat.Type == ir.NullType {
		// A null pattern matches ANY value, so it constrains presence and nothing else.
		if s.kind == postPresent || s.kind == postObject {
			return triTrue
		}
		return triMaybe
	}
	if s.kind == postObject {
		if pat.Type != ir.ObjectType {
			return triFalse // the state is an object and the pattern asks for something else
		}
		out := triTrue
		for i, key := range pat.Fields {
			if key == nil || key.Type != ir.StringType {
				return triMaybe
			}
			out = out.and(eval(s.field(key.String), pat.Values[i]))
		}
		return out
	}
	return triMaybe
}

// walk returns the states an action can leave its subject in: the one it reaches by running to
// completion, and one for every other way it can end.
//
// A completion hook is an ALTERNATIVE — exactly one terminal status happens, and none may — so
// each contributes its own state rather than composing with its siblings. A SEQUENCE composes:
// its elements run in order, each starting where the last left off.
func walk(a Action, in *post) (through *post, ended []*post) {
	through = apply(in, a.Patch)
	for _, el := range a.Seq {
		var offRamps []*post
		through, offRamps = walk(el, through)
		// An element's completion hook is an off-ramp: firing one ends the sequence, so its
		// states are terminal here rather than the ground the next element stands on.
		ended = append(ended, offRamps...)
	}
	for _, h := range []*Action{a.OnDone, a.OnFailed, a.OnCancelled, a.OnOrphaned} {
		if h == nil {
			continue
		}
		t, e := walk(*h, through)
		ended = append(ended, t)
		ended = append(ended, e...)
	}
	return through, ended
}

// latch is what the check can SEE about an action's ability to take its subject out of its own
// selection. Four answers, in two groups: it latches or it may, and it definitely does not, for
// one of two reasons an author is told apart. A refusal rests on the second group only —
// undecided is not a no, and a refusal is a contract.
type latch int

const (
	// latchUnsure — it writes where the predicate reads, and what lands there is not knowable
	// at install ($result, an operator this does not model). It may latch; nothing here can
	// say. Treated as a pass, which is where this is deliberately looser than the fold: a
	// check that refused every charter it could not decide would refuse `patch: {stage:
	// $result.status}`, which is an ordinary latch.
	latchUnsure latch = iota
	// latchHolds — there is a way for the action to end after which the subject's predicate is
	// definitely false. ANY way, rather than every way: a rule whose onDone latches and whose
	// onFailed does not still spins on the failing path, and this does not say so — a
	// judgement inherited from the check this replaced, and one to revisit on its own rather
	// than under a change of relation.
	latchHolds
	// latchUntouched — no way of ending writes anywhere the predicate reads, so the predicate is
	// exactly what it was at fire time, and at fire time it was TRUE. This is the sound form of
	// "an untouched path holds": said of the WHOLE predicate at once, where a `!not` cannot turn
	// it into a refutation, rather than of a leaf, where it can (see the note on this file).
	latchUntouched
	// latchMatches — every way of ending that writes what the predicate reads leaves it
	// SATISFIED. The subject stays exactly where it was, whatever the write cost. This is what
	// the old key-intersection check waved through as "writes the same value back, which
	// converges anyway": true of the WRITE, which raises no commit, and false of the SPAWN,
	// which is what is being bounded here.
	latchMatches
)

// latchOf decides which of the four an action is, for one subject predicate. A way of ending
// that writes nothing the predicate reads is one that still matches, which is why a `patch`
// beside the spawn and a `patch` in an onDone are both latches on their own: the state where
// the hook has not fired is the one the process is running in.
func latchOf(pred *ir.Node, a Action) latch {
	if pred == nil {
		return latchUntouched // no predicate is a predicate nothing can falsify
	}
	paths := readPaths(pred)
	through, ended := walk(a, unwritten)
	out, wrote := latchMatches, false
	for _, s := range append(ended, through) {
		if !writesAny(s, paths) {
			// This way of ending leaves every path the predicate reads exactly as it was, and
			// at fire time the predicate was TRUE — so it still is, and this state spins.
			continue
		}
		wrote = true
		switch eval(s, pred) {
		case triFalse:
			return latchHolds
		case triMaybe:
			out = latchUnsure
		}
	}
	if !wrote {
		return latchUntouched
	}
	return out
}

// latches reports whether an action is one a rule may keep — it deselects its subject, or the
// check cannot tell that it does not.
func latches(pred *ir.Node, a Action) bool {
	switch latchOf(pred, a) {
	case latchHolds, latchUnsure:
		return true
	}
	return false
}

// readPath is a path into the payload, by segment. The empty path is the payload itself.
type readPath []string

// readPaths names the paths a predicate constrains, for a diagnostic. It descends the boolean
// operators the fold descends, so the paths it reports are the ones an author would have to
// write to, and stops at any other operator — `{stage: !or [open, merged]}` reads `stage`.
func readPaths(pat *ir.Node) []readPath {
	var out []readPath
	var descend func(pat *ir.Node, at readPath)
	descend = func(pat *ir.Node, at readPath) {
		if pat == nil {
			return
		}
		_, tag, _, child, err := mergeop.SplitChild(pat)
		if err == nil && (tag == "and" || tag == "or" || tag == "not") {
			if child.Type == ir.ArrayType && tag != "not" {
				for _, v := range child.Values {
					descend(v, at)
				}
				return
			}
			descend(child, at)
			return
		}
		// An object with fields reads each of them; one WITHOUT fields still constrains the
		// path it sits at, since `{}` asks for an object and a scalar there is not one.
		if err == nil && tag == "" && pat.Type == ir.ObjectType && len(pat.Fields) > 0 {
			for i, key := range pat.Fields {
				if key == nil || key.Type != ir.StringType {
					continue
				}
				descend(pat.Values[i], append(append(readPath{}, at...), key.String))
			}
			return
		}
		out = append(out, at)
	}
	descend(pat, nil)
	return out
}

// writesAny reports whether a state has anything written where the predicate reads. MAY have
// written counts: what this decides is whether the predicate is UNTOUCHED, and a refusal rests
// on that being certain.
func writesAny(s *post, paths []readPath) bool {
	for _, p := range paths {
		at := s
		for _, seg := range p {
			at = at.field(seg)
		}
		if at.kind != postUnwritten {
			return true
		}
	}
	return false
}

// pathList renders the paths a predicate reads for a diagnostic, in a stable order.
func pathList(paths []readPath) string {
	seen := map[string]bool{}
	out := make([]string, 0, len(paths))
	for _, p := range paths {
		if len(p) == 0 {
			return "the whole payload"
		}
		s := strings.Join(p, ".")
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	sort.Strings(out)
	return strings.Join(out, ", ")
}
