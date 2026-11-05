package runnerd

import (
	"io"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"strings"
	"testing"

	"github.com/signadot/tony-format/go-tony/ir"
	"github.com/signadot/verse/pkg/runner"
	"github.com/signadot/verse/pkg/systems"
	"github.com/signadot/verse/pkg/task"
)

// allowing is an environment whose network is allow, with these `tcp-client` shapes.
func allowing(shapes ...systems.Shape) systems.Environment {
	return systems.Environment{Systems: systems.Systems{"network": {{
		Attributes: map[string]string{"mode": runner.NetworkAllow},
		Verbs:      map[string][]systems.Shape{"tcp-client": shapes},
	}}}}
}

// A CONNECTION IS DECIDED AS A STEP: outside the environment's `tcp-client` shapes it is refused
// saying what they are, and a shape naming no port allows any; within them a row on the network
// decides, and the default row does not, whatever it says: what the shapes give goes ahead under no
// autonomy, a default of deny, or one of ask; then the runner's ceiling, whatever the rows said.
func TestAConnectionIsDecidedByTheEnvironmentTheAutonomyAndTheCeiling(t *testing.T) {
	allow, deny, ask := task.Then{Mode: task.ThenAllow}, task.Then{Mode: task.ThenDeny}, task.Then{Mode: task.ThenAsk}
	env := allowing(systems.Shape{"host": systems.Glob("*.github.com")}, systems.Shape{"host": ir.FromString("registry.internal"), "port": ir.FromInt(5000)}, systems.Shape{"host": ir.FromString("169.254.169.254")})
	open := task.Autonomy{{Match: &task.Match{System: "network"}, Then: allow}, {Then: ask}}
	noAPI := task.Autonomy{
		{Name: "no-api", Match: &task.Match{System: "network", Params: map[string]*ir.Node{"host": ir.FromString("api.github.com")}}, Then: deny},
		{Then: allow},
	}
	bound := ceiling(t, `{mode: allow, tcp-client: [{host: !glob "*.github.com"}, {host: registry.internal}]}`)
	for _, c := range []struct {
		p          task.Autonomy
		host, port string
		refused    string // "" = admitted
	}{
		{open, "api.github.com", "443", ""},
		{open, "api.github.com", "22", ""}, // a shape naming no port allows any
		{open, "github.com", "443", "outside the environment"},
		{open, "registry.internal", "5000", ""},
		{open, "registry.internal", "443", "it gives {host: !glob *.github.com}, {host: registry.internal, port: 5000}"},
		{noAPI, "api.github.com", "443", "the autonomy's row no-api"},
		{noAPI, "raw.github.com", "443", ""},
		{nil, "api.github.com", "443", ""},                         // no autonomy: the default row asks, and does not decide it
		{task.Autonomy{{Then: deny}}, "api.github.com", "443", ""}, // nor a default of deny
		{task.Autonomy{{Then: ask}}, "github.com", "443", "outside the environment"},
		{task.Autonomy{{Then: allow}}, "169.254.169.254", "80", "beyond the runner's ceiling for network"},
		{open, "169.254.169.254", "80", "beyond the runner's ceiling for network: no shape of `tcp-client` the runner's ceiling gives matches (host=169.254.169.254 port=80)"},
	} {
		err := connectionRefused(env, c.p, bound, c.host, c.port)
		switch {
		case c.refused == "" && err != nil:
			t.Errorf("%s:%s under %v: refused: %v", c.host, c.port, c.p, err)
		case c.refused != "" && (err == nil || !strings.Contains(err.Error(), c.refused)):
			t.Errorf("%s:%s under %v: %v, want it refused saying %q", c.host, c.port, c.p, err, c.refused)
		}
	}
	if err := connectionRefused(systems.Environment{}, open, nil, "api.github.com", "443"); err == nil || !strings.Contains(err.Error(), "outside the environment") {
		t.Errorf("no network block: %v", err)
	}
}

// A HOST DENIED AT LAUNCH IS ALLOWED ONCE A REPLACEMENT LIFTS THE DENY: the runner's process
// holds the task's current autonomy, and the next connection is decided by it. The environment
// stays what the launch said.
func TestAReplacementDecidesTheNextConnection(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusNoContent) }))
	t.Cleanup(upstream.Close)
	u, err := url.Parse(upstream.URL)
	if err != nil {
		t.Fatal(err)
	}
	h := NewSubprocessHandler(SubprocessConfig{Log: io.Discard})
	h.Describe(described(agentOffer(nil)))
	l := &runner.Launch{ID: "x", Prompt: "p", Agent: &runner.Agent{Name: "verse"}, Environment: allowing(systems.Shape{"host": ir.FromString("google.com")}),
		Autonomy: task.Autonomy{{Name: "not-yet", Match: &task.Match{System: "network", Params: map[string]*ir.Node{"host": ir.FromString("google.com")}}, Then: task.Then{Mode: task.ThenDeny}}, {Then: task.Then{Mode: task.ThenAllow}}}}
	ready := make(chan struct{})
	close(ready)
	p := &proc{id: l.ID, l: l, ready: ready, gone: make(chan struct{}), done: make(chan struct{}), prox: httputil.NewSingleHostReverseProxy(u)}
	decide := p.connections(l, h.describe)
	h.mu.Lock()
	h.tasks[l.ID] = p
	h.mu.Unlock()
	srv := httptest.NewServer(h.HTTPHandler())
	t.Cleanup(srv.Close)

	if err := decide("google.com", "443"); err == nil || !strings.Contains(err.Error(), "not-yet") {
		t.Fatalf("at launch: %v, want google.com refused by not-yet", err)
	}
	req, err := http.NewRequest(http.MethodPut, srv.URL+"/tasks/x/autonomy", strings.NewReader(
		`{autonomy: [{name: now, match: {system: network, host: google.com}, then: allow}, {then: deny}]}`))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/tony")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	msg, _ := io.ReadAll(res.Body)
	res.Body.Close()
	if res.StatusCode != http.StatusNoContent {
		t.Fatalf("the replacement = %d %q, want 204", res.StatusCode, msg)
	}
	if err := decide("google.com", "443"); err != nil {
		t.Errorf("after the replacement: %v, want google.com admitted", err)
	}
}

// WHAT THE PROXY REFUSED IS ON THE TASK'S SNAPSHOT, by destination and how often: the runner's
// account, which the task's process never has.
func TestARefusedConnectionIsOnTheSnapshot(t *testing.T) {
	eg, err := NewEgress("x", nil, func(host, port string) error {
		return connectionRefused(allowing(systems.Shape{"host": ir.FromString("google.com")}), nil, nil, host, port)
	}, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if err := eg.Permits("evil.example:443"); err != nil {
			eg.note("evil.example:443", err)
		}
	}
	p := &proc{id: "x", known: task.Snapshot{ID: "x"}, ready: make(chan struct{})}
	p.egress.Store(eg)
	if got := p.as(task.Running, "").Refused; got["evil.example:443"] != 2 {
		t.Errorf("refused on the snapshot: %v, want evil.example:443 twice", got)
	}
}
