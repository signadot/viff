package versed

import (
	"net/http"
	"testing"
	"time"

	"github.com/signadot/verse/entity"
	"github.com/signadot/verse/entity/entitytest"
	"github.com/signadot/verse/mirror"
)

// A RULE'S TASK MAY CARRY A THEN ON ITS ROWS. The task acts for nobody and has no record yet when
// its spawn is checked, so the check runs as the rule that launches it, which is the authority the
// task's own `via:` resolves to once it stands; the spawn is not refused for acting as nobody.
func TestARulesSpawnMayCarryAThenOnItsRows(t *testing.T) {
	srv, ts := asking(t)
	resp := installAs(t, "user:alice@example.com", ts.URL, `{
		name: run-g,
		condition: {subject: {path: "demo.g.*", value: {stage: open}}},
		action: {spawn: {script: "true", environment: {systems: {commands: {run: {}}, network: {mode: open}}},
			autonomy: [{match: {system: commands}, then: {ask: {then: {spawn: {script: "true", environment: {systems: {network: {mode: open}}}}}}}}, {then: allow}]},
			onDone: {patch: {stage: ran}}},
	}`)
	resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("install: %s", resp.Status)
	}
	subject := entity.RefAt("demo", "g", "1")
	entitytest.Set(t, srv.Store, subject, entity.Obj{"stage": "open"}.Payload())
	deadline := time.After(5 * time.Second)
	for {
		for _, e := range entity.Below(srv.Store, mirror.IsTask, 1, entity.VerseRoot, mirror.System, mirror.InstanceKind) {
			if entity.Str(e.Payload, "flow") != subject.String() && entity.Str(e.Payload, "subject") != subject.String() {
				continue
			}
			if why := entity.Str(e.Payload, "error"); why != "" || entity.Str(e.Payload, "status") == "failed" {
				t.Fatalf("the rule's spawn failed: %v", entity.ToObj(e.Payload))
			}
			if s := entity.Str(e.Payload, "status"); s == "running" || s == "done" {
				return
			}
		}
		select {
		case <-deadline:
			t.Fatalf("the rule never spawned its task about %s", subject)
		case <-time.After(10 * time.Millisecond):
		}
	}
}
