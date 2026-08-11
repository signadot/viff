// Command coderunner is a verse runner that gives each task its own git checkout and runs an
// agent in it — either the LLM harness in this tree, or an external program named in its config
// (a coding agent binary such as Claude Code, or a script).
//
//	go run ./docs/reference/examples/coderunner -config coderunner.tony -addr :9802
//	verse runner add pool:code http://localhost:9802
//	verse task run --runner runners:pool:code --environment verse-main --as scott "fix the flaky test"
//
// It is the second example on purpose. [examples/runner] is the smallest thing that serves the
// [task-control] contract, with the two functions you change marked; this is what a real coding
// runner does with that contract, and the two together are a ladder rather than one file trying
// to be both. Both compile with the tree, for the same reason: a stub in a fenced block goes
// stale the first time a signature moves and nobody finds out.
//
// The design and its open corners are in docs/working/coding-runner.md; the containment
// vocabulary is docs/working/runner-descriptor.md, and it is a SKETCH — the chains here are
// real and only their weakest rungs are implemented (confine.go).
//
// # What it does that the small example does not
//
//   - A workspace per task, over as many repositories as the environment names: a bare mirror
//     per repository, a clone per task on its own branch, removed when the task ends. Pure
//     go-git — no git binary, and no ambient credential. See workspace.go.
//   - A PROCESS per task, inside the containment, serving the same task-control contract this
//     runner serves to verse — so this process is a proxy and the sandbox is drawn once, around
//     everything the task does. See hop.go (this side) and taskproc.go (the other).
//   - Delivery, by the runner and not by the agent. The task never runs `git push`: it cannot
//     reach the credential, so the push is the runner's act and carries provenance trailers.
//   - Two drivers. `llm` is harness.Build rooted at the workspace, every action gated and
//     narrated; anything else names a program under `agents:`, run through ONE subprocess path.
//     A program that declares how to resume gets steering and answer-contract repair; one that
//     does not gets a turn and says so.
//   - A containment point per task, refused rather than degraded when it cannot be built.
//
// [examples/runner]: ../runner/main.go
// [task-control]: ../../../task-control.md
package main

import (
	"flag"
	"log"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"

	"github.com/signadot/verse/codec"
)

func main() {
	// `-task` is this program's other half: one task, in its own process, inside the
	// containment, serving the same contract (taskproc.go). It is checked before the flag set
	// is even defined because that process takes NO flags — everything it is told arrives on
	// stdin, so there is nothing here for it to parse.
	if len(os.Args) > 1 && os.Args[1] == "-task" {
		if err := runTaskProcess(); err != nil {
			log.Fatal(err)
		}
		return
	}
	addr := flag.String("addr", ":9802", "address to serve the task-control contract on")
	cfgPath := flag.String("config", "coderunner.tony", "the runner's config: environments")
	data := flag.String("data", "", "directory for mirrors and workspaces (default: a temp dir)")
	keep := flag.Bool("keep", false, "keep workspaces after a task ends, for debugging")
	tokenEnv := flag.String("token-env", "CODERUNNER_GIT_TOKEN",
		"environment variable holding the git token this runner pushes with")
	confine := flag.String("confine", "host",
		"how to build a containment point: host (claims almost nothing) | bwrap")
	flag.Parse()

	cfg, err := LoadConfig(*cfgPath)
	if err != nil {
		log.Fatal(err)
	}
	root := *data
	if root == "" {
		if root, err = os.MkdirTemp("", "coderunner-*"); err != nil {
			log.Fatal(err)
		}
	}
	// The credential, named. A runner that shells out to `git` would have picked one up from a
	// credential helper or an ssh agent without anything in the code saying so; this says so.
	// Empty is fine and is what a local path wants.
	// Named by the deployment rather than sniffed. Choosing a confiner by what happens to be
	// installed would mean an image missing bwrap silently runs every task unconfined, which
	// is the degrade-instead-of-refuse this whole design is arranged against.
	conf, err := confinerNamed(*confine)
	if err != nil {
		log.Fatal(err)
	}
	// Where this process reaches its task processes. NOT under -data, and deliberately: it holds
	// one unix socket per running task, and a control plane a task can dial is a task that can
	// answer its own gates. Under a temp root it is short (the sun path limit is about a hundred
	// bytes), outside every workspace, and outside anything a confiner binds into a sandbox.
	control, err := os.MkdirTemp("", "coderunner-ctl-")
	if err != nil {
		log.Fatal(err)
	}
	self, err := os.Executable()
	if err != nil {
		log.Fatal(err)
	}
	r := &Runner{
		cfg:        cfg,
		mirrors:    NewMirrors(filepath.Join(root, "mirrors"), TokenAuth(os.Getenv(*tokenEnv))),
		workspaces: filepath.Join(root, "workspaces"),
		conf:       conf,
		keep:       *keep,
		log:        os.Stderr,
		self:       self,
		control:    control,
		children:   map[string]*child{},
	}
	defer r.Shutdown()

	// A runner that is killed must not leave task processes holding workspaces. The lifeline
	// covers a crash; this covers the ordinary stop, and does it before the sockets go.
	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM)
	go func() {
		<-stop
		log.Printf("coderunner: stopping %d task process(es)", r.running())
		r.Shutdown()
		os.Exit(0)
	}()

	log.Printf("coderunner listening on %s, state under %s — install it with: verse runner add <kind>:<name> http://<host>%s",
		*addr, root, *addr)
	for _, e := range cfg.Environments {
		log.Printf("  environment %s (driver %s):", e.Name, orDefault(e.Driver, "llm"))
		for _, rp := range e.Repos {
			log.Printf("    %s: %s at %s, pushes to %s",
				rp.Name, rp.Repo, rp.Base, orDefault(rp.Push, "nowhere"))
		}
	}
	if err := http.ListenAndServe(*addr, r.Handler()); err != nil {
		log.Fatalf("coderunner: %v", err)
	}
}

// Handler is the verse-facing surface: the task-control contract, served by FORWARDING it.
//
// There is no taskd here. This process hosts no task — it prepares a workspace, starts one
// contained process per task, and proxies the contract to it (hop.go). The routes are the same
// ones taskd registers, which is the property worth keeping: verse cannot tell, and neither can
// `verse task attach`.
func (r *Runner) Handler() http.Handler {
	mux := http.NewServeMux()
	// The descriptor comes first: it is what an operator reads before pointing a charter here,
	// and what `verse runner add` should ask. A STUB — it says what this runner is and what it
	// can contain, and nothing yet consumes it.
	mux.HandleFunc("GET /runner", r.Descriptor)
	mux.HandleFunc("POST /tasks", r.spawn)
	mux.HandleFunc("GET /tasks", r.list)
	mux.HandleFunc("/tasks/{id}", r.forward)
	mux.HandleFunc("/tasks/{id}/{op}", r.forward)
	return mux
}

// Runner holds what every task needs and nothing a task owns — and, since the hop, nothing a
// task may REACH: the mirrors, the push credential inside them, and the control sockets all live
// on this side of a process boundary the task cannot cross.
type Runner struct {
	cfg        *Config
	mirrors    *Mirrors
	workspaces string
	conf       Confiner
	keep       bool
	log        *os.File

	// self is the program to start for each task — this binary, with `-task`. A field rather
	// than a call to os.Executable at each spawn so a test can point it at what it built.
	self    string
	control string // the directory holding one control socket per task
	seq     atomic.Uint64

	mu       sync.Mutex
	children map[string]*child
}

func (r *Runner) running() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.children)
}

// Descriptor is the `GET /runner` stub: what this is, how it applies control, what it can host,
// and what it can contain. Nothing consumes it yet — `verse runner add` still probes with
// `GET /tasks` — so it is here to be read by a person and to keep the shape honest while the
// contract half is designed.
func (r *Runner) Descriptor(w http.ResponseWriter, req *http.Request) {
	// Control is applied SYNCHRONOUSLY: a cancel that returns has been applied, which is what
	// verse assumes of anything it reaches over a url. A runner that instead queued control
	// would have to say `relayed` here — that field is the whole reason the descriptor is
	// owed (docs/working/runners-residual.md).
	body := map[string]any{
		"runner":  "coderunner",
		"control": "synchronous",
		// Derived, not listed: `claude` is a config entry rather than a type in this program, so
		// a hard-coded list here would describe a build instead of a deployment.
		"drivers": drivers(r.cfg),
		"isolation": map[string]any{
			"provides": pointMap(hostProvides),
			"defaults": pointMap(hostProvides),
		},
		"environments": r.cfg.names(),
	}
	out, err := codec.Marshal(body, codec.Tony)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", codec.MediaType(codec.Tony))
	_, _ = w.Write(out)
}

// pointFor is the environment's declared containment. This runner has no profile catalogue
// yet, so an environment either names nothing (the defaults) or names a profile this runner
// does not publish — which is refused, since a profile nobody defined is not a weaker floor,
// it is an unknown one.
func pointFor(env Environment) Point {
	if env.Isolation == "" {
		p := Point{}
		for leaf, v := range hostProvides {
			p[leaf] = v
		}
		// An egress allowlist IS the declaration of restricted egress, derived here rather than
		// stated twice so the two cannot disagree — and `restricted` is built by unsharing the
		// network and binding one socket back in, so it arrives with `ingress: none` attached.
		// A confiner that cannot do both refuses, which is how a machine without bwrap declines
		// to pretend.
		if len(env.Egress) > 0 {
			p["network.egress"] = "restricted"
			p["network.ingress"] = "none"
		}
		return p
	}
	// Deliberately unknown: naming a profile this sketch does not define asks for containment
	// that cannot be checked, and the strongest point is the safe reading of "I asked for
	// something specific" — so Provides refuses it and says what is missing.
	p := Point{}
	for leaf, chain := range chains {
		p[leaf] = chain[len(chain)-1]
	}
	return p
}

func pointMap(p Point) map[string]any {
	out := map[string]any{}
	for leaf, v := range p {
		cur := out
		parts := strings.Split(leaf, ".")
		for _, seg := range parts[:len(parts)-1] {
			next, _ := cur[seg].(map[string]any)
			if next == nil {
				next = map[string]any{}
				cur[seg] = next
			}
			cur = next
		}
		cur[parts[len(parts)-1]] = v
	}
	return out
}

// drivers is what this deployment can actually run: the one driver this program implements, plus
// every program its config names.
func drivers(c *Config) []string {
	out := []string{driverLLM}
	if len(c.Agents) > 0 {
		out = append(out, agentNames(c.Agents)...)
	}
	return out
}

func lookupEnv(name string) (string, bool) { return os.LookupEnv(name) }

func orDefault(s, def string) string {
	if s == "" {
		return def
	}
	return s
}
