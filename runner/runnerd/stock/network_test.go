package stock

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
	if got := strings.Join(desc.Offers[0].Systems["network"].Builds[0].Attributes["mode"], ","); got != "open,none,allow" {
		t.Errorf("the runner builds networks %s, want what bwrap builds", got)
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
	none := envOf(&runner.Network{Mode: runner.NetworkNone}, nil, nil)
	for _, l := range []runner.Launch{
		{ID: "s1", Autonomy: task.Autonomy{{Then: task.Then{Mode: task.ThenAllow}}}, Script: "#!/bin/sh\ntrue", Environment: none},
		{ID: "s2", Autonomy: task.Autonomy{{Then: task.Then{Mode: task.ThenAllow}}}, Script: "#!/bin/sh\ntrue"},
		// An agent's none is an allow for its model, and the refusal says what the launch asked.
		{ID: "a1", Autonomy: task.Autonomy{{Then: task.Then{Mode: task.ThenAllow}}}, Prompt: "go", Agent: &runner.Agent{Name: AgentName}, Environment: none},
	} {
		frames, stop := rt.Launch(l).Subscribe()
		f := <-frames
		stop()
		if task.Status(f.Text) != task.Failed || !strings.Contains(f.Err, "environment.systems.network: mode none: this runner builds open only") {
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
	h, err := Standalone(opened(Config{}), Secrets{}, Tokens{}, sb, []string{os.Args[0], "-test.run=^$", taskArg}, work, control, io.Discard)
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
	rt.Launch(runner.Launch{ID: id, Autonomy: task.Autonomy{{Then: task.Then{Mode: task.ThenAllow}}}, Script: src, Environment: envOf(n, nil, nil)})
	return ended(t, rt, id)
}

// A ROW ON THE NETWORK DECIDES A CONNECTION THE ENVIRONMENT GIVES, under bwrap through the task's
// proxy: denied, the script reads a refusal naming the row, and the task's snapshot counts it;
// allowed, it reaches the host.
func TestARowOnTheNetworkDecidesAConnection(t *testing.T) {
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
	rt, _ := serveNetwork(t, sb)
	allowed := strings.TrimPrefix(origin.URL, "http://")
	host, _, _ := strings.Cut(allowed, ":")
	n := &runner.Network{Mode: runner.NetworkAllow, Allow: []string{allowed}}
	probe := "#!/bin/sh\ncurl -s -m 5 " + origin.URL + " || echo origin-unreachable\n"
	for want, autonomy := range map[string]task.Autonomy{
		"not-here": {{Name: "not-here", Match: &task.Match{System: "network", Params: map[string]string{"host": host}}, Then: task.Then{Mode: task.ThenDeny}}, {Then: task.Then{Mode: task.ThenAllow}}},
		"reached":  {{Then: task.Then{Mode: task.ThenAllow}}},
	} {
		rt.Launch(runner.Launch{ID: want, Autonomy: autonomy, Script: probe, Environment: envOf(n, nil, nil)})
		in := ended(t, rt, want)
		if !strings.Contains(in.Result, want) {
			t.Errorf("%s: the output doesn't say %q:\n%s", want, want, in.Result)
		}
		if refused := in.Refused[allowed] > 0; refused != (want == "not-here") {
			t.Errorf("%s: refused on the snapshot %v", want, in.Refused)
		}
	}
}
