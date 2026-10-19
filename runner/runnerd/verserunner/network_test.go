package verserunner

import (
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/signadot/verse/codec"
	"github.com/signadot/verse/runner"
	"github.com/signadot/verse/runner/client"
	"github.com/signadot/verse/runner/runnerd"
	"github.com/signadot/verse/task"
)

// A TASK REACHES WHAT ITS LAUNCH'S NETWORK SAYS, AND THE RUNNER BUILDS IT: under allow, a host on
// its list through the proxy and nothing else, the runner itself included; under none, nothing.
// The runner offers the networks its sandbox builds.
func TestATasksNetworkIsWhatItsLaunchSays(t *testing.T) {
	sb, err := runnerd.Bwrap()
	if err != nil {
		t.Skipf("bwrap can't run here: %v", err)
	}
	if _, err := exec.LookPath("curl"); err != nil {
		t.Skip("no curl")
	}
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "reached")
	}))
	defer origin.Close()
	rt, url := serveNetwork(t, sb)

	resp, err := http.Get(url + "/describe")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	var desc runner.Description
	if err := codec.Unmarshal(body, &desc, codec.Tony); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(desc.Offers[0].With["network"].Values, ","); got != "open,none,allow" {
		t.Errorf("the runner offers networks %s, want what bwrap builds", got)
	}

	allowed := strings.TrimPrefix(origin.URL, "http://")
	probe := "#!/bin/sh\n" +
		"curl -s -m 5 " + origin.URL + " || echo origin-unreachable\n" +
		"echo; curl -s -m 5 -o /dev/null -w 'other=%{http_code}\\n' http://not-allowed.example/ || echo other-unreachable\n" +
		"curl -s -m 5 --noproxy '*' " + url + "/tasks >/dev/null && echo runner-reached || echo runner-unreachable\n" +
		"curl -sf -m 5 " + url + "/tasks >/dev/null && echo runner-proxied || echo runner-refused\n"

	in := scriptWith(t, rt, "allow", probe, &runner.Network{Mode: runner.NetworkAllow, Allow: []string{allowed}})
	for _, want := range []string{"reached", "other=403", "runner-unreachable", "runner-refused"} {
		if !strings.Contains(in.Result, want) {
			t.Errorf("allow: %s not in the output:\n%s", want, in.Result)
		}
	}
	in = scriptWith(t, rt, "none", probe, &runner.Network{Mode: runner.NetworkNone})
	for _, want := range []string{"origin-unreachable", "runner-unreachable"} {
		if !strings.Contains(in.Result, want) {
			t.Errorf("none: %s not in the output:\n%s", want, in.Result)
		}
	}
}

// AN UNCONFINED RUNNER CAN'T TAKE THE NETWORK AWAY, so it offers open alone and refuses a launch
// that asks for less, the one that names no network among them (absent is none), rather than
// run the task on its own network.
func TestAnUnconfinedRunnerRefusesANetworkItCantBuild(t *testing.T) {
	rt, _ := serve(t, Secrets{PortkeyAPIKey: "k", PortkeyBaseURL: "https://model.example"}, runnerd.Unconfined{})
	none := runner.With{Network: &runner.Network{Mode: runner.NetworkNone}}
	for _, l := range []runner.Launch{
		{ID: "s1", Policy: task.Policy{{Then: task.Then{Mode: task.ThenAllow}}}, Doer: runner.Doer{Script: "#!/bin/sh\ntrue", With: none}},
		{ID: "s2", Policy: task.Policy{{Then: task.Then{Mode: task.ThenAllow}}}, Doer: runner.Doer{Script: "#!/bin/sh\ntrue"}},
		// An agent's none is an allow for its model, and the refusal says what the launch asked.
		{ID: "a1", Policy: task.Policy{{Then: task.Then{Mode: task.ThenAllow}}}, Prompt: "go", Doer: runner.Doer{Agent: AgentName, With: none}},
	} {
		frames, stop := rt.Launch(l).Subscribe()
		f := <-frames
		stop()
		if task.Status(f.Text) != task.Failed || !strings.Contains(f.Err, "builds the network open, not none") {
			t.Errorf("%s came back %+v", l.ID, f)
		}
	}
}

// serveNetwork serves the verse runner in sb on a real address, so a task can try to reach it.
func serveNetwork(t *testing.T, sb runnerd.Sandbox) (client.Launcher, string) {
	t.Helper()
	work, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	control, err := os.MkdirTemp("", "vr")
	if err != nil {
		t.Fatal(err)
	}
	h, err := Standalone(Config{}, Secrets{}, Tokens{}, sb, []string{os.Args[0], "-test.run=^$", taskArg}, work, control, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	srv := &httptest.Server{Listener: ln, Config: &http.Server{Handler: h.HTTPHandler()}}
	srv.Start()
	t.Cleanup(func() { srv.Close(); h.Shutdown(); os.RemoveAll(control) })
	return client.HTTP(srv.URL), srv.URL
}

// scriptWith runs a script as a task under a network, and returns it once it has ended.
func scriptWith(t *testing.T, rt client.Launcher, id, src string, n *runner.Network) task.Snapshot {
	t.Helper()
	rt.Launch(runner.Launch{ID: id, Policy: task.Policy{{Then: task.Then{Mode: task.ThenAllow}}}, Doer: runner.Doer{Script: src, With: runner.With{Network: n}}})
	return ended(t, rt, id)
}
