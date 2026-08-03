package trigger

import "fmt"

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
	covered := map[[2]string]bool{}
	for _, st := range t.Condition.On.Subtrees() {
		covered[st] = true
	}
	var missed [][2]string
	for _, st := range append(t.Condition.When.Subtrees(), matchFormula(t.Condition.Subject).Subtrees()...) {
		if !covered[st] && !covered[[2]string{st[0], ""}] {
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
func subtreeList(sts [][2]string) string {
	out := ""
	for i, st := range sts {
		if i > 0 {
			out += ", "
		}
		out += st[0]
		if st[1] != "" {
			out += ":" + st[1]
		}
	}
	return out
}
