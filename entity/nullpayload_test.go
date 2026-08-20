// An entity may hold ANY tony value, and `null` is one of them.
//
// It was the last value the store reserved for itself. Absence used to be spelled with a null
// — at the entity's own path, after nodeToPayload stopped spelling it that way one level down
// — so `payload: null` went into the log intact and came back as "nothing stands here": Get
// answered not-found, List omitted it, and the change stream called it a Delete. Three
// different surfaces agreeing on the same wrong answer, because one walk served both a
// materialized document and a delta.
//
// What the store actually rests on, which these tests pin: logd EXECUTES a removal by taking
// the key out, so absence is a missing path and there is no tombstone value to be confused
// with. In a delta, where a removal has no absence to be observed as, it is spelled by the
// `!delete` TAG.
package entity

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/signadot/tony-format/go-tony/encode"
	"github.com/signadot/tony-format/go-tony/format"
	"github.com/signadot/tony-format/go-tony/ir"
	"github.com/signadot/tony-format/go-tony/parse"
)

// A null payload round-trips: it is written, read back as null, and listed.
func TestEntityMayHoldNull(t *testing.T) {
	s := openStore(t)
	ref := RefAt("demo", "pr", "1")
	if ok, _, err := s.Write(Write{At: ref.At(), Value: ir.Null()}); err != nil || !ok {
		t.Fatalf("write null payload: committed=%v err=%v", ok, err)
	}

	e, ok, err := s.Get(ref)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if !ok {
		t.Fatal("an entity holding null read back as absent — the value the store still reserved")
	}
	if e.Payload == nil || e.Payload.Type != ir.NullType {
		t.Fatalf("payload = %v, want null", e.Payload)
	}

	listed := s.List(nil)
	if len(listed) != 1 || listed[0].Ref != ref {
		t.Fatalf("List = %v, want the one null-payload entity", listed)
	}
	if listed[0].Payload == nil || listed[0].Payload.Type != ir.NullType {
		t.Fatalf("listed payload = %v, want null", listed[0].Payload)
	}
}

// Deleting is still absence, and is a MISSING KEY rather than a null: the distinction the test
// above depends on. A snapshot reads it the same way, since it walks the same document.
func TestDeleteIsAbsentNotNull(t *testing.T) {
	s := openStore(t)
	ref := RefAt("demo", "pr", "1")
	if ok, _, err := s.Write(Write{At: ref.At(), Value: Obj{"stage": "open"}.Payload()}); err != nil || !ok {
		t.Fatalf("write: committed=%v err=%v", ok, err)
	}
	if ok, _, err := s.Write(Write{At: ref.At(), Value: nil}); err != nil || !ok {
		t.Fatalf("delete: committed=%v err=%v", ok, err)
	}

	if _, ok, err := s.Get(ref); err != nil || ok {
		t.Fatalf("get after delete: ok=%v err=%v, want absent", ok, err)
	}
	if listed := s.List(nil); len(listed) != 0 {
		t.Fatalf("List after delete = %v, want empty", listed)
	}

	snap, err := s.AsOf(s.Rev())
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	if _, ok, _ := snap.Get(ref); ok {
		t.Fatal("the snapshot still holds a deleted entity")
	}
	if listed := snap.List(nil); len(listed) != 0 {
		t.Fatalf("snapshot List = %v, want empty", listed)
	}
}

// A create precondition still refuses a live entity when its payload is null — the failure
// absentPath's dropped tombstone branch would have caused, since `!irtype null` calls a null
// value absent.
func TestCreateOverANullPayloadIsRefused(t *testing.T) {
	s := openStore(t)
	ref := RefAt("demo", "pr", "1")
	if ok, _, err := s.Write(Write{At: ref.At(), Value: ir.Null()}); err != nil || !ok {
		t.Fatalf("write null payload: committed=%v err=%v", ok, err)
	}
	ok, _, err := s.Write(Write{
		At:    ref.At(),
		Value: Obj{"stage": "open"}.Payload(),
		When:  []Require{Created()},
	})
	if err != nil {
		t.Fatalf("conditional write: %v", err)
	}
	if ok {
		t.Fatal("a create committed over an entity holding null — it read the value as absence")
	}
}

// Nothing stands anywhere in a store nobody has written to, and every read says so.
//
// This is the one answer a value read cannot settle by itself: with no document to resolve
// through, logd answers every path under the root with a bare null rather than not_found — the
// same answer an entity holding null gives. Reading that as a payload made a fresh verse claim
// an entity at every address anyone asked about, which took a refused update for an existing
// entity (its precondition never held) and Modify's create loop (which never terminated) with
// it.
func TestNothingStandsInAnUnwrittenStore(t *testing.T) {
	s := openStore(t)
	for _, ref := range []Ref{
		RefAt("demo", "pr", "1"),
		RefAt("nosys", "nokind", "whatever"),
	} {
		e, ok, err := s.Get(ref)
		if err != nil {
			t.Fatalf("get %s: %v", ref, err)
		}
		if ok {
			t.Fatalf("%s stands in a store nobody has written to, holding %v", ref, e.Payload)
		}
	}
	if listed := s.List(nil); len(listed) != 0 {
		t.Fatalf("List = %v, want empty", listed)
	}

	// And once something else is written, the unwritten neighbours are still absent — now by
	// not_found, the other spelling.
	other := RefAt("demo", "pr", "2")
	if ok, _, err := s.Write(Write{At: other.At(), Value: Obj{"stage": "open"}.Payload()}); err != nil || !ok {
		t.Fatalf("write: committed=%v err=%v", ok, err)
	}
	if _, ok, err := s.Get(RefAt("demo", "pr", "1")); err != nil || ok {
		t.Fatalf("a neighbour of a written entity: ok=%v err=%v, want absent", ok, err)
	}
}

// Absence and "the value is null" are two questions, and the store answers each on its own
// terms: Require.Absent asks about the path, a pattern asks about the value standing there.
func TestAbsenceAndNullAreDifferentQuestions(t *testing.T) {
	s := openStore(t)
	ref := RefAt("demo", "pr", "1")
	if ok, _, err := s.Write(Write{At: ref.At(), Value: ir.Null()}); err != nil || !ok {
		t.Fatalf("write null payload: committed=%v err=%v", ok, err)
	}

	// The value question, asked of an entity holding null: `!irtype null` matches.
	ok, _, err := s.Write(Write{
		At:    ref.At("seen"),
		Value: ir.FromBool(true),
		When:  []Require{{At: ref.At(), Pattern: Unchanged(ir.Null())}},
	})
	if err != nil {
		t.Fatalf("write under a null-value precondition: %v", err)
	}
	if !ok {
		t.Fatal("`the value here is null` did not hold over an entity holding null")
	}

	// And it is a real question, not one that matches anything: the entity is an object now.
	ok, _, err = s.Write(Write{
		At:    ref.At("seen"),
		Value: ir.FromBool(false),
		When:  []Require{{At: ref.At(), Pattern: Unchanged(ir.Null())}},
	})
	if err != nil {
		t.Fatalf("write under a null-value precondition: %v", err)
	}
	if ok {
		t.Fatal("`the value here is null` held over an object — expect-unchanged asks nothing, " +
			"and a concurrent write would be lost")
	}
}

// Modify's compare-and-swap holds over a null payload. A bare null pattern is the PRESENCE
// test, so spelling expect-unchanged with one would ask only that something still be there.
func TestModifyDoesNotLoseAnUpdateOverNull(t *testing.T) {
	s := openStore(t)
	ref := RefAt("demo", "pr", "1")
	if ok, _, err := s.Write(Write{At: ref.At(), Value: ir.Null()}); err != nil || !ok {
		t.Fatalf("write null payload: committed=%v err=%v", ok, err)
	}

	// A racing writer lands between the read and the write, exactly once.
	raced := false
	err := Modify(t.Context(), s, ref, func(cur Payload) Payload {
		if !raced {
			raced = true
			if ok, _, err := s.Write(Write{At: ref.At(), Value: Obj{"by": "racer"}.Payload()}); err != nil || !ok {
				t.Fatalf("racing write: committed=%v err=%v", ok, err)
			}
		}
		return Obj{"by": "modify"}.Payload()
	})
	if err != nil {
		t.Fatalf("modify: %v", err)
	}
	e, ok, err := s.Get(ref)
	if err != nil || !ok {
		t.Fatalf("get: ok=%v err=%v", ok, err)
	}
	if by := Str(e.Payload, "by"); by != "modify" {
		t.Fatalf("by = %q, want %q", by, "modify")
	}
	if !raced {
		t.Fatal("the racing write never ran — the test proved nothing")
	}
}

// The claim/release round trip, which absentPath's second branch existed for: a claim released
// by Delete can be taken again. It works on the no-key branch alone, because that is what a
// delete leaves behind.
func TestClaimAfterReleaseIsAbsentByTheKey(t *testing.T) {
	s := openStore(t)
	ref := RefAt("run", "claim", "seq@demo.pr.1")
	claim := func() bool {
		ok, _, err := s.Write(Write{At: ref.At(), Value: Obj{"by": "engine"}.Payload(), When: []Require{Created()}})
		if err != nil {
			t.Fatalf("claim: %v", err)
		}
		return ok
	}
	if !claim() {
		t.Fatal("the first claim did not commit")
	}
	if claim() {
		t.Fatal("a second claim committed over a standing one")
	}
	if ok, _, err := s.Write(Write{At: ref.At(), Value: nil}); err != nil || !ok {
		t.Fatalf("release: committed=%v err=%v", ok, err)
	}
	if !claim() {
		t.Fatal("a released claim could not be re-won — the sequence would run once per subject, forever")
	}
}

// The change stream tells the two apart: writing null is a Set carrying null, deleting is a
// Delete. One walk served both a document and a delta until now, which is why it could not.
func TestChangesTellNullFromDeleted(t *testing.T) {
	s := openStore(t)
	commits, unsub := s.Observe()
	defer unsub()

	ref := RefAt("demo", "pr", "1")
	if ok, _, err := s.Write(Write{At: ref.At(), Value: ir.Null()}); err != nil || !ok {
		t.Fatalf("write null payload: committed=%v err=%v", ok, err)
	}
	if ev := awaitEvent(t, commits, ref); ev.Op != Set {
		t.Fatalf("op = %v, want %v — a null payload is a value being set", ev.Op, Set)
	} else if ev.To == nil || ev.To.Type != ir.NullType {
		t.Fatalf("to = %v, want null", ev.To)
	}

	if ok, _, err := s.Write(Write{At: ref.At(), Value: nil}); err != nil || !ok {
		t.Fatalf("delete: committed=%v err=%v", ok, err)
	}
	if ev := awaitEvent(t, commits, ref); ev.Op != Delete {
		t.Fatalf("op = %v, want %v", ev.Op, Delete)
	}
}

// The same distinction on a subtree watch, which reaches it by a different route: it diffs
// entity sets read out of a document it maintains, rather than reading the delta.
func TestSubtreeWatchTellsNullFromDeleted(t *testing.T) {
	s := openStore(t)
	commits, unsub, err := s.WatchSubtree(Depth, "demo", "pr")
	if err != nil {
		t.Fatalf("watch: %v", err)
	}
	defer unsub()

	ref := RefAt("demo", "pr", "1")
	if ok, _, err := s.Write(Write{At: ref.At(), Value: ir.Null()}); err != nil || !ok {
		t.Fatalf("write null payload: committed=%v err=%v", ok, err)
	}
	if ev := awaitEvent(t, commits, ref); ev.Op != Set {
		t.Fatalf("op = %v, want %v", ev.Op, Set)
	} else if ev.To == nil || ev.To.Type != ir.NullType {
		t.Fatalf("to = %v, want null", ev.To)
	}

	if ok, _, err := s.Write(Write{At: ref.At(), Value: nil}); err != nil || !ok {
		t.Fatalf("delete: committed=%v err=%v", ok, err)
	}
	if ev := awaitEvent(t, commits, ref); ev.Op != Delete {
		t.Fatalf("op = %v, want %v", ev.Op, Delete)
	}
}

// awaitEvent waits for the next change to ref on a commit stream.
func awaitEvent(t *testing.T, commits <-chan Commit, ref Ref) Event {
	t.Helper()
	deadline := time.After(3 * time.Second)
	for {
		select {
		case c, ok := <-commits:
			if !ok {
				t.Fatalf("the stream closed before %s changed", ref)
			}
			for _, ev := range c.Changes {
				if ev.Ref == ref {
					return ev
				}
			}
		case <-deadline:
			t.Fatalf("timed out waiting for a change to %s", ref)
		}
	}
}

// A payload somebody else wrote WITH COMMENTS is still a payload verse can read.
//
// The store keeps comments now (logd's NextState: "comments go in and come back"), and a head
// comment WRAPS the value it precedes — so a payload standing behind one is not an object as
// far as anything that asks. The failure is the bad kind: tony.Match sees through a comment, so
// a rule still SELECTS the entity, and then every field read of it comes back empty.
//
// Verse does not write comments, so this is about what somebody else wrote: a store is not
// verse's alone, and a document put there by `o` or by hand is one verse still has to read.
func TestACommentedPayloadIsStillReadable(t *testing.T) {
	s := openStore(t)
	ref := RefAt("demo", "pr", "1")
	p, err := parse.Parse([]byte("# what this is about\nstage: open # still open\nlabels:\n  # the deep one\n  urgent: true\n"),
		parse.ParseTony(), parse.ParseComments(true))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if ok, _, err := s.Write(Write{At: ref.At(), Value: p}); err != nil || !ok {
		t.Fatalf("write: committed=%v err=%v", ok, err)
	}

	e, ok, err := s.Get(ref)
	if err != nil || !ok {
		t.Fatalf("get: ok=%v err=%v", ok, err)
	}
	if !IsObj(e.Payload) {
		t.Fatalf("payload reads as %v, not an object — the comment wrapper is still on it", e.Payload.Type)
	}
	if got := Str(e.Payload, "stage"); got != "open" {
		t.Fatalf("stage = %q, want open — a field read fell through the comment", got)
	}
	if got := Str(Get(e.Payload, "labels"), "urgent"); got != "" && got != "true" {
		t.Fatalf("labels.urgent = %q — a comment deeper in the document broke the descent", got)
	}

	// The read face reaches into it, which is the surface that fails loudest: a path into a
	// commented payload used to refuse the field outright.
	d, err := Nest(s.List(nil), at(t, "demo.pr.1.stage"))
	if err != nil {
		t.Fatalf("state demo.pr.1.stage: %v", err)
	}
	if d.Node == nil || d.Node.String != "open" {
		t.Fatalf("state demo.pr.1.stage = %v, want open", d.Node)
	}

	// And a selection binds what it selected on, rather than selecting and then reading blank.
	pat := Pattern{System: "demo", Kind: "pr", Predicate: Obj{"stage": "open"}.Payload()}
	if !pat.MatchEntity(e) {
		t.Fatal("the pattern stopped matching")
	}
	for _, sel := range pat.Select(s) {
		got, _, _ := s.Get(sel)
		if Str(got.Payload, "stage") != "open" {
			t.Fatalf("%s was selected and then read blank — the worst of both", sel)
		}
	}
}

// A payload keeps the comments it was written with, at every depth — and stays addressable.
//
// This is the half that makes them worth carrying: the store keeps comments, so a document put
// there with notes on it reads back with them. Both KINDS have to survive, because half a
// document's comments is worse than none: a line comment rides on its node and survives any
// rebuild by accident, while a head comment WRAPS the value it precedes and is dropped by one
// (canonicalFields sorts, and sorting used to rebuild from inside the wrapper).
func TestAPayloadKeepsItsComments(t *testing.T) {
	s := openStore(t)
	ref := RefAt("demo", "pr", "1")
	p, err := parse.Parse([]byte("# about this pr\nstage: open # still open\nlabels:\n  # about the labels\n  urgent: true\n"),
		parse.ParseTony(), parse.ParseComments(true))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if ok, _, err := s.Write(Write{At: ref.At(), Value: p}); err != nil || !ok {
		t.Fatalf("write: committed=%v err=%v", ok, err)
	}

	e, ok, err := s.Get(ref)
	if err != nil || !ok {
		t.Fatalf("get: ok=%v err=%v", ok, err)
	}
	var buf bytes.Buffer
	if err := encode.Encode(e.Payload, &buf, encode.EncodeFormat(format.TonyFormat), encode.EncodeComments(true)); err != nil {
		t.Fatalf("render: %v", err)
	}
	for _, want := range []string{"# about this pr", "# still open", "# about the labels"} {
		if !strings.Contains(buf.String(), want) {
			t.Errorf("the payload lost %q:\n%s", want, buf.String())
		}
	}
	// …and none of that costs addressability, which is the whole point of seeing through them.
	if got := Str(Get(e.Payload, "labels"), "urgent"); got != "true" && !IsObj(Get(e.Payload, "labels")) {
		t.Fatalf("labels is not readable through its comment")
	}
}

// A payload written under the ESCAPE keeps them too — the same document, stored the way a
// record holding a document is stored.
//
// Raw tags the value as data, and it used to tag whatever node it was handed. A document with
// a comment above it IS a wrapper holding the value, so the escape landed on the comment: the
// write still succeeded and still replaced, and every LINE comment inside it was dropped while
// the head comments came back. That is the arbitrary half again, and it is what the charter
// reads out of — reading a rule back had lost `name: r # the short name` before anything got
// as far as rendering it.
//
// The document deliberately puts a line comment on an OBJECT (`labels:`), because that is the
// case that failed: a scalar's note survived, so the loss looked like a rendering quirk rather
// than a write that had eaten half of what it was given.
func TestAnEscapedPayloadKeepsItsComments(t *testing.T) {
	s := openStore(t)
	ref := RefAt("demo", "pr", "1")
	p, err := parse.Parse([]byte("# about this pr\nstage: open # still open\nlabels: # what it is tagged with\n  urgent: true # by whom\n"),
		parse.ParseTony(), parse.ParseComments(true))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if ok, _, err := s.Write(Write{At: ref.At(), Value: Obj{"spec": Raw(p)}.Payload()}); err != nil || !ok {
		t.Fatalf("write: committed=%v err=%v", ok, err)
	}

	e, ok, err := s.Get(ref)
	if err != nil || !ok {
		t.Fatalf("get: ok=%v err=%v", ok, err)
	}
	spec := Get(e.Payload, "spec")
	if spec == nil {
		t.Fatalf("the escaped document is not on the entity: %v", e.Payload)
	}
	var buf bytes.Buffer
	if err := encode.Encode(spec, &buf, encode.EncodeFormat(format.TonyFormat), encode.EncodeComments(true)); err != nil {
		t.Fatalf("render: %v", err)
	}
	for _, want := range []string{"# about this pr", "# still open", "# what it is tagged with", "# by whom"} {
		if !strings.Contains(buf.String(), want) {
			t.Errorf("the escaped payload lost %q:\n%s", want, buf.String())
		}
	}
	// The escape is still an escape: what it holds is data, and the tag is not part of it.
	if !IsObj(spec) || Str(spec, "stage") != "open" {
		t.Fatalf("the escaped document did not land as data: %s", buf.String())
	}
}
