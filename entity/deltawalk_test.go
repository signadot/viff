package entity

import (
	"testing"
)

// changesOf runs the delta walk over a document written as tony, keyed by ref so a test can
// say what it expects to be reported and what it expects not to be.
func changesOf(t *testing.T, src string) map[Ref]entityChange {
	t.Helper()
	out := map[Ref]entityChange{}
	for _, ch := range walkEntityChanges(doc(t, src)) {
		out[ch.ref] = ch
	}
	return out
}

// A REMOVAL IS AN OPERATION, NOT A STRING. A tag composes, and what a patch executes is the
// first registered label in the chain — so a delete carrying anything alongside it is still a
// delete, and the walk has to ask the same question the patch asks (opIn).
//
// Comparing the whole tag answers no to it, and the answer it gives instead is worse than a
// miss: the entity is reported as SET, to whatever the operand of the removal was, and a
// consumer acts on an entity the store no longer holds.
func TestAComposedDeleteIsStillARemoval(t *testing.T) {
	changes := changesOf(t, `demo: pr: "1": !delete.logd-patch-root null`)
	ch, ok := changes[RefAt("demo", "pr", "1")]
	if !ok {
		t.Fatalf("the removal was not reported at all; got %v", changes)
	}
	if !ch.deleted {
		t.Errorf("reported as a set of %+v, want a removal — `!delete` wearing a second label "+
			"is the operation the store executed", ch.payload)
	}
}

// A `!comment` ABOVE AN ENTITY IS NOT A LEVEL OF THE NAMESPACE. It is an ordinary tagged object
// whose fields name the parts of a statement — head, line, value — so a walk that descends it
// reads those parts as addresses and reports entities that do not exist.
//
// Here the comment is on the KIND, and it says nothing about any entity under it. The right
// number of changes is none.
func TestACommentOnAContainerReportsNoEntities(t *testing.T) {
	changes := changesOf(t, `demo: pr: !comment {head: ["# the prs"]}`)
	if len(changes) != 0 {
		t.Fatalf("reported %v, want nothing — a comment on the kind changed no entity under it",
			changes)
	}
}

// The same node WITH a value states both at once: what the comments are, and what the value is.
// The entities are under the value, at the depth they always stood at.
func TestACommentCarryingAValueReportsWhatIsUnderIt(t *testing.T) {
	changes := changesOf(t,
		`demo: pr: !comment {head: ["# the prs"], value: {"1": {stage: "open"}}}`)
	ch, ok := changes[RefAt("demo", "pr", "1")]
	if !ok {
		t.Fatalf("the entity under value: was not reported; got %v", changes)
	}
	if got := Str(ch.payload, "stage"); got != "open" {
		t.Errorf("payload = %+v (stage %q), want the value the operand states", ch.payload, got)
	}
	if len(changes) != 1 {
		t.Errorf("reported %d changes, want the one entity — head: is not an address", len(changes))
	}
}

// A `!comment` AT the entity whose value: removes it is a removal. The operation says so — with
// nothing left to hold a comment, the node is gone — and a walk that stopped at the wrapper
// would report a set whose payload is the statement itself.
func TestACommentWhoseValueDeletesIsARemoval(t *testing.T) {
	changes := changesOf(t, `demo: pr: "1": !comment {head: ["# gone"], value: !delete null}`)
	ch, ok := changes[RefAt("demo", "pr", "1")]
	if !ok {
		t.Fatalf("nothing reported for the entity; got %v", changes)
	}
	if !ch.deleted {
		t.Errorf("reported as a set of %+v, want a removal", ch.payload)
	}
}

// A `!comment` AT the entity with no value: changed the comments and not the value. The entity
// still stands and still holds what it held, so the change is reported with NO payload — the
// walk has none to give — and the level state comes from the document (eventsFromDelta).
//
// Reporting the operand as the payload is the failure this rules out: `{head: […]}` is not what
// the entity holds, and a consumer reading a field off it reads nothing.
func TestACommentOnlyChangeCarriesNoPayload(t *testing.T) {
	changes := changesOf(t, `demo: pr: "1": !comment {head: ["# noted"]}`)
	ch, ok := changes[RefAt("demo", "pr", "1")]
	if !ok {
		t.Fatalf("the comment-only change was not reported; got %v", changes)
	}
	if ch.deleted {
		t.Fatalf("reported as a removal — the entity is still there, its comments changed")
	}
	if ch.payload != nil {
		t.Errorf("payload = %+v, want none: the operand is a statement about the entity, "+
			"not the entity", ch.payload)
	}
}

// ChangedAt walks the SAME delta at a depth its caller names, and is what tells a causal chain
// whether a commit touched a ref. It reads the parts of a statement as addresses in exactly the
// way the entity walk does, so it is pinned here too.
func TestChangedAtDoesNotReadACommentOperandAsAddresses(t *testing.T) {
	c := Commit{}.WithDelta(doc(t, `demo: pr: !comment {head: ["# the prs"]}`))
	if got := c.ChangedAt(Depth); len(got) != 0 {
		t.Fatalf("ChangedAt(%d) = %v, want nothing — head: is not an entity", Depth, got)
	}

	c = Commit{}.WithDelta(doc(t,
		`demo: pr: !comment {head: ["# the prs"], value: {"1": {stage: "open"}}}`))
	want := RefAt("demo", "pr", "1")
	if got := c.ChangedAt(Depth); len(got) != 1 || got[0] != want {
		t.Fatalf("ChangedAt(%d) = %v, want [%s]", Depth, got, want)
	}
}
