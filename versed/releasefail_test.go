package versed

import (
	"errors"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/signadot/verse/entity"
	"github.com/signadot/verse/mirror"
	"github.com/signadot/verse/resolver"
)

// listingFails is a store whose listings fail, as a busy store's time out: a read that did not
// happen, which is not a verse holding nothing.
type listingFails struct{ entity.Store }

func (l listingFails) ListAtErr([]string, int, func(entity.Ref) bool) ([]entity.Entity, error) {
	return nil, errors.New("the store is busy")
}

// questionWritesFail is a store whose writes to a question fail, and nothing else's.
type questionWritesFail struct{ entity.Store }

func (q questionWritesFail) Write(w entity.Write) (bool, int64, error) {
	questions := entity.RefAt(entity.VerseRoot, mirror.QuestionSystem, mirror.QuestionKind).String() + "."
	if strings.HasPrefix(w.At.String(), questions) {
		return false, 0, errors.New("the store is busy")
	}
	return q.Store.Write(w)
}

// ownedByAlpha is a standing resolver alpha and a run whose question it owns.
func ownedByAlpha(t *testing.T) (*Server, string) {
	t.Helper()
	srv := askingServer(t)
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)
	installResolver(t, ts.URL, standing("alpha", "payments")).Body.Close()
	pr(t, srv, "1", "payments")
	spawnFor(t, ts, "run-1", `change.pr."1"`, "")
	qid := waitQuestion(t, srv, "run-1")
	if got := waitOwned(t, srv, qid)["resolver"]; got != "verse.resolvers.alpha" {
		t.Fatalf("the fixture is wrong: owned by %v", got)
	}
	return srv, qid
}

// A RELEASE THAT COULD NOT READ WHAT IT HELD SAYS SO. Read as empty, the listing would hand on
// nothing, leave every question naming the resolver, and report success — which is how a
// removal on a busy verse stranded its questions on a name nothing answers.
func TestReleasingAResolverWhoseQuestionsCannotBeReadIsAnError(t *testing.T) {
	srv, qid := ownedByAlpha(t)
	store := listingFails{entity.As(srv.Store, entity.SystemDocket)}

	err := srv.releaseResolver(store, resolver.Ref("alpha"))
	if err == nil || !strings.Contains(err.Error(), "the store is busy") {
		t.Fatalf("release = %v, want the failed read", err)
	}
	if got := question(t, srv, qid)["resolver"]; got != "verse.resolvers.alpha" {
		t.Fatalf("the question moved though nothing could be read: %v", got)
	}
}

// A HANDOVER WHOSE WRITE FAILED IS NAMED, question by question, so whoever removed the resolver
// knows which questions still name it.
func TestReleasingAResolverWhoseHandoverCannotBeWrittenNamesTheQuestion(t *testing.T) {
	srv, qid := ownedByAlpha(t)
	store := questionWritesFail{entity.As(srv.Store, entity.SystemDocket)}

	err := srv.releaseResolver(store, resolver.Ref("alpha"))
	if err == nil || !strings.Contains(err.Error(), qid) {
		t.Fatalf("release = %v, want the question that was not handed on", err)
	}
}
