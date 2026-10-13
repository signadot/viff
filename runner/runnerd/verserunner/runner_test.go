package verserunner

import (
	"context"
	"io"
	"strings"
	"testing"

	"github.com/signadot/verse/task"
)

func TestCheckRefusesAnnotationsItDoesNotKnow(t *testing.T) {
	for _, tc := range []struct {
		c    Config
		want string
	}{
		{Config{Annotations: map[string]string{"verse.portkey/route": "string"}}, "no annotation"},
		{Config{Annotations: map[string]string{AnnotationVirtualKey: "int"}}, "takes a string"},
	} {
		if err := tc.c.Check(); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%+v: got %v, want %q", tc.c, err, tc.want)
		}
	}
}

// The agent is offered only when the runner can reach its model; scripts always are.
func TestDescribeOffersTheAgentOnlyWithAKey(t *testing.T) {
	c := Config{Annotations: map[string]string{AnnotationVirtualKey: "string"}}

	d := c.Describe(Secrets{})
	if len(d.Offers) != 1 || !d.Offers[0].Script || d.Offers[0].With != nil {
		t.Fatalf("without a key: %+v", d)
	}

	d = c.Describe(Secrets{PortkeyAPIKey: "pk-test"})
	if len(d.Offers) != 2 || d.Offers[0].Agent != AgentName {
		t.Fatalf("with a key: %+v", d)
	}
	agent := d.Offers[0]
	if !agent.With["model"].Any || agent.Annotations[AnnotationVirtualKey] != "string" ||
		strings.Join(agent.Intercepts, ",") != "files.write,commands.run" {
		t.Fatalf("agent offer: %+v", agent)
	}
}

// A virtual key the spawn picked is used only when the runner accepts the annotation.
func TestTheVirtualKeyAnnotation(t *testing.T) {
	c := Config{Models: Models{Portkey: Portkey{VirtualKey: "default-vk"}}}
	if got := New(c, Secrets{}, t.TempDir(), io.Discard).virtualKey("picked"); got != "default-vk" {
		t.Fatalf("not accepted: %q", got)
	}
	c.Annotations = map[string]string{AnnotationVirtualKey: "string"}
	r := New(c, Secrets{}, t.TempDir(), io.Discard)
	if got := r.virtualKey("picked"); got != "picked" {
		t.Fatalf("accepted: %q", got)
	}
	if got := r.virtualKey(""); got != "default-vk" {
		t.Fatalf("none picked: %q", got)
	}
}

// An llm spawn with no model key fails, saying how to give the runner one: where the key comes
// from depends on who hosts the runner.
func TestAnLLMSpawnWithNoKeySaysWhereTheKeyGoes(t *testing.T) {
	for _, tc := range []struct{ noKey, want string }{
		{"", "llm driver unavailable: no model key"},
		{"set llm.apiKey in verse.tony", "llm driver unavailable: set llm.apiKey in verse.tony"},
	} {
		d := Drivers{Root: t.TempDir(), Env: task.DefaultShellEnv, Log: io.Discard, NoKey: tc.noKey}
		_, err := d.Build(task.Launch{ID: "t1", Driver: "llm"}, task.NewRegistry()).Run(context.Background())
		if err == nil || err.Error() != tc.want {
			t.Errorf("NoKey %q: got %v, want %q", tc.noKey, err, tc.want)
		}
	}
	if got := New(Config{}, Secrets{}, t.TempDir(), io.Discard).drivers.NoKey; got != "set PORTKEY_API_KEY" {
		t.Errorf("runnerd's hint: %q", got)
	}
}
