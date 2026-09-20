package versed

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/signadot/verse/api"
	"github.com/signadot/verse/codec"
	"github.com/signadot/verse/entity"
	"github.com/signadot/verse/entity/entitytest"
	"github.com/signadot/verse/mirror"
	"github.com/signadot/verse/resolver"
)

// commitsOf reads a spine and returns the events it carried: until `until` holds of what has
// arrived, and then until it goes quiet.
//
// UNTIL A CONDITION, NOT UNTIL A SILENCE. The catch-up is flushed only once the whole of it has
// been read, and a loaded machine can take longer over that than any silence a test would think
// to allow — so a positive assertion made over "whatever came before it went quiet" fails on an
// empty read, for a reason that has nothing to do with the spine (git-issue
// nkptp9b3h12ksx3kg9n0). The quiet tail after it is for the negatives, which can only ever ask
// what arrived by then; a nil `until` is one of those, and reads until quiet from the start.
func commitsOf(t *testing.T, url string, until func([]entity.Event) bool) []entity.Event {
	t.Helper()
	_, lines, stop := sseData(t, http.DefaultClient, url)
	defer stop()
	var out []entity.Event
	met := until == nil
	// A hang guard, not a budget: it is reached only when what is waited for never comes,
	// and the caller's assertion then says what was missing.
	giveUp := time.After(30 * time.Second)
	for {
		var quiet <-chan time.Time
		if met {
			quiet = time.After(400 * time.Millisecond)
		}
		select {
		case d, ok := <-lines:
			if !ok {
				return out
			}
			var ev entity.Event
			if codec.Unmarshal([]byte(d), &ev, codec.Tony) == nil {
				out = append(out, ev)
			}
			met = met || until(out)
		case <-quiet:
			return out
		case <-giveUp:
			return out
		}
	}
}

// carries is the `until` for a spine that must carry at least n events matching want.
func carries(n int, want func(entity.Event) bool) func([]entity.Event) bool {
	return func(evs []entity.Event) bool {
		var got int
		for _, ev := range evs {
			if want(ev) {
				got++
			}
		}
		return got >= n
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
		condition: {subject: {path: "change.pr.*", value: {area: payments}}},
		gates:  [{name: approval, mode: ask}],
		action: {patch: {stage: reviewed}},
	}`)

	subject := entity.RefAt("change", "pr", "1")
	// The rule's installer's, so that the firing — which acts as them — may advance it.
	entitytest.Set(t, asTester(srv), subject, entity.Obj{"area": "payments", "stage": "draft"}.Payload())
	entitytest.Set(t, asTester(srv), subject, entity.Obj{"area": "payments", "stage": "proposed"}.Payload())
	waitOwner(t, srv, "guard")

	inst := resolver.InstanceRef("payments-review", mirror.OriginKey(subject))
	got := commitsOf(t, ts.URL+"/resolvers/instance/"+inst.Name()+"/events?since=0",
		carries(2, func(ev entity.Event) bool { return ev.Ref == subject }))
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
		case ev.Ref.Under(entity.VerseRoot, mirror.QuestionSystem), mirror.IsTaskish(ev.Ref):
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
		return mirror.IsQuestion(ref)
	}) {
		qid = ent.Ref.Name()
	}
	resp := postTony(t, ts.URL+"/resolvers/instance/"+inst.Name()+"/verdicts",
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

	subject := entity.RefAt("change", "pr", "1")
	mirror.Watch(srv.Store, "verse.resolvers.payments-review", "payments-review", subject)
	inst := resolver.InstanceRef("payments-review", mirror.OriginKey(subject))
	before, _, _ := srv.Store.Get(inst)
	startedAt := entity.ToObj(before.Payload)["position"]

	entitytest.Set(t, srv.Store, subject, entity.Obj{"area": "payments"}.Payload())
	entitytest.Set(t, srv.Store, subject,
		entity.Obj{"area": "payments", entity.VanishingField: "2026-07-29T13:04:05Z"}.Payload())
	if _, _, err := srv.Store.Write(entity.Write{At: subject.At(), Value: nil}); err != nil {
		t.Fatalf("delete: %v", err)
	}
	entitytest.Set(t, srv.Store, subject, entity.Obj{"area": "payments"}.Payload())

	got := commitsOf(t, ts.URL+"/resolvers/instance/"+inst.Name()+"/events?since=0",
		carries(4, func(ev entity.Event) bool { return ev.Ref == subject }))
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

	subject := entity.RefAt("change", "pr", "1")
	entitytest.Set(t, srv.Store, subject, entity.Obj{"area": "payments"}.Payload())
	mirror.Watch(srv.Store, "verse.resolvers.payments-review", "payments-review", subject)

	spawnFor(t, ts, "run-1", subject.String(), "")
	waitQuestion(t, srv, "run-1")

	inst := resolver.InstanceRef("payments-review", mirror.OriginKey(subject))
	got := commitsOf(t, ts.URL+"/resolvers/instance/"+inst.Name()+"/events?since=0",
		carries(1, func(ev entity.Event) bool { return ev.Ref == mirror.Ref("run-1") }))

	var anchor entity.Event
	for _, ev := range got {
		if ev.Ref == mirror.Ref("run-1") {
			anchor = ev
		}
	}
	if anchor.Ref.Name() == "" {
		t.Fatalf("no run appeared on the subject's spine, so a resolver reading it would not "+
			"know there was work to watch: %+v", got)
	}
	if entity.Str(anchor.To, "flow") != subject.String() {
		t.Fatalf("the anchor does not name the subject it is about: %+v", anchor)
	}

	// And a commit about somebody ELSE's run is not on this spine.
	other := entity.RefAt("change", "pr", "2")
	entitytest.Set(t, srv.Store, other, entity.Obj{"area": "payments"}.Payload())
	spawnFor(t, ts, "run-2", other.String(), "")
	waitQuestion(t, srv, "run-2")
	for _, ev := range commitsOf(t, ts.URL+"/resolvers/instance/"+inst.Name()+"/events?since=0", nil) {
		if ev.Ref == mirror.Ref("run-2") {
			t.Fatalf("another subject's run is on this spine: %+v", ev)
		}
	}

	// The fine grain is where the anchor says: that run's own instance, serving its frames.
	fine := resolver.InstanceRef("payments-review", "run-1")
	waitInstance(t, srv, "payments-review", "run-1")
	if len(framesSoFar(t, ts.URL+"/resolvers/instance/"+fine.Name()+"/events?since=0")) == 0 {
		t.Fatal("the run instance served no frames")
	}
}
