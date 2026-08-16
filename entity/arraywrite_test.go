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

// WHY A WRITE PATH IS OBJECT FIELDS, checked against the substrate rather than assumed.
//
// `ParsePath` refuses `[0]` and `(scott)`, and the reason written beside it used to be "the
// store patches at a kpath of object fields, so the substrate would reject it". That is not
// true, and these tests are what it is instead — three different answers to three paths that
// look alike:
//
//	votes[0].choice     an IN-RANGE index: the patch lands and reads back
//	votes(scott).choice a KEYED element:   refused by the substrate, no harm done
//	votes[2] on 2       PAST THE END:      accepted, and then nothing in the store can be read
//
// The third is why the refusal stays. It is not a bad write that fails; it is a bad write that
// SUCCEEDS and takes the log with it. Whether an index is in range is a fact about the value at
// the time of the write, which a patch does not check and verse cannot check without a read it
// would then be racing. So the CLI refuses the whole shape, and these tests pin the premise —
// including the fatal one, which will start failing when go-tony fixes it, at which point this
// decision is worth re-taking rather than inheriting.
//
// The READ side is unaffected and does offer both spellings: it navigates a materialized
// document in Go (entity.Nest), not a kpath handed to the substrate.

// An in-range index patch works, so "the substrate cannot write into an array" is false.
func TestTheSubstrateDoesPatchAnInRangeArrayElement(t *testing.T) {
	s := openStore(t)
	ref := Ref{System: "demo", Kind: "pr", ID: "1"}
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

// A KEYED element is a different answer: the substrate refuses it, and nothing is damaged.
// `(key)` is therefore not a spelling a write could be given even if the shape were offered.
func TestTheSubstrateRefusesAKeyedElementPatch(t *testing.T) {
	s := openStore(t)
	ref := Ref{System: "demo", Kind: "pr", ID: "1"}
	if _, _, err := s.Write(Write{At: ref.At(), Value: Raw(mustParseNode(t,
		`{votes: !key(by) [{by: scott, choice: approve}, {by: dee, choice: reject}]}`))}); err != nil {
		t.Fatalf("seed: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if _, err := s.sess.Patch(ctx, s.refPath(ref)+".votes(scott).choice", ir.FromString("reject")); err == nil {
		t.Fatal("a keyed-element patch was accepted; the refusal's reasoning assumes it is not")
	}
	// Unharmed: the entity still reads, and still says what it said.
	if got := renderPayload(t, s, ref); !strings.Contains(got, "choice: approve") {
		t.Fatalf("a refused patch changed something: %s", got)
	}
}

// AND THE ONE THAT MATTERS. An index PAST THE END is accepted by the substrate and poisons the
// log: every subsequent read replays that patch and fails, so the entity, its neighbours and
// the whole listing are gone — from a client that only ever wrote one field.
//
// It cannot be undone from here. A fresh session replays the same log; deleting the field and
// deleting the entity are themselves patches appended after the bad one, and the read still
// dies on the way past it.
//
// This is a go-tony defect, not a verse policy, and it is pinned here because a policy of ours
// rests on it: `ParsePath` refuses array paths, so verse itself can never emit this patch.
func TestAnArrayPatchPastTheEndPoisonsTheLog(t *testing.T) {
	addr := startLogd(t)
	s := openStoreAt(t, addr)
	ref := Ref{System: "demo", Kind: "pr", ID: "1"}
	other := Ref{System: "demo", Kind: "pr", ID: "2"}
	seedVotes(t, s, ref)
	if _, _, err := s.Write(Write{At: other.At(), Value: Obj{"stage": "open"}.Payload()}); err != nil {
		t.Fatalf("seed the neighbour: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	// Two elements stand there; this addresses the third.
	if _, err := s.sess.Patch(ctx, s.refPath(ref)+".votes[2]", mustParseNode(t, `{by: ana}`)); err != nil {
		t.Skipf("the substrate now refuses a patch past the end (%v) — good news, and the "+
			"reasoning in ParsePath and reference/entities.md should be re-taken", err)
	}

	if _, _, err := s.Get(ref); err == nil {
		t.Fatal("the entity still reads — the defect this pins is fixed; re-take the refusal")
	}
	// The blast radius is the LOG, not the entity: a neighbour nothing touched is unreadable
	// too, and the listing every read plane is built on comes back empty.
	if _, _, err := s.Get(other); err == nil {
		t.Error("a neighbour entity still reads; the damage is narrower than this test claims")
	}
	if n := len(s.List(func(Ref) bool { return true })); n != 0 {
		t.Errorf("List saw %d entities; the damage is narrower than this test claims", n)
	}
	// And it is not repairable from a client: not by a new session, not by removing the field,
	// not by removing the entity.
	if _, _, err := openStoreAt(t, addr).Get(ref); err == nil {
		t.Error("a fresh session reads it; the log is not poisoned after all")
	}
	if _, _, err := s.Write(Write{At: Path{Ref: ref, Field: []string{"votes"}}}); err != nil {
		t.Fatalf("removing the field: %v", err)
	}
	if _, _, err := s.Write(Write{At: ref.At()}); err != nil {
		t.Fatalf("removing the entity: %v", err)
	}
	if _, _, err := s.Get(ref); err == nil {
		t.Error("removing the entity repaired the log; say so rather than calling it fatal")
	}
}

func seedVotes(t *testing.T, s *LogdStore, ref Ref) {
	t.Helper()
	if _, _, err := s.Write(Write{At: ref.At(), Value: Raw(mustParseNode(t,
		`{votes: [{by: scott, choice: approve}, {by: dee, choice: reject}]}`))}); err != nil {
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
