package versed

import (
	"bufio"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/signadot/verse/api"
	"github.com/signadot/verse/codec"
	"github.com/signadot/verse/entity"
	"github.com/signadot/verse/entity/entitytest"
	"github.com/signadot/verse/mirror"
	"github.com/signadot/verse/resolver"
)

// commitsOf reads a spine and returns the events it carried, stopping when it goes quiet.
func commitsOf(t *testing.T, url string) []entity.Event {
	t.Helper()
	resp, err := http.Get(url)
	if err != nil {
		t.Fatalf("spine: %v", err)
	}
	defer resp.Body.Close()
	lines := make(chan string, 256)
	go func() {
		sc := bufio.NewScanner(resp.Body)
		for sc.Scan() {
			if d, ok := strings.CutPrefix(sc.Text(), "data: "); ok {
				lines <- d
			}
		}
		close(lines)
	}()
	var out []entity.Event
	for {
		select {
		case d, ok := <-lines:
			if !ok {
				return out
			}
			var ev entity.Event
			if codec.Unmarshal([]byte(d), &ev, codec.Tony) == nil {
				out = append(out, ev)
			}
		case <-time.After(400 * time.Millisecond):
			return out
		}
	}
}

// A standing resolver reads its subject's history and answers a firing's gate.
//
// The evidence is the subject's history rather than a run's, because a firing has no event
// stream — it is over in a commit or two. Which is why the position here is a revision: the
// store keeps its commits, so a resolver minted at the first question still reads everything
// that led to it.
func TestASpineReadsASubjectAndAnswersAFiring(t *testing.T) {
	srv := testServer(t)
	srv.Start(t.Context())
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()
	installResolver(t, ts.URL, standing("payments-review", "payments")).Body.Close()
	addRule(t, ts, `{
		name: guard,
		condition: {subject: {system: change, kind: pr, value: {area: payments}}},
		gates:  [{name: approval, mode: ask}],
		action: {patch: {stage: reviewed}},
	}`)

	subject := entity.Ref{System: "change", Kind: "pr", ID: "1"}
	entitytest.Set(t, srv.Store, subject, entity.Obj{"area": "payments", "stage": "draft"}.Payload())
	entitytest.Set(t, srv.Store, subject, entity.Obj{"area": "payments", "stage": "proposed"}.Payload())
	waitOwner(t, srv, "guard")

	inst := resolver.InstanceRef("payments-review", subject.String())
	got := commitsOf(t, ts.URL+"/resolvers/instance/"+inst.ID+"/events?since=0")
	if len(got) < 2 {
		t.Fatalf("the spine carried %d commits, want the subject's history: %+v", len(got), got)
	}
	// Everything on it is ABOUT this subject: its own commits, the questions raised about it,
	// and the runs about it. Nothing else — the negative that matters is another subject's,
	// which TestTheSpineCarriesItsAnchors pins.
	var own int
	for _, ev := range got {
		switch {
		case ev.Ref == subject:
			own++
		case ev.Ref.System == mirror.QuestionSystem, ev.Ref.System == mirror.System:
		default:
			t.Fatalf("the spine carried a commit about %s", ev.Ref)
		}
	}
	if own < 2 {
		t.Fatalf("the spine carried %d of the subject's own commits, want its history", own)
	}

	// And it answers the firing's gate through its own instance.
	var qid string
	for _, ent := range srv.Store.List(func(ref entity.Ref) bool {
		return ref.System == mirror.QuestionSystem && ref.Kind == mirror.QuestionKind
	}) {
		qid = ent.Ref.ID
	}
	resp := postTony(t, ts.URL+"/resolvers/instance/"+inst.ID+"/verdicts",
		api.Verdict{ID: qid, Verdict: "proceed", Why: "read the history first"})
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("a spine's verdict = %s, want 204", resp.Status)
	}
	// The firing proceeds: its action lands on the subject.
	for range 400 {
		e, _, _ := srv.Store.Get(subject)
		if entity.Str(e.Payload, "stage") == "reviewed" {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	e, _, _ := srv.Store.Get(subject)
	t.Fatalf("the firing never proceeded: %+v", entity.ToObj(e.Payload))
}

// A whole life on one spine: born, marked vanishing, deleted, and written again under the same
// ref — four commits in order, to a resolver that never stopped reading.
//
// This is why the narrowing is by REF and not by the binding's condition. A predicate reads a
// payload and a deletion has none, so a condition-narrowed stream would hide exactly the end
// of a life it most needs to show. And nothing about the entity's death ends the watch: a
// position addresses the STORE's revision line, which does not die when an entity does.
func TestASpineCarriesAWholeLife(t *testing.T) {
	srv := testServer(t)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()
	installResolver(t, ts.URL, standing("payments-review", "payments")).Body.Close()

	subject := entity.Ref{System: "change", Kind: "pr", ID: "1"}
	mirror.Watch(srv.Store, "resolvers:security:payments-review", "payments-review", subject)
	inst := resolver.InstanceRef("payments-review", subject.String())
	before, _, _ := srv.Store.Get(inst)
	startedAt := entity.ToObj(before.Payload)["position"]

	entitytest.Set(t, srv.Store, subject, entity.Obj{"area": "payments"}.Payload())
	entitytest.Set(t, srv.Store, subject,
		entity.Obj{"area": "payments", entity.VanishingField: "2026-07-29T13:04:05Z"}.Payload())
	if _, _, err := srv.Store.Write(entity.Write{At: subject.At(), Value: nil}); err != nil {
		t.Fatalf("delete: %v", err)
	}
	entitytest.Set(t, srv.Store, subject, entity.Obj{"area": "payments"}.Payload())

	got := commitsOf(t, ts.URL+"/resolvers/instance/"+inst.ID+"/events?since=0")
	if len(got) != 4 {
		t.Fatalf("a whole life came through as %d commits, want 4: %+v", len(got), got)
	}
	var sawDelete, sawRebirth bool
	for i, ev := range got {
		if i > 0 && ev.Rev <= got[i-1].Rev {
			t.Fatalf("the spine is out of order at %d: %+v", i, got)
		}
		if ev.Op == entity.Delete {
			sawDelete = true
			continue
		}
		if sawDelete {
			sawRebirth = true
		}
	}
	if !sawDelete {
		t.Fatalf("the deletion never reached the spine — which is the commit a predicate "+
			"cannot see, since a delete carries no payload: %+v", got)
	}
	if !sawRebirth {
		t.Fatalf("the rebirth never reached the spine: %+v", got)
	}

	// And the instance is the same record throughout, at the position it had reached.
	after, ok, _ := srv.Store.Get(inst)
	if !ok {
		t.Fatal("the instance did not survive its subject")
	}
	if entity.ToObj(after.Payload)["position"] != startedAt {
		t.Fatalf("the instance moved on its own: %v → %v",
			startedAt, entity.ToObj(after.Payload)["position"])
	}
}

// The spine carries the ANCHORS, and the fine grain hangs off them.
//
// A run about the subject writes a `task:instance` entity naming it as its flow, and a
// spawn is a commit there because the mirror writes it. So a resolver reading the spine learns
// a run began — and its own instance for that run is where the frames are. That is the whole
// of the navigation between the two planes: no sequencer, and no merged order for anything to
// disagree about.
func TestTheSpineCarriesItsAnchors(t *testing.T) {
	srv := askingServer(t)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()
	installResolver(t, ts.URL, standing("payments-review", "payments")).Body.Close()

	subject := entity.Ref{System: "change", Kind: "pr", ID: "1"}
	entitytest.Set(t, srv.Store, subject, entity.Obj{"area": "payments"}.Payload())
	mirror.Watch(srv.Store, "resolvers:security:payments-review", "payments-review", subject)

	spawnFor(t, ts, "run-1", subject.String(), "")
	waitQuestion(t, srv, "run-1")

	inst := resolver.InstanceRef("payments-review", subject.String())
	got := commitsOf(t, ts.URL+"/resolvers/instance/"+inst.ID+"/events?since=0")

	var anchor entity.Event
	for _, ev := range got {
		if ev.Ref == mirror.Ref("run-1") {
			anchor = ev
		}
	}
	if anchor.Ref.ID == "" {
		t.Fatalf("no run appeared on the subject's spine, so a resolver reading it would not "+
			"know there was work to watch: %+v", got)
	}
	if entity.Str(anchor.To, "flow") != subject.String() {
		t.Fatalf("the anchor does not name the subject it is about: %+v", anchor)
	}

	// And a commit about somebody ELSE's run is not on this spine.
	other := entity.Ref{System: "change", Kind: "pr", ID: "2"}
	entitytest.Set(t, srv.Store, other, entity.Obj{"area": "payments"}.Payload())
	spawnFor(t, ts, "run-2", other.String(), "")
	waitQuestion(t, srv, "run-2")
	for _, ev := range commitsOf(t, ts.URL+"/resolvers/instance/"+inst.ID+"/events?since=0") {
		if ev.Ref == mirror.Ref("run-2") {
			t.Fatalf("another subject's run is on this spine: %+v", ev)
		}
	}

	// The fine grain is where the anchor says: that run's own instance, serving its frames.
	fine := resolver.InstanceRef("payments-review", "run-1")
	if _, ok, _ := srv.Store.Get(fine); !ok {
		t.Fatal("the run the spine anchored has no instance to read it through")
	}
	if len(framesSoFar(t, ts.URL+"/resolvers/instance/"+fine.ID+"/events?since=0")) == 0 {
		t.Fatal("the run instance served no frames")
	}
}
