package versed

import (
	"io"
	"strings"
	"testing"

	"github.com/signadot/verse/codec"
	"github.com/signadot/verse/entity"
	"github.com/signadot/verse/entity/entitytest"
	"github.com/signadot/verse/runner/runnerd/verserunner"
	"github.com/signadot/verse/runner/runnerd/verserunner/agent/modeltest"
	"github.com/signadot/verse/task"
	"github.com/signadot/verse/trigger"
)

// harnessServer is a verse hosting its tasks as `verse up` does, its model the scripted one.
func harnessServer(t *testing.T, h *verserunner.Drivers) *Server {
	t.Helper()
	srv := NewWithStore(h.Build, entitytest.Store(t))
	srv.Start(t.Context())
	var trig trigger.Trigger
	if err := codec.Unmarshal([]byte(`{
		name: read,
		condition: {subject: {path: "demo.note.*", value: !and [{text: null}, !not {read: null}]}},
		action: {
			spawn:  {driver: llm, prompt: $subject.value.text, frame: "answer in one word", vector: auto},
			onDone: {patch: {read: $result}},
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

// A SPAWN'S LLM TASK IS THE ONE `verse up` BUILDS. The harness hands the model the spawn's frame
// and prompt, and the model's last message is the task's result, which the rule reads.
func TestTheHarnessRunsAnLLMSpawnAgainstItsModel(t *testing.T) {
	model := modeltest.New(t, func(r modeltest.Request) modeltest.Reply {
		if !strings.Contains(r.System(), "answer in one word") {
			return modeltest.Say("no frame")
		}
		return modeltest.Say("heard: " + r.Prompt())
	})
	srv := harnessServer(t, &verserunner.Drivers{
		Root: t.TempDir(), Env: task.DefaultShellEnv, Log: io.Discard,
		Default: "scripted", LLMBaseURL: model.URL, LLMKey: "test-key"})

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
	srv := harnessServer(t, &verserunner.Drivers{
		Root: t.TempDir(), Env: task.DefaultShellEnv, Log: io.Discard,
		Default: "scripted"})

	ref := entity.RefAt("demo", "note", "1")
	entitytest.Set(t, srv.Store, ref, entity.Obj{"text": "hello"}.Payload())
	if got := readOf(t, srv, ref); got != "failed" {
		t.Fatalf("the rule read %q, want the task to have failed", got)
	}
}
