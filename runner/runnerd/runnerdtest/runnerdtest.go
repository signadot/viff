// Package runnerdtest serves a runner for a test: a builder's tasks behind the task-control
// contract, over HTTP, proving itself to a verse with a static token as a registered runner does.
// A verse runs no task in its own process, so a test that runs tasks registers one of these.
package runnerdtest

import (
	"crypto/rand"
	"encoding/hex"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/signadot/tony-format/go-tony/ir"
	"github.com/signadot/verse/runner"
	"github.com/signadot/verse/runner/runnerd"
	"github.com/signadot/verse/runner/runnerd/stock"
	"github.com/signadot/verse/systems"
)

// Runner is a runner served for a test: where verse reaches it, the subject its registration
// names, and the handler holding its tasks, for a test to look at them where they run.
type Runner struct {
	URL     string
	Subject string
	Token   string
	*runnerd.InProcessHandler
}

// Serve serves build as a runner describing itself as desc. It answers its static token on every
// describe, to whoever asks, and takes any bearer: it proves itself to a verse without holding the
// verse to anything, which a test verse's door does not need. Its tasks are cancelled, and its
// connections closed, when the test ends.
func Serve(t testing.TB, build runnerd.Builder, desc runner.Description) *Runner {
	t.Helper()
	var b [32]byte
	if _, err := rand.Read(b[:]); err != nil {
		t.Fatal(err)
	}
	token := hex.EncodeToString(b[:])
	sub, err := runner.StaticSubject(token)
	if err != nil {
		t.Fatal(err)
	}
	h := runnerd.NewInProcessHandler(build)
	h.Describe(desc)
	inner := h.HTTPHandler()
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && r.URL.Path == "/describe" {
			w.Header().Set(runner.TokenHeader, token)
		}
		inner.ServeHTTP(w, r)
	}))
	t.Cleanup(func() {
		for _, s := range h.Snapshots() {
			if p := h.Lookup(s.ID); p != nil {
				_ = p.Cancel("the test ended", "test")
			}
		}
		ts.CloseClientConnections()
		ts.Close()
	})
	return &Runner{URL: ts.URL, Subject: sub, Token: token, InProcessHandler: h}
}

// Agents is the agents Everything offers: every name the tests spawn.
var Agents = []string{"verse", "fake", "codex", "claude", "steps", "parrot", "other", "sample"}

// Everything is a description offering scripts and each agent named (Agents where none is), each
// doer holding every system with nothing over it, and every network built: a runner that takes
// whatever a test asks of it, its builder the one to refuse what it does not run.
func Everything(agents ...string) runner.Description {
	if len(agents) == 0 {
		agents = Agents
	}
	held := map[string]runner.System{}
	for _, doc := range systems.All() {
		if doc.Name == "network" {
			// What it builds, every network, and no ceiling: an unwritten network ceiling bounds
			// nothing, where Open would allow no connection.
			held[doc.Name] = runner.System{Builds: systems.Ceiling{{Attributes: map[string]*ir.Node{
				"mode": systems.OneOf(runner.NetworkOpen, runner.NetworkNone, runner.NetworkAllow)}}}}
			continue
		}
		held[doc.Name] = runner.System{Ceiling: systems.Open()}
	}
	d := runner.Description{Version: runner.DescriptionVersion}
	for _, a := range agents {
		d.Offers = append(d.Offers, runner.Offer{Agent: &runner.AgentOffer{Name: a,
			Settings: map[string]runner.Setting{"model": {}, "effort": {}}}, Converse: true, Systems: held})
	}
	d.Offers = append(d.Offers, runner.Offer{Script: true, Systems: held})
	return d
}

// Scripted is the verse runner with no model credential: it runs scripts, and refuses an agent.
// Its operator is the test's author, so what it stops inside a run is open (stock.Open).
var Scripted = stock.New(stock.Open(), stock.Secrets{}, scriptedRoot(), io.Discard).Build

// scriptedRoot is where Scripted's tasks work: a directory of its own under the temporary
// directory, since the runner keeps a task's credential and its tools socket OUTSIDE the
// workspace, and makes them under the temporary directory.
func scriptedRoot() string {
	root := filepath.Join(os.TempDir(), "verse-embedded")
	if err := os.MkdirAll(root, 0o700); err != nil {
		return os.TempDir()
	}
	return root
}
