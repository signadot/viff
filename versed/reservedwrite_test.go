package versed

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/signadot/verse/entity"
	"github.com/signadot/verse/entity/entitytest"
	"github.com/signadot/verse/schedule"
)

// VERSE'S HALF IS NOT A RULE'S TO WRITE, unless the rule is the system's. The api refuses a
// generic write under `verse`, and a firing's patch or derive is that write by another door:
// the rows there hold `child-owner` out to everyone, so a person's rule deriving a name the
// system has not used yet — `verse.schedule.date`, before the base charter first derives it —
// made the node, owned it, and the system then wrote into what was theirs.
func TestAPersonsRuleDoesNotWriteUnderVerse(t *testing.T) {
	srv := NewWithStore(ScriptedBuilder, entitytest.Store(t))
	srv.Start(t.Context()) // the base charter's levels stand, so the derive below is otherwise well formed
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()
	const alice = "alice@example.com"

	for _, c := range []struct{ what, rule string }{
		{"a derive into the calendar's place", `{
			name: squat,
			stratum: ticks,
			condition: {subject: {path: "verse.schedule.minutely.tick.*"}},
			action: {derive: [{path: "verse.schedule", id: weekday, value: {weekday: "never"}}]},
		}`},
		{"a patch of a subject it names under verse", `{
			name: scribble,
			condition: {subject: {path: "verse.task.instance.*", value: {status: done}}},
			action: {patch: {noted: true}},
		}`},
	} {
		resp := installAs(t, alice, ts.URL, c.rule)
		body := bodyOf(t, resp)
		if resp.StatusCode != http.StatusForbidden || !strings.Contains(body, "verbs that own it") {
			t.Errorf("%s: %s %q, want 403 saying whose it is to write", c.what, resp.Status, body)
		}
	}
	// Reading it is anybody's: a tick is a subject like any other, and a spawn about it writes
	// nothing a rule wrote.
	if resp := installAs(t, alice, ts.URL, `{
		name: on-the-hour,
		condition: {subject: {path: "verse.schedule.hourly.tick.*"}},
		action: {spawn: {driver: scripted, prompt: "true"}},
	}`); resp.StatusCode != http.StatusCreated {
		t.Errorf("a rule that reads verse's half and spawns: %s %s", resp.Status, bodyOf(t, resp))
	}
	if e, stands, _ := srv.Store.Get(entity.VerseRef(schedule.System, "weekday")); stands && entity.Str(e.Payload, "weekday") == "never" {
		t.Error("the refused rule's derive landed")
	}
}
