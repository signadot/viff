package mirror

import (
	"testing"

	"github.com/signadot/verse/pkg/entity"
)

// AN ASK SHOWS WHAT BECAME OF IT: `pending` only while it waits, its answer once it has one, and,
// where it settled with nobody to answer it, its status and why rather than `pending`, so whoever
// reads it back does not wait on what nobody will answer.
func TestAnAskShowsWhatBecameOfIt(t *testing.T) {
	row := func(rec map[string]any) entity.Obj {
		return AskShown(entity.Entity{Ref: AskRef("a-1"), Payload: entity.Val(rec)})
	}
	if got := row(map[string]any{"id": "a-1", "status": QuestionPending, "waitingOn": []any{"user:ana@example.com"}}); got["answer"] != "pending" || got["status"] != QuestionPending || got["waitingOn"] == nil {
		t.Errorf("a waiting ask shows %v", got)
	}
	if got := row(map[string]any{"id": "a-1"}); got["answer"] != "pending" {
		t.Errorf("an ask not yet posed shows %v, want it pending", got)
	}
	if got := row(map[string]any{"id": "a-1", "status": QuestionAnswered, "answer": "yes"}); got["answer"] != "yes" || got["status"] != QuestionAnswered {
		t.Errorf("an answered ask shows %v", got)
	}
	got := row(map[string]any{"id": "a-1", "status": QuestionOrphaned, "why": "it could not be asked: ana cannot read it"})
	if _, ok := got["answer"]; ok || got["status"] != QuestionOrphaned || got["why"] != "it could not be asked: ana cannot read it" {
		t.Errorf("an ask settled with no answer shows %v, want its status and why and no answer", got)
	}
	got = row(map[string]any{"id": "a-1", "unreadable": "field due: not a date"})
	if _, ok := got["answer"]; ok || got["unreadable"] != "field due: not a date" {
		t.Errorf("an ask that does not read shows %v, want that it does not read and no answer", got)
	}
}
