package trigger

import (
	"fmt"
	"strings"

	"github.com/signadot/tony-format/go-tony/ir"
	"github.com/signadot/tony-format/go-tony/mergeop"

	"github.com/signadot/verse/entity"
	"github.com/signadot/verse/mirror"
)

// advise returns the notes a charter earns without being wrong: shapes that install and run,
// and that an author may not have meant.
//
// They are warnings rather than refusals because each is a legitimate thing to write. What
// they have in common is that the charter reads as one sentence and means another, and the
// difference only shows up in production — which is the definition of the thing worth saying
// at install.
func advise(t Trigger) []string {
	var out []string
	if w := adviseSampling(t); w != "" {
		out = append(out, w)
	}
	out = append(out, adviseDenyLatch(t)...)
	out = append(out, adviseHookFlatRef(t)...)
	out = append(out, adviseTagKeyedInValue(t)...)
	return out
}

// adviseTagKeyedInValue names the operator that is not one.
//
// A MATCH HAS TWO POSITIONS AND ONE OF THEM TAKES BOTH SPELLINGS. On a dimension — system, kind,
// id, root — `{glob: "hot-*"}` and `!glob "hot-*"` are one grammar, and the tag-keyed object is
// how a JSON-shaped charter reaches an operator at all, since JSON has no tag syntax. Inside
// `value` only the tag works:
//
//	value: {not: {reviewed: null}}    matches the payload EQUAL to that object — nothing, ever
//	value: !not {reviewed: null}      the absence latch
//
// and the first is silent. Checked: the two lines above select false and true against the same
// payload, in flow style and block alike (git-issue srrwnc0kh12kr1r1e1n0, whose own diagnosis —
// that the axis is flow-vs-block — its discussion then corrected to dimension-vs-value).
//
// IT CANNOT BE DESUGARED, which is why this is a note and not a fix. Under `value` a single-key
// object named `not` is legitimate payload data — a field genuinely called that, holding one
// key — so desugaring unconditionally would take away the only way to match it. tonycodec.go
// says as much where it desugars a dimension: "inside a dimension there are no object VALUES to
// be confused with, which is what makes this unambiguous here and impossible inside fields".
//
// So: a note, on the charter that almost certainly meant the operator, leaving the charter that
// meant the data working and unbothered except for a line it can ignore. The worst case it
// covers is a JSON charter, which cannot write ANY operator under `value` and has no way to say
// so.
func adviseTagKeyedInValue(t Trigger) []string {
	seen := map[string]bool{}
	var out []string
	mats := append([]Match{t.Condition.Subject}, formulaMatches(t.Condition.When)...)
	mats = append(mats, formulaMatches(t.Condition.On)...)
	for _, g := range t.AllGates() {
		mats = append(mats, formulaMatches(g.Formula)...)
	}
	for _, m := range mats {
		for _, name := range operatorShapedKeys(m.Predicate()) {
			if seen[name] {
				continue
			}
			seen[name] = true
			out = append(out, fmt.Sprintf(
				"a condition's `value` holds {%s: …}, which is READ AS DATA: the tag-keyed "+
					"spelling of an operator works on system/kind/id, and inside `value` only "+
					"the tag does. Write `!%s …` if the operator is what was meant — as it "+
					"stands the predicate asks for a payload field literally named %q",
				name, name, name))
		}
	}
	return out
}

// formulaMatches is every point-in-time Match a formula reads — a `when`, an `on`, a gate's
// requirement. They are match positions like the subject's and take the same `value`.
func formulaMatches(f *Formula) []Match {
	if f == nil {
		return nil
	}
	var out []Match
	if f.Req != nil && f.Req.Facts != nil {
		out = append(out, f.Req.Facts)
	}
	for _, a := range f.Args {
		out = append(out, formulaMatches(a)...)
	}
	return out
}

// operatorShapedKeys is every single-key object under a predicate whose key names a known
// operator and which carries no semantic tag of its own — the shape that reads as an operator
// and is not one.
func operatorShapedKeys(p entity.Payload) []string {
	var out []string
	var walk func(n entity.Payload)
	walk = func(n entity.Payload) {
		n = ir.Uncomment(n)
		if n == nil {
			return
		}
		if n.Type == ir.ObjectType && len(n.Fields) == 1 && isLayoutTag(n.Tag) {
			if name := n.Fields[0].String; mergeop.Lookup(name) != nil {
				out = append(out, name)
			}
		}
		for _, v := range n.Values {
			walk(v)
		}
	}
	walk(p)
	return out
}

// adviseHookFlatRef names the reference a completion hook cannot resolve, at install rather
// than at the firing nobody is watching.
//
// A HOOK'S FLAT `$name` RESOLVES AGAINST THE TASK, not against the subject. That is right — a
// hook is woken by a task finishing and is handed its record — and it is the opposite of what
// the rule beside it reads like, because in the ACTION two lines above, the flat names ARE the
// subject's:
//
//	action:
//	  patch: {stage: claimed, covering: !get-path {…}}   # $covering here is the subject's
//	  onDone:
//	    patch: {sha: $covering}                          # …and here it is nothing at all
//
// What that costs is the whole firing. The reference does not resolve, the patch is refused,
// and the record stays exactly where it was — a cursor stuck in `claimed` with a completed task
// beside it (git-issue 91jktrr3h12ksa8fgsn0). It IS written down, onto the task's own record
// under `hook`, which is where an operator who already suspects a hook goes looking; this is for
// the operator who does not yet.
//
// `$subject` is the one reference that still means the subject inside a hook, so the note says
// to spell it `$subject.value.<name>`.
//
// ADVICE, NOT A REFUSAL, and the reason is mirror.RecordFields: it is a list maintained beside
// the writes, so a field added to a task record and not added there would make a correct hook
// refuse to install. A note that is occasionally unnecessary is the right failure for a list
// that can be a name short.
func adviseHookFlatRef(t Trigger) []string {
	var out []string
	for _, h := range hooksOf(&t.Action) {
		for _, name := range flatRefs(h.act.Patch) {
			if mirror.RecordFields[name] {
				continue
			}
			out = append(out, fmt.Sprintf(
				"%s names $%s, which a task record does not carry — a hook's flat names are the "+
					"TASK's (%s), where the action above it has the subject's. If %s is a field "+
					"of the subject, spell it $subject.value.%s; the firing writes nothing at all "+
					"as it stands",
				h.chain, name, "status, result, data, err, flow, chain, …", name, name))
		}
	}
	return out
}

// hookHere is one completion hook and the chain that names it, for a message that says WHICH.
type hookHere struct {
	chain string
	act   *Action
}

// hooksOf collects the completion hooks under an action, including those of a sequence's
// elements — a `seq` element carries its own off-ramps, and they deref the same way.
func hooksOf(a *Action) []hookHere {
	if a == nil {
		return nil
	}
	var out []hookHere
	for _, h := range []struct {
		name string
		act  *Action
	}{
		{"onDone", a.OnDone}, {"onFailed", a.OnFailed},
		{"onCancelled", a.OnCancelled}, {"onOrphaned", a.OnOrphaned},
	} {
		if h.act != nil {
			out = append(out, hookHere{chain: h.name, act: h.act})
		}
	}
	for i := range a.Seq {
		for _, h := range hooksOf(&a.Seq[i]) {
			out = append(out, hookHere{chain: fmt.Sprintf("seq[%d].%s", i, h.chain), act: h.act})
		}
	}
	return out
}

// flatRefs is every `$name` in a payload that names ONE identifier — no dot, so no record to
// walk into. `$subject.value.x` and `$data.x` are not flat and are not this function's business.
func flatRefs(p entity.Payload) []string {
	seen := map[string]bool{}
	var out []string
	var walk func(n entity.Payload)
	walk = func(n entity.Payload) {
		n = ir.Uncomment(n)
		if n == nil {
			return
		}
		if n.Type == ir.StringType {
			s := n.String
			for i := 0; i < len(s); i++ {
				if s[i] != '$' {
					continue
				}
				k := refAt(s[i:])
				if k == 0 {
					continue
				}
				if name := s[i+1 : i+k]; !strings.Contains(name, ".") && !seen[name] {
					seen[name] = true
					out = append(out, name)
				}
				i += k - 1
			}
			return
		}
		for _, v := range n.Values {
			walk(v)
		}
	}
	walk(p)
	return out
}

// adviseDenyLatch names the half of a gated rule nobody writes: what a NO means.
//
// A denied firing writes nothing at all — deliberately, so that a firing which did not happen
// leaves no trace of having happened. The consequence is that the subject is left in the exact
// state that asked the question. Any later commit that touches it re-selects it, the rule
// fires again, and the gate is re-posed under its deterministic id, to somebody who already
// answered it.
//
// The latch a well-formed rule carries cannot help: it lives in the ACTION, and on a deny the
// action never runs. So this is not a defect an author can fix by writing the rule better —
// which is why it earns a note rather than a refusal, and why the note has to say what to
// write instead of only what is wrong.
//
// A warning rather than an error for the other reason too: re-asking is a legitimate thing to
// want. "Ask again next time somebody touches it" is a real policy, and a rule that means it
// is indistinguishable from one that forgot.
func adviseDenyLatch(t Trigger) []string {
	pred := t.Condition.Subject.Predicate()
	if pred == nil {
		// No payload predicate: the subject is selected by system/kind alone, so nothing a
		// deny could write would deselect it. A different note, and not this one.
		return nil
	}
	var out []string
	for _, g := range t.AllGates() {
		m, err := g.mode()
		if err != nil || !m.Blocks() {
			continue // auto passes without asking anybody; nothing to refuse
		}
		var denied Action
		if g.OnDenied != nil {
			denied = *g.OnDenied
		}
		// The same relation the refusal uses, and the same tolerance: a note is earned by a
		// refusal the check can SEE leaves the subject where it was, never by one it merely
		// cannot decide. Advice that is wrong is worse than silence for whoever reads it.
		if latches(pred, denied) {
			continue
		}
		name := g.Name
		if name == "" {
			name = t.Name
		}
		out = append(out, fmt.Sprintf("gate %q blocks, and its refusal does not advance the "+
			"subject past its own predicate (%s): a denied firing writes nothing at all, so "+
			"the subject stays in the state that asked and the next commit to touch it re-poses "+
			"this question over the answer it already has. Say what a no means — "+
			"`onDenied: {patch: {…}}` on the gate, writing one of those paths a value the "+
			"predicate refuses", name, pathList(readPaths(pred))))
	}
	return out
}

// adviseSampling names the difference between the two sentences a narrow `on` can be read as.
//
// A trigger with an `on` fires ONCE, at the edge, over the subject set as it stood at that
// commit. If the subject or the condition reads a subtree the cause does not cover, a member
// arriving afterwards is not acted on — until the cause falls and rises again. That is
// deterministic and often exactly right, and it is not what "ship every reviewed PR when CI is
// green" sounds like:
//
//	on:      { system: ci,   kind: status, value: { state: green } }
//	subject: { system: demo, kind: pr,     value: { stage: reviewed } }
//
// says *ship everything reviewed AT THE MOMENT CI went green*, not *while CI is green*. The
// second is what you get by dropping the `on` and keeping the condition as a `when`: then
// every commit re-evaluates, and a PR arriving later is picked up.
func adviseSampling(t Trigger) string {
	if t.Condition.On == nil {
		return "" // no cause named: every commit re-evaluates, so nothing is sampled
	}
	covered := map[Subtree]bool{}
	for _, st := range t.Condition.On.Subtrees() {
		covered[st] = true
	}
	var missed []Subtree
	for _, st := range append(t.Condition.When.Subtrees(), matchFormula(t.Condition.Subject).Subtrees()...) {
		if !covered[st] && !covered[st.Parent()] {
			missed = append(missed, st)
		}
	}
	if len(missed) == 0 {
		return ""
	}
	return fmt.Sprintf("`on` does not cover %s, which this rule also reads. It fires at the "+
		"edge over whatever stood there THEN — an entity arriving afterwards waits for the "+
		"cause to fall and rise again. If you meant \"whenever this holds\" rather than \"at "+
		"the moment it became true\", drop the `on` and let the condition be a `when`",
		subtreeList(missed))
}

// subtreeList renders (system, kind) pairs for a diagnostic.
func subtreeList(sts []Subtree) string {
	out := ""
	for i, st := range sts {
		if i > 0 {
			out += ", "
		}
		out += strings.Join(st.Path(), ":")
	}
	return out
}
