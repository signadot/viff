package trigger

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/signadot/tony-format/go-tony/ir"

	"github.com/signadot/verse/codec"
	"github.com/signadot/verse/entity"
	"github.com/signadot/verse/entity/entitytest"
	"github.com/signadot/verse/task"
)

// A COMMENT MUST NOT CHANGE WHAT A RULE DOES, and it did: a `!get-path` written under one was
// invisible to both walks that have to see it, and the rule silently never fired.
//
// A head comment above a list element makes that element (or the list) a CommentType node
// holding the real one. `eachPathRead` and `expandInto` both switched on Object/Array and
// stopped at the wrapper, so a read under a comment was neither counted among what the rule
// READS — leaving its subtree out of the snapshot the condition is evaluated against — nor
// EXPANDED, leaving `!get-path` in the document that was handed to tony.Match, which does not
// know that operator. A predicate that cannot be evaluated is a non-match, so the rule selected
// nothing, at every commit, forever, with nothing said anywhere.
//
// It cost a live drive of the merge train and a long hunt (git-issue 5dfazxf5h12krxvtg9n0): the
// same predicate fired in a test with no comments and never fired in the file it was written in.
// In a codebase whose charters are more comment than rule, that is the worst possible place for
// a walk to stop.
func TestACommentDoesNotHideAReadFromTheWalks(t *testing.T) {
	const let = "- !let\n  let: [{tip: !get-path {at: [git, head, verse], path: sha}}]\n  in: {sha: .[tip]}\n"
	for _, tc := range []struct{ name, src string }{
		{"no comment", "!and\n- !has-path branches\n" + let},
		{"a comment above the read", "!and\n- !has-path branches\n# why this clause is here\n" + let},
		{"a comment above an earlier clause", "!and\n# why this clause is here\n- !has-path branches\n" + let},
		{"comments above both", "!and\n# one\n- !has-path branches\n# two\n" + let},
	} {
		n, err := codec.ParseNode([]byte(tc.src), codec.Tony)
		if err != nil {
			t.Errorf("%s: %v", tc.name, err)
			continue
		}
		specs, err := readTargets(n)
		if err != nil || len(specs) != 1 {
			t.Errorf("%s: the walk found %d reads (err %v) — the rule's snapshot would not hold "+
				"what it reads", tc.name, len(specs), err)
		}
		out, err := expandPatch(n, ir.FromMap(map[string]*ir.Node{}),
			func(*ir.Node, entity.Payload) (entity.Payload, error) { return ir.FromString("aaa111"), nil })
		if err != nil {
			t.Errorf("%s: expand: %v", tc.name, err)
			continue
		}
		var s strings.Builder
		if err := codec.EncodeNode(&s, out, codec.Tony); err != nil {
			t.Fatal(err)
		}
		if strings.Contains(s.String(), "get-path") {
			t.Errorf("%s: the read was left in the predicate, which tony.Match cannot evaluate:\n%s",
				tc.name, s.String())
		}
	}
}

// And the whole way through: a rule whose condition reads another entity, with the comments a
// charter author would actually write around it, selects and fires.
func TestARuleWithCommentsAroundItsReadFires(t *testing.T) {
	store := entitytest.Store(t)
	eng := New(store, task.AutoResolver{})
	setPayload(t, store, entity.RefAt("git", "head", "verse"), `{repo: verse, sha: aaa111}`)
	// The id an engine writes, colons and all — the shape the live charter's subjects have.
	subj := entity.RefAt("verse-dev", "merge", "train", "train-cut@git:head:verse@aaa111")
	setPayload(t, store, subj, `{sha: aaa111, branches: [one]}`)

	if err := eng.AddTrigger(mustParseCharter(t, `
- name: run
  condition:
    subject:
      path: [verse-dev, merge, train, "*"]
      value: !and
      # PRESENT AND NON-EMPTY, in that order.
      - !has-path branches
      - !not {branches: []}
      # UNDECIDED, which is the absence of ` + "`decided`" + `.
      - !not.has-path decided
      # AND ABOUT THE MAIN THAT IS ACTUALLY THERE: the address carries the sha, so a new main
      # is a new train and a new question.
      - !let
        let: [{tip: !get-path {at: [git, head, verse], path: sha}}]
        in: {sha: .[tip]}
  action:
    patch: {hit: yes}
`)); err != nil {
		t.Fatalf("install: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	eng.ObserveScoped(ctx)
	setPayload(t, store, subj, `{sha: aaa111, branches: [one], touched: 1}`)

	for i := 0; i < 150; i++ {
		if e, _, _ := store.Get(subj); entity.Str(e.Payload, "hit") != "" {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("the rule never selected: a comment in its predicate is still hiding the read")
}

// THE SAME BLINDNESS, IN THE CHECK THAT KEEPS A CASCADE FINITE. `eachRef` walks a derive's
// content looking for references it may not carry — `$subject.commit` above all, which is a
// function of WHEN the firing happened and so makes a derive write something new on every lap.
// It stopped at a comment wrapper too, so writing a comment above the field turned the check off
// and the derive installed.
func TestACommentDoesNotHideAReference(t *testing.T) {
	for _, tc := range []struct{ name, src string }{
		{"plain", "{at: $subject.commit}"},
		{"under a head comment", "# why this is here\n{at: $subject.commit}"},
		{"the field under a comment", "{ok: 1\n# why this is here\nat: $subject.commit}"},
	} {
		n, err := codec.ParseNode([]byte(tc.src), codec.Tony)
		if err != nil {
			t.Errorf("%s: %v", tc.name, err)
			continue
		}
		if got := firstBadRef(n); got != "subject.commit" {
			t.Errorf("%s: the content check found %q, so a derive carrying the firing's occasion "+
				"would install", tc.name, got)
		}
	}
}
