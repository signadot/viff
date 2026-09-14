package versed

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/signadot/verse/entity"
	"github.com/signadot/verse/entity/entitytest"
	"github.com/signadot/verse/mirror"
	"github.com/signadot/verse/resolver"
	"github.com/signadot/verse/trigger"
)

// waitAnyQuestion returns the first pending question to appear, whoever raised it.
func waitAnyQuestion(t *testing.T, srv *Server) string {
	t.Helper()
	for range 300 {
		for _, ent := range srv.Store.List(func(ref entity.Ref) bool {
			return ref.System() == mirror.QuestionSystem && ref.Kind() == mirror.QuestionKind
		}) {
			if entity.ToObj(ent.Payload)["status"] == mirror.QuestionPending {
				return ent.Ref.ID()
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("no question was ever posed")
	return ""
}

// compacted is a store whose history is GONE at any cursor — what logd answers for a revision
// it has compacted past, and the case a resolver meets after being away. It names that failure
// rather than a bare no: the store has to say which of its failures this is, because a resolver
// that missed history and a store that cannot be reached are not the same predicament.
type compacted struct{ entity.Store }

func (compacted) Since(from int64, _ ...string) ([]entity.Commit, error) {
	return nil, fmt.Errorf("replaying verse from %d: %w", from, entity.ErrReplayTooOld)
}

// A resolver over a SUBJECT that resumes below the retained window must be told that it happened
// AND what is still open.
//
// It used to be told neither: streamSpine sent nothing at all when Since refused, so a resolver
// reconnected, learned nothing, and sat waiting for a question that had already been asked. It
// has no second path to find that out — the design says the question appears in its stream and
// nowhere else — so silence there is a resolver that never answers.
func TestASubjectStreamBelowTheWindowSaysWhatIsOpen(t *testing.T) {
	srv := NewWithStore(askingBuilder(t), compacted{entitytest.Store(t)})
	srv.Start(t.Context())
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	installResolver(t, ts.URL, standing("payments-review", "payments")).Body.Close()
	// A FIRING's question, not a run's: its origin is the SUBJECT, because a firing has no
	// event stream of its own — which is the whole reason this path exists.
	if err := srv.Eng.AddTrigger(trigger.Trigger{
		Name:      "review",
		Condition: trigger.Condition{Subject: trigger.Match{"system": "change", "kind": "pr"}},
		Gating:    &trigger.Gating{Gates: []trigger.Gate{{Name: "sec", Mode: "ask"}}},
		Action:    trigger.Action{Patch: entity.Obj{"seen": true}.Payload()},
	}); err != nil {
		t.Fatalf("AddTrigger: %v", err)
	}
	pr(t, srv, "1", "payments")
	qid := waitAnyQuestion(t, srv)

	inst := resolver.InstanceRef("payments-review", mirror.OriginKey(entity.RefAt("change", "pr", "1"))).ID()
	_, lines, stop := sseData(t, http.DefaultClient, ts.URL+"/resolvers/instance/"+inst+"/events?since=1")
	defer stop()

	var gap, sawQuestion bool
	deadline := time.After(3 * time.Second)
read:
	for !sawQuestion {
		select {
		case d, ok := <-lines:
			if !ok {
				break read
			}
			gap = gap || strings.Contains(d, "gap:")
			sawQuestion = strings.Contains(d, qid)
		case <-deadline:
			break read
		}
	}

	if !gap {
		t.Fatal("resuming below the window said nothing happened: no gap marker")
	}
	if !sawQuestion {
		t.Fatalf("the gap was announced and %s — still open, and this resolver's to answer — "+
			"was not replayed, so it has no way to learn the question exists", qid)
	}
}
