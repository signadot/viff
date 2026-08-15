// Package versed is the reactive engine as a service: it assembles the entity
// store, the trigger engine, and a gate registry, and serves an HTTP surface that
// the `verse` CLI drives. The surface keeps the two planes distinct — observation
// (watch, entity reads) separate from control (gate resolve, and later flow
// steering) — per docs/deployment-and-steering.md.
//
// Scaffold status: the task runtime (task) is not yet mounted; spawning actions
// no-op until it is wired.
package versed

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/signadot/tony-format/go-tony/ir"
	"github.com/signadot/tony-format/go-tony/ir/kpath"

	"github.com/signadot/verse/answer"
	"github.com/signadot/verse/api"
	"github.com/signadot/verse/broker"
	"github.com/signadot/verse/codec"
	"github.com/signadot/verse/entity"
	"github.com/signadot/verse/launch"
	"github.com/signadot/verse/mirror"
	"github.com/signadot/verse/resolver"
	"github.com/signadot/verse/runner"
	"github.com/signadot/verse/source"
	"github.com/signadot/verse/task"
	"github.com/signadot/verse/task/taskd"
	"github.com/signadot/verse/trigger"
)

// Server is the monolith's in-process assembly: one store, one engine, one gate
// registry, and the task runtime, behind one HTTP surface.
type Server struct {
	Store  entity.Store
	Reg    *task.Registry
	Eng    *trigger.Engine
	Taskd  *taskd.Daemon
	runner task.Runner // how tasks are run/governed; local (taskd) today

	// Addr/BrokerAddr are this verse's OWN listen addresses — the operator API and (if
	// served) the broker face — published on /status so a client or worker can discover
	// where verse listens, as the docd addresses are published for a controller. Set by the
	// task that binds them (cmd/verse up); empty until then.
	Addr       string
	BrokerAddr string

	// LLM is a secrets-safe summary of the task runtime's LLM config, published on
	// /status so you can see whether agent creds are configured on THIS server (the one
	// that hosts the runtime) — the common gotcha. Set by `up`; nil when unconfigured.
	LLM *api.LLM

	// DocdClient/DocdMount are the addresses of the docd this verse is backed by,
	// when store-kind is docd. They are published on GET /status so a separately-run
	// controller task (e.g. `verse connect`) can find docd's mount face without
	// guessing the ephemeral ports `up` chose.
	//
	// They are what a CLIENT should dial, which is not necessarily what versed dialled:
	// `up --docd-advertise-addr` sets these when the two differ, as they do in a cluster,
	// where versed reaches docd by a Service name that resolves nowhere else. The server
	// never connects to them — it is handed an open store — so there is one reader, and
	// it is somewhere else.
	DocdClient string
	DocdMount  string

	// ctx is the lifetime captured at Start; scoped engines start under a child of it.
	ctx context.Context
	// Per-scope engines. A trigger installed under ?scope=<s> runs on a dedicated
	// engine observing store.Scope(s) — the reactivity for one dev's federation
	// (verse connect) lives in its own copy-on-write scope, isolated from baseline and
	// from other scopes. Created lazily on the first scoped trigger, stopped when the
	// scope is dropped (disconnect).
	scopeMu   sync.Mutex
	scopeEngs map[string]*scopeEngine
	// Per-registration reflectors. An installed source runs one, under a child of ctx, and it
	// is the only handle on a goroutine that WRITES a slice — so a remove that forgets to stop
	// one leaks a second writer past the disjointness check the install just made.
	srcMu   sync.Mutex
	srcRuns map[string]*sourceRun

	// standingOwner answers "which standing binding covers this subject", for the paths that
	// post a question without one — a brokered run's frames, whose deriver ran elsewhere.
	standingOwner func(subject string) string
	// tripPace is the cycle throttle's D, applied to every engine this server owns. Guarded by
	// scopeMu because scopeEngine reads it when it builds a scope's engine, lazily.
	tripPace    time.Duration
	tripPaceSet bool

	// brokers are worker-facing handlers hosted under /broker/<nonce> (see BrokerHandler).
	// The nonce is a capability: knowing the URL is the grant to dial in as that runner's
	// worker. The well-known "local" nonce is handled specially (the in-process supervisor).
	brokerMu sync.Mutex
	brokers  map[string]http.Handler

	// routes is the published route table, filled in by Handler as it registers each
	// route — never maintained separately. See routes.go.
	routes []api.Route
}

// SetTripPace sets the cycle throttle's pace D on this server's engines — the baseline one now,
// and every scope engine as it is created. Zero refuses a tripped firing outright instead of
// pacing it. Unset leaves each engine on trigger.DefaultTripPace.
//
// It is here rather than a NewWithStore argument because a scope engine is built lazily, on first
// use, and a pace applied only to the baseline would leave a developer's federated scope running
// a different policy from the verse it overlays.
func (s *Server) SetTripPace(d time.Duration) {
	s.scopeMu.Lock()
	s.tripPace, s.tripPaceSet = d, true
	engs := make([]*trigger.Engine, 0, len(s.scopeEngs))
	for _, se := range s.scopeEngs {
		engs = append(engs, se.eng)
	}
	s.scopeMu.Unlock()
	s.Eng.SetTripPace(d)
	for _, e := range engs {
		e.SetTripPace(d)
	}
}

// scopeEngine is a per-scope engine plus the cancel that stops it (on scope delete).
type scopeEngine struct {
	eng    *trigger.Engine
	cancel context.CancelFunc
}

// NewWithStore assembles the server with the given task driver builder (scripted,
// LLM, …) over a store. The engine resolves trigger gates through the shared registry,
// so a human answers them out-of-band via POST /gate/resolve; a trigger's spawn action
// goes through the task runtime (hosted, mirrored into the verse).
func NewWithStore(build taskd.Builder, store entity.Store) *Server {
	reg := task.NewRegistry()
	eng := trigger.New(store, reg)
	taskd := taskd.New(build)
	s := &Server{Store: store, Reg: reg, Eng: eng, Taskd: taskd,
		scopeEngs: map[string]*scopeEngine{}, srcRuns: map[string]*sourceRun{},
		// Replaced by Start's, which is the real lifetime. A default here rather than a nil
		// check at every use: a source installed on a server nobody started still needs a
		// context to run under, and one that is never cancelled would be worse than one
		// cancelled at task exit.
		ctx: context.Background()}
	s.runner = localRunner{taskd} // the in-process runner ("/broker/local")

	// Publish every posed question as `gate:question:<id>`, from both places questions are
	// raised: the engine's own registry for a firing's gates, and each hosted task for a
	// run's. One record shape either way — the docket, a charter predicate and an auditor all
	// read the same fact, and neither source is privileged.
	// The layer rule, applied where a question is posed. A question carries the owner its
	// LAYER gives it: what named it, else the standing binding covering its subject, else
	// nothing — which is `⊤`, the docket a person reads.
	//
	// Absent layers are skipped rather than ranked. A standing binding is not a wider claim
	// on a named one's questions; it answers a different set.
	owner := func(st entity.Store, q *task.GateQuestion) {
		if q.BoundTo != "" || q.Subject == "" {
			return
		}
		ref, err := entity.ParseRef(q.Subject)
		if err != nil {
			return
		}
		if res, ok := resolver.Owner(st, ref); ok {
			q.BoundTo = resolver.Ref(res.Kind, res.Name).String()
		}
	}
	standingOwner := func(subject string) string {
		ref, err := entity.ParseRef(subject)
		if err != nil {
			return ""
		}
		if res, ok := resolver.Owner(store, ref); ok {
			return resolver.Ref(res.Kind, res.Name).String()
		}
		return ""
	}
	s.standingOwner = standingOwner
	// A question is where an instance is minted, because that is where ownership is decided.
	// A spawn mints one for the binding it NAMES, since ownership is settled then and a
	// resolver should be reading before the first ask; a standing binding cannot be minted
	// that early, because one installed mid-run would own questions an earlier mint never
	// heard of. Both call the same create-only Watch, so the two are one act done twice.
	watch := func(st entity.Store, q task.GateQuestion, taskID string) {
		if q.BoundTo == "" {
			return
		}
		origin, err := entity.ParseRef(q.Subject)
		if taskID != "" {
			origin, err = mirror.Ref(taskID), nil // a run's questions are watched over the run
		}
		if err != nil {
			return
		}
		mirror.Watch(st, q.BoundTo, resolverName(q.BoundTo), origin)
	}
	// The clock a binding puts on its own silence, stamped when the question is posed and
	// ABSOLUTE so a restart cannot slide it. A binding cannot know when a question will
	// arrive, so it says a duration and the question says an instant.
	arm := func(st entity.Store, q task.GateQuestion) {
		if q.BoundTo == "" {
			return
		}
		res, ok := resolver.Find(st, q.BoundTo)
		if !ok || res.Deadline == nil {
			return
		}
		after, err := res.Deadline.After()
		if err != nil {
			return
		}
		_, _, _ = st.Write(entity.Write{
			At:    mirror.QuestionRef(q.ID).At(),
			Value: entity.Obj{"escalatesAt": time.Now().Add(after).UTC().Format(time.RFC3339Nano)}.Payload(),
		})
	}
	reg.OnPost(func(p task.Pending) {
		// The registry's own posts are baseline's: this seam is the standing-gate path,
		// which has no task and therefore no scope to be in.
		owner(store, &p.Question)
		watch(store, p.Question, "")
		mirror.PostQuestion(store, p, "")
		arm(store, p.Question)
	})
	// And the counterpart. A question whose last waiter stopped waiting leaves the registry
	// silently; without this the record it published stays `pending` on the docket, offering
	// a decision nothing is left to receive. The reason comes with it, because "the task died"
	// and "an operator redirected the run" retire a question the same way and read to an
	// answerer completely differently.
	reg.OnDrop(func(q task.GateQuestion, why string) { mirror.DropQuestion(store, q.ID, why) })
	taskd.OnQuestion(func(taskID, scope string, p task.Pending) {
		// WHERE this question goes is the task's to say, not this closure's. taskd is one
		// runtime under baseline and every scoped engine, so a publisher registered once
		// closed over the baseline store and posted every scope's questions there — while
		// the task's own record went to the scope that spawned it. `verse gates` then
		// showed a pending gate `verse ps` would not list, on every dev's docket at once,
		// naming content only one of them could read (git-issue kwxxm8cth12ksn99fhn0).
		//
		// The scope rides on the launch, so it is known before the task is, and every
		// record this callback writes goes to the same store as the instance record: the
		// docket a reader sees is the tasks a reader sees.
		store := s.storeForScope(scope)
		// A task gate's question knows what it would DO but not what it is about: the
		// flow belongs to the task, not to the action. Fill it in from the mirror, so a
		// rule can select "the questions about demo:pr:1" by matching one record rather
		// than joining two. The docket read it off the engine before; the engine forgets
		// when it restarts, and the record does not.
		if p.Question.Subject == "" {
			if e, ok, _ := store.Get(mirror.Ref(taskID)); ok {
				p.Question.Subject = entity.Str(e.Payload, "flow")
			}
		}
		owner(store, &p.Question)
		watch(store, p.Question, taskID)
		mirror.PostQuestion(store, p, taskID)
		arm(store, p.Question)
	})

	// The Spawner owns the full lifecycle: host it via the runner, then mirror
	// its state into the verse so triggers can react to its completion.
	eng.SetSpawner(s.spawnerFor(store, "", nil))
	return s
}

// localRunner is the in-process runner: it hosts tasks on the embedded
// taskd runtime — the "/broker/local" handler. Remote runners (a base
// URL: k8s, lambda, a human broker) implement the same task.Runner behind the
// wire contract; the engine drives them through the identical seam. See docs/task-control.md.
type localRunner struct{ d *taskd.Daemon }

func (l localRunner) Spawn(spec task.Launch, content task.ContentReader) task.Handle {
	return l.d.Spawn(spec, content)
}

// Attach returns the still-live in-process handle for id, or nil — after a restart the
// in-memory runtime is gone, so the caller orphans (a local task cannot outlive verse).
func (l localRunner) Attach(id string) task.Handle {
	if p := l.d.Lookup(id); p != nil {
		return p
	}
	return nil
}

func (localRunner) Ref() string { return "local" }

// SetRunner rebinds how this server runs tasks — e.g. to a remote runner base
// URL (runner.HTTP) instead of the in-process local supervisor. The spawnerFor
// closures read s.runner at spawn time, so this takes effect for subsequent spawns
// (baseline and scoped). content federation is not forwarded to a remote runner yet.
func (s *Server) SetRunner(p task.Runner) { s.runner = p }

// UseBroker installs an in-process dial-in broker as the runner, hosted under a
// minted nonce, and returns it (e.g. for the operator to Revoke a claim). face is this
// verse's externally-reachable broker base (scheme://host:broker-addr — what a worker can
// reach); the broker publishes <face>/broker/<nonce> as each task's `dialURL`, closing
// the mint→publish→dial loop so a worker is told where to dial in rather than pre-configured.
func (s *Server) UseBroker(face string) *broker.Broker {
	b := broker.New()
	nonce := s.HostBroker("", b.WorkerHandler())
	b.SetBase(strings.TrimRight(face, "/") + "/broker/" + nonce)
	s.SetRunner(b)
	return b
}

// BrokerHandler serves this verse's broker face — the runner contract hosted under
// /broker/<nonce>, so a remote verse or a worker reaches a runner through verse's own URL
// rather than a separate service (see docs/task-control.md "Hosting"). The well-known
// "local" nonce is the built-in supervisor (taskd); any other nonce is a handler
// registered via HostBroker (e.g. a dial-in broker's WorkerHandler). It is intended for a
// SIBLING port, kept distinct from the trusted operator API.
func (s *Server) BrokerHandler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/broker/{nonce}/", func(w http.ResponseWriter, r *http.Request) {
		nonce := r.PathValue("nonce")
		h := s.brokerFor(nonce)
		if h == nil {
			http.NotFound(w, r)
			return
		}
		// Strip /broker/<nonce> so the mounted handler sees its own /tasks/... paths,
		// exactly as it would if hosted at a root.
		http.StripPrefix("/broker/"+nonce, h).ServeHTTP(w, r)
	})
	return mux
}

// brokerFor resolves a nonce to its worker-facing handler. "local" is the built-in
// supervisor; every other nonce must have been registered via HostBroker.
func (s *Server) brokerFor(nonce string) http.Handler {
	if nonce == "local" {
		return s.Taskd.Handler()
	}
	s.brokerMu.Lock()
	defer s.brokerMu.Unlock()
	return s.brokers[nonce]
}

// HostBroker mounts a worker-facing handler under /broker/<nonce> and returns the nonce a
// worker dials. Pass a well-known nonce to pin the URL, or "" to mint a fresh unguessable
// one — the nonce is the capability, so an unguessable mint is the private default. The
// handler is expected to serve the worker-facing dial-in protocol (claim/inbox/report),
// e.g. broker.Broker.WorkerHandler().
func (s *Server) HostBroker(nonce string, h http.Handler) string {
	if nonce == "" {
		nonce = mintNonce()
	}
	s.brokerMu.Lock()
	if s.brokers == nil {
		s.brokers = map[string]http.Handler{}
	}
	s.brokers[nonce] = h
	s.brokerMu.Unlock()
	return nonce
}

// mintNonce returns an unguessable path segment used as a broker capability.
func mintNonce() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

// spawnerFor builds a Spawner that hosts a task via the runner and mirrors its
// state into st — the baseline store for the baseline engine, a scoped store for a
// per-scope engine, so a scoped flow's task state stays in that scope. content (nil
// for baseline) routes a spawned agent's read_file through the verse.
// scope is the federation st belongs to (empty for baseline). It is passed rather than
// derived from st because a task must be able to say where it lives before it exists —
// see task.Launch.Scope, and git-issue kwxxm8cth12ksn99fhn0 for what publishing without
// it cost.
func (s *Server) spawnerFor(st entity.Store, scope string, content task.ContentReader) trigger.Spawner {
	return func(id string, spec launch.Spec) task.Handle {
		// WHERE this runs, resolved before anything is attempted, so the meta below records
		// the runner the work actually went to rather than the default it did not.
		place, placeName, err := s.runnerFor(st, spec.Runner)
		if err != nil {
			// Same shape as the envelope and the resolver below: a spawn that cannot run
			// where it said does not run somewhere else instead.
			meta := mirror.Meta{Subject: spec.Subject, Chain: spec.Chain, Prompt: spec.Prompt}
			h := task.FailedHandle(fmt.Sprintf("spawn %s: %v", id, err))
			s.reflect(st, id, h, meta)
			return h
		}
		// The provenance every reflection of this spawn carries, envelope included: which
		// doer a selection picked and what the launch may do. Built once so the failure path
		// and the running path cannot disagree about what was attempted.
		meta := mirror.Meta{
			Subject: spec.Subject, Runner: place.Ref(), RunnerName: placeName,
			Chain: spec.Chain, Prompt: spec.Prompt, Occurrence: spec.Occurrence,
			Doer: spec.Doer, Grant: spec.Grant, Tools: spec.Tools,
			Resolver: spec.Resolver,
		}
		if why := ghostResolver(st, spec.Resolver); why != "" {
			// The same shape as an envelope this build does not have, below: a spawn that
			// cannot be supervised as written does not run unsupervised instead.
			h := task.FailedHandle(fmt.Sprintf("spawn %s: %s", id, why))
			s.reflect(st, id, h, meta)
			return h
		}
		l, err := launchOf(id, spec)
		if err != nil {
			// A charter that names an envelope this build does not have must not run
			// UNBOUNDED. Dropping the field is how `grant: read-only` came to mean nothing
			// in the first place, and defaulting a misspelling to the wildcard is the same
			// mistake with an error message attached. So the spawn fails, visibly, and the
			// trigger's own failure path carries the reason.
			h := task.FailedHandle(fmt.Sprintf("spawn %s: %v", id, err))
			s.reflect(st, id, h, meta)
			return h
		}
		l.Scope = scope                // where this task's records live, so its gates land there too
		l.Occurrence = spec.Occurrence // and which run it is, so the process can say so too
		h := place.Spawn(l, content)
		s.reflect(st, id, h, meta)
		// Who is watching this run, if anybody was named. Minted beside the mirror because
		// the two answer adjacent questions — what the work is doing, and who is reading it.
		mirror.BindResolver(st, l.Resolver, resolverName(l.Resolver), id)
		return h
	}
}

// launchOf turns a bound launch.Spec into the runner-facing task.Launch, resolving the
// two axes that are named rather than written out.
//
// This is the consumer those fields never had. `vector:` and `grant:` have been on the spec
// since it was written — parsed, persisted, expanded by Bind, rendered back by `trigger
// show` — and read by nothing, so a charter could state an envelope, see it echoed, and get
// an agent that ignored it. Resolution happens HERE, on the engine side, rather than at
// whatever runtime ends up hosting the task: the vocabulary then lives in one place, and
// what crosses the wire is the predicate rather than a word two builds might read
// differently.
//
// Resolver is still unread, and still named in the spec — there is no registry of named
// resolvers to resolve it TO. Naming that plainly is better than another silent drop.
//
// Environment is now CARRIED rather than resolved, and the difference from vector and grant
// is the point: verse holds the vocabulary for an envelope because it can enforce one, and
// holds none for a workspace because only the runner can build one. So it travels as a name,
// the way `driver:` does, and a runner handed one it does not have refuses the launch. That
// is the first consumer the field has had (docs/working/coding-runner-residual.md).
//
// Provenance travels for a different reason: not so the runner can decide anything, but so
// it can record why it was asked. Subject and Chain are what a firing knows about its own
// cause, and verse has always kept them on its side of the wire — which left a remote runner
// unable to attribute its own effects (docs/working/attribution.md).
// callsOf carries a charter's call list across to the runtime form and CHECKS it, which is the
// point: `fatal: sideways` is the charter's error, reported when the trigger fires, rather than
// a stop condition nothing will ever match on a machine its author does not have.
func callsOf(cs []launch.Call) ([]task.Call, error) {
	out := make([]task.Call, 0, len(cs))
	for _, c := range cs {
		tc := task.Call{Tool: c.Tool, Input: c.Input}
		// nil, empty and listed are three different statements — "anything ends this call",
		// "nothing does", and "these do" — so the conversion preserves which it was.
		if c.Fatal != nil {
			fs := make([]task.StepOutcome, 0, len(*c.Fatal))
			for _, f := range *c.Fatal {
				fs = append(fs, task.StepOutcome(f))
			}
			tc.Fatal = &fs
		}
		out = append(out, tc)
	}
	if err := task.CheckCalls(out); err != nil {
		return nil, err
	}
	return out, nil
}

func launchOf(id string, spec launch.Spec) (task.Launch, error) {
	l := task.Launch{
		ID: id, Prompt: spec.Prompt, Driver: spec.Driver,
		Frame: spec.Frame, Tools: spec.Tools, Answer: spec.Answer, Retain: spec.Retain,
		Resolver: spec.Resolver, Environment: spec.Environment,
		Provenance: task.Provenance{Subject: spec.Subject, Chain: spec.Chain},
	}
	// The contract travels as source, but it is PARSED here as well, and the parse is the
	// point: a charter that wrote a contract it cannot mean fails its firing now, instead of
	// paying for a model call, a run, and a repair round before failing a check it could
	// never have passed. Same discipline as the two axes below — the engine resolves what it
	// can before the spawn crosses the seam. (The runtime parses it again for real; this is
	// a gate, not a hand-off.)
	if spec.Answer != nil {
		if _, err := answer.Parse(spec.Answer); err != nil {
			return task.Launch{}, fmt.Errorf("answer contract: %w", err)
		}
	}
	// The call list, typed and checked here for the same reason: a charter that wrote
	// `fatal: [sideways]` should fail its firing, not produce a task whose stop condition
	// silently never matches. The tool NAMES cannot be checked here — `push` is a runner's
	// word, not the engine's — so an unknown one is refused at the far end, the way an
	// environment name is.
	if len(spec.Calls) > 0 {
		calls, err := callsOf(spec.Calls)
		if err != nil {
			return task.Launch{}, err
		}
		l.Calls = calls
		// The field selects the driver, so a charter writing a list need not also name one.
		// A spawn that names a DIFFERENT driver is refused rather than having one of the two
		// quietly ignored: whichever we picked, half of what the charter said would be a lie.
		switch spec.Driver {
		case "", task.DriverCalls:
			l.Driver = task.DriverCalls
		default:
			return task.Launch{}, fmt.Errorf(
				"a spawn carrying calls: cannot also be driver %q — the calls ARE the driver", spec.Driver)
		}
	}
	vec, err := task.NamedVector(spec.Vector)
	if err != nil {
		return task.Launch{}, err
	}
	l.Vector = vec
	g, ok, err := task.NamedGrant(spec.Grant)
	if err != nil {
		return task.Launch{}, err
	}
	if ok {
		l.Grant = &g
	}
	return l, nil
}

// reconcileTasks runs on Start: for each non-terminal task:instance, either RESUME
// (a remote runner still running the work — re-attach and re-mirror from the entity's
// runner + cursor) or ORPHAN (a local in-process runtime that died with the previous
// run). This refines the old orphan-only reconcile now that a task can outlive verse
// behind a remote runner. Control re-tracking (cancel/say a resumed task) is a
// follow-on; this keeps the entity truthful.
func (s *Server) reconcileTasks() {
	var resumed []resumedRun
	for _, ent := range s.Store.List(func(r entity.Ref) bool { return r.System == mirror.System }) {
		if ent.Ref.Kind == mirror.RunKind {
			// A record of a run that HAPPENED, not a task that is running. Named explicitly
			// for the reason the claim below is: it carries a `status` like an instance does,
			// so nothing later would catch it, and reconcile would try to resume history —
			// dialling a runner about a run that ended, or stamping `orphaned` on the record
			// of one that did not.
			continue
		}
		if ent.Ref.Kind == mirror.ClaimKind {
			// A run claim, not a task. It is cleared below rather than here, and it is
			// named explicitly so it is not merely skipped by accident (a claim carries no
			// `status`, so it would fall through the check that follows).
			continue
		}
		status := entity.Str(ent.Payload, "status")
		if status == "" || task.TerminalStatus(status) {
			continue
		}
		runnerRef := entity.Str(ent.Payload, "runner")
		subject := entity.Str(ent.Payload, "flow")
		// Reflect REPLACES the entity's payload, so provenance it does not carry is lost on
		// re-attach. Read the chain back off the entity and hand it straight in: it is the
		// address of the action that spawned this task, and a resumed task should not
		// forget where it came from. AdoptChain re-derives its hooks from that address —
		// see docs/trigger-model.md.
		chain := entity.Str(ent.Payload, "chain")
		if runnerRef != "" && runnerRef != "local" { // remote: the work outlived verse
			// The URL is what this attaches with, and that is the whole reason it is stamped
			// beside the name. A restart that resolved the name instead would depend on the
			// runner ENTITY still existing, so removing one would orphan work that is running
			// perfectly well — a deletion with an action at a distance, and the thing that
			// would make step 1's `--leave` a lie.
			if h := runner.HTTP(runnerRef).Attach(ent.Ref.ID); h != nil {
				// re-attach: observe
				// The binding its spawn named is read back off the entity rather than
				// remembered: a resumed task must be watched by whoever was watching
				// it, and the launch that said so is long gone.
				s.reflect(s.Store, ent.Ref.ID, h, mirror.Meta{
					Subject: subject, Runner: runnerRef, Chain: chain,
					// Carried for the same reason chain is: Reflect REPLACES the payload, so
					// provenance this does not hand back is provenance the resumed task
					// forgets. `verse ps --runner <name>` reads this field, and a task
					// that lost it on restart would drop out of the answer while still
					// running there.
					RunnerName: entity.Str(ent.Payload, "runnerName"),
					// Carried, not re-stamped: a resumed task did not start again.
					StartedAt: entity.Str(ent.Payload, "startedAt"),
					Resolver:  entity.Str(ent.Payload, "resolver"),
				})
				// re-track: steerable, AND its hooks rehydrated from the chain, so the
				// chain or sequence it belongs to carries on rather than ending silently.
				s.Eng.AdoptChain(subject, ent.Ref.ID, chain, h)
				resumed = append(resumed, resumedRun{subject: subject, chain: chain})
				continue
			}
		}
		// local (or unresolvable): the in-process runtime is gone → orphan. Only the status
		// changes, so only the status is written — the rest of what the task recorded is
		// none of this reconcile's business.
		s.write(ent.Ref.At("status"), entity.Val(string(task.Orphaned)))
	}
	s.reconcileClaims(resumed)
	s.reconcileQuestions() // AFTER the above: it reads the statuses this just settled
}

// reconcileQuestions settles the questions nobody is waiting on any more.
//
// A question is a durable record and its WAITER is not: whatever blocked on the answer was a
// goroutine in the verse that just died. So the record outliving the restart is only useful
// where something can still receive an answer, and the docket has to say which is which —
// listing a question nobody will read asks a person to decide something into a void.
//
// Two cases, and the second is the one that is easy to miss:
//
//   - A TASK raised it. It survives exactly as its task does, which is the distinction
//     reconcileTasks already draws: resumed behind a runner that is still there, orphaned
//     when the runtime died with us. The question does not get its own opinion.
//   - A FIRING raised it, before anything spawned. That waiter was in this verse, always, so
//     a restart ends it unconditionally. If the rule still matches, its replay poses the
//     question again under the same deterministic id and it comes back answerable within
//     milliseconds — which is what a deterministic id is for. If it no longer matches, the
//     question was about a world that has moved on, and it should not be on a docket.
func (s *Server) reconcileQuestions() {
	for _, ent := range pendingQuestions(s.Store) {
		taskID, _ := entity.ToObj(ent.Payload)["task"].(string)
		if taskID != "" {
			e, ok, err := s.Store.Get(mirror.Ref(taskID))
			if err != nil {
				continue // the store is unwell; leave the record alone rather than guess
			}
			if ok && !task.TerminalStatus(entity.Str(e.Payload, "status")) {
				continue // its task is still believed alive, so the answer still has a reader
			}
		}
		s.write(ent.Ref.At("status"), entity.Val(mirror.QuestionOrphaned))
		s.write(ent.Ref.At("orphanedAt"), entity.Val(time.Now().UTC().Format(time.RFC3339)))
		s.write(ent.Ref.At("why"), entity.Val("the runtime that raised it did not survive a restart"))
	}
}

// write applies a value at a path during startup reconciliation, reporting a failure on
// stderr. Reconcile runs before the server serves, with no request to fail: a write that did
// not land leaves the verse claiming something untrue about a task, and the operator
// deserves to see that at the point it happens rather than to deduce it later.
func (s *Server) write(at entity.Path, v entity.Payload) {
	if _, _, err := s.Store.Write(entity.Write{At: at, Value: v}); err != nil {
		fmt.Fprintf(os.Stderr, "verse: reconcile: write %s: %v\n", at, err)
	}
}

// resumedRun is a task that survived the restart, and the chain it came from — enough to
// tell whether the sequence a claim guards is still alive.
type resumedRun struct{ subject, chain string }

// reconcileClaims drops run claims whose run did not survive the restart.
//
// A claim is an exclusive lease on a sequence, released when the sequence ends — however it
// ends, including at an off-ramp (Engine.stopSeq), which used to leak one and leave this
// function as the only thing that ever freed it. Nothing releases a claim across a RESTART, so
// what reaches here is a run that was interrupted, and the question is whether it survived.
//
// It survives when its task was re-adopted WITH its hooks — that task will complete,
// and its completion advances the sequence. Then the claim must stay: dropping it would let a
// concurrent firing start a second run alongside the one still going.
//
// It does not survive when the task was orphaned (a local runtime cannot outlive verse) or
// when nothing was resumed at all. Then no completion is coming, and a surviving claim would
// block that sequence forever with no live task to explain why.
//
// So a claim is exactly as durable as the run it guards — which is what makes a durable lease
// safe rather than a liability, and is only decidable now that resume rehydrates hooks.
//
// Scoped engines hold their claims in their own scope stores; those are created lazily and
// are not walked here. A scoped sequence therefore keeps a stale claim across a restart —
// worth fixing when scoped resume is, since both are the same gap.
func (s *Server) reconcileClaims(resumed []resumedRun) {
	for _, ent := range s.Store.List(func(r entity.Ref) bool {
		return r.System == mirror.System && r.Kind == mirror.ClaimKind
	}) {
		if claimIsLive(ent, resumed) {
			continue
		}
		s.write(ent.Ref.At(), nil) // release: nothing stands there any more
	}
}

// claimIsLive reports whether any resumed task belongs to the sequence this claim holds:
// same subject, and a chain at or below the claim's own.
func claimIsLive(claim entity.Entity, resumed []resumedRun) bool {
	subject := entity.Str(claim.Payload, "flow")
	chain := entity.Str(claim.Payload, "chain")
	if subject == "" || chain == "" {
		return false
	}
	for _, r := range resumed {
		if r.subject == subject && trigger.ChainUnder(chain, r.chain) {
			return true
		}
	}
	return false
}

// storeContentReader resolves a federated file's content by fetching its
// local:dir-content-<scope>:<path> entity — served on demand by the connect content
// controller through docd, never stored. It reads via the baseline store, since the
// content mount is baseline (mounts are not scope-specific).
type storeContentReader struct {
	store entity.Store
	kind  string // dir-content-<scope>
}

func (c storeContentReader) ReadContent(path string) (string, bool, error) {
	e, ok, err := c.store.Get(entity.Ref{System: source.FederationSystem, Kind: c.kind, ID: path})
	if err != nil {
		return "", false, err
	}
	if !ok {
		return "", false, nil
	}
	body := entity.Get(e.Payload, "content")
	if body == nil {
		return "", false, nil
	}
	return body.String, true, nil
}

// engFor returns the engine serving a scope for read/manage operations: the baseline
// engine for the empty scope, the scope's engine if one exists, or nil if that scope
// has no engine yet (reads treat nil as empty — never create an engine on a GET).
func (s *Server) engFor(scope string) *trigger.Engine {
	if scope == "" {
		return s.Eng
	}
	s.scopeMu.Lock()
	defer s.scopeMu.Unlock()
	if se, ok := s.scopeEngs[scope]; ok {
		return se.eng
	}
	return nil
}

// scopeEngine returns the engine for a scope, creating and starting it on first use.
// It observes store.Scope(scope) — one dev's copy-on-write federation — and spawns
// into that same scope. Requires Start to have captured the lifetime ctx.
func (s *Server) scopeEngine(scope string) *trigger.Engine {
	s.scopeMu.Lock()
	defer s.scopeMu.Unlock()
	if se, ok := s.scopeEngs[scope]; ok {
		return se.eng
	}
	scoped := s.Store.Scope(scope)
	eng := trigger.New(scoped, s.Reg)
	if s.tripPaceSet { // same throttle policy as the baseline it overlays (see SetTripPace)
		eng.SetTripPace(s.tripPace)
	}
	// Scoped agents read file content through the verse (the connect content
	// controller), so a validator isn't tied to the machine holding the files.
	content := storeContentReader{store: s.Store, kind: source.ContentPrefix + scope}
	eng.SetSpawner(s.spawnerFor(scoped, scope, content))
	sctx, cancel := context.WithCancel(s.ctx)
	// Per-trigger observation: no root watch. Each scoped trigger watches exactly the
	// (system,kind) subtree it matches, scoped to this federation. cancel stops them.
	eng.ObserveScoped(sctx)
	s.scopeEngs[scope] = &scopeEngine{eng: eng, cancel: cancel}
	return eng
}

// Start begins the engine's reactive loop (observing the store). It returns
// immediately; the loop runs until ctx is cancelled. It first reconciles orphans —
// task:instance entities a previous run left running — since this is the monolith
// (the task runtime restarts with the engine, so those tasks are gone).
func (s *Server) Start(ctx context.Context) {
	s.ctx = ctx        // scoped engines (created later, per federation) start under this
	s.reconcileTasks() // resume remote-hosted tasks; orphan local-dead ones
	// Per-trigger subtree observation — no root watch. The baseline engine watches
	// exactly the (system[,kind]) subtree each trigger matches, the same docd-aligned
	// model the scoped engines use; a mount under verse no longer collides with (and
	// force-ends) a whole-verse root watch. The old root-watch dispatch loop (Start)
	// and its cursor/steering control plane remain in the engine, dormant, for
	// reintroduction when engine-wide steering is wired.
	s.Eng.ObserveScoped(ctx)

	// The durable charter: the rules standing in the verse are reinstalled, so the program a
	// restart comes back to is the one it went down with. Each opens its own watch as it
	// installs and REPLAYS what already matches it — which is why this follows reconcile: a
	// restored trigger should see a task entity that tells the truth about the restart
	// rather than the `running` a dead runtime left behind.
	//
	// And why it follows ObserveScoped, which is what binds ctx to the engine: a goal
	// restored before that would run its monitor under a context nothing cancels, so it
	// would outlive the shutdown that was supposed to stop it.
	//
	// A rule that could not be restored is reported and skipped, so one bad entity does not
	// cost the rest of the charter. Scoped charters are not restored here: a scope's engine
	// is created on demand (verse connect) and a scoped store reads baseline layered
	// underneath, so restoring one means enumerating scopes and reading only the scope's own
	// layer — the same gap scoped resume has (see reconcileClaims).
	// The ORDERING first: a rule names the stratum it lives in, and one restored before its
	// stratum is held rather than installed — correct, and it would then only start firing when
	// something happened to touch the stratum again. Loading strata first makes the restart what
	// it should be, an install over a verse that has moved on.
	if err := s.Eng.LoadStrata(); err != nil {
		fmt.Fprintf(os.Stderr, "verse: strata: %v\n", err)
	}
	if err := s.Eng.LoadCharter(); err != nil {
		fmt.Fprintf(os.Stderr, "verse: charter: %v\n", err)
	}
	// Then the goals still being pursued, each reinstalling the charter it owns. After the
	// standalone charter, deliberately: the two share one name space, so a collision leaves
	// the rule somebody installed directly holding its name.
	if err := s.Eng.LoadGoals(); err != nil {
		fmt.Fprintf(os.Stderr, "verse: goals: %v\n", err)
	}
	// And the sources, last: each one's first pass is a write the restored charter should be
	// standing to react to. A level-triggered pass does not repeat itself, so what a rule
	// missed by being late it would miss for good.
	s.restoreSources()
}

// Handler returns the HTTP surface. Observation (read plane) and control are
// deliberately distinct routes.
func (s *Server) Handler() http.Handler {
	rt := &router{mux: http.NewServeMux()}
	// observation (read plane). The response of every route is declared as one/list/stream
	// (or nothing), which is what the published table needs to be right about a listing and
	// what the root schema is derived from — see rootSignature.
	rt.handle("GET /status", s.status, "", one("verse-status"))
	rt.handle("GET /watch", s.watch, "", stream("verse-event"))
	rt.handle("GET /state", s.state, "", one("verse-state"))
	rt.handle("GET /entity", s.getEntity, "", one("verse-entity"))
	rt.handle("GET /gates", s.listGates, "", nothing)
	// The distinct subjects with live work — addresses, so a list of plain strings rather
	// than of anything verse names. That is the whole shape, so it is published.
	rt.handle("GET /flows", s.listFlows, "", list("string"))
	rt.handle("GET /flow/tree", s.flowTree, "", list("verse-flow-tree"))
	rt.handle("GET /triggers", s.listTriggers, "", list("verse-trigger"))
	rt.handle("GET /triggers/{name}", s.getTrigger, "", one("verse-trigger"))
	rt.handle("GET /strata", s.listStrata, "", list("verse-stratum"))
	rt.handle("GET /strata/{name}", s.getStratum, "", one("verse-stratum"))
	rt.handle("GET /goals", s.listGoals, "", list("verse-goal"))
	// the API describing itself: the route table, and the schema its entries name
	rt.handle("GET /api", s.serveAPI, "", one("verse-routes"))
	rt.handle("GET /schema", s.serveSchema, "", nothing)
	rt.handle("GET /schema/{name}", s.serveDefinition, "", nothing)
	// mutation + control (out-of-band from observation; priority-serviced)
	rt.handle("POST /entity", s.putEntity, "verse-put-entity", one("verse-entity"))
	rt.handle("DELETE /entity", s.deleteEntity, "", nothing)
	rt.handle("POST /triggers", s.addTrigger, "verse-trigger", nothing)
	rt.handle("DELETE /triggers/{name}", s.removeTrigger, "", nothing)
	// resolvers — who answers a question. A binding is installed rather than declared in a
	// charter, because it outlives any one rule and is managed by whoever supervises rather
	// than by whoever wrote the work.
	rt.handle("GET /resolvers", s.listResolvers, "", nothing)
	rt.handle("POST /resolvers", s.addResolver, "", nothing)
	rt.handle("GET /resolvers/{kind}/{name}", s.getResolver, "", nothing)
	rt.handle("DELETE /resolvers/{kind}/{name}", s.removeResolver, "", nothing)
	// runners — WHERE work runs, as against who answers for it. Installed rather than chosen
	// at startup, so a drive can add one for work this verse does not otherwise need to know
	// about. See docs/working/per-task-runners.md.
	// sources — what REFLECTS an external system into the verse, as against where work runs.
	// Addressed by the slice reflected, because that is a question verse already asks: who
	// writes `git:*`. See docs/sources.md.
	rt.handle("GET /sources", s.listSources, "", nothing)
	rt.handle("POST /sources", s.addSource, "", nothing)
	rt.handle("GET /sources/{system}/{name}", s.getSource, "", nothing)
	rt.handle("GET /sources/{system}/{name}/check", s.checkSource, "", nothing)
	rt.handle("DELETE /sources/{system}/{name}", s.removeSource, "", nothing)
	// The hint: a delivery that carries WHICH SLICE and nothing else, so it can cost at most
	// a reconcile of a slice its sender does not choose the contents of.
	rt.handle("POST /sources/{system}/{name}/hint", s.hintSource, "", nothing)
	rt.handle("GET /runners", s.listRunners, "", nothing)
	rt.handle("POST /runners", s.addRunner, "", nothing)
	rt.handle("GET /runners/{kind}/{name}", s.getRunner, "", nothing)
	rt.handle("GET /runners/{kind}/{name}/check", s.checkRunner, "", nothing)
	rt.handle("DELETE /runners/{kind}/{name}", s.removeRunner, "", nothing)
	// The two streams a resolver lives on. Addressed by INSTANCE — the binding watching one
	// run — so neither route needs a second parameter to say which run it is about.
	rt.handle("GET /resolvers/instance/{id}/events", s.resolverEvents, "", stream("verse-frame"))
	rt.handle("POST /resolvers/instance/{id}/verdicts", s.resolverVerdict, "verse-verdict", nothing)
	rt.handle("POST /resolvers/instance/{id}/position", s.resolverPosition, "verse-position", nothing)
	rt.handle("POST /strata", s.addStratum, "verse-stratum", nothing)
	rt.handle("DELETE /strata/{name}", s.removeStratum, "", nothing)
	rt.handle("POST /goals", s.addGoal, "verse-goal", nothing)
	rt.handle("DELETE /goals/{name}", s.removeGoal, "", nothing)
	rt.handle("POST /goal/pause", s.goalPause, "verse-goal-steer", one("verse-steered"))
	rt.handle("POST /goal/resume", s.goalResume, "verse-goal-steer", one("verse-steered"))
	rt.handle("POST /goal/cancel", s.goalCancel, "verse-goal-steer", one("verse-steered"))
	rt.handle("POST /goal/say", s.goalSay, "verse-goal-steer", one("verse-steered"))
	rt.handle("POST /scope/delete", s.deleteScope, "", nothing)
	rt.handle("POST /gate/resolve", s.resolveGate, "verse-resolve", nothing)
	rt.handle("POST /flow/pause", s.flowPause, "verse-flow-steer", one("verse-steered"))
	rt.handle("POST /flow/resume", s.flowResume, "verse-flow-steer", one("verse-steered"))
	rt.handle("POST /flow/cancel", s.flowCancel, "verse-flow-steer", one("verse-steered"))
	rt.handle("POST /flow/say", s.flowSay, "verse-flow-steer", one("verse-steered"))
	// task runtime (task) — mounted; serves /tasks[/{id}[/…]]. versed intercepts the
	// two READ routes to union the local runtime with the active runner's own tasks
	// (e.g. a broker's), stamping each with its runner; everything else (spawn, events,
	// control) falls through to the local runtime.
	ph := s.Taskd.Handler()
	rt.handle("GET /tasks", s.listTasks, "", list("verse-instance"))
	rt.handle("POST /tasks", s.spawnTask, "verse-spawn", one("verse-spawned"))
	rt.handle("GET /tasks/{id}", s.getTask, "", one("verse-instance"))
	rt.handle("GET /tasks/{id}/inbox", s.taskInbox, "", list("verse-inbox-item"))
	rt.handle("GET /tasks/{id}/events", func(w http.ResponseWriter, r *http.Request) {
		s.taskEvents(w, r, ph)
	}, "", stream("verse-frame"))
	// cancel/say are runner-agnostic: local tasks go to the runtime, brokered/remote
	// ones to the runner's handle — so `task cancel`/`say` work wherever a task runs.
	rt.handle("POST /tasks/{id}/cancel", func(w http.ResponseWriter, r *http.Request) {
		s.taskControl(w, r, ph, "cancel")
	}, "", nothing)
	rt.handle("POST /tasks/{id}/say", func(w http.ResponseWriter, r *http.Request) {
		s.taskControl(w, r, ph, "say")
	}, "verse-say", nothing)
	// Answering is the ONE path (answerQuestion), not a case of taskControl: this route
	// differs from `/gate/resolve` only in being TOLD which task holds the question
	// instead of working it out. Everything that follows — attribution, delivery, the record,
	// a stale answer — is the same act and must not have two implementations.
	rt.handle("POST /tasks/{id}/resolve", func(w http.ResponseWriter, r *http.Request) {
		var body api.Resolve
		if err := codec.Read(r, &body); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		s.answerQuestion(w, r, body, r.PathValue("id"))
	}, "verse-resolve", nothing)
	// re-vector (slide autonomy on a running task) — runner-agnostic like cancel/say: a
	// local task is served by the runtime mount (taskd's setVector), a brokered/remote
	// one goes to the runner handle's SetVector.
	rt.handle("PUT /tasks/{id}/vector", func(w http.ResponseWriter, r *http.Request) {
		s.taskControl(w, r, ph, "vector")
	}, "verse-set-vector", nothing)
	rt.mount("/tasks", ph)
	rt.mount("/tasks/", ph)
	s.routes = rt.routes
	// Every answer says which verse gave it, and a client that cannot be talking to this one
	// is turned away before any of the above runs. See versioned.
	return versioned(rt.mux)
}

// ── scripted driver: the default task runtime driver ─────────────────────
//
// A dependency-free driver that performs a few steps of "work" then completes, so
// the runtime is exercisable without model credentials. `verse up --llm` swaps in
// the LLM harness.

// ScriptedBuilder builds the credential-free task runtime driver: it runs the prompt as a real
// shell command, confined to root and cancellable via the task context — a governed shell
// command as a task. It is the dependency-free stand-in for an LLM agent, so the runtime is
// exercisable and steerable (and can run real work like `sleep`) without model credentials.
// ScriptedBuilder is the taskd.Builder form (root = the task's cwd). `verse up` uses
// NewScriptedTask directly to confine scripted commands to the agent root.
func ScriptedBuilder(l task.Launch, reg *task.Registry, _ task.ContentReader) *task.Task {
	return NewScriptedTask(l, "", reg)
}

// NewScriptedTask builds a scripted task rooted at root — a call list of exactly one call,
// which is what `scripted` always was.
//
// It used to be its own driver with its own vector, hardcoded to Auto. Both are gone. The
// driver is task's, because one shell command is the degenerate case of a list of calls and
// keeping a second implementation of "propose one action, then stop" bought nothing; the
// vector is now the SPAWN's, because the old one was not a default but an override — it
// ignored a `vector:` the charter had written, so a charter asking to be asked was disobeyed
// in silence.
//
// The consequence is deliberate and is the reason this is a contract change: a `driver:
// scripted` spawn that names no vector now ASKS, where it used to run unattended. The
// exemption that justified auto — "you authorized it by typing it" — is true of `verse task
// run --driver scripted` and of nothing else, so it lives on that path now (cmd/verse/task.go)
// rather than travelling to charters that typed nothing.
func NewScriptedTask(l task.Launch, root string, reg *task.Registry) *task.Task {
	// An empty prompt is an empty list: the task completes having done nothing, which is
	// what the old driver did with a blank command.
	var calls []task.Call
	if strings.TrimSpace(l.Prompt) != "" {
		calls = []task.Call{{Tool: "run_bash", Input: map[string]any{"command": l.Prompt}}}
	}
	return task.NewCallsTaskFrom(l, calls, reg, &shellExec{root: root})
}

// shellExec runs run_bash for real: cancellable via ctx (so `cancel` kills a running command),
// confined to root. It is the scripted driver's counterpart of the harness's fsExec.
type shellExec struct{ root string }

func (e *shellExec) Execute(ctx context.Context, tu task.ToolUse) (task.StepResult, []task.ToolUse, error) {
	// bash -c (non-login): inherits the server's env/PATH, and avoids the login shell's
	// job-control setpgid ("failed to change group ID") that fails in restricted envs (CI, sandboxes).
	c := exec.CommandContext(ctx, "bash", "-c", str(tu.Input, "command"))
	c.Dir = e.root
	out, err := c.CombinedOutput()
	res := strings.TrimSpace(string(out))
	// The code, alongside the Err this executor folds it into. Both, because the fold is this
	// executor's reading and a driver may want the fact underneath it — see
	// task.StepResult.Exit. A spawn failure (no such binary) has no exit code and gets none.
	var code *int
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		n := ee.ExitCode()
		code = &n
	}
	if err != nil {
		if res != "" {
			res += "\n"
		}
		res += "exit: " + err.Error()
	}
	if res == "" {
		res = "(ok)"
	}
	// The failure rides in the StepResult rather than as an Execute error, because Run
	// discards the result on an error return and the command's output is the whole point
	// of a failure. A cancel is not a failure: the kill that ends `sleep` surfaces here as
	// an error too, and Run's boundary check reaches ctx.Err() first — but say so here, so
	// the driver's cancel and failure paths do not depend on that ordering.
	if ctx.Err() != nil {
		err = nil
	}
	return task.StepResult{Use: tu, Output: res, Err: err, Exit: code}, nil, nil
}

func str(m map[string]any, k string) string { s, _ := m[k].(string); return s }

// deleteScope drops the ?scope copy-on-write overlay and all its data — federation
// teardown. It runs against the baseline store (only baseline may delete a scope), so it
// is independent of scopedStore.
func (s *Server) deleteScope(w http.ResponseWriter, r *http.Request) {
	name := r.URL.Query().Get("scope")
	if name == "" {
		http.Error(w, "scope delete: ?scope required", http.StatusBadRequest)
		return
	}
	// Stop the scope's SOURCES before its data goes away: a reflector writing into a scope
	// that no longer exists would be re-creating an overlay nobody asked for, one converge at
	// a time. This is what makes a scoped source's lifetime free — it has no deregistration
	// path to forget, because the scope going away takes it.
	s.stopScopeSources(name)
	// Stop the scope's engine (if any) before its data goes away, so it isn't
	// observing a store whose scope is being deleted.
	s.scopeMu.Lock()
	if se, ok := s.scopeEngs[name]; ok {
		se.cancel()
		delete(s.scopeEngs, name)
	}
	s.scopeMu.Unlock()
	if err := s.Store.DeleteScope(name); err != nil {
		http.Error(w, "scope delete: "+err.Error(), http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// scopedStore returns the store view for the request's ?scope (baseline if absent).
// The seam for local-dev federation — the logd-backed store isolates the scope (see
// docs/deployment-and-steering.md).
// storeForScope is the store a scope's records belong in: the scope's own overlay, or
// baseline when the scope is empty. It is the non-HTTP half of scopedStore — same question,
// asked by machinery that has a task rather than a request.
func (s *Server) storeForScope(scope string) entity.Store {
	if scope == "" {
		return s.Store
	}
	return s.Store.Scope(scope)
}

func (s *Server) scopedStore(r *http.Request) entity.Store {
	if sc := r.URL.Query().Get("scope"); sc != "" {
		return s.Store.Scope(sc)
	}
	return s.Store
}

// queryPattern reads the PAYLOAD half of the selector a read-plane route was given: ?if is a
// tony match over the payload, rooted at the field path in ?if-at (one parameter per segment,
// so a field name is never re-split here).
//
// It no longer reads ?system/?kind, and nothing sends them: which entities a read is about is
// the positional PATH now (?at), which says the same thing at any depth instead of only at two
// — see querySelector, which pairs the two halves.
//
// ?if arrives as TONY whatever the operator typed, because a GET carries no Content-Type to
// declare a dialect in; the client parses in --ifmt and re-renders (see payloadFilter.apply).
func queryPattern(q url.Values) (entity.Pattern, error) {
	pred, err := codec.ParseNodeOrNil([]byte(q.Get("if")), codec.Tony)
	if err != nil {
		return entity.Pattern{}, fmt.Errorf("?if: %w", err)
	}
	return entity.Pattern{Predicate: pred, At: q["if-at"]}, nil
}

// selector is what a read-plane route was asked for: a path naming the entities, and a
// predicate over the payloads standing at it.
//
// It is ONE type because the streaming face and the listing face must name the SAME set —
// `verse watch --if X demo.pr` and `verse state --if X demo.pr` differ in when they answer, not
// in what they pick. That was true of the pattern alone before the path existed; it has to stay
// true now that the narrowing lives in two places, and it is no longer visible by eye, so
// TestStateAndWatchNameTheSameSet says it instead.
type selector struct {
	at  *kpath.KPath
	pat entity.Pattern
}

// querySelector reads both halves.
func querySelector(q url.Values) (selector, error) {
	pat, err := queryPattern(q)
	if err != nil {
		return selector{}, err
	}
	at, err := entity.ParseAt(q.Get("at"))
	if err != nil {
		return selector{}, fmt.Errorf("?at: %w", err)
	}
	return selector{at: at, pat: pat}, nil
}

// matchRef is the prefilter a store can apply for itself: the address dimensions, which is
// everything the path says down to the id.
func (s selector) matchRef(ref entity.Ref) bool { return entity.MatchesAt(ref, s.at) }

// matchEntity is the whole question about a stored entity, and matchEvent the whole question
// about a change.
func (s selector) matchEntity(e entity.Entity) bool {
	return s.matchRef(e.Ref) && s.pat.MatchEntity(e)
}

func (s selector) matchEvent(ev entity.Event) bool {
	return s.matchRef(ev.Ref) && s.pat.MatchEvent(ev)
}

// watch streams store changes as SSE, catching up from ?since=<rev> first, then
// live — the cursor model surfaced at the wire. What it delivers is queryPattern's selector.
func (s *Server) watch(w http.ResponseWriter, r *http.Request) {
	store := s.scopedStore(r)
	q := r.URL.Query()
	// Before NewSSE, which commits the response: a malformed predicate has to reach the
	// client as a 400 it can report, not as a header'd stream that then says nothing.
	sel, err := querySelector(q)
	if err != nil {
		codec.HTTPError(w, r, http.StatusBadRequest, err.Error())
		return
	}
	since, _ := strconv.ParseInt(q.Get("since"), 10, 64)
	match := sel.matchEvent

	// Subscribe BEFORE the catch-up snapshot, so an event landing between the two is
	// captured live rather than lost in the gap. The catch-up and the live stream
	// overlap; dedup by revision (send only strictly-ascending revs).
	ch, unsub := store.Observe()
	defer unsub()

	// A cursor the store cannot answer from is REFUSED, before NewSSE commits the response.
	//
	// `Since` reports ok=false when `since` is below what the store still retains, and it does
	// that deliberately rather than returning what it happens to still have. Skipping the
	// catch-up on that answer — which is what this did — hands the client an open stream with no
	// history and no notice, indistinguishable from a verse where nothing has happened. Observed
	// from the outside as: the same `--since 700` printing three commits, then nothing, then only
	// new ones, as the window slid past the cursor between runs.
	//
	// A gap MARKER would be the wrong shape here even though the live path has one: `Gap` means
	// "commits were dropped, recover them with Since", and Since is exactly what just could not
	// answer. So this is a refusal that says what to ask for instead — the caller wanted history
	// and has to decide whether to take what is retained or to stream from now.
	catchUp, ok := store.Since(since)
	if !ok {
		oldest := int64(0)
		if kept, keptOK := store.Since(0); keptOK && len(kept) > 0 {
			oldest = kept[0].Rev
		}
		codec.HTTPError(w, r, http.StatusConflict, fmt.Sprintf(
			"since=%d is below the retained window: the store still has commits from %d. "+
				"Ask for --since %d to replay what it kept, or drop --since to stream from now",
			since, oldest, oldest))
		return
	}

	sse, err := codec.NewSSE(w, r)
	if err != nil {
		codec.HTTPError(w, r, http.StatusInternalServerError, err.Error())
		return
	}

	// The wire is a stream of EVENTS, not commits: a reader of /watch is watching entities,
	// and the commit framing is the store's way of saying which changes were atomic — a fact
	// this surface has never carried and nothing consuming it asks for. So a commit is
	// flattened here, in commit order, and its Rev rides each event as it always did.
	last := since
	for _, c := range catchUp {
		for _, ev := range c.Changes {
			if match(ev) {
				_ = sse.Send(ev)
			}
		}
		if c.Rev > last {
			last = c.Rev
		}
	}
	sse.Flush()

	for {
		select {
		case <-r.Context().Done():
			return
		case c, ok := <-ch:
			if !ok {
				return
			}
			if c.Rev != 0 && c.Rev <= last {
				continue // already delivered by the catch-up (overlap window)
			}
			// A dropped commit is reported once, as a gap marker, not once per event it
			// held — the count is in commits and saying otherwise would overstate it.
			if c.Gap > 0 {
				_ = sse.Send(entity.Event{Rev: c.Rev, Gap: c.Gap})
			}
			for _, ev := range c.Changes {
				if match(ev) {
					_ = sse.Send(ev)
				}
			}
			if c.Rev > last {
				last = c.Rev
			}
		}
	}
}

// state answers the verse AT A PATH: the subtree standing there, which is what the store holds
// rather than a shape assembled for the wire. It is narrowed by the same selector /watch
// streams, so a read and a stream never disagree about what a filter names.
//
// The address dimensions go to the store as List's prefilter, which is where they belong: they
// are the ones a store can apply for itself. The payload predicate is answered here, because
// answering it needs the payload — so the split is what the store can do, not a second opinion
// about the selector.
//
// `--if` narrows WHICH ENTITIES remain, per entity, before the nesting. It never prunes inside
// a payload: the document's leaves are whole payloads, and a filter that reached into one would
// answer with entities that do not hold what they say they hold. (`--if-at` says where the
// predicate is evaluated, which is a different question again.)
//
// A path naming nothing is `{}` with 200 at collection depth and 404 below it. The cut is where
// a path stops describing a SLICE and starts naming a THING: `demo.nosuch` asks which prs a
// system that is not there has, and "none" is a true answer to it; `demo.pr.nosuch` names one
// entity, and answering `{}` would say it is there and empty. The empty answer is `{}` rather
// than an absent one for the same reason inverted — at collection depth the slice is the answer,
// and it is empty.
func (s *Server) state(w http.ResponseWriter, r *http.Request) {
	sel, err := querySelector(r.URL.Query())
	if err != nil {
		codec.HTTPError(w, r, http.StatusBadRequest, err.Error())
		return
	}
	ents := s.scopedStore(r).List(sel.matchRef)
	kept := make([]entity.Entity, 0, len(ents))
	for _, e := range ents {
		if sel.matchEntity(e) {
			kept = append(kept, e)
		}
	}
	doc, err := entity.Nest(kept, sel.at)
	if err != nil {
		codec.HTTPError(w, r, http.StatusBadRequest, err.Error())
		return
	}
	if doc.Node == nil {
		if entity.AtDepth(sel.at) >= 3 {
			codec.HTTPError(w, r, http.StatusNotFound, fmt.Sprintf(
				"nothing stands at %s", r.URL.Query().Get("at")))
			return
		}
		doc.Node = ir.FromMap(map[string]*ir.Node{})
	}
	writeOut(w, r, http.StatusOK, doc)
}

func (s *Server) getEntity(w http.ResponseWriter, r *http.Request) {
	ref, err := entity.ParseRef(r.URL.Query().Get("ref"))
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	e, ok, err := s.scopedStore(r).Get(ref)
	if err != nil {
		// A read that could not be performed is NOT a 404. Reporting it as one tells the
		// caller the entity does not exist, which is a different — and actionable —
		// statement from "the store is unreachable", and the caller acts on it: an agent
		// creates the file it was told is missing. 502: verse is up, its store is not.
		http.Error(w, "entity read failed: "+err.Error(), http.StatusBadGateway)
		return
	}
	if !ok {
		http.NotFound(w, r)
		return
	}
	// The entity itself. Its Ref carries MarshalText, so it still renders as the address
	// string this always sent — and the route table's `verse-entity` now names the type
	// actually written rather than a map that happened to match it.
	writeOut(w, r, http.StatusOK, e)
}

// deleteEntity removes whatever stands at a ref — or, with ?field=a&field=b, at one place
// inside it. Removing what is not there is not an error: the request says what should stand
// there afterwards, and nothing already does.
func (s *Server) deleteEntity(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	ref, err := entity.ParseRef(q.Get("ref"))
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if _, _, err := s.scopedStore(r).Write(entity.Write{At: ref.At(q["field"]...)}); err != nil {
		http.Error(w, "entity remove failed: "+err.Error(), http.StatusBadGateway)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// putEntity is the write surface: a value MERGES in at a place — an entity, or a field
// inside one — iff every stated condition holds.
//
// A refused condition is 409, not an error: the request was well-formed and the store was
// reachable, and the answer is "not while that is true". The success body is the entity as
// it now stands, read back, because a write that merges into a place is precisely the case
// where the caller cannot compute the result from what it sent.
func (s *Server) putEntity(w http.ResponseWriter, r *http.Request) {
	var body api.PutEntity
	if err := codec.Read(r, &body); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	ref, err := entity.ParseRef(body.Ref)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	when, err := conditions(body.When)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	store := s.scopedStore(r)
	committed, _, err := store.Write(entity.Write{At: ref.At(body.Field...), Value: body.Payload, When: when})
	if err != nil {
		http.Error(w, "entity write failed: "+err.Error(), http.StatusBadGateway)
		return
	}
	if !committed {
		http.Error(w, "write refused: a condition did not hold", http.StatusConflict)
		return
	}
	e, ok, err := store.Get(ref)
	if err != nil || !ok {
		w.WriteHeader(http.StatusNoContent) // it landed; we just cannot show it back
		return
	}
	writeOut(w, r, http.StatusOK, e)
}

// conditions maps the wire's preconditions onto the store's. A condition that asks nothing
// is refused rather than silently dropped: sending one means intending a guard, and a guard
// that is quietly not applied is worse than a rejected request.
func conditions(cs []api.Condition) ([]entity.Require, error) {
	out := make([]entity.Require, 0, len(cs))
	for _, c := range cs {
		var at entity.Path
		if c.Ref != "" {
			ref, err := entity.ParseRef(c.Ref)
			if err != nil {
				return nil, fmt.Errorf("condition ref %q: %w", c.Ref, err)
			}
			at = ref.At(c.Field...)
		}
		switch {
		case c.Pattern != nil:
			out = append(out, entity.Require{At: at, Pattern: c.Pattern})
		case c.Exists != nil && *c.Exists:
			out = append(out, entity.Require{At: at, Pattern: entity.Present()})
		case c.Exists != nil:
			out = append(out, entity.Require{At: at, Absent: true})
		default:
			return nil, fmt.Errorf("condition asks nothing: set `exists` or `pattern`")
		}
	}
	return out, nil
}

// listGates is the unified pending-gate view: engine (trigger) gates raised by a firing AND
// per-task gates (an agent blocked on write/shell/delegate). Both are published as
// `gate:question:<id>` records, and both are resolvable by id.
func (s *Server) listGates(w http.ResponseWriter, r *http.Request) {
	// The docket is a slice of the store, not a walk of two in-memory registries. That is
	// what makes it survive a restart for work that outlived the daemon, and what lets it
	// say what each question is ABOUT rather than only what it is called: the record carries
	// the tool, the derived effects and the subject, which is the difference between a name
	// and something a person can answer.
	//
	// Absent keys stay absent, as they did when this compacted a map: a missing key is
	// visible, an empty one is camouflage. The record is written that way, so nothing here
	// has to strip.
	// A reader arriving is when a deadline is evaluated: the docket a person opens is the
	// docket as of now, and `⊤` is a person reading.
	s.escalateOverdue(s.scopedStore(r))
	out := make([]map[string]any, 0)
	for _, ent := range pendingQuestions(s.scopedStore(r)) {
		p := entity.ToObj(ent.Payload)
		rec := map[string]any{}
		for k, v := range p {
			switch k {
			case "status", "posedAt": // structural: every pending question has them
			default:
				rec[k] = v
			}
		}
		// where says which plane raised it, and is derived rather than stored: a question
		// with a task is a run's, one without is a firing's. Storing it would be a third
		// spelling of what `task` already says.
		if _, ok := rec["task"]; ok {
			rec["where"] = "task"
		} else {
			rec["where"] = "engine"
		}
		out = append(out, rec)
	}
	writeOut(w, r, http.StatusOK, out)
}

// relaysControl reports whether an answer to this task's gates is QUEUED for a worker
// rather than applied. A local task's registry takes it on the spot and a remote runtime
// takes it over the wire that carried it; a broker holds it until somebody dials in.
func (s *Server) relaysControl(taskID string) bool {
	if taskID == "" {
		return false // a firing's gate: the registry answers it here, now
	}
	if _, ok := s.Taskd.SnapshotOf(taskID); ok {
		return false
	}
	if h := s.runner.Attach(taskID); h != nil {
		r, ok := h.(interface{ RelaysControl() bool })
		return ok && r.RelaysControl()
	}
	return false
}

// ownerOf says which task holds a question, empty for a firing's gate.
//
// The RECORD is asked first, and it is the only source that answers while the runner holding
// the work is down. The alternatives all interrogate the fleet — is this id a task, does
// any snapshot hold this gate — so an unreachable runner made every question on it look like
// a question about nothing, and the operator was sent hunting a typo for a gate that was open
// and waiting. Which is the one case where retrying is exactly right.
//
// The fallbacks remain for a question this verse never published: the id's own prefix (a
// runtime gate is `<task-id>~dN`), then a sweep of the snapshots for an executor-raised
// gate, whose id carries no owner by construction.
func (s *Server) ownerOf(store entity.Store, questionID string) string {
	if e, ok, err := store.Get(mirror.QuestionRef(questionID)); err == nil && ok {
		if p := entity.Str(e.Payload, "task"); p != "" {
			return p
		}
		// The record exists and names no task: a firing raised it, and the registry is
		// where it lives. Answering that is not a fleet question at all.
		return ""
	}
	owner := questionID
	if i := strings.Index(owner, task.GateSep); i >= 0 {
		owner = owner[:i]
	}
	if s.isTask(owner) {
		return owner
	}
	return s.taskHoldingGate(questionID)
}

// pendingQuestions selects the open questions: the docket, as a slice of the store.
//
// One selector, because `verse gate` and `verse status` must name the same set. They did not
// before — status counted the engine registry, which never held a task's questions and
// holds nothing at all after a restart, so it could report no gates beside a docket listing
// several.
func pendingQuestions(store entity.Store) []entity.Entity {
	var out []entity.Entity
	for _, ent := range store.List(func(ref entity.Ref) bool {
		return ref.System == mirror.QuestionSystem && ref.Kind == mirror.QuestionKind
	}) {
		if p := entity.ToObj(ent.Payload); p != nil && p["status"] == mirror.QuestionPending {
			out = append(out, ent)
		}
	}
	return out
}

func (s *Server) resolveGate(w http.ResponseWriter, r *http.Request) {
	var body api.Resolve
	if err := codec.Read(r, &body); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	s.answerQuestion(w, r, body, "")
}

// answerQuestion is the ONE answering path. Both routes reach it — `/gate/resolve`, which
// finds the owner from the id, and `/tasks/{id}/resolve`, which is told it — because the
// two differ in how the question is addressed and in nothing else. Having had two
// implementations is how `decisionId`/`proceed` drifted from `id`/`approve` unnoticed, and an
// answer's attribution and its record are exactly the things that must not drift.
//
// taskID is the task the answer is addressed to, empty when the caller did not say and the
// id must be read for it.
func (s *Server) answerQuestion(w http.ResponseWriter, r *http.Request, body api.Resolve, taskID string) {
	if body.ID == "" {
		http.Error(w, "id is required: an answer names the question it answers", http.StatusBadRequest)
		return
	}
	// Attribution is required, not defaulted. The server used to fill in `human:cli`, which
	// put a name on the record that belongs to nobody — and now that an answer is a durable
	// entry on the question, an unattributable one is a permanent unattributable one. A
	// decision is an act; acts have actors.
	if body.By == "" {
		http.Error(w, "by is required: a decision names who made it", http.StatusBadRequest)
		return
	}
	res := task.Resolution{Proceed: body.Approve, By: body.By, Steer: body.Say}

	store := s.scopedStore(r)
	// `exclusive` governs whose answer COUNTS. Not who may SEE the question: the docket is
	// unchanged, and a person may read — and argue about — what an exclusive binding owns.
	// And not a security boundary: it decides who answers an ask, never what an answer may
	// authorise. The grant does that, and the two are not the same axis.
	holder, exclusive := exclusiveOwner(store, body.ID)
	if exclusive && body.By != holder && !body.Override {
		http.Error(w, holder+" answers this question exclusively; "+
			"say override to answer it anyway, and the record will name you both",
			http.StatusForbidden)
		return
	}
	overriding := exclusive && body.By != holder
	owner := taskID
	if owner == "" {
		owner = s.ownerOf(store, body.ID)
	}

	var err error
	if owner != "" {
		err = s.resolveTaskDecision(owner, body.ID, res)
	} else if !s.Reg.Answer(body.ID, res) {
		err = task.ErrNoSuchDecision
	}
	if err == nil {
		// Accepted is not the same as delivered. A broker takes an answer for a worker that
		// must dial in and pull it, so the record says so — `delivered: false` — until the
		// worker's acknowledgement clears it. Everywhere else the answer has already landed
		// on the party that must act, and the record is simply settled.
		if s.relaysControl(owner) {
			mirror.QueuedAnswer(store, body.ID, res)
		} else {
			mirror.AnswerQuestion(store, body.ID, res)
		}
		if overriding {
			mirror.Overrode(store, body.ID, holder)
		}
		w.WriteHeader(http.StatusNoContent)
		return
	}

	// Nothing took the answer, and WHY is three different facts. Two of them used to be one
	// 404 and the third had no way to be said at all.
	//
	// The runner could not be reached. Nothing is known about the question — it may be open
	// and waiting — so this is not an answer about the question at all, and calling it "no
	// such question" sent the operator hunting a typo for a runner that was simply down.
	if errors.Is(err, task.ErrUnreachable) {
		http.Error(w, "the runner holding this question could not be reached: "+err.Error(),
			http.StatusBadGateway)
		return
	}
	if errors.Is(err, task.ErrUnsupported) {
		http.Error(w, "runner does not support gate resolution", http.StatusNotImplemented)
		return
	}
	// Somebody was here first. The loser is recorded rather than dropped: two people at a
	// docket answering at once is normal, and a docket that keeps only the winner cannot
	// show that the question was contested.
	won, settled := mirror.SettledBy(store, body.ID)
	if settled || errors.Is(err, task.ErrAlreadyAnswered) {
		if won == "" {
			won = "an earlier answer"
		}
		mirror.StaleAnswer(store, body.ID, res)
		http.Error(w, "already answered by "+won+"; your answer was recorded as stale", http.StatusConflict)
		return
	}
	http.Error(w, "no such question", http.StatusNotFound)
}

// instanceOf reads one resolver instance: the binding it is, and the task it watches.
//
// Both come off the record rather than being parsed out of the address. The address is for
// finding it; the fields are what the checks below compare, and a check that re-derives its
// inputs from a string is one that can disagree with what is stored.
func (s *Server) instanceOf(r *http.Request) (binding string, origin entity.Ref, ok bool) {
	ref := entity.Ref{System: resolver.System, Kind: resolver.InstanceKind, ID: r.PathValue("id")}
	e, found, err := s.scopedStore(r).Get(ref)
	if err != nil || !found {
		return "", entity.Ref{}, false
	}
	p := entity.ToObj(e.Payload)
	binding, _ = p["binding"].(string)
	got, _ := p["origin"].(string)
	origin, err = entity.ParseRef(got)
	if err != nil {
		return "", entity.Ref{}, false
	}
	return binding, origin, binding != "" && origin.ID != ""
}

// watchesARun reports which plane this instance is on. It follows from the ORIGIN and is not
// stored beside it: a run's fine grain hangs off the spine, and which one you are reading is
// a property of what you are watching rather than a flag that could disagree with it.
func watchesARun(origin entity.Ref) bool {
	return origin.System == mirror.System && origin.Kind == mirror.InstanceKind
}

// resolverEvents hands a resolver the stream of the run it watches — the task's OWN read
// plane, from `?since=`, not a projection of it.
//
// Context arrives before the ask, which is the whole reason a resolver is attached rather than
// handed a question: by the time it reads the gate it has read what led to it.
func (s *Server) resolverEvents(w http.ResponseWriter, r *http.Request) {
	s.escalateOverdue(s.scopedStore(r)) // a resolver arriving is a reader arriving
	_, origin, ok := s.instanceOf(r)
	if !ok {
		http.NotFound(w, r)
		return
	}
	if !watchesARun(origin) {
		s.streamSpine(w, r, origin)
		return
	}
	// No local special case: the runner attaches a live in-process handle by the same call,
	// so a resolver over a local run and one over a brokered run read through one path. The
	// task route splits only because it forwards to the runtime's own mount, and a
	// resolver is not asking that route's question.
	s.streamEvents(w, r, origin.ID)
}

// streamSpine serves the commits that touched one subject, from `?since=<revision>`.
//
// The narrowing is BY REF — every commit touching it, and nothing else. Not the binding's
// condition, and the reason is not tidiness: a predicate reads a payload and a deletion has
// none, so a condition-narrowed stream would hide exactly the end of a life it most needs to
// show. `sys:k:i` born, marked vanishing, deleted and written again under the same ref is one
// story, and a resolver for that ref sees all of it.
//
// A position here is a revision rather than a cursor, which is why an instance may be minted
// late and still read everything that led to a question: the store keeps its commits, so the
// subject's history is re-readable in a way a run's bounded ring is not.
//
// It also carries the ANCHORS: a run about this subject writes a `task:instance` entity
// naming it as its flow, and a spawn and a terminal are both commits there because the mirror
// writes them. That is what makes this the spine and task streams the fine grain hanging
// off it — a resolver reading here learns a run began, and its own instance for that run is
// where the frames are. Without them the spine would be a subject's history with the work
// about it left out, which is most of what a supervisor is watching for.
func (s *Server) streamSpine(w http.ResponseWriter, r *http.Request, subject entity.Ref) {
	store := s.scopedStore(r)
	sse, err := codec.NewSSE(w, r)
	if err != nil {
		codec.HTTPError(w, r, http.StatusInternalServerError, err.Error())
		return
	}
	since, _ := strconv.ParseInt(r.URL.Query().Get("since"), 10, 64)
	onSpine := func(ev entity.Event) bool { return spineCarries(ev, subject) }

	// Subscribe BEFORE the catch-up, so a commit landing between the two is carried live
	// rather than lost in the gap; the two overlap and revision order dedups them.
	ch, unsub := store.Observe()
	defer unsub()
	last := since
	if commits, ok := store.Since(since); ok {
		for _, c := range commits {
			for _, ev := range c.Changes {
				if onSpine(ev) {
					_ = sse.Send(ev)
				}
			}
			if c.Rev > last {
				last = c.Rev
			}
		}
	} else {
		// BELOW THE WINDOW. `Since` refuses a revision older than the store has observed —
		// logd may have compacted it — and this said nothing at all, so a reader could not
		// tell "nothing happened" from "you missed everything". A resolver is a client of
		// this call and nothing else, so it reconnected, learned nothing, and sat waiting
		// for a question that had already been asked.
		//
		// Say that it happened, and then say WHAT — which is the half that matters, and the
		// same two moves SubscribeSince makes on the task plane for the same reason.
		_ = sse.Send(entity.Event{Gap: 1})
		for _, ent := range pendingQuestions(store) {
			if entity.Str(ent.Payload, "subject") != subject.String() {
				continue
			}
			// Rev 0: this is current state, not a position. A reader tracks the highest Rev
			// it has processed, and a replayed question must not move that cursor in either
			// direction — the catch-up below is what advances it.
			_ = sse.Send(entity.Event{Ref: ent.Ref, Op: entity.Set, To: ent.Payload})
		}
	}
	sse.Flush()

	for {
		select {
		case <-r.Context().Done():
			return
		case c, ok := <-ch:
			if !ok {
				return
			}
			if c.Rev != 0 && c.Rev <= last {
				continue
			}
			if c.Gap > 0 {
				_ = sse.Send(entity.Event{Rev: c.Rev, Gap: c.Gap})
			}
			for _, ev := range c.Changes {
				if onSpine(ev) {
					_ = sse.Send(ev)
				}
			}
			if c.Rev > last {
				last = c.Rev
			}
		}
	}
}

// spineCarries reports whether a change belongs on one subject's spine: the subject's own
// commits, and the runs about it.
//
// The subject half is BY REF and unfiltered — a predicate reads a payload and a deletion has
// none, so a payload-narrowed spine would hide the end of a life. The run half must read one:
// a task names its subject in `flow`, and there is nothing in its address that says so. A
// run's terminal is a set rather than a delete, so the anchors that matter carry a payload to
// be read.
func spineCarries(ev entity.Event, subject entity.Ref) bool {
	if ev.Ref == subject {
		return true
	}
	// The QUESTIONS about it, which is how an escalation arrives rather than having to be
	// polled for: a transfer writes the question, and the layer above is reading here. The
	// design says the question "appears in the stream of the layer above" and that there is
	// no second path anywhere, `⊤` included.
	if ev.Ref.System == mirror.QuestionSystem && ev.Ref.Kind == mirror.QuestionKind {
		return entity.Str(ev.To, "subject") == subject.String()
	}
	if ev.Ref.System != mirror.System || ev.Ref.Kind != "instance" {
		return false
	}
	return entity.Str(ev.To, "flow") == subject.String()
}

// resolverVerdict applies one answer, through the SAME path a person's `verse gate approve`
// takes. Not a new answering path, and not a write: the record is written from the decision
// after the fact, which is what the steering invariant requires.
func (s *Server) resolverVerdict(w http.ResponseWriter, r *http.Request) {
	binding, origin, ok := s.instanceOf(r)
	if !ok {
		http.NotFound(w, r)
		return
	}
	var v api.Verdict
	if err := codec.Read(r, &v); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if v.ID == "" {
		http.Error(w, "id is required: a verdict names the question it answers", http.StatusBadRequest)
		return
	}
	var proceed bool
	switch v.Verdict {
	case "proceed":
		proceed = true
	case "deny":
	case "escalate":
		s.escalate(w, r, v.ID, binding, origin)
		return
	default:
		http.Error(w, "verdict must be proceed or deny", http.StatusBadRequest)
		return
	}
	// A stream it may READ is not a docket it may ANSWER. The check is structural rather
	// than a policy about who is asking: the instance names its binding and its origin, so
	// this is a comparison of two fields the question already carries.
	q, found, err := s.scopedStore(r).Get(mirror.QuestionRef(v.ID))
	if err != nil || !found {
		http.Error(w, "no such question", http.StatusNotFound)
		return
	}
	if !ownedBy(entity.ToObj(q.Payload), binding, origin) {
		http.Error(w, "that question is not this resolver's to answer", http.StatusForbidden)
		return
	}
	// Attribution is verse's: `by` is the binding, stamped here. A service says what it
	// decided, never who it is.
	//
	// The task id is what routes the answer, and a firing's question has none — it is
	// answered through the engine's own registry, which answerQuestion reaches when told no
	// task holds it.
	var taskID string
	if watchesARun(origin) {
		taskID = origin.ID
	}
	s.answerQuestion(w, r, api.Resolve{
		ID: v.ID, Approve: proceed, By: binding, Say: v.Say,
	}, taskID)
}

// ghostResolver reports why a spawn's `resolver:` cannot be honoured, or "" when it can.
//
// A binding that is not installed is REFUSED rather than ignored, and rather than left standing
// on the question. Left standing it is worse than useless: the question is owned by something
// that cannot answer, and — the case that makes this a refusal rather than a shrug — it SHADOWS
// the standing layer that covers the same subject, which never sees the question and has no
// instance through which to reach it. A misspelled binding therefore turns a supervised subject
// into an unsupervised one, silently, which is the shape of the hazard the first resolver plan
// refused a `condition` to avoid.
//
// Checked at spawn rather than at install, because a charter and a binding are separate
// entities and either may arrive first; what must hold is that a RUN is never bound to a name
// nobody answers to.
func ghostResolver(store entity.Store, ref string) string {
	if ref == "" {
		return "" // named nobody, which is a state and not a mistake
	}
	if _, ok := resolver.Find(store, ref); ok {
		return ""
	}
	return fmt.Sprintf("resolver %q is not installed: a spawn may not name a binding that does "+
		"not exist — the question would be owned by something that cannot answer it, and any "+
		"standing binding covering the same subject would never see it. Install it "+
		"(`verse resolver add`), or leave `resolver` empty to let the standing layer own it", ref)
}

// escalate transfers a question to the layer above. It does NOT ask a favour: the layer above
// owns it from that moment, and its answer resolves the ORIGINAL question, because escalating
// changes who answers and not what is being answered.
//
// Nothing waits. The escalating resolver is not blocked and gets no answer back — it keeps
// reading and may answer other questions in the next frame, and only the WORK is blocked, at
// the gate it always was. The verdict, when it comes, arrives on its stream like any other
// frame, which is what lets it lean on the precedent next time rather than remember it.
func (s *Server) escalate(w http.ResponseWriter, r *http.Request, id, binding string, origin entity.Ref) {
	store := s.scopedStore(r)
	q, ok, err := store.Get(mirror.QuestionRef(id))
	if err != nil || !ok {
		http.Error(w, "no such question", http.StatusNotFound)
		return
	}
	p := entity.ToObj(q.Payload)
	if p["status"] != mirror.QuestionPending {
		http.Error(w, "that question is already settled", http.StatusConflict)
		return
	}
	if !ownedBy(p, binding, origin) {
		http.Error(w, "that question is not this resolver's to escalate", http.StatusForbidden)
		return
	}
	subject, _ := p["subject"].(string)
	if !reown(store, mirror.QuestionRef(id), questionOrigin(p, subject), subject, binding, "escalated") {
		// Answered between the check above and the write. Not this resolver's to escalate any
		// more, and saying 204 would tell it the layer above now holds a question that is shut.
		http.Error(w, "that question was answered while you were escalating it", http.StatusConflict)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// escalateOverdue moves the questions whose deadline has passed, and is called when a READER
// arrives rather than by a clock of its own.
//
// The layer above is the only party an expiry is for, and it learns by reading — its spine, or
// its docket if it is `⊤` and a person. So the reader's arrival is the moment to make the
// transfer appear, not a moment too late: a sweeper would compute the same thing slightly
// earlier, for nobody. It is the shape the claim lease already uses, where expiry happens when
// somebody next asks for the work.
//
// What it costs, said rather than hidden: a question overdue while nothing is reading stays
// where it is, and moves the instant anything looks. For `⊤` that is exactly right — the
// docket a person opens is the docket as of now.
func (s *Server) escalateOverdue(store entity.Store) {
	now := time.Now()
	for _, ent := range pendingQuestions(store) {
		p := entity.ToObj(ent.Payload)
		at, _ := p["escalatesAt"].(string)
		if at == "" {
			continue
		}
		// Nano, not RFC3339: the second-granularity spelling TRUNCATES, so a deadline
		// would fire up to a second early — and one shorter than a second would be past
		// the instant it was stamped.
		due, err := time.Parse(time.RFC3339Nano, at)
		if err != nil || now.Before(due) {
			continue
		}
		owner, _ := p["resolver"].(string)
		subject, _ := p["subject"].(string)
		reown(store, ent.Ref, questionOrigin(p, subject), subject, owner, "overdue")
	}
}

// exclusiveOwner reports the binding that owns this question and whether it owns it
// exclusively. A question nobody owns, or one owned by an advisory binding, is answerable by
// the first party to answer — which is what advisory means, and is the default.
func exclusiveOwner(store entity.Store, id string) (string, bool) {
	e, ok, err := store.Get(mirror.QuestionRef(id))
	if err != nil || !ok {
		return "", false
	}
	binding := entity.Str(e.Payload, "resolver")
	if binding == "" {
		return "", false
	}
	res, ok := resolver.Find(store, binding)
	return binding, ok && res.Exclusive
}

// questionOrigin is what an owner of this question watches: the run that raised it, or the
// subject it is about when a firing did. It is the same distinction an instance's address
// carries, read off the question rather than guessed.
func questionOrigin(q map[string]any, subject string) entity.Ref {
	if p, _ := q["task"].(string); p != "" {
		return mirror.Ref(p)
	}
	ref, _ := entity.ParseRef(subject)
	return ref
}

// ownedBy is the check both answering and escalating make: this binding, and this instance's
// own origin. A run instance acts on the questions of its run and a spine on those of its
// subject, which is how a binding with several instances cannot act through the wrong one.
func ownedBy(q map[string]any, binding string, origin entity.Ref) bool {
	if q["resolver"] != binding {
		return false
	}
	if watchesARun(origin) {
		return q["task"] == origin.ID
	}
	return q["subject"] == origin.String()
}

// resolverPosition records how far a resolver has read, and what it wants its successor to
// know. On the INSTANCE, because who was watching is per run: writing it on the binding would
// be one resolver's second run overwriting what its first one saw.
func (s *Server) resolverPosition(w http.ResponseWriter, r *http.Request) {
	if _, _, ok := s.instanceOf(r); !ok {
		http.NotFound(w, r)
		return
	}
	var p api.Position
	if err := codec.Read(r, &p); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	rec := entity.Obj{"position": p.Position}
	if p.Notes != "" {
		rec["notes"] = p.Notes
	}
	ref := entity.Ref{System: resolver.System, Kind: resolver.InstanceKind, ID: r.PathValue("id")}
	if _, _, err := s.scopedStore(r).Write(entity.Write{At: ref.At(), Value: rec.Payload()}); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// reflect mirrors a task, filling in the provenance every call site shares rather than
// asking each to remember it.
//
// The standing-owner lookup is the reason it exists. It is a callback because mirror must not
// import resolver — resolver reads the charter's match vocabulary, trigger reads mirror, and
// the cycle closes — and an optional field on a struct three call sites build by hand is a
// field one of them will forget. One did, within the commit that introduced it: reconcile
// rebuilt its own Meta and a resumed task's questions lost their layer in silence.
func (s *Server) reflect(store entity.Store, id string, h task.Handle, m mirror.Meta) {
	m.Owner = s.standingOwner
	mirror.Reflect(store, id, h, m)
}

// resolverName is the name half of a binding's ref — the last segment of
// `resolvers:<kind>:<name>`. An instance is addressed by name and origin rather than by the
// whole ref, because the kind is the operator's filing and the name is the identity: two
// resolvers cannot share a name, so carrying the kind into every instance address would be
// spelling out a fact that is already implied.
func resolverName(ref string) string {
	if i := strings.LastIndex(ref, ":"); i >= 0 {
		return ref[i+1:]
	}
	return ref
}

// ── resolvers: who answers ───────────────────────────────────────────────────

// addResolver installs a binding at `resolvers:<kind>:<name>`.
//
// A VERB rather than a raw write, and the reason survives this increment having nothing to
// check across bindings yet: installing one is where the check that two standing conditions
// do not overlap has to live, and a route that exists is a place to put it. A raw write has
// nowhere.
func (s *Server) addResolver(w http.ResponseWriter, r *http.Request) {
	// Read as a DOCUMENT first, so a field the decoder would drop can be named. `metod:`
	// decodes to nothing and installs a resolver that answers over no protocol, reporting
	// success — the same silence trigger.CheckFields exists to break.
	node, err := codec.ReadNode(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if err := resolver.CheckFields(node); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	var res resolver.Resolver
	if err := codec.FromNode(node, &res); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if err := res.Check(); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	ref := resolver.Ref(res.Kind, res.Name)
	body := entity.Obj{
		"name": res.Name, "kind": res.Kind, "byRef": res.ByRef,
		"method": entity.Obj{"driver": res.Method.Driver, "url": res.Method.URL},
	}
	if res.Condition != nil {
		body["condition"] = entity.Obj{"subject": map[string]any(res.Condition.Subject)}
	}
	if res.Deadline != nil {
		// The outcome is stored RESOLVED rather than as written: a binding that omitted it
		// still means escalate, and a record that says so is one a later reader does not
		// have to know the default to understand.
		body["deadline"] = entity.Obj{
			"within": res.Deadline.Within, "then": res.Deadline.Outcome(),
		}
	}
	if res.Exclusive {
		body["exclusive"] = true
	}
	if m := res.Method; m.Vector != "" || len(m.Tools) > 0 || m.Grant != "" {
		meth := body["method"].(entity.Obj)
		if m.Vector != "" {
			meth["vector"] = m.Vector
		}
		if len(m.Tools) > 0 {
			meth["tools"] = m.Tools
		}
		if m.Grant != "" {
			meth["grant"] = m.Grant
		}
	}
	if _, _, err := s.scopedStore(r).Write(entity.Write{At: ref.At(), Value: body.Payload()}); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	// What it may find itself ordered against, reported and not refused. Overlap is decided
	// by name at the moment a question is posed; saying so at install is what keeps a
	// shadowed binding from being a silence rather than a fact.
	out := map[string]any{"ref": ref.String()}
	if over := resolver.Overlaps(s.scopedStore(r), res); len(over) > 0 {
		out["overlaps"] = over
	}
	writeOut(w, r, http.StatusCreated, out)
}

func (s *Server) listResolvers(w http.ResponseWriter, r *http.Request) {
	out := make([]map[string]any, 0)
	for _, ent := range s.scopedStore(r).List(func(ref entity.Ref) bool {
		// Bindings only. An instance shares the system deliberately — one `--system
		// resolvers` shows both — but `verse resolver` is asking what is INSTALLED, and a
		// record of who is watching is a different question.
		return ref.System == resolver.System && ref.Kind != resolver.InstanceKind
	}) {
		rec := entity.ToObj(ent.Payload)
		if rec == nil {
			continue
		}
		rec["ref"] = ent.Ref.String()
		out = append(out, rec)
	}
	writeOut(w, r, http.StatusOK, out)
}

func (s *Server) getResolver(w http.ResponseWriter, r *http.Request) {
	ref := resolver.Ref(r.PathValue("kind"), r.PathValue("name"))
	e, ok, err := s.scopedStore(r).Get(ref)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if !ok {
		http.NotFound(w, r)
		return
	}
	writeOut(w, r, http.StatusOK, entity.ToObj(e.Payload))
}

func (s *Server) removeResolver(w http.ResponseWriter, r *http.Request) {
	ref := resolver.Ref(r.PathValue("kind"), r.PathValue("name"))
	if _, ok, err := s.scopedStore(r).Get(ref); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	} else if !ok {
		http.NotFound(w, r)
		return
	}
	if _, _, err := s.scopedStore(r).Write(entity.Write{At: ref.At(), Value: nil}); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	s.releaseResolver(s.scopedStore(r), ref)
	w.WriteHeader(http.StatusNoContent)
}

// reown hands one question to the next layer, for whatever reason it changed hands.
//
// One function because there is one act. Removal asks "who owns this now that I am gone" and
// escalation asks "who owns this now that I have declined"; the ladder answers both the same
// way, and `why` is the only thing that differs — which is worth recording, since a question
// that changed hands should say what moved it.
// It reports whether the handover LANDED. It can fail to: a question that settled between the
// decision to move it and the write is one that was answered, and an answer beats a transfer —
// merging over it would leave a record saying both "decided by X" and "X let its deadline
// pass", handing the layer above a question somebody had already closed. The check is a
// precondition on the write rather than a read before it, because a read before it is the same
// race with a smaller window.
func reown(store entity.Store, at, origin entity.Ref, subject, from, why string) bool {
	rec := entity.Obj{
		// nil, not absent: a write merges, so saying "this is gone" is the writer's job. A
		// question left naming a binding that will not answer is one nobody can answer,
		// listed on a docket as though somebody were coming.
		"resolver": nil,
		// And the clock goes with the owner it belonged to; the branch below arms the new
		// one's, if it has one.
		"escalatesAt": nil,
		// The handover, readable rather than inferred. A question that changed hands
		// without saying so is one whose audit trail has a hole exactly where somebody
		// would look.
		"reownedFrom": from,
		"reownedWhy":  why,
		"reownedAt":   time.Now().UTC().Format(time.RFC3339),
	}
	binding := ""
	if subject != "" {
		if ref, err := entity.ParseRef(subject); err == nil {
			if next, ok := resolver.Next(store, ref, from); ok {
				binding = resolver.Ref(next.Kind, next.Name).String()
				rec["resolver"] = binding
				// The new owner's clock, not the old one's. Without re-arming, a question
				// would stay overdue against a deadline nobody set and move again on every
				// read; without CLEARING it, one handed to a binding with no deadline would
				// keep the last one's.
				rec["escalatesAt"] = entity.Removed()
				if res, ok := resolver.Find(store, binding); ok && res.Deadline != nil {
					if after, err := res.Deadline.After(); err == nil {
						rec["escalatesAt"] = time.Now().Add(after).UTC().Format(time.RFC3339Nano)
					}
				}
			}
		}
	}
	// Still pending, as of the write itself.
	won, _, err := store.Write(entity.Write{
		At: at.At(), Value: rec.Payload(),
		When: []entity.Require{{Pattern: entity.Obj{"status": mirror.QuestionPending}.Payload()}},
	})
	if err != nil || !won {
		return false
	}
	// The new owner gets an instance for what it now watches, because re-owning IS an
	// ownership decision and that is where instances are minted. Without it a layer could own
	// a question with no record through which to act on it — and no way to read the fine grain
	// of a run it has just inherited. Create-only, so a binding that was already watching keeps
	// the position it had. AFTER the write, so a handover that lost the race mints nothing.
	if binding != "" {
		mirror.Watch(store, binding, resolverName(binding), origin)
	}
	return true
}

// releaseResolver hands on what a removed binding was holding: the questions it owned, and
// the instances that were reading for it.
//
// RE-ROUTE RATHER THAN DRAIN. Draining means the removed binding keeps answering what it
// already owns until those settle, which sounds gentler and is worse in the case that
// matters: an operator removing a resolver wants it to stop, and its service is usually
// being shut down in the same breath — so what it still owns is stuck until something
// re-routes it anyway, and draining is a state machine that postpones the same work.
//
// Called AFTER the binding is gone, which is what makes the recomputation right: the layer
// rule asked now returns the next name in order, or nothing. So this is a change of owner and
// not only a loss of one, which is what allowing overlap bought.
func (s *Server) releaseResolver(store entity.Store, gone entity.Ref) {
	for _, ent := range pendingQuestions(store) {
		p := entity.ToObj(ent.Payload)
		if p["resolver"] != gone.String() {
			continue
		}
		subject, _ := p["subject"].(string)
		reown(store, ent.Ref, questionOrigin(p, subject), subject, gone.String(), "removed")
	}
	// Its instances go with it, and this is the only thing that reaps one. They are
	// machinery for a reader that no longer exists; what they were evidence of is in the
	// store, where a position report and a note were commits when they were made and
	// remain commits afterwards.
	for _, ent := range store.List(func(ref entity.Ref) bool {
		return ref.System == resolver.System && ref.Kind == resolver.InstanceKind
	}) {
		if entity.Str(ent.Payload, "binding") == gone.String() {
			_, _, _ = store.Write(entity.Write{At: ent.Ref.At(), Value: nil})
		}
	}
}

// ── triggers: the charter ────────────────────────────────────────────────────

func (s *Server) listTriggers(w http.ResponseWriter, r *http.Request) {
	eng := s.engFor(r.URL.Query().Get("scope"))
	if eng == nil {
		writeOut(w, r, http.StatusOK, []trigger.Trigger{})
		return
	}
	writeOut(w, r, http.StatusOK, eng.Triggers())
}

// status is a one-shot summary of the running verse: the store's revision and the
// live counts across the planes — the charter size, in-flight flows, pending gates,
// hosted tasks, and total firings.
func (s *Server) status(w http.ResponseWriter, r *http.Request) {
	out := api.Status{
		StoreRev: s.Store.Rev(),
		Triggers: len(s.Eng.Triggers()),
		Flows:    len(s.Eng.Flows()),
		Gates:    len(pendingQuestions(s.scopedStore(r))),
		Tasks:    s.Taskd.Count(),
		Fired:    s.Eng.Fired(),
		Runner:   s.runnerStatus(), // how tasks run: kind + resume + dial legs
		LLM:      s.LLM,            // secrets-safe: are agent creds configured on THIS server
	}
	// When docd-backed, publish its addresses so a controller task (verse connect)
	// can reach the mount face; the client face is where store clients connect.
	if s.DocdMount != "" || s.DocdClient != "" {
		out.Docd = &api.Docd{Client: s.DocdClient, Mount: s.DocdMount}
	}
	// This verse's own listen addresses (operator API, and the broker face when served).
	if s.Addr != "" || s.BrokerAddr != "" {
		out.Listen = &api.Listen{API: s.Addr, Broker: s.BrokerAddr}
	}
	writeOut(w, r, http.StatusOK, out)
}

// runnerStatus describes the runner along its two independent legs, so the two are not
// confused: `resume` is the VERSE-facing control/re-attach address (what a restart needs),
// `dial` is the WORKER-facing address (where a dial-in worker connects). They coincide for
// a remote taskd (one URL, no separate worker leg) and diverge for a broker (verse drives
// it in-process — ephemeral — while a worker dials a separate face). See docs/task-control.md.
func (s *Server) runnerStatus() *api.RunnerStatus {
	pol := &api.RunnerStatus{Resume: "ephemeral"}
	switch {
	case hasFace(s.runner): // a dial-in broker: in-process control, external worker face
		pol.Kind = "broker"
		if face := s.runner.(interface{ Face() string }).Face(); face != "" {
			pol.Dial = face
		}
	case s.runner.Ref() == "local":
		pol.Kind = "local"
	default: // a remote runner base URL — the Ref IS the re-attach address
		pol.Kind = "remote"
		pol.Resume = s.runner.Ref()
	}
	return pol
}

func hasFace(p task.Runner) bool {
	_, ok := p.(interface{ Face() string })
	return ok
}

// taskLister is task.Lister: a runner that keeps its OWN task registry (a broker),
// distinct from the local runtime. localRunner does NOT implement it — it delegates to the
// same taskd the union already lists — so brokered tasks are added without
// double-counting local ones.
//
// The declaration moved to the task package so both implementations assert against it and
// a signature change is a compile error where the implementation is. Asserting it structurally
// here, as this did, meant a widened signature made the assertion report false rather than
// fail to build — which is how the same mistake took out gate resolution once already.
type taskLister = task.Lister

// gateResolver is a Handle that can answer a gate its worker raised, by relaying the
// outcome to that worker. It is optional on Handle because only a runner with a worker leg
// has one to relay to — a local task resolves through its own registry instead.
//
// It is NAMED, and that is the point. It was written inline at both of its call sites, and a
// structural assertion is checked with `, ok`: widening the signature on one side alone does
// not fail to compile, it makes the assertion silently report false and gate resolution stop
// working. Which is exactly what happened — adding `by` updated one site, and the other kept
// asserting the three-argument shape and started answering 501. One declaration means one
// thing to change, and every implementer breaks loudly when it does.
type gateResolver interface {
	Resolve(decision string, proceed bool, say, by string) error
}

// spawnTask is the ad-hoc `task run` path. Unlike a trigger spawn it has no
// subject/flow, but it goes through the SAME seam: host it via the runner (so a run
// honors --runner/--broker, not just trigger spawns) and mirror it into the store (so a run
// is a task:instance entity — observable, and its completion can drive triggers) — the
// consistency the "tasks are entities" model wants.
// spawnID settles what a run is called, and refuses an id that would break the surfaces an id
// crosses.
//
// `~` is the separator between a task and its questions (task.GateSep), and answering one
// recovers the owner by cutting at the FIRST occurrence — so `a~b` raises `a~b~d1` and hands it
// to a task called `a`, which is either nothing or somebody else's run. The rest are the
// characters an id cannot survive in a URL path, since ids are addressed at `/tasks/{id}`.
// The trigger side has stripped exactly these from subject-derived ids all along (safeSeg); the
// ad-hoc face never had to, because until `--name` it never let a caller near one.
//
// A NAME is the safe way to say what a run is: it is sanitised and made unique here, so the
// caller gets a readable id back rather than choosing an address.
func spawnID(body api.Spawn) (string, error) {
	if body.ID != "" {
		if bad := strings.IndexAny(body.ID, badInID); bad >= 0 {
			return "", fmt.Errorf("task id %q may not contain %q: it is the prefix of every "+
				"gate question this run raises (<id>~d1) and it is addressed in a URL path. "+
				"Use `name` instead and the id is derived from it", body.ID, body.ID[bad:bad+1])
		}
		return body.ID, nil
	}
	nonce := mintNonce()[:12]
	if body.Name == "" {
		return "run-" + nonce, nil
	}
	label := strings.Map(func(r rune) rune {
		if strings.ContainsRune(badInID, r) {
			return '_'
		}
		return r
	}, body.Name)
	return label + "-" + nonce, nil
}

// badInID is `~` plus what an id cannot survive in a URL path.
const badInID = "~/\\#?% \t\n"

func (s *Server) spawnTask(w http.ResponseWriter, r *http.Request) {
	var body api.Spawn
	if err := codec.Read(r, &body); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	// One or the other says what the work IS. A call list is the work, so a spawn carrying one
	// needs no prompt — and a spawn with neither has not said anything to do.
	if body.Prompt == "" && len(body.Calls) == 0 {
		http.Error(w, "prompt is required (or calls, for a call list)", http.StatusBadRequest)
		return
	}
	if len(body.Calls) > 0 {
		if err := task.CheckCalls(body.Calls); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if body.Driver != "" && body.Driver != task.DriverCalls {
			http.Error(w, fmt.Sprintf("a spawn carrying calls cannot also be driver %q — "+
				"the calls ARE the driver", body.Driver), http.StatusBadRequest)
			return
		}
		body.Driver = task.DriverCalls
	}
	id, err := spawnID(body)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	// Honor --scope: mirror the task:instance into that scope's copy-on-write view, not
	// baseline — else a scoped run leaks into baseline (a baseline watch would see it). On the
	// memory store Scope is a passthrough, so isolation only applies to the docd/logd store.
	scope := r.URL.Query().Get("scope")
	store := s.Store
	if scope != "" {
		store = s.Store.Scope(scope)
	}
	if why := ghostResolver(store, body.Resolver); why != "" {
		http.Error(w, why, http.StatusBadRequest)
		return
	}
	vec, err := task.NamedVector(body.Vector)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	// The second spawn path, refusing the same way the first does. A misspelled runner that
	// quietly ran the work in-process would be the same class of bug as a misspelled resolver
	// that quietly left it unsupervised: the deployment says one thing and the verse does
	// another. Refused BEFORE the spawn, so nothing is started and nothing is mirrored.
	place, placeName, err := s.runnerFor(store, body.Runner)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	// Parsed here as well as at the runtime, and for the reason launchOf parses a charter's:
	// a contract that cannot be meant should fail before a model call is paid for, not after
	// a run and a repair round.
	if body.Answer != nil {
		if _, err := answer.Parse(body.Answer); err != nil {
			http.Error(w, "answer contract: "+err.Error(), http.StatusBadRequest)
			return
		}
	}
	h := place.Spawn(task.Launch{
		ID: id, Prompt: body.Prompt, Driver: body.Driver, Retain: body.Retain,
		Resolver: body.Resolver, Vector: vec, Environment: body.Environment,
		Answer: body.Answer, Calls: body.Calls,
		// And the same scope the record above goes to, so this run's gates reach the
		// docket of whoever asked for it rather than everybody's.
		Scope: scope,
		// The ad-hoc path is the one place a principal exists today: somebody typed this.
		// A firing has no equivalent until an install can record an author, so it sends
		// Subject and Chain instead — see launchOf.
		Provenance: task.Provenance{Principal: body.Principal, Subject: body.Subject},
	}, nil)
	s.reflect(store, id, h, mirror.Meta{
		Runner: place.Ref(), RunnerName: placeName,
		Subject: body.Subject, Resolver: body.Resolver,
		// No Occurrence: nothing caused this one, so there is no commit to name and the
		// mirror stamps the revision its own first write landed at instead. Reading
		// Store.Rev() here looked obvious and is a store ROUND TRIP — it opens a watch when
		// none is live — which put a network call in front of the record write and let the
		// task's first gate question arrive before the record it is about.
	})
	mirror.BindResolver(store, body.Resolver, resolverName(body.Resolver), id)
	writeOut(w, r, http.StatusOK, api.Spawned{ID: id})
}

// listTasks is the unified task list: the local runtime plus, when the runner is a
// broker (or any runner with its own registry), its tasks — each stamped with its runner
// so a client sees whether a task is local or brokered (and, for a broker, its dialURL).
func (s *Server) listTasks(w http.ResponseWriter, r *http.Request) {
	out, err := s.taskSnapshots(s.scopedStore(r))
	if err != nil {
		codec.HTTPError(w, r, http.StatusBadGateway, err.Error())
		return
	}
	// `verse ps --runner <name>`: the question a drive asks. It selects on the NAME stamped at
	// spawn, not on what is installed now, so it keeps answering for a runner that has since
	// been removed — which is when an operator most wants it, since that is exactly the case
	// `verse runner` can no longer show. `--runner local` selects the ones that named none.
	if want := r.URL.Query().Get("runner"); want != "" {
		kept := make([]task.Instance, 0, len(out))
		for _, inst := range out {
			if inst.RunnerName == want ||
				(inst.RunnerName == "" && want == "local" && inst.Runner == "local") {
				kept = append(kept, inst)
			}
		}
		out = kept
	}
	writeOut(w, r, http.StatusOK, out)
}

// taskSnapshots is the unified task list for one store view (baseline, or a ?scope
// overlay): the local runtime plus the active runner's own tasks (a broker's), each
// stamped with its runner.
//
// The runtime and the broker are scope-blind — they hold every task globally — so a
// bare list would leak a `verse connect` scope's tasks into baseline (and across
// scopes). The one record that carries scope is the task:instance:<id> entity that
// mirror.Reflect writes (synchronously, at spawn) into the spawning scope's copy-on-write
// store. So a task belongs to this view iff its instance entity is visible here: the
// overlay's own writes, plus baseline seen through the overlay. On a store without real
// scopes every instance would be visible in every scope, making this a no-op — the
// isolation boundary is the store's (real on docd/logd).
//
// The ROWS come from the task:instance mirror, and the runtimes are consulted only to
// enrich them. That inversion is what per-task runners require: a task on a named runner
// is in neither the local runtime nor the default runner's registry, so a union over those two
// would simply not list it — and the fix is not to fan out over every installed runner, which
// would let one dead runner take out the whole list in a feature whose whole point is that
// runners come and go. The mirror is written at spawn and is complete without asking anybody.
//
// It also settles what an unreachable runner costs here. It used to cost ROWS, silently
// (git-issue 6mnmxj6mh12krz3ae1n0), and then — once ListTasks could say so — the whole
// list. Now it costs neither: the rows are already here, and a runtime that does not answer
// only means its live detail is missing, so the record's own account stands in.
func (s *Server) taskSnapshots(store entity.Store) ([]task.Instance, error) {
	// The live view, by id, from whatever can be asked cheaply: the in-process runtime, and
	// the default runner when it keeps its own registry (a broker).
	live := map[string]task.Instance{}
	for _, inst := range s.Taskd.Snapshots() {
		inst.Runner = "local"
		live[inst.ID] = inst
	}
	if l, ok := s.runner.(taskLister); ok {
		// Best effort, deliberately: an error here means this runner's DETAIL is missing, and
		// the row survives without it. Failing the list on it would be trading a complete
		// answer for no answer.
		if insts, err := l.ListTasks(); err == nil {
			ref := s.runner.Ref()
			for _, inst := range insts {
				inst.Runner = ref
				live[inst.ID] = inst
			}
		}
	}
	out := make([]task.Instance, 0)
	for _, ent := range store.List(func(r entity.Ref) bool {
		return r.System == mirror.System && r.Kind == mirror.InstanceKind
	}) {
		rec := entity.ToObj(ent.Payload)
		if rec == nil {
			continue
		}
		inst, ok := live[ent.Ref.ID]
		if !ok {
			inst = instanceFromRecord(ent.Ref.ID, rec)
		}
		// Where it ran is the RECORD's to say, not the runtime's: the runtime knows it hosts
		// the task, and the record knows which installed runner that is and what the
		// charter called it.
		if on, _ := rec["runner"].(string); on != "" {
			inst.Runner = on
		}
		if named, _ := rec["runnerName"].(string); named != "" {
			inst.RunnerName = named
		}
		// And WHICH RUN, for the same reason: the runtime may know it — a firing carries the
		// commit that caused it — but only the record always does. An ad-hoc run's occurrence
		// is the revision its own first record write landed at, which is decided after the
		// process is already running, so a live snapshot of one would report nothing and the
		// same task would start naming its run only once the runtime had let it go.
		if occ, _ := rec["occurrence"].(int64); occ != 0 {
			inst.Occurrence = occ
		}
		// WHEN is the record's too, and for the reason the two above are: a live snapshot
		// knows it is hosting the task, and the mirror is what wrote down when that began. A
		// row for a running task would otherwise carry no start at all, which is the one it
		// is most often asked for.
		if started, _ := rec["started"].(string); started != "" && inst.Started == "" {
			inst.Started = started
		}
		if ended, _ := rec["ended"].(string); ended != "" && inst.Ended == "" {
			inst.Ended = ended
		}
		out = append(out, inst)
	}
	return out, nil
}

// instanceFromRecord is what verse can say about a task it is not currently holding a
// handle on — one on a named runner, or one whose runtime has not been asked. It is the
// durable account rather than a live one, which is exactly what the mirror is for.
func instanceFromRecord(id string, rec map[string]any) task.Instance {
	str := func(k string) string { v, _ := rec[k].(string); return v }
	num := func(k string) int64 { v, _ := rec[k].(int64); return v }
	return task.Instance{
		ID: id, Prompt: str("prompt"), Status: task.Status(str("status")),
		Runner: str("runner"), RunnerName: str("runnerName"),
		Result: str("result"), Err: str("err"),
		Started: str("started"), Ended: str("ended"),
		// WHICH RUN this is, read from the record rather than from the runtime, because the
		// record has it either way: a firing's occurrence comes from the commit that caused
		// it and is known before the task starts, while an ad-hoc run's is the revision its
		// own first record write landed at — known only after. Reading it here is what makes
		// `verse ps` able to answer "is this the run that record is about" without a second
		// lookup and a comparison by hand.
		Occurrence: num("occurrence"),
	}
}

// scopeMembers returns the set of task ids whose task:instance entity is visible in
// store — the scope's membership.
func (s *Server) scopeMembers(store entity.Store) map[string]bool {
	ids := map[string]bool{}
	for _, e := range store.List(func(ref entity.Ref) bool {
		return ref.System == mirror.System && ref.Kind == "instance"
	}) {
		ids[e.Ref.ID] = true
	}
	return ids
}

// isTask reports whether id names a task in the runtime or the active runner.
//
// A runner that did not answer reports false here, which is not the same fact and is the best
// this signature can hold. It is bounded: the one caller uses this to decide whether a gate id
// is addressed to a task, having already consulted the mirror's question record, and it
// falls through to the same answer it would have given anyway. Widening it would widen the
// gate-routing chain above for a case that chain cannot act on.
func (s *Server) isTask(id string) bool {
	if _, ok := s.Taskd.SnapshotOf(id); ok {
		return true
	}
	if l, ok := s.runner.(taskLister); ok {
		if _, err := l.TaskSnapshot(id); err == nil {
			return true
		}
	}
	if l, ok := s.placeOf(id).(taskLister); ok {
		_, err := l.TaskSnapshot(id)
		return err == nil
	}
	return false
}

// placeOf is the runner a task actually ran on, which is not necessarily this verse's DEFAULT
// one — `runner:` on a spawn picks per task, and that is the whole feature. nil when the task
// ran locally, ran on the default runner, or is not one this verse knows.
//
// Everything that reached across the seam used to consult `s.runner` alone, so work sent
// somewhere by name was invisible to it: a gate raised on a per-spawn runner could be LISTED
// and not answered, because the owner lookup could not see the task and the answer was offered
// to the local registry, which had never heard of the question. Listing worked because it reads
// the mirror; answering did not because it read a field.
//
// The url is read off task:instance for the same reason a restart reads it there — it is
// stamped beside the NAME precisely so reaching the work does not depend on the runner entity
// still existing, which is what keeps removing a runner from orphaning work that is running
// perfectly well (see reconcileTasks, and docs/runners.md on what a restart does).
func (s *Server) placeOf(id string) task.Runner {
	e, ok, err := s.Store.Get(mirror.Ref(id))
	if err != nil || !ok {
		return nil
	}
	ref := entity.Str(e.Payload, "runner")
	// `local` means in-process, which the taskd branch above already covers; equal to the
	// default runner means the branch above covered it too, and rebuilding a second client
	// for the same place would lose whatever the configured one knows about itself — a
	// broker's handle relays control, and an HTTP client reconstructed from its url does not
	// say so.
	if ref == "" || ref == "local" || ref == s.runner.Ref() {
		return nil
	}
	return runner.HTTP(ref)
}

// taskHoldingGate finds which task has decisionID open, for a gate id that does not
// carry its owner.
//
// A gate the RUNTIME raises is <task-id>~dN, so its owner is readable off the id. One an
// EXECUTOR raises (`task do gate`) is <name>-<hex>, deliberately free of GateSep —
// shortID avoids it so the id survives a shell and a URL intact — with the consequence that
// splitting on GateSep recovers nothing and `verse gate approve` answered 404 on a gate
// `verse gate` was listing one line above, `task:` and all. The routing information was
// never missing; it was in the snapshot rather than in the id. This reads it from there.
//
// Unscoped, like isTask beside it: a resolve is addressed by a unique decision id, not
// discovered by listing, so it does not go through taskSnapshots' scope filter.
func (s *Server) taskHoldingGate(decisionID string) string {
	holds := func(inst task.Instance) bool {
		for _, pg := range inst.Pending {
			if pg.ID == decisionID {
				return true
			}
		}
		return false
	}
	for _, inst := range s.Taskd.Snapshots() {
		if holds(inst) {
			return inst.ID
		}
	}
	if l, ok := s.runner.(taskLister); ok {
		// A runner that did not answer is not a runner holding no gate, and this does not
		// distinguish them — deliberately, because it is already the fallback: the caller
		// consults the mirror's question record first and only lands here when no record
		// names a task. There is nothing better to say from here, and saying it would
		// mean widening every caller between this and the resolve.
		insts, _ := l.ListTasks()
		for _, inst := range insts {
			if holds(inst) {
				return inst.ID
			}
		}
	}
	return ""
}

// resolveTaskDecision answers a task's pending gate (write/shell/delegate) — the
// runtime for a local task, the runner handle for a brokered one.
func (s *Server) resolveTaskDecision(taskID, decisionID string, res task.Resolution) error {
	if _, ok := s.Taskd.SnapshotOf(taskID); ok {
		if s.Taskd.Resolve(taskID, decisionID, res) {
			return nil
		}
		// The local registry answers by id and cannot say which of the two it was. The
		// record can, and the caller consults it; here the honest answer is "not open".
		return task.ErrNoSuchDecision
	}
	if h := s.attach(taskID); h != nil {
		// by is carried across this seam, not dropped: the local leg above takes the whole
		// Resolution, so a brokered gate that lost the answerer would be attributed
		// differently from an identical local one purely by where it happened to run.
		if rh, ok := h.(gateResolver); ok {
			return rh.Resolve(decisionID, res.Proceed, strings.Join(res.Steer, " "), res.By)
		}
		return task.ErrUnsupported
	}
	return task.ErrNoSuchTask
}

// attach reaches a task wherever it actually runs: the runner its own task:instance records
// first, this verse's default runner second.
//
// The order is not a preference. An HTTP runner's Attach hands back a handle for ANY id without
// asking, so consulting the default first would reach whichever place happens to be configured
// rather than the one holding the task — silently, and only for work sent somewhere by name.
//
// One helper rather than the same two lines at each call site, because "reach this task" was
// written three times and every copy assumed the default runner: answering a gate, streaming a
// read plane, and deciding whether an id is a task at all. A per-spawn runner is the whole point
// of `runner:` on a spawn, and each copy was blind to it.
func (s *Server) attach(id string) task.Handle {
	if p := s.placeOf(id); p != nil {
		if h := p.Attach(id); h != nil {
			return h
		}
	}
	return s.runner.Attach(id)
}

// getTask resolves one task across backends: the local runtime first (live detail),
// then the runner's own registry (a brokered task) — so `task get <id>` works whether
// the task runs locally or behind a broker.
func (s *Server) getTask(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if inst, ok := s.Taskd.SnapshotOf(id); ok {
		inst.Runner = "local"
		writeOut(w, r, http.StatusOK, inst)
		return
	}
	if l, ok := s.runner.(taskLister); ok {
		inst, err := l.TaskSnapshot(id)
		switch {
		case err == nil:
			inst.Runner = s.runner.Ref()
			writeOut(w, r, http.StatusOK, inst)
			return
		case errors.Is(err, task.ErrNoSuchTask):
			// The runner answered and has never heard of it. That is a 404 about the id, and
			// it is the only case below that is — but not yet, because a task on a NAMED
			// runner is in nobody's registry here. The record is asked before anyone gives up.
		default:
			// A runner that did not answer is not a runner without this task. Reporting
			// 404 sent an operator to look for a typo in an id that was never wrong, for a
			// task that is running somewhere they cannot currently see.
			codec.HTTPError(w, r, http.StatusBadGateway,
				fmt.Sprintf("runner %s: %s", s.runner.Ref(), err))
			return
		}
	}
	// The mirror, last: a task on a named runner is held by neither the local runtime nor
	// the default runner, so without this `task get` would 404 for work that is running.
	// The account is durable rather than live, which is what the record is for.
	if e, ok, _ := s.scopedStore(r).Get(mirror.Ref(id)); ok {
		if rec := entity.ToObj(e.Payload); rec != nil {
			writeOut(w, r, http.StatusOK, instanceFromRecord(id, rec))
			return
		}
	}
	noSuchTask(w, r, id)
}

// noSuchTask is the 404 every task route ends at, and it says which id space it is about.
//
// It used to be `http.NotFound`, which answers `404 page not found` — Go's default, about a URL,
// in nobody's voice, naming neither the id nor what was looked in. The routes above go to real
// trouble NOT to 404 wrongly (a runner that did not answer is not a runner without the task) and
// then threw that reasoning away at the last line.
//
// The clause about flows is there because the CLI hands you the wrong id and then rejects it.
// `verse flows` lists SUBJECTS — `drift:head:head-moved@git:head:verse` — while a task is named
// `<chain>@<subject>`, so the obvious copy-paste is a 404 that says nothing. Both ids carry `@`
// and `:`, so nothing about their shape tells them apart, which is exactly why the answer has to.
func noSuchTask(w http.ResponseWriter, r *http.Request, id string) {
	codec.HTTPError(w, r, http.StatusNotFound, fmt.Sprintf(
		"no task %q — not in this verse's runtime, not on its runner, and no record of one. "+
			"If that came from `verse flows`, it is a SUBJECT rather than a task id: a trigger "+
			"names its task `<chain>@<subject>`. `verse ps` lists task ids; "+
			"`verse flow show %s` reads the flow.", id, id))
}

// inboxReader is a runner (a broker) that can surface a task's queued worker control for
// operator observability, unfenced. localRunner doesn't implement it (no dial-in worker).
type inboxReader interface {
	TaskInbox(id string) ([]api.InboxItem, bool)
}

// taskInbox is an operator peek at the control queued for a brokered task's worker —
// unfenced, so it doesn't disturb a live worker. A local task has no worker inbox → empty.
func (s *Server) taskInbox(w http.ResponseWriter, r *http.Request) {
	items := []api.InboxItem{}
	if ir, ok := s.runner.(inboxReader); ok {
		if got, ok := ir.TaskInbox(r.PathValue("id")); ok {
			items = got
		}
	}
	// The bare list, not {items: [...]}. Every other collection this server serves is a
	// bare array — triggers, gates, goals, flows, tasks, flow trees — and the wrapper
	// here was a JSON habit rather than a decision. Nothing decoded it structurally:
	// `verse task inbox` re-renders the document.
	writeOut(w, r, http.StatusOK, items)
}

// taskEvents bridges `attach` across backends: a locally-hosted task streams from the
// runtime (its native SSE); a brokered/remote one is bridged here — Subscribe the runner
// handle (which replays the merged log then goes live) and re-emit each event as SSE.
func (s *Server) taskEvents(w http.ResponseWriter, r *http.Request, runtime http.Handler) {
	id := r.PathValue("id")
	if _, ok := s.Taskd.SnapshotOf(id); ok {
		runtime.ServeHTTP(w, r) // local runtime owns it
		return
	}
	s.streamEvents(w, r, id)
}

// streamEvents serves one task's read plane over SSE, from `?since=` if it carries one.
//
// It is factored out because a resolver reads THIS stream, not a projection of it: the design
// requires that a resolver sees what an attached observer sees, and the moment it is a
// different stream there are two orders to reconcile and a supervised run that reads
// differently depending on who is looking.
func (s *Server) streamEvents(w http.ResponseWriter, r *http.Request, id string) {
	// Wherever it runs, not wherever the default runner is: `verse task attach` on work sent
	// to a per-spawn runner answered 404, and so — per the note above, since a resolver reads
	// this same stream — did every attempt to supervise it.
	h := s.attach(id)
	if h == nil {
		noSuchTask(w, r, id)
		return
	}
	sse, err := codec.NewSSE(w, r)
	if err != nil {
		codec.HTTPError(w, r, http.StatusInternalServerError, err.Error())
		return
	}
	ch, unsub := subscribeAt(h, r.URL.Query().Get("since"))
	defer unsub()
	sse.Flush()
	for {
		select {
		case ev, ok := <-ch:
			if !ok {
				return
			}
			_ = sse.Send(ev)
		case <-r.Context().Done():
			return
		}
	}
}

// sinceSubscriber is a handle that can resume from a cursor. It is optional on task.Handle
// rather than part of it, the way gate resolution is: a handle wraps somebody else's runtime,
// and one that cannot replay should say so by not implementing this rather than by pretending.
type sinceSubscriber interface {
	SubscribeSince(since int64) (<-chan task.Frame, func())
}

// subscribeAt opens the read plane at the cursor the client asked for, so `?since=` means the
// same thing on this route as it does on the local runtime's: the frames AFTER that cursor,
// then live, with an EvGap first if the cursor is below what is still retained.
//
// It read nothing before, so a brokered or remote task replayed from the beginning however
// far along the client already was. A resumable client would then see events it had already
// acted on — which is not a cosmetic difference for a reader that acts on what it sees.
//
// An absent `since` is a live attach, and a handle that cannot resume falls back to one. That
// fallback delivers MORE than was asked for rather than less, which a cursor-carrying reader
// can discard; the opposite — silently delivering less — is what EvGap exists to prevent.
func subscribeAt(h task.Handle, since string) (<-chan task.Frame, func()) {
	if since == "" {
		return h.Subscribe()
	}
	sub, ok := h.(sinceSubscriber)
	if !ok {
		return h.Subscribe()
	}
	n, err := strconv.ParseInt(since, 10, 64)
	if err != nil {
		return h.Subscribe()
	}
	return sub.SubscribeSince(n)
}

// taskControl routes cancel/say: a task the local runtime hosts is served by it (live
// detail); otherwise the request goes to the runner's handle (a brokered/remote task),
// so the same command works wherever a task runs.
func (s *Server) taskControl(w http.ResponseWriter, r *http.Request, runtime http.Handler, op string) {
	id := r.PathValue("id")
	if _, ok := s.Taskd.SnapshotOf(id); ok {
		runtime.ServeHTTP(w, r) // local runtime owns it
		return
	}
	// WHEREVER IT RUNS, not wherever the default runner is. s.attach reads the task's own
	// record to find the runner hosting it and falls back to the default; s.runner.Attach
	// asks the default and nothing else. Every task a charter spawns onto a named pool
	// therefore had no reachable control at all: `verse ps cancel <id>` answered 404 while
	// the runner holding it cancelled the same task happily when asked directly.
	//
	// The events route learned this already — "wherever it runs, not wherever the default
	// runner is" is its comment, added when attach 404'd on per-spawn runners — and control
	// was left behind. Reading a stream and stopping the thing that produces it are the same
	// question about where a task lives.
	h := s.attach(id)
	if h == nil {
		noSuchTask(w, r, id)
		return
	}
	switch op {
	case "cancel":
		h.Cancel("")
	case "say":
		var body api.Say
		_ = codec.Read(r, &body)
		h.Steer(body.Instruction, body.By)
	case "vector":
		// slide autonomy on a running (brokered/remote) task: {vector, by} is relayed to
		// the worker via the handle, `by` attributing the operator who slid it.
		var body api.SetVector
		if err := codec.Read(r, &body); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if body.Vector == nil {
			http.Error(w, "vector required", http.StatusBadRequest)
			return
		}
		h.SetVector(body.Vector, body.By)
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) addTrigger(w http.ResponseWriter, r *http.Request) {
	// Read as a DOCUMENT first, so an unknown field can be named. Decoding drops one silently
	// — gomap ignores what it does not recognise — and the fields it would drop are the ones
	// that say what the rule is allowed to do: `startum:` installs unstratified, `derve:` is a
	// derivation that never happens. Both report success. See trigger.CheckFields.
	node, err := codec.ReadNode(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if err := trigger.CheckFields(node); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	var d trigger.Trigger
	if err := codec.FromNode(node, &d); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if d.Name == "" {
		http.Error(w, "trigger name required", http.StatusBadRequest)
		return
	}
	// ?scope=<s> installs the trigger on that scope's engine (created on first use),
	// observing and acting within the scope — the reactivity for a `verse connect`
	// federation. No scope → the baseline charter.
	eng := s.Eng
	if scope := r.URL.Query().Get("scope"); scope != "" {
		if s.ctx == nil {
			http.Error(w, "engine not started", http.StatusServiceUnavailable)
			return
		}
		eng = s.scopeEngine(scope)
	}
	if err := eng.AddTrigger(d); err != nil {
		// A rule the store would not record is not a bad request: the trigger is fine and
		// the client cannot fix it by sending a better one. Saying 400 there sends someone
		// to debug a correct charter while the verse is the thing that is unwell.
		code := http.StatusBadRequest
		if errors.Is(err, trigger.ErrCharter) {
			code = http.StatusServiceUnavailable
		}
		http.Error(w, err.Error(), code)
		return
	}
	w.WriteHeader(http.StatusCreated)
}

func (s *Server) getTrigger(w http.ResponseWriter, r *http.Request) {
	eng := s.engFor(r.URL.Query().Get("scope"))
	if eng == nil {
		http.NotFound(w, r)
		return
	}
	d, ok := eng.Trigger(r.PathValue("name"))
	if !ok {
		http.NotFound(w, r)
		return
	}
	writeOut(w, r, http.StatusOK, d)
}

func (s *Server) removeTrigger(w http.ResponseWriter, r *http.Request) {
	eng := s.engFor(r.URL.Query().Get("scope"))
	if eng == nil || !eng.RemoveTrigger(r.PathValue("name")) {
		http.NotFound(w, r)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ── strata: the declared level ordering the charter is checked against ────────────

// listStrata returns the installed strata, ordered by level.
func (s *Server) listStrata(w http.ResponseWriter, r *http.Request) {
	eng := s.engFor(r.URL.Query().Get("scope"))
	if eng == nil {
		writeOut(w, r, http.StatusOK, []trigger.Stratum{})
		return
	}
	writeOut(w, r, http.StatusOK, eng.Strata())
}

func (s *Server) getStratum(w http.ResponseWriter, r *http.Request) {
	eng := s.engFor(r.URL.Query().Get("scope"))
	if eng == nil {
		http.NotFound(w, r)
		return
	}
	st, ok := eng.Stratum(r.PathValue("name"))
	if !ok {
		http.NotFound(w, r)
		return
	}
	writeOut(w, r, http.StatusOK, st)
}

// addStratum installs one stratum. Its refusals — a name already taken, a LEVEL already taken,
// a pattern overlapping another stratum's — are all 400s: each is a property of the charter the
// client sent, and each is fixable by sending a different one.
func (s *Server) addStratum(w http.ResponseWriter, r *http.Request) {
	var st trigger.Stratum
	if err := codec.Read(r, &st); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	eng := s.Eng
	if scope := r.URL.Query().Get("scope"); scope != "" {
		if s.ctx == nil {
			http.Error(w, "engine not started", http.StatusServiceUnavailable)
			return
		}
		eng = s.scopeEngine(scope)
	}
	if err := eng.AddStratum(st); err != nil {
		code := http.StatusBadRequest
		if errors.Is(err, trigger.ErrCharter) {
			code = http.StatusServiceUnavailable
		}
		http.Error(w, err.Error(), code)
		return
	}
	w.WriteHeader(http.StatusCreated)
}

// removeStratum removes one. A stratum still holding rules is a CONFLICT rather than a 404 or a
// bad request: the request names something real and is well formed, and what refuses it is the
// state of the charter — which the client can fix by removing those rules.
func (s *Server) removeStratum(w http.ResponseWriter, r *http.Request) {
	eng := s.engFor(r.URL.Query().Get("scope"))
	if eng == nil {
		http.NotFound(w, r)
		return
	}
	err := eng.RemoveStratum(r.PathValue("name"))
	switch {
	case err == nil:
		w.WriteHeader(http.StatusNoContent)
	case errors.Is(err, trigger.ErrNoStratum):
		http.NotFound(w, r)
	default:
		http.Error(w, err.Error(), http.StatusConflict)
	}
}

// ── goals: an owning aggregate (owned triggers + done/fail latches + status) ──────

// listGoals returns the live goal roster read from the goal:instance entities — name,
// subject, status, waitingOn, deadline, and (once terminal) outcome — the `verse goal` view.
func (s *Server) listGoals(w http.ResponseWriter, r *http.Request) {
	ents := s.Store.List(func(ref entity.Ref) bool { return ref.System == "goal" && ref.Kind == "instance" })
	out := make([]map[string]any, 0, len(ents))
	for _, e := range ents {
		row := map[string]any{"name": e.Ref.ID}
		fields := entity.ToObj(e.Payload)
		for _, k := range []string{"subject", "status", "waitingOn", "deadline", "outcome", "terminal"} {
			if v, ok := fields[k]; ok {
				row[k] = v
			}
		}
		out = append(out, row)
	}
	writeOut(w, r, http.StatusOK, out)
}

func (s *Server) addGoal(w http.ResponseWriter, r *http.Request) {
	var d trigger.GoalDef
	if err := codec.Read(r, &d); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if d.Name == "" {
		http.Error(w, "goal name required", http.StatusBadRequest)
		return
	}
	if err := s.Eng.AddGoal(d); err != nil {
		code := http.StatusBadRequest // a goal the store would not record is not a bad request
		if errors.Is(err, trigger.ErrCharter) {
			code = http.StatusServiceUnavailable
		}
		http.Error(w, err.Error(), code)
		return
	}
	w.WriteHeader(http.StatusCreated)
}

func (s *Server) removeGoal(w http.ResponseWriter, r *http.Request) {
	if !s.Eng.RemoveGoal(r.PathValue("name")) {
		http.NotFound(w, r)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// goalPause/Resume/Cancel/Say steer a goal by delegating to its flow (a goal is a flow with a
// destination) — the same control surface, keyed by the goal name.
func (s *Server) goalPause(w http.ResponseWriter, r *http.Request) {
	s.goalSteer(w, r, func(name string) api.Steered {
		return api.Steered{Goal: name, Verb: "pause", Affected: s.Eng.PauseGoal(name)}
	})
}

func (s *Server) goalResume(w http.ResponseWriter, r *http.Request) {
	s.goalSteer(w, r, func(name string) api.Steered {
		return api.Steered{Goal: name, Verb: "resume", Affected: s.Eng.ResumeGoal(name)}
	})
}

func (s *Server) goalCancel(w http.ResponseWriter, r *http.Request) {
	s.goalSteer(w, r, func(name string) api.Steered {
		return api.Steered{Goal: name, Verb: "cancel", Affected: s.Eng.CancelGoal(name)}
	})
}

func (s *Server) goalSay(w http.ResponseWriter, r *http.Request) {
	var body api.GoalSteer
	if err := codec.Read(r, &body); err != nil || body.Name == "" || body.Text == "" {
		http.Error(w, "name and text required", http.StatusBadRequest)
		return
	}
	by := body.By
	if by == "" {
		by = "human:cli"
	}
	writeOut(w, r, http.StatusOK, api.Steered{
		Goal: body.Name, Verb: "say",
		Affected: s.Eng.SteerGoal(body.Name, body.Text, by),
	})
}

func (s *Server) goalSteer(w http.ResponseWriter, r *http.Request, act func(name string) api.Steered) {
	var body api.GoalSteer
	if err := codec.Read(r, &body); err != nil || body.Name == "" {
		http.Error(w, "name required", http.StatusBadRequest)
		return
	}
	writeOut(w, r, http.StatusOK, act(body.Name))
}

// ── flow: engine steering (control plane) ────────────────────────────────────

func (s *Server) listFlows(w http.ResponseWriter, r *http.Request) {
	eng := s.engFor(r.URL.Query().Get("scope"))
	if eng == nil {
		writeOut(w, r, http.StatusOK, []string{})
		return
	}
	writeOut(w, r, http.StatusOK, eng.Flows())
}

// flowTree renders a subject's work as a SHAPE — the trigger that acted on it, the task
// that ran, and the completion hooks branching off it, including the ones that have not run
// yet. A read, so the address rides the query string rather than a body (steering is POST).
//
// It is derived per request from the charter, the mirror and the live registry (see
// trigger.FlowTrees). Nothing about the tree is stored: a materialized one would be the
// flows[subject] index that a flow-as-a-query deliberately replaced.
func (s *Server) flowTree(w http.ResponseWriter, r *http.Request) {
	eng := s.engFor(r.URL.Query().Get("scope"))
	if eng == nil {
		writeOut(w, r, http.StatusOK, []trigger.FlowTree{})
		return
	}
	addr := r.URL.Query().Get("subject")
	if addr == "" {
		http.Error(w, "subject required (an entity address, e.g. demo:pr:1 or demo:pr:*)", http.StatusBadRequest)
		return
	}
	m, err := trigger.ParseExpr(addr)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	writeOut(w, r, http.StatusOK, eng.FlowTrees(m))
}

// flowAddress decodes the steering body's `subject` — an entity ADDRESS, not just a ref:
// "demo:pr:1" names one subject, "demo:pr:*" the whole kind. A flow is a query over live
// tasks' subjects, so one field serves both, and a plain ref stays valid (no client
// breaks). Returns false having already written the error response.
func flowAddress(w http.ResponseWriter, r *http.Request, body *api.FlowSteer) (trigger.Match, bool) {
	if err := codec.Read(r, body); err != nil || body.Subject == "" {
		http.Error(w, "subject required (an entity address, e.g. demo:pr:1 or demo:pr:*)", http.StatusBadRequest)
		return nil, false
	}
	m, err := trigger.ParseExpr(body.Subject)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return nil, false
	}
	return m, true
}

// flowPause/flowResume hold the addressed tasks at their next boundary — serviced with
// priority over observation by the engine's control plane (see
// docs/deployment-and-steering.md).
func (s *Server) flowPause(w http.ResponseWriter, r *http.Request) {
	var b api.FlowSteer
	m, ok := flowAddress(w, r, &b)
	if !ok {
		return
	}
	writeOut(w, r, http.StatusOK, api.Steered{Subject: b.Subject, Verb: "pause", Affected: s.Eng.PauseFlow(m)})
}

func (s *Server) flowResume(w http.ResponseWriter, r *http.Request) {
	var b api.FlowSteer
	m, ok := flowAddress(w, r, &b)
	if !ok {
		return
	}
	writeOut(w, r, http.StatusOK, api.Steered{Subject: b.Subject, Verb: "resume", Affected: s.Eng.ResumeFlow(m)})
}

func (s *Server) flowCancel(w http.ResponseWriter, r *http.Request) {
	var b api.FlowSteer
	m, ok := flowAddress(w, r, &b)
	if !ok {
		return
	}
	writeOut(w, r, http.StatusOK, api.Steered{Subject: b.Subject, Verb: "cancel", Affected: s.Eng.CancelFlow(m)})
}

func (s *Server) flowSay(w http.ResponseWriter, r *http.Request) {
	var b api.FlowSteer
	m, ok := flowAddress(w, r, &b)
	if !ok {
		return
	}
	if b.Text == "" {
		http.Error(w, "text required", http.StatusBadRequest)
		return
	}
	by := b.By
	if by == "" {
		by = "human:cli"
	}
	writeOut(w, r, http.StatusOK, api.Steered{Subject: b.Subject, Verb: "say", Affected: s.Eng.SteerFlow(m, b.Text, by)})
}

// eventWire is gone: an Event is written as itself. It used to be rebuilt field by field
// into a map — ref stringified, to/from/gap copied conditionally — which is exactly what the
// type's own tony tags already say (omitzero on to, from and gap), and entity.Ref renders as
// its address through MarshalText. The rebuild could only ever agree with the type by
// coincidence.

// writeOut renders a response in the format the request asked for. It takes the request
// because that is where the answer lives — ?format=, then Accept, then the task default.
func writeOut(w http.ResponseWriter, r *http.Request, code int, v any) {
	codec.Write(w, r, code, v)
}
