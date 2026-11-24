package entity

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"

	"github.com/signadot/tony-format/go-tony/encode"
	"github.com/signadot/tony-format/go-tony/format"
	"github.com/signadot/tony-format/go-tony/ir"
	"github.com/signadot/tony-format/go-tony/parse"
)

// WHAT THE SUBSTRATE DOES WITH AN ARRAY PATH, checked rather than assumed — three answers to
// three paths that look alike:
//
//	votes[0].choice     an IN-RANGE index: the patch lands and reads back
//	votes(walter).choice a KEYED element:   refused at the commit, no harm done
//	votes[2] on 2       PAST THE END:      refused at the patch, naming the length
//
// The third one is the history worth keeping. It used to be ACCEPTED, and the log was then
// unreadable — a bad write that succeeded and took every later read with it. That is what
// `ParsePath`'s refusal of array element writes was resting on, and it is fixed upstream as of
// go-tony v0.0.149 (tony-format 7cdvym1fh12ksmd5g5n0), which is why the refusal now rests on
// something else: `Path.Field` is a list of object field names, and offering `[0]` or `(walter)`
// means teaching the write path — and its preconditions — to carry a kpath segment. That is a
// feature, filed rather than assumed away.
//
// The READ side is unaffected and does offer both spellings: it navigates a materialized
// document in Go (entity.Nest), not a kpath handed to the substrate.

// An in-range index patch works, so "the substrate cannot write into an array" is false.
func TestTheSubstrateDoesPatchAnInRangeArrayElement(t *testing.T) {
	s := openStore(t)
	ref := RefAt("demo", "pr", "1")
	seedVotes(t, s, ref)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if _, err := s.sess.Patch(ctx, s.refPath(ref)+".votes[1].choice", ir.FromString("approve")); err != nil {
		t.Fatalf("patch at an in-range index: %v", err)
	}

	got := renderPayload(t, s, ref)
	if !strings.Contains(got, "choice: approve") || strings.Contains(got, "reject") {
		t.Fatalf("the element did not change: %s", got)
	}
}

// A `(key)` element is a different answer: the substrate refuses it, and nothing is damaged.
//
// TWICE OVER NOW. The array below carries no `!key` tag, because viff refuses one on the way in
// (checkNotKeyed): identity is the store's, given by the schema, and the store's schema keys
// nothing here. So `(walter)` names no element of a positional array, which is what the patch is
// told -- and it is why an element is addressed by position everywhere viff spells an address.
func TestTheSubstrateRefusesAKeyedElementPatch(t *testing.T) {
	s := openStore(t)
	ref := RefAt("demo", "pr", "1")
	if _, _, err := s.Write(Write{At: ref.At(), Value: Raw(mustParseNode(t,
		`{votes: [{by: walter, choice: approve}, {by: dee, choice: reject}]}`))}); err != nil {
		t.Fatalf("seed: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if _, err := s.sess.Patch(ctx, s.refPath(ref)+".votes(walter).choice", ir.FromString("reject")); err == nil {
		t.Fatal("a keyed-element patch was accepted; the refusal's reasoning assumes it is not")
	}
	// Unharmed: the entity still reads, and still says what it said.
	if got := renderPayload(t, s, ref); !strings.Contains(got, "choice: approve") {
		t.Fatalf("a refused patch changed something: %s", got)
	}
}

// AND THE ONE THE REFUSAL RESTS ON. An index PAST THE END is refused, and refused at the write
// — where the array's length is known and a caller can be told.
//
// It was not always. Until go-tony v0.0.149 this patch was ACCEPTED and the log was then
// unreadable: every later read replayed it and failed, so the entity, its neighbours and the
// whole listing were gone, unrepairably, from one write (tony-format 7cdvym1fh12ksmd5g5n0). The
// test that pinned that is this one, inverted — it skipped on a refusal and said to re-take the
// decision, which is what happened when the fix landed.
//
// Kept, because a promise this cheap to check should not be inherited: the reason viff refuses
// array element writes is no longer "it would take the store with it", and if this ever starts
// being accepted again the reason has to be re-taken a second time.
func TestAnArrayPatchPastTheEndIsRefused(t *testing.T) {
	addr := startLogd(t)
	s := openStoreAt(t, addr)
	ref := RefAt("demo", "pr", "1")
	other := RefAt("demo", "pr", "2")
	seedVotes(t, s, ref)
	if _, _, err := s.Write(Write{At: other.At(), Value: Obj{"stage": "open"}.Payload()}); err != nil {
		t.Fatalf("seed the neighbour: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	// Two elements stand there; this addresses the third.
	_, err := s.sess.Patch(ctx, s.refPath(ref)+".votes[2]", mustParseNode(t, `{by: ana}`))
	if err == nil {
		t.Fatal("a patch past the end of an array was accepted — the log is now unreadable, and " +
			"the reasoning in ParsePath and reference/entities.md rests on it not being")
	}
	if !strings.Contains(err.Error(), "past the end") {
		t.Errorf("the refusal does not say what is wrong: %v", err)
	}

	// And NOTHING IS DAMAGED, which is the half that changed: the entity reads, its neighbour
	// reads, and the listing every read plane is built on still answers.
	if got := renderPayload(t, s, ref); !strings.Contains(got, "choice: approve") {
		t.Errorf("the refused patch changed the entity: %s", got)
	}
	if _, ok, err := s.Get(other); err != nil || !ok {
		t.Errorf("a neighbour entity is unreadable after a refused patch: ok=%v err=%v", ok, err)
	}
	if n := len(s.List(func(Ref) bool { return true })); n != 2 {
		t.Errorf("List saw %d entities, want 2 — the log did not survive a refused patch", n)
	}
}

func seedVotes(t *testing.T, s *LogdStore, ref Ref) {
	t.Helper()
	if _, _, err := s.Write(Write{At: ref.At(), Value: Raw(mustParseNode(t,
		`{votes: [{by: walter, choice: approve}, {by: dee, choice: reject}]}`))}); err != nil {
		t.Fatalf("seed: %v", err)
	}
}

func renderPayload(t *testing.T, s *LogdStore, ref Ref) string {
	t.Helper()
	e, ok, err := s.Get(ref)
	if err != nil || !ok {
		t.Fatalf("get %s: ok=%v err=%v", ref, ok, err)
	}
	var buf bytes.Buffer
	if err := encode.Encode(payloadToNode(e.Payload), &buf, encode.EncodeFormat(format.TonyFormat)); err != nil {
		t.Fatalf("encode: %v", err)
	}
	return buf.String()
}

func mustParseNode(t *testing.T, src string) *ir.Node {
	t.Helper()
	n, err := parse.Parse([]byte(src), parse.ParseTony())
	if err != nil {
		t.Fatalf("parse %q: %v", src, err)
	}
	return n
}
