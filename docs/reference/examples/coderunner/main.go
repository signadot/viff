// Command coderunner is a verse runner that gives each task its own git checkout and runs an
// agent in it — either the LLM harness in this tree or a coding agent binary such as Claude
// Code.
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
//   - Delivery, by the runner and not by the agent. The task never runs `git push`: it cannot
//     reach the credential, so the push is the runner's act and carries provenance trailers.
//   - Two drivers. `llm` is harness.Build rooted at the workspace; `claude` runs the agent
//     binary multi-turn over a resumed session, so steers and answer contracts work.
//   - A containment point per task, refused rather than degraded when it cannot be built.
//
// [examples/runner]: ../runner/main.go
// [task-control]: ../../../task-control.md
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/signadot/verse/answer"
	"github.com/signadot/verse/codec"
	"github.com/signadot/verse/task"
	"github.com/signadot/verse/task/harness"
	"github.com/signadot/verse/task/taskd"
)

func main() {
	addr := flag.String("addr", ":9802", "address to serve the task-control contract on")
	cfgPath := flag.String("config", "coderunner.tony", "the runner's config: environments")
	data := flag.String("data", "", "directory for mirrors and workspaces (default: a temp dir)")
	keep := flag.Bool("keep", false, "keep workspaces after a task ends, for debugging")
	tokenEnv := flag.String("token-env", "CODERUNNER_GIT_TOKEN",
		"environment variable holding the git token this runner pushes with")
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
	r := &Runner{
		cfg:        cfg,
		mirrors:    NewMirrors(filepath.Join(root, "mirrors"), TokenAuth(os.Getenv(*tokenEnv))),
		workspaces: filepath.Join(root, "workspaces"),
		conf:       hostConfiner{},
		keep:       *keep,
		log:        os.Stderr,
	}

	d := taskd.New(r.Build)
	mux := http.NewServeMux()
	// The descriptor comes first: it is what an operator reads before pointing a charter
	// here, and what `verse runner add` should ask. A STUB — it says what this runner is and
	// what it can contain, and nothing yet consumes it.
	mux.HandleFunc("GET /runner", r.Descriptor)
	mux.Handle("/", d.Handler())

	log.Printf("coderunner listening on %s, state under %s — install it with: verse runner add <kind>:<name> http://<host>%s",
		*addr, root, *addr)
	for _, e := range cfg.Environments {
		log.Printf("  environment %s (driver %s):", e.Name, orDefault(e.Driver, "llm"))
		for _, rp := range e.Repos {
			log.Printf("    %s: %s at %s, pushes to %s",
				rp.Name, rp.Repo, rp.Base, orDefault(rp.Push, "nowhere"))
		}
	}
	if err := http.ListenAndServe(*addr, mux); err != nil {
		log.Fatalf("coderunner: %v", err)
	}
}

// Runner holds what every task needs and nothing a task owns.
type Runner struct {
	cfg        *Config
	mirrors    *Mirrors
	workspaces string
	conf       Confiner
	keep       bool
	log        *os.File
}

// Build turns one launch into one task: resolve the workspace it names, check the containment
// can be built, open the workspace, and hand it to a driver.
//
// Every refusal here is a FAILED task carrying the reason rather than a smaller task that runs
// anyway. That is the contract a runner declines by — an error from a spawn travels onto the
// flow with its words, and the drive that deployed the runner is the party that knows.
func (r *Runner) Build(l task.Launch, reg *task.Registry, content task.ContentReader) *task.Task {
	env, err := r.cfg.Environment(l.Environment)
	if err != nil {
		return task.NewFailed(l.ID, reg, err.Error())
	}
	// The containment this environment declares, checked BEFORE any work is set up. A runner
	// that cannot provide the floor does not run the task with a weaker one: that difference
	// is invisible from the outside, which is the direction that costs.
	point := pointFor(env)
	if err := r.conf.Provides(point); err != nil {
		return task.NewFailed(l.ID, reg, fmt.Sprintf("environment %q: %v", env.Name, err))
	}
	driver := l.Driver
	if driver == "" {
		driver = orDefault(env.Driver, "llm")
	}
	if driver != "llm" && driver != "claude" {
		return task.NewFailed(l.ID, reg, fmt.Sprintf("this runner has no driver %q (want llm or claude)", driver))
	}

	ws, err := r.mirrors.Open(context.Background(), l.ID, env, r.workspaces)
	if err != nil {
		return task.NewFailed(l.ID, reg, fmt.Sprintf("environment %q: %v", env.Name, err))
	}

	// The workspace layout is this runner's convention, so the runner describes it. It rides
	// the PROMPT rather than the frame because harness.Build derives a default frame from the
	// tools and vector when a spawn writes none, and setting one here would silently replace
	// advice that is more useful than this.
	l.Prompt = ws.Brief() + l.Prompt

	var t *task.Task
	switch driver {
	case "llm":
		// The harness, rooted at the workspace root so every repository is reachable from it.
		// Frame, tools, answer and vector come from the spawn, exactly as they do for an
		// in-process task: the seam is complete or the guarantee is local-only.
		t, _ = harness.Build(harness.Config{
			ID: l.ID, Prompt: l.Prompt, Root: ws.Root, Resolver: reg, Content: content,
			Frame: l.Frame, Tools: l.Tools, Answer: l.Answer, Vector: l.Vector,
			Log: r.log,
		})
	case "claude":
		t, err = r.buildClaude(l, reg, ws, env, point)
		if err != nil {
			_ = ws.Remove(context.Background())
			return task.NewFailed(l.ID, reg, err.Error())
		}
	}
	// What a gate can match on: the branch, the base, what has been touched, and where it
	// would go. `GateMatch.Branches` and `GateMatch.OnPush` read this, so a charter can say
	// `ask` on anything touching a release branch and `auto` everywhere else.
	t.SetRepo(ws.RepoContext)
	go r.finish(t, ws, l)
	return t
}

func (r *Runner) buildClaude(l task.Launch, reg *task.Registry, ws *Workspace, env Environment, point Point) (*task.Task, error) {
	var contract *answer.Contract
	if l.Answer != nil {
		c, err := answer.Parse(l.Answer)
		if err != nil {
			// A contract that cannot be parsed fails the spawn, for the reason an unknown
			// tool name does: the charter asked for something specific, and running without
			// it while saying nothing is how a field comes to mean nothing.
			return nil, fmt.Errorf("answer contract: %w", err)
		}
		contract = c
	}
	vec := l.Vector
	if vec == nil {
		// One action per turn, and that action is a delegation to a foreign agent. ASK is
		// the honest default for it — verse cannot see inside, so the invocation is the only
		// thing there is to decide about.
		vec = &task.Vector{Default: task.Auto, Gates: []task.Gate{{
			Name: "agent", Mode: task.Ask,
			Match: task.GateMatch{Verbs: []task.Verb{task.CallAgent}},
		}}}
	}
	d := &claudeDriver{
		prompt: l.Prompt, frame: l.Frame, env: env, point: point, conf: r.conf,
		dir: ws.Root, contract: contract,
	}
	t := task.NewTask(l.ID, task.NewSupervisor(vec, reg), d, &claudeExec{d: d})
	t.SetLog(r.log)
	d.emit = t.Emit
	return t, nil
}

// finish waits for the task to end, delivers whatever it produced, and removes the workspace.
//
// It watches the read plane rather than being called back, because the loop offers no
// completion hook — and it must not hold the Build call, which taskd expects to return a task
// it can start.
//
// Delivering AFTER the terminal is this example's sharpest shortcut, and it costs more than the
// missing gate it looks like.
//
// The push is the edge into whatever happens next: a git source reflects the branch, a charter
// conditions on it, and "review this work" is a flow that continues from it. Happening out here,
// it is three things it should not be — ungated (the vector never sees it, so
// `RepoContext.PushTarget` and `GateMatch.OnPush` are decoration), unrecorded (the task's read
// plane never names the branch it produced, so `verse task get` cannot say where the work went),
// and not a cause (the next flow chains from a ref appearing in a source reflection rather than
// from a boundary in the task that made it).
//
// Inside the loop, before terminal, all three close at once. What blocks it is that
// harness.Build returns a finished *task.Task and exposes neither driver nor executor, so there
// is nothing here to compose a final action onto — and reimplementing the harness to get one is
// the thing this example exists not to do. See docs/working/coding-runner.md.
func (r *Runner) finish(t *task.Task, ws *Workspace, l task.Launch) {
	frames, cancel := t.Subscribe()
	defer cancel()
	for f := range frames {
		if f.Kind != task.EvStatus || !task.Status(f.Text).Terminal() {
			continue
		}
		break
	}
	ctx := context.Background()
	// Both halves are reported, and neither is conditional on the other: a workspace over
	// several repositories can deliver two and fail on a third, and an operator who is told
	// only about the failure does not know what did land.
	delivered, err := ws.Deliver(ctx, l, t.Result())
	for _, d := range delivered {
		fmt.Fprintf(r.log, "coderunner: %s: pushed %s: %s\n", l.ID, ws.Branch, d)
	}
	if err != nil {
		fmt.Fprintf(r.log, "coderunner: %s: deliver: %v\n", l.ID, err)
	}
	// A workspace is removed only when its work LEFT, and this is the second thing this
	// function got wrong rather than a precaution.
	//
	// It used to remove unconditionally, so a delivery that failed — or that found nothing to
	// deliver — destroyed the tree the work was in. That is not a lost push, it is a lost
	// result: the commit lives in the task's own clone, and `os.RemoveAll` takes its object
	// store with it. Observed: an agent produced fourteen verified findings across a dozen
	// files, delivered nothing, and left nothing behind to look at. The disk argument for
	// removing is real and is an argument about SUCCESSFUL runs; it says nothing about
	// keeping the only copy of work nobody has seen.
	//
	// So: an error keeps it, and so does a workspace that was supposed to deliver and did
	// not. A read-only environment — no push target anywhere — has nothing to preserve and is
	// removed as before.
	switch {
	case r.keep:
		fmt.Fprintf(r.log, "coderunner: %s: workspace kept at %s\n", l.ID, ws.Root)
	case err != nil:
		fmt.Fprintf(r.log, "coderunner: %s: KEEPING %s — delivery failed and this is the only "+
			"copy of the work\n", l.ID, ws.Root)
	case ws.Deliverable() && len(delivered) == 0:
		fmt.Fprintf(r.log, "coderunner: %s: KEEPING %s — this environment pushes somewhere and "+
			"nothing was delivered, so either the task changed nothing or something is wrong\n",
			l.ID, ws.Root)
	default:
		if err := ws.Remove(ctx); err != nil {
			fmt.Fprintf(r.log, "coderunner: %s: remove workspace: %v\n", l.ID, err)
		}
	}
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
		"drivers": []string{"llm", "claude"},
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
		return hostProvides
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

func lookupEnv(name string) (string, bool) { return os.LookupEnv(name) }

func orDefault(s, def string) string {
	if s == "" {
		return def
	}
	return s
}
