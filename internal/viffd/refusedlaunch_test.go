package viffd

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"go.viff.dev/viff/pkg/entity"
	"go.viff.dev/viff/pkg/entity/entitytest"
	"go.viff.dev/viff/pkg/mirror"
	"go.viff.dev/viff/pkg/runner/runnerd/runnerdtest"
)

// A LAUNCH THE ENGINE REFUSED IS A FAILED TASK ON RECORD, like every refusal the spawner makes
// itself: the task id holds `status: failed` with the reason and the run it was about, and the
// rule's onFailed still writes the subject down. Before, the engine kept the refusal to itself
// and the task id held nothing, so `viff task get` had nothing to show for it.
func TestALaunchTheEngineRefusedIsAFailedTaskOnRecord(t *testing.T) {
	srv := serverWith(t, runnerdtest.Scripted, entitytest.Store(t))
	srv.Start(t.Context())
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	// The rule's installer owns the place, so its patches land.
	if err := entity.Claim(srv.Store, entity.RefAt("demo"), "user:alice@example.com"); err != nil {
		t.Fatal(err)
	}
	resp := installAs(t, "alice@example.com", ts.URL, `{
		name: develop,
		condition: {subject: {path: "demo.pr.*", value: {stage: open}}},
		action: {
			patch: {stage: developing},
			spawn: {script: "true", environment: {vars: {DEEP: $[subject.value.changes.never]}, systems: {commands: {run: {}}, network: {mode: open}}}, autonomy: `+stepAsksTony+`},
			onFailed: {patch: {stage: failed, was: .[subject.value.stage]}},
		},
	}`)
	resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("install: %s", resp.Status)
	}
	subject := entity.RefAt("demo", "pr", "1")
	entitytest.Set(t, srv.Store, subject, entity.Obj{"stage": "open", "changes": entity.Obj{"a": 1}}.Payload())

	rec := taskFor(t, srv, "develop", subject.String())
	deadline := time.After(5 * time.Second)
	for entity.Str(rec.Payload, "status") != "failed" {
		select {
		case <-deadline:
			t.Fatalf("the refused task's record = %v, want it failed", entity.ToObj(rec.Payload))
		case <-time.After(10 * time.Millisecond):
		}
		rec, _, _ = srv.Store.Get(rec.Ref)
	}
	if got := entity.Str(rec.Payload, "err"); !strings.Contains(got, `no "never" in subject.value.changes`) {
		t.Errorf("err = %q, want the refusal naming the reference", got)
	}
	if mirror.OccurrenceOf(rec.Payload) == 0 {
		t.Errorf("the record says no occurrence: %v", entity.ToObj(rec.Payload))
	}
	waitSubjectField(t, srv, subject, "stage", "failed")
	if e, _, _ := srv.Store.Get(subject); entity.Str(e.Payload, "was") != "developing" {
		t.Errorf("the subject = %v, want was: developing, the subject as it is when the hook runs", entity.ToObj(e.Payload))
	}
}

func waitSubjectField(t *testing.T, srv *Server, ref entity.Ref, field, want string) {
	t.Helper()
	deadline := time.After(5 * time.Second)
	for {
		if e, ok, _ := srv.Store.Get(ref); ok && entity.Str(e.Payload, field) == want {
			return
		}
		select {
		case <-deadline:
			e, _, _ := srv.Store.Get(ref)
			t.Fatalf("%s.%s never became %q: %v", ref, field, want, entity.ToObj(e.Payload))
		case <-time.After(10 * time.Millisecond):
		}
	}
}
