package viffd

import (
	"io"
	"strings"
	"testing"
	"time"

	"go.viff.dev/viff/internal/trigger"
	"go.viff.dev/viff/pkg/codec"
	"go.viff.dev/viff/pkg/entity"
	"go.viff.dev/viff/pkg/entity/entitytest"
	"go.viff.dev/viff/pkg/runner/runnerd/stock"
	"go.viff.dev/viff/pkg/runner/runnerd/viff/modeltest"
)

// harnessServer is a viff hosting its tasks as `viff up` does, with the model its runner
// reaches: the test's, or none.
func harnessServer(t *testing.T, modelURL string) *Server {
	t.Helper()
	s := stock.Secrets{}
	if modelURL != "" {
		s = stock.Secrets{PortkeyAPIKey: "test-key", PortkeyBaseURL: modelURL}
	}
	h := stock.New(stock.Open(stock.AgentName), s, t.TempDir(), io.Discard)
	srv := serverWith(t, h.Build, entitytest.Store(t))
	srv.Start(t.Context())
	var trig trigger.Trigger
	if err := codec.Unmarshal([]byte(`{
		name: read,
		condition: {subject: {path: "demo.note.*", value: !and [{text: null}, !not {read: null}]}},
		action: {
			spawn:  {agent: {name: viff}, prompt: "answer in one word: $[subject.value.text]", environment: {systems: {network: {mode: open}}}, autonomy: [{then: allow}]},
			onDone: {patch: {read: .[result]}},
			onFailed: {patch: {read: failed}},
		},
	}`), &trig, codec.Tony); err != nil {
		t.Fatal(err)
	}
	if err := srv.Eng.AddTrigger(trig); err != nil {
		t.Fatal(err)
	}
	return srv
}

// readOf waits for the rule to write what became of the note's task.
func readOf(t *testing.T, srv *Server, ref entity.Ref) string {
	t.Helper()
	var read string
	waitUntil(t, "the note to be read", func() bool {
		e, ok, err := srv.Store.Get(ref)
		if err == nil && ok {
			read = entity.Str(e.Payload, "read")
		}
		return read != ""
	})
	return read
}

// A SPAWN'S LLM TASK IS THE ONE `viff up` BUILDS. The harness hands the model the spawn's frame
// and prompt, and the model's last message is the task's result, which the rule reads.
func TestTheHarnessRunsAnLLMSpawnAgainstItsModel(t *testing.T) {
	model := modeltest.New(t, func(r modeltest.Request) modeltest.Reply {
		if !strings.Contains(r.Prompt(), "answer in one word") {
			return modeltest.Say("no instruction")
		}
		return modeltest.Say("heard: " + strings.TrimPrefix(r.Prompt(), "answer in one word: "))
	})
	srv := harnessServer(t, model.URL)

	ref := entity.RefAt("demo", "note", "1")
	entitytest.Set(t, srv.Store, ref, entity.Obj{"text": "hello"}.Payload())
	if got := readOf(t, srv, ref); got != "heard: hello" {
		t.Fatalf("the rule read %q, want the model's answer to the spawn's prompt", got)
	}
	if len(model.Seen()) == 0 {
		t.Fatal("the model was never asked")
	}
}

// With no model configured, an llm spawn fails its own task, and the engine goes on: the rule
// hears the failure.
func TestTheHarnessFailsAnLLMSpawnWithNoModel(t *testing.T) {
	srv := harnessServer(t, "")

	ref := entity.RefAt("demo", "note", "1")
	entitytest.Set(t, srv.Store, ref, entity.Obj{"text": "hello"}.Payload())
	if got := readOf(t, srv, ref); got != "failed" {
		t.Fatalf("the rule read %q, want the task to have failed", got)
	}
}

// waitUntil polls until cond holds, or fails the test.
func waitUntil(t *testing.T, why string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", why)
}
