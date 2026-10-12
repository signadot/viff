// Package versed is the verse as one process: the entity store, the trigger engine, the task
// runtime and the door that says who a request is from, assembled behind one HTTP surface that
// the `verse` CLI drives (Server).
//
// The model lives elsewhere — entity, trigger, task, io, mirror — and this package is where it
// is wired together, made durable, and served. Start restores the live half of every durable
// record in named phases (startup.go): the perms mirror, suspensions, tasks, the charter and its
// packages, selections, clocks, asks and sources; a phase whose read did not happen is asked
// again rather than taken as empty. Handler registers the routes and publishes the table it
// built (routes.go); every request passes the principals middleware, which verifies a bearer,
// admits what a header may assert, and refuses a write naming nobody. The surface keeps
// observation (watch, reads) distinct from control (answering, steering, cancelling), per
// docs/deployment-and-steering.md.
package versed

import (
	"context"
	"crypto/rand"
	_ "embed"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/signadot/tony-format/go-tony/ir"
	"github.com/signadot/tony-format/go-tony/ir/kpath"

	"github.com/signadot/verse/answer"
	"github.com/signadot/verse/api"
	"github.com/signadot/verse/broker"
	"github.com/signadot/verse/codec"
	"github.com/signadot/verse/entity"
	verseio "github.com/signadot/verse/io"
	"github.com/signadot/verse/launch"
	"github.com/signadot/verse/mirror"
	"github.com/signadot/verse/resolver"
	"github.com/signadot/verse/runner"
	"github.com/signadot/verse/runner/runnerd"
	"github.com/signadot/verse/runner/runnerd/verserunner"
	"github.com/signadot/verse/task"
	"github.com/signadot/verse/trigger"
)

// Server is the monolith's in-process assembly: one store, one engine, one gate
// registry, and the task runtime, behind one HTTP surface.
type Server struct {
	// seedErr is what the permission seed refused with: a server holding one has no policy and
	// must not serve (SeedError).
	seedErr error

	// SystemCharter is the path of the charter file installed at start as the system, when the
	// deployment names one (config.Config.SystemCharter).
	SystemCharter string
	// SystemCharters are charters installed at start as the system beside that file, each a
	// text with a name for the log: what `verse up` renders from the config for charters verse
	// ships at the system level (system/repocharters), which the engine knows nothing of.
	SystemCharters []SystemCharter
	// systemWaiting are those of SystemCharters refused at start, tried again as ios are
	// registered (installWaitingSystemCharters); systemMu orders the two.
	systemMu      sync.Mutex
	systemWaiting []SystemCharter
	Store         entity.Store
	// Zone is the zone a schedule's ticks are spelled in, and the one a period of whole days
	// steps in; nil is time.Local, which a deployment sets with TZ (schedule.go).
	Zone *time.Location
	// clock is where the server reads the time and sets its timers: the wall clock, or the
	// one a test moves (clock.go).
	clock Clock
	// clocks is the schedule timers this process holds (schedule.go).
	clocks clocks
	// AskKeep is how long a settled ask stands when it says nothing itself; zero is a week
	// (askttl.go).
	AskKeep time.Duration
	// expiries is the settled asks' timers this process holds (askttl.go).
	expiries askExpiries
	// asks is the person's asks this process waits on (asks.go).
	asks asksWaiting
	// users is every principal this process has recorded as `verse.principal.user.<principal>`; see noteUser.
	users sync.Map
	// slides is, by task id, the last slide the control route relayed — who for, and to which
	// mode (mirror.Meta.SlidBy).
	slides sync.Map
	Reg    *task.Registry
	Eng    *trigger.Engine
	// Local is the runner this verse embeds, for `verse up`: its tasks run in this process.
	Local  *runnerd.Daemon
	runner task.Runner // how tasks are run/governed; the embedded runner unless SetRunner

	// broker is the dial-in broker, when one is served: a place a spawn may NAME, beside the
	// default rather than instead of it. It is addressed as `broker` the way the in-process
	// supervisor is addressed as `local` — a reserved name rather than an installed entity,
	// because a runner entity carries a url for runner.HTTP to drive and this one is a field
	// on this struct. Making verse dial itself to reach it would buy a loopback hop and a
	// second copy of the contract.
	//
	// Nil when `--broker` was not asked for, and `--runner broker` is then refused by name
	// like any other runner that is not there.
	broker task.Runner

	// Addr/BrokerAddr are this verse's OWN listen addresses — the operator API and (if
	// served) the broker face — published on /status so a client or worker can discover
	// where verse listens, as the docd addresses are published for a controller. Set by the
	// task that binds them (cmd/verse up); empty until then.
	Addr       string
	BrokerAddr string
	// AdminAddr is the out-of-band pprof/stacks face (adminface), published on /status
	// because its default is DERIVED from the API address rather than fixed — knowable,
	// but not something an operator should have to re-derive during an incident.
	AdminAddr string

	// LLM is a secrets-safe summary of the task runtime's LLM config, published on
	// /status so you can see whether agent creds are configured on THIS server (the one
	// that hosts the runtime) — the common gotcha. Set by `up`; nil when unconfigured.
	LLM *api.LLM

	// DocdClient is the client-face address of the docd this verse is backed by, when
	// store-kind is docd. It is published on GET /status so a store client run beside this
	// verse can find docd without guessing the ephemeral port `up` chose.
	//
	// It is what a CLIENT should dial, which is not necessarily what versed dialled:
	// `up --docd-advertise-addr` sets it when the two differ, as they do in a cluster,
	// where versed reaches docd by a Service name that resolves nowhere else. The server
	// never connects to it — it is handed an open store — so there is one reader, and
	// it is somewhere else.
	DocdClient string

	// ctx is the lifetime captured at Start; scoped engines start under a child of it.
	ctx context.Context
	// Per-scope engines. A trigger installed under ?scope=<s> runs on a dedicated
	// engine observing store.Scope(s) — the reactivity for one scope
	// lives in its own copy-on-write overlay, isolated from baseline and
	// from other scopes. Created lazily on the first scoped trigger, stopped when the
	// scope is dropped (POST /scope/delete).
	// restored reports that Start's phases have finished. /healthz answers with it, so a
	// probe can tell STARTING from wedged — the two looked identical while nothing listened
	// until Start returned.
	restored  atomic.Bool
	scopeMu   sync.Mutex
	scopeEngs map[string]*scopeEngine
	// charterMu serializes charter MUTATION — one operation at a time, across every address
	// and scope. A capacity-one channel rather than a mutex, so it can be waited for with a
	// deadline; see charterlock.go for what it does and does not promise.
	charterMu chan struct{}
	// charterWait overrides charterLockWait, for a test that has to observe the refusal
	// rather than sit out the real one. Zero is the constant, which is every other caller.
	charterWait time.Duration
	// waiting is the phases of Start whose store read did not happen and that are being asked
	// again (startup.go); status shows them as `restoring`. Guarded by waitMu.
	waitMu  sync.Mutex
	waiting map[string]bool
	// retryWait is the first pause before a phase is asked again; it doubles per attempt up to
	// retryWaitMax. Zero is the constant, which is every caller but a test.
	retryWait time.Duration
	// resumed marks the engines whose open selections have been given their awaiters back, so
	// a resume asked again after the docket did not answer resumes each engine once — an
	// awaiter given twice would answer twice.
	resumed map[*trigger.Engine]bool
	// Per-registration reflectors. An installed source runs one, under a child of ctx, and it
	// is the only handle on a goroutine that WRITES a slice — so a remove that forgets to stop
	// one leaks a second writer past the disjointness check the install just made.
	srcMu  sync.Mutex
	ioRuns map[string]*sourceRun

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
	// unattributed is the patterns a request may write through naming nobody (router.handleUnattributed).
	unattributed map[string]bool
	// control is the patterns a trusted io may act through for a person it relays (router.handleControl).
	control map[string]bool
	// auth is how people are authenticated at the door (SetAuth); nil verifies nobody.
	auth *authn
	// issuer signs tokens as this verse and publishes the keys that verify them (SetIssuer); nil
	// issues none.
	issuer *issuer

	// susp is baseline's suspension table, which baseline's engine and every package's ask; a
	// scope's is beside its engine. suspMu is the one writer's lock: a suspension's record and
	// its table change under it, in that order (suspend.go).
	susp   *trigger.Suspensions
	suspMu sync.Mutex
}

// SetTripPace sets the cycle throttle's pace D on this server's engines — the baseline one now,
// and every scope engine as it is created. Zero refuses a tripped firing outright instead of
// pacing it. Unset leaves each engine on trigger.DefaultTripPace.
//
// It is here rather than a NewWithStore argument because a scope engine is built lazily, on first
// use, and a pace applied only to the baseline would leave a scope running
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
	ctx    context.Context // the scope's lifetime: what its watches, and its perms mirror, run under
	cancel context.CancelFunc
	// restore runs the scope's durable charter back into eng, exactly once and off the
	// scope lock — see scopeEngine (the method).
	restore sync.Once
	// susp is the scope's suspension table; nil for a package's engine, which asks baseline's.
	susp *trigger.Suspensions
}

// Option is a deployment's say in how the server is assembled. Variadic because what a
// deployment sets is what its config named and nothing else: a test that says none gets the
// verse the check describes on its own.
type Option func(*options)

type options struct{ perms Perms }

// WithPerms carries the permission policy a deployment set — the root's default and its admins.
// It is read BEFORE the seed runs, since the seed writes the records it describes; a verse
// assembled without it is the open one.
func WithPerms(p Perms) Option { return func(o *options) { o.perms = p } }

// NewWithStore assembles the server with the given task driver builder (scripted,
// LLM, …) over a store. The engine resolves trigger gates through the shared registry,
// so a human answers them out-of-band via POST /asks/{id}/answer; a trigger's spawn action
// goes through the task runtime (hosted, mirrored into the verse).
func NewWithStore(build runnerd.Builder, store entity.Store, opts ...Option) *Server {
	var o options
	for _, opt := range opts {
		opt(&o)
	}
	// THE STORE ACTS AS THE SYSTEM unless a request or a firing says otherwise. A mirror's
	// projection, a question posed, the charter restored at start: none of these was asked for
	// by anybody outside verse, and a write that names nobody would leave what it creates
	// ownerless. scopedStore re-derives a view per request, and a firing's causal store stamps
	// its rule, so this default is what remains when neither applies.
	store = entity.As(store, entity.SystemDefault)
	// The root's default, BEFORE the seed: the records the seed writes hold out what it says,
	// and setting it afterwards left a deployment's own default reaching nothing the seed
	// wrote — which is the order this fixes rather than documents.
	// NIL IS UNSET AND EMPTY IS A SETTING: `perms: {child-grants: {}}` is a verse where what anybody
	// creates is theirs alone, and reading it as "not set" made a verse configured private
	// create everything readable by everyone, with nothing said.
	// A root default that cannot be one is refused as a seed is: fatal, saying why.
	rootDefault := entity.CheckChildGrants(o.perms.ChildGrants)
	if rootDefault != nil {
		rootDefault = fmt.Errorf("the root's default (perms child-grants): %w", rootDefault)
	} else if o.perms.ChildGrants != nil {
		entity.PolicyOf(store).SetRootChildGrants(o.perms.ChildGrants)
	}
	// And what the system holds out, written before anything is asked of the store on
	// anybody's behalf: a request that arrived before the seed would be checked against a
	// policy admitting nobody.
	// A FAILED SEED IS FATAL, and it is kept here for StartServing to refuse on. The seed IS the
	// policy — the root's record is what admits anybody at all — so a verse that serves without
	// it refuses every request from every person, which looks like a verse that has lost its
	// data.
	// THE MIGRATION FIRST, since the seed and every check after it read the mirror in the new
	// spelling alone: a record still in the old one holds out nothing, so a store left half way
	// is a verse where nobody can spawn a task. It is fatal for the reason the seed is.
	seedErr := rootDefault
	if seedErr == nil {
		seedErr = migratePerms(store)
	}
	if seedErr == nil {
		seedErr = seedPerms(store, o.perms)
	}
	reg := task.NewRegistry()
	eng := trigger.New(store, reg)
	// Baseline's suspensions. A server that has not begun its restore has none to restore, so
	// the table starts empty and loaded; Start unloads it before anything else and its first
	// phase reads it back, and until then it holds everything this engine would start.
	susp := trigger.NewSuspensions()
	susp.Load(nil)
	eng.SetSuspensions(susp)
	host := runnerd.New(build)
	s := &Server{Store: store, Reg: reg, Eng: eng, Local: host, susp: susp, clock: wallClock{},
		seedErr:   seedErr,
		scopeEngs: map[string]*scopeEngine{}, ioRuns: map[string]*sourceRun{},
		charterMu: make(chan struct{}, 1),
		// Replaced by Start's, which is the real lifetime. A default here rather than a nil
		// check at every use: a source installed on a server nobody started still needs a
		// context to run under, and one that is never cancelled would be worse than one
		// cancelled at task exit.
		ctx: context.Background()}
	s.runner = localRunner{host} // the in-process runner, reached in process and nowhere else

	// Publish every posed question as `gate:question:<id>`, from both places questions are
	// raised: the engine's own registry for a firing's gates, and each hosted task for a
	// run's. One record shape either way — the docket, a charter predicate and an auditor all
	// read the same fact, and neither source is privileged.
	// A question's clock, where it has one: stamped when it is posed, and armed.
	arm := func(st entity.Store, q task.GateQuestion) {
		if q.Deadline != nil {
			s.armQuestion(st, q)
		}
	}
	reg.OnPost(func(p task.Pending) {
		// WHERE this one goes is the ASKER's to say, exactly as it is on the task seam
		// below — one registry serves baseline and every scoped engine, so a publisher
		// closed over the baseline store put every scope's firing gates on everybody's
		// docket, each naming a subject only the scope that raised it can read. That was
		// git-issue kwxxm8cth12ksn99fhn0 on the task side; a firing's gates are the same
		// question asked one plane over, and "no task, therefore no scope" was never true
		// of them: the TRIGGER has a scope whether or not a task exists yet.
		store := s.storeForScope(p.Question.Scope)
		if mirror.PostQuestion(store, p, "") {
			arm(store, p.Question)
		}
	})
	// And the counterpart. A question whose last waiter stopped waiting leaves the registry
	// silently; without this the record it published stays `pending` on the docket, offering
	// a decision nothing is left to receive. The reason comes with it, because "the task died"
	// and "an operator redirected the run" retire a question the same way and read to an
	// answerer completely differently.
	// …and the retraction goes to the store the post went to, or a scope's question is
	// published in one place and retired in another, leaving it `pending` forever.
	reg.OnDropCause(func(q task.GateQuestion, why string, cause error) {
		// A suspension's withdrawal is kept to be asked again when it lifts; anything else
		// retires the question.
		if task.Suspends(cause) {
			mirror.HoldQuestion(s.storeForScope(q.Scope), q.ID, why)
			return
		}
		mirror.DropQuestion(s.storeForScope(q.Scope), q.ID, why)
	})
	// A HOSTED task's questions are retired here. The registry above is the ENGINE's — a
	// firing's own gates — and a task spawned into runnerd gets a registry of its own, which is
	// why this is a second registration rather than the same one reaching both.
	host.OnDrop(func(_, scope, questionID, why string) {
		mirror.DropQuestion(s.storeForScope(scope), questionID, why)
	})
	// NOBODY ANSWERS AN ASK ABOUT SOMETHING THEY CANNOT READ (9ce3bva0, D3): an ask naming
	// answerers who may not read its subject is refused before it is posed, to the firing or the
	// action that asked it.
	reg.OnCheck(func(q task.GateQuestion) error {
		return checkAudience(s.storeForScope(q.Scope), q)
	})
	host.OnCheck(func(l task.Launch, q task.GateQuestion) error {
		return checkAudience(s.storeForScope(l.Scope), hostedQuestion(l, q))
	})
	host.OnQuestion(func(l task.Launch, p task.Pending) {
		// WHERE this question goes is the task's to say, not this closure's. runnerd is one
		// runtime under baseline and every scoped engine, so a publisher registered once
		// closed over the baseline store and posted every scope's questions there — while
		// the task's own record went to the scope that spawned it. `verse gates` then
		// showed a pending gate `verse ps` would not list, on every dev's docket at once,
		// naming content only one of them could read (git-issue kwxxm8cth12ksn99fhn0).
		//
		// The scope rides on the launch, so it is known before the task is, and every
		// record this callback writes goes to the same store as the instance record: the
		// docket a reader sees is the tasks a reader sees.
		store := s.storeForScope(l.Scope)
		// A task gate's question knows what it would DO but not what it is about: the
		// flow belongs to the task, not to the action. Fill it in from the launch, so a
		// rule can select "the questions about demo:pr:1" by matching one record rather
		// than joining two (hostedQuestion).
		p.Question = hostedQuestion(l, p.Question)
		if mirror.PostQuestion(store, p, l.ID) {
			arm(store, p.Question)
		}
	})

	// The Spawner owns the full lifecycle: host it via the runner, then mirror
	// its state into the verse so triggers can react to its completion.
	eng.SetSpawner(s.spawnerFor(store, "", eng))
	eng.SetDeadlineMover(s.moveDeadline)
	return s
}

// hostedQuestion is a hosted task's question as the docket poses it: about the task's subject where
// it names none; held out, where it names nobody, to whom the launch says such questions go
// (task.Launch.Answerers), and otherwise to the docket; and with task.PrincipalAnswerer put as
// whom the task acts for (mirror.WithPrincipal).
//
// A task gate's question knows what it would DO but not what it is about: the flow belongs to the
// task, not to the action. FROM THE LAUNCH, not from the task's record: both spawn paths start the
// task and then write its record, and a task that asks straight away asks before the record lands
// (git-issue nkptp9b3h12ksx3kg9n0).
func hostedQuestion(l task.Launch, q task.GateQuestion) task.GateQuestion {
	if q.Subject == "" {
		q.Subject = l.Provenance.Subject
	}
	if len(q.Answerers) == 0 {
		q.Answerers = l.Answerers
	}
	q.Answerers = mirror.WithPrincipal(q.Answerers, mirror.ActsFor(l.Provenance.Principal))
	return q
}

// checkAudience is mirror.CheckAudience for a question as it will be posed.
func checkAudience(store entity.Store, q task.GateQuestion) error {
	return mirror.CheckAudience(store, q.Subject, entity.Canonical(q.Answerers))
}

// localRunner is the embedded runner (Server.Local) as a task.Runner: verse reaches it by Go
// calls rather than over HTTP. Remote runners (a base URL: k8s, lambda, a human broker)
// implement the same task.Runner behind the wire contract; the engine drives them through the
// identical seam. See docs/task-control.md.
//
// The runtime is served to nobody raw. The operator API serves it behind the door (a
// principal, the perms check, the mirror), and the broker face serves only what HostBroker
// registered under a minted nonce. A fixed name on the broker face used to map to this
// runtime whole — POST /tasks, resolve, cancel, the vector — for anyone who could reach the
// port, with no check, no owner and no record (git-issue jp9dy8anh12kr9r7nxn0).
type localRunner struct{ d *runnerd.Daemon }

func (l localRunner) Spawn(spec task.Launch) task.Handle {
	return l.d.Spawn(spec)
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
// (baseline and scoped).
func (s *Server) SetRunner(p task.Runner) { s.runner = p }

// UseBroker serves an in-process dial-in broker, hosted under a minted nonce, and returns it
// (e.g. for the operator to Revoke a claim). face is this verse's externally-reachable broker
// base (scheme://host:broker-addr — what a worker can reach); the broker publishes
// <face>/broker/<nonce> as each task's `dialURL`, closing the mint→publish→dial loop so a
// worker is told where to dial in rather than pre-configured.
//
// It is installed BESIDE the default runner and not as it. Serving a broker says a verse can
// put work in front of a person; it does not say every spawn is for a person. A spawn that
// names nobody means "run this", and the answer to that is the runtime this verse has —
// otherwise turning brokering on takes away the ability to run anything at all, and every
// charter that never mentioned a runner starts waiting for somebody instead of working.
//
// So the three ways of running work are three names: nothing is local, `broker` is here, and
// an installed runner is wherever its url says. See runnerFor.
func (s *Server) UseBroker(face string) *broker.Broker {
	b := broker.New()
	nonce := s.HostBroker("", b.WorkerHandler())
	b.SetBase(strings.TrimRight(face, "/") + "/broker/" + nonce)
	s.broker = b
	return b
}

// BrokerHandler serves this verse's broker face — a handler hosted under /broker/<nonce>, so a
// worker reaches a dial-in broker through verse's own URL rather than a separate service (see
// docs/task-control.md "Hosting"). Every nonce is one HostBroker registered, minted unguessable
// unless the registrant pinned it, and the nonce is the capability: there is no well-known
// name, and the built-in supervisor is not here (see localRunner). It is intended for a
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

// brokerFor resolves a nonce to its worker-facing handler: one registered via HostBroker, or
// nil for a nonce nobody minted.
func (s *Server) brokerFor(nonce string) http.Handler {
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
// scope is the scope st belongs to (empty for baseline). It is passed rather than
// derived from st because a task must be able to say where it lives before it exists —
// see task.Launch.Scope, and git-issue kwxxm8cth12ksn99fhn0 for what publishing without
// it cost.
func (s *Server) spawnerFor(st entity.Store, scope string, eng *trigger.Engine) trigger.Spawner {
	return func(id string, spec launch.Spec) task.Handle {
		// WHO THIS SPAWN IS BY: the rule that fired, as the engine spells it. Every record of
		// the task is written as the rule, so the task is owned by whoever owns the rule, and
		// the runner is told the same name as the caller it is working for.
		by := eng.Principal(spec.Chain)
		// AND WHO ASKED, where the rule said `as: subject`: the task acts as the pair, on the
		// asker's authority (entity.Pair). The engine set the asker from the subject's owner
		// record and nothing else sets it. A system asker is no party, so the rule acts alone, as
		// it would without `as`.
		if spec.Asker != "" && !entity.IsSystem(spec.Asker) {
			by = entity.Pair(spec.Asker, by)
		}
		base := st
		st := entity.As(st, by)
		// THE TASK'S RECORD IS KEPT AS ITS OWNER. It is the asker's where the task acts for one
		// (the pair resolves to them), and what the mirror writes on it after — running, done, its
		// result — is bookkeeping about their task, not an act of it. Written as the pair, every
		// write after the first would need the rule's installer to hold the asker's record too,
		// which they do not, and the task would stand `running` forever. What the TASK does still
		// acts as the pair (the launch's principal).
		rec := st
		if spec.Asker != "" && !entity.IsSystem(spec.Asker) {
			rec = entity.As(base, spec.Asker)
		}
		// NOT UNDER SOMEBODY ELSE'S ID. A rule's task id is a function of what fired, so anybody
		// can run a task under it first; the record this spawn writes would go over theirs, and
		// the task — its gate with it, through the link under it — would be theirs. Refused
		// before anything starts, and NOT mirrored: the record at that id is not this spawn's
		// to write a failure onto.
		if owner, other, err := entity.OwnedByOther(st, mirror.Ref(id), by); err != nil || other {
			why := fmt.Sprintf("spawn %s: the id stands as %s's, and an id names one party's work", id, owner)
			if err != nil {
				why = fmt.Sprintf("spawn %s: whose the id is could not be read: %v", id, err)
			}
			fmt.Fprintf(os.Stderr, "verse: %s\n", why)
			return task.FailedHandle(why)
		}
		// WHERE this runs, resolved before anything is attempted, so the meta below records
		// the runner the work actually went to rather than the default it did not.
		place, placeName, err := s.runnerFor(st, spec.Runner)
		if err != nil {
			// Same shape as the envelope and the resolver below: a spawn that cannot run
			// where it said does not run somewhere else instead.
			meta := mirror.Meta{Subject: spec.Subject, Chain: spec.Chain, Prompt: spec.Prompt, Driver: spec.Driver}
			h := task.FailedHandle(fmt.Sprintf("spawn %s: %v", id, err))
			s.reflect(rec, id, h, meta)
			return h
		}
		// The provenance every reflection of this spawn carries, envelope included: which
		// doer a selection picked and what the launch may do. Built once so the failure path
		// and the running path cannot disagree about what was attempted.
		meta := mirror.Meta{
			Subject: spec.Subject, Runner: place.Ref(), RunnerName: placeName,
			Chain: spec.Chain, Prompt: spec.Prompt, Driver: spec.Driver, Occurrence: spec.Occurrence,
			Doer: spec.Doer, Grant: spec.Grant, Tools: spec.Tools, Asker: spec.Asker,
			Gates:  spec.Gates,
			HeldBy: func() []string { return eng.HeldBy(id) },
		}
		// WHICH CREDENTIAL, resolved as the rule that named it — before the launch, so a spawn
		// that may not spend what it asked for never starts.
		key, kerr := credentialAt(st, by, spec.VirtualKey)
		if kerr != nil {
			h := task.FailedHandle(fmt.Sprintf("spawn %s: %v", id, kerr))
			s.reflect(rec, id, h, meta)
			return h
		}
		spec.VirtualKey = key
		// AND THE GATE'S CREDENTIAL, where the spawn asks for one: minted now, for this task, from
		// what the firing's gates settled (gatecredential.go). A spawn that asks for one it cannot be
		// given does not run without it, since the work it names would reach for it and fail later.
		var credential string
		if spec.Credential != nil {
			token, minted, cerr := s.mintCredential(base, eng, id, spec, by)
			if cerr != nil {
				h := task.FailedHandle(fmt.Sprintf("spawn %s: %v", id, cerr))
				s.reflect(rec, id, h, meta)
				return h
			}
			// On the task's record from its first write: what it is good for and until when.
			credential, meta.Credential = token, minted
		}
		l, err := launchOf(id, spec, by)
		if err != nil {
			// A charter that names an envelope this build does not have must not run
			// UNBOUNDED. Dropping the field is how `grant: read-only` came to mean nothing
			// in the first place, and defaulting a misspelling to the wildcard is the same
			// mistake with an error message attached. So the spawn fails, visibly, and the
			// trigger's own failure path carries the reason.
			h := task.FailedHandle(fmt.Sprintf("spawn %s: %v", id, err))
			s.reflect(rec, id, h, meta)
			return h
		}
		l.Credential = credential
		l.Scope = scope                // where this task's records live, so its gates land there too
		l.Occurrence = spec.Occurrence // and which run it is, so the process can say so too
		// A TASK ACTING FOR SOMEBODY ASKS THEM: `as: subject` runs on the asker's authority, so
		// its own questions naming nobody are theirs, and a gate naming $principal asks them. A
		// rule's task acts for nobody, and such a gate is refused rather than left to the docket.
		actsFor := mirror.ActsFor(by)
		if err := mirror.CheckPrincipal(spec.Gates, actsFor); err != nil {
			h := task.FailedHandle(fmt.Sprintf("spawn %s: %v", id, err))
			s.reflect(rec, id, h, meta)
			return h
		}
		if actsFor != "" {
			l.Answerers = []string{task.PrincipalAnswerer}
			meta.Answerers = l.Answerers
		}
		h := place.Spawn(l)
		s.reflect(rec, id, h, meta)
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
// is the first consumer the field has had (git-issue km5mzagq).
//
// Provenance travels for a different reason: not so the runner can decide anything, but so
// it can record why it was asked. Subject and Chain are what a firing knows about its own
// cause, and verse has always kept them on its side of the wire — which left a remote runner
// unable to attribute its own effects.
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

// credentialAt resolves the ADDRESS a spawn named for its model credential to the credential
// itself, reading the node AS WHOEVER NAMED IT.
//
// THE READ IS THE AUTHORITY. Spending a credential has a cost, so who may spend it is a
// permission — and the permissions model verse already has answers it, rather than a group or a
// list invented for keys. Checked against the namer and not against the runtime: a runner reads
// as itself, so a charter naming an address its own owner may not read would otherwise work, and
// the whole property would evaporate.
//
// THE NODE HOLDS THE KEY. That makes the store hold bearer strings, which is a real cost —
// backups, snapshots, and any rule that may read one may also derive it into an io's place and
// have it said out loud. It is taken deliberately, because the alternative (a reference to a
// name the runtime allowlists) bought less than it looked: whoever may read a credential may
// already SPEND it by naming it in a spawn, so the reference only moved where the key was typed,
// at the price of a third name, an allowlist per runtime to keep in step, and a deploy to
// rotate. A Portkey virtual key is bounded by its own budget and route, which is what makes that
// trade payable; a provider key would not be.
//
// So: one write rotates a key, perms decide who may spend it, and a node holding one wants the
// same fencing verse's own half has, or a stray `--below` grant reaches it.
//
// The refusal cannot tell "not there" from "not yours" (git-issue 65xaf72n), so it says both.
func credentialAt(store entity.Store, principal, at string) (string, error) {
	if at == "" {
		return "", nil
	}
	ref, err := entity.ParseAddr(at)
	if err != nil {
		return "", fmt.Errorf("virtualKey %q is not an address: %w", at, err)
	}
	e, ok, err := entity.As(store, principal).Get(ref)
	if err != nil {
		return "", fmt.Errorf("reading the credential at %s: %w", at, err)
	}
	if !ok {
		return "", fmt.Errorf("no credential stands at %s, or it is not %s's to read: a spawn "+
			"spends what its namer may read, and this one may not", at, principalOrNobody(principal))
	}
	key := entity.Str(e.Payload, "key")
	if key == "" {
		return "", fmt.Errorf("the credential at %s holds no `key`: the node IS the credential, "+
			"and one that carries nothing would run this work on a budget nobody named", at)
	}
	return key, nil
}

// principalOrNobody names the namer in a refusal, for a spawn that named nobody.
func principalOrNobody(p string) string {
	if p == "" {
		return "nobody"
	}
	return p
}

func launchOf(id string, spec launch.Spec, principal string) (task.Launch, error) {
	l := task.Launch{
		ID: id, Prompt: spec.Prompt, Driver: spec.Driver,
		Frame: spec.Frame, Tools: spec.Tools, Answer: spec.Answer, Retain: spec.Retain,
		Environment: spec.Environment, VirtualKey: spec.VirtualKey,
		// A firing has no launcher, so its principal is the rule that fired — the address of
		// its record, which the runner can read back. See trigger.Engine.Principal.
		Provenance: task.Provenance{Principal: principal, Subject: spec.Subject, Chain: spec.Chain},
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
	vec, err := task.SpawnVector(spec.Vector, spec.Gates)
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
func (s *Server) reconcileTasks() error {
	if err := s.awaiting(phaseSuspensions); err != nil {
		return err
	}
	var resumed []resumedRun
	// The task slice, not the verse. This read at the root was 18.9s of staging's startup.
	tasks := entity.Below(s.Store, mirror.IsTaskish, 2, entity.VerseRoot, mirror.System)
	if err := entity.ReadFailed(s.Store); err != nil {
		// Not "no tasks outlived us": orphaning nothing is right, and resuming nothing is not.
		// Reported as the read it is, and the phase asks again.
		return fmt.Errorf("tasks could not be read, so none were resumed or orphaned: %w", err)
	}
	for _, ent := range tasks {
		if ent.Ref.Under(entity.VerseRoot, mirror.System, mirror.RunKind) {
			// A record of a run that HAPPENED, not a task that is running. Named explicitly
			// for the reason the claim below is: it carries a `status` like an instance does,
			// so nothing later would catch it, and reconcile would try to resume history —
			// dialling a runner about a run that ended, or stamping `orphaned` on the record
			// of one that did not.
			continue
		}
		if ent.Ref.Under(entity.VerseRoot, mirror.System, mirror.ClaimKind) {
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
		// What can still be re-attached is what did not die with this process, and the entity
		// says which by naming where it ran. `broker` is a reserved NAME rather than a url, so
		// it is resolved to the runtime this process holds — which survives a restart exactly
		// when the broker object does, as one handed in from outside does and one this verse
		// serves itself does not. Dialling the name as a url would instead ask DNS for a host
		// called "broker" and orphan the task on the failure: the right answer for the
		// in-process case, reached by a hung request, and the wrong one for the other.
		if runnerRef != "" && runnerRef != "local" { // the work may have outlived verse
			// The URL is what this attaches with, and that is the whole reason it is stamped
			// beside the name. A restart that resolved the name instead would depend on the
			// runner ENTITY still existing, so removing one would orphan work that is running
			// perfectly well — a deletion with an action at a distance, and the thing that
			// would make step 1's `--leave` a lie.
			place := task.Runner(runner.HTTP(runnerRef))
			if runnerRef == brokerName {
				place = s.brokerRuntime()
			}
			if h := attachOn(place, ent.Ref.Name()); h != nil {
				// re-attach: observe
				s.reflect(s.Store, ent.Ref.Name(), h, mirror.Meta{
					Subject: subject, Runner: runnerRef, Chain: chain,
					// Carried for the same reason chain is: Reflect REPLACES the payload, so
					// provenance this does not hand back is provenance the resumed task
					// forgets. `verse ps --runner <name>` reads this field, and a task
					// that lost it on restart would drop out of the answer while still
					// running there.
					RunnerName: entity.Str(ent.Payload, "runnerName"),
					// Carried, not re-stamped: a resumed task did not start again.
					StartedAt: entity.Str(ent.Payload, "startedAt"),
					// And whose authority it runs with beside its rule: the runner still
					// holds the pair it launched with, and the record says so across the
					// restart rather than forgetting on the first re-reflection.
					Asker:     entity.Str(ent.Payload, "asker"),
					Answerers: entity.Strs(ent.Payload, "answerers"),
					HeldBy:    func() []string { return s.Eng.HeldBy(ent.Ref.Name()) },
				})
				// re-track: steerable, AND its hooks rehydrated from the chain, so the
				// chain or sequence it belongs to carries on rather than ending silently.
				// A task about no subject is the flow of its own id, as at spawn.
				flow := subject
				if flow == "" {
					flow = ent.Ref.Name()
				}
				s.Eng.AdoptChain(flow, ent.Ref.Name(), chain, h)
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
	return nil
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
//   - A PERSON raised it: an ask. Its waiter is versed on the asker's behalf, and the asker is
//     still here whatever became of the process, so it is waited on again (resumeAsks) and
//     left as it stands.
func (s *Server) reconcileQuestions() {
	for _, ent := range pendingQuestions(s.Store) {
		if entity.Str(ent.Payload, "asker") != "" {
			continue
		}
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
		s.write(ent.Ref.At("orphanedAt"), entity.Val(s.now().UTC().Format(time.RFC3339)))
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

// brokerRuntime is the dial-in broker this process can still reach: the one it serves, or one
// installed as the default runner from outside. Nil when there is none, which is what a
// brokered task whose runtime did not outlive the restart resolves to.
//
// Two places to look because a broker arrives two ways, and they differ in exactly the thing a
// restart cares about: `--broker` builds one here, so it is new and knows nothing about what
// ran before; one handed in with SetRunner is somebody else's object, and a worker dialled into
// it never noticed this process go away.
func (s *Server) brokerRuntime() task.Runner {
	if s.broker != nil {
		return s.broker
	}
	if s.runner != nil && s.runner.Ref() == brokerName {
		return s.runner
	}
	return nil
}

// attachOn re-attaches to a task on p, tolerating a p that is not there. A nil runner is a
// runtime that did not outlive the restart, and it re-attaches to nothing rather than panicking
// on the way to the same answer.
func attachOn(p task.Runner, id string) task.Handle {
	if p == nil {
		return nil
	}
	return p.Attach(id)
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
	for _, ent := range entity.Below(s.Store, func(r entity.Ref) bool {
		return r.ChildOf(entity.VerseRoot, mirror.System, mirror.ClaimKind)
	}, 1, entity.VerseRoot, mirror.System, mirror.ClaimKind) {
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

// engineOf is the engine that holds a scope's live work: baseline's for the empty scope, the
// scope's own otherwise, opened if nobody has yet.
func (s *Server) engineOf(scope string) *trigger.Engine {
	if scope == "" {
		return s.Eng
	}
	return s.scopeEngine(scope)
}

// scopeEngine returns the engine for a scope, creating and starting it on first use.
// It observes store.Scope(scope) — one copy-on-write overlay — and spawns
// into that same scope. Requires Start to have captured the lifetime ctx.
//
// The scope's own charter is RESTORED into it, which is what makes `verse c -a
// --scope me` durable in the sense the word usually carries: the rule was always written
// down, and until this it was written down and then interpreted by nobody, because a scope's
// engine is created on demand and restored nothing. A dev came back from a restart to a
// scope whose charter listed fine and fired at nothing.
//
// Baseline's rules, which the scoped store reads through, are NOT installed here — see
// Engine.LoadCharter. That is a decision about what a scope means and it is still open; what
// is not open is that a scope's own rules must run.
func (s *Server) scopeEngine(scope string) *trigger.Engine {
	s.scopeMu.Lock()
	se, ok := s.scopeEngs[scope]
	if !ok {
		scoped := s.Store.Scope(scope)
		eng := trigger.New(scoped, s.Reg)
		eng.SetScope(scope) // before anything is installed: it is stamped on what this engine writes
		// The scope's own suspensions, which hold the scope's work and nobody else's, loaded
		// below with the charter and before it arms anything.
		susp := trigger.NewSuspensions()
		eng.SetSuspensions(susp)
		if s.tripPaceSet { // same throttle policy as the baseline it overlays (see SetTripPace)
			eng.SetTripPace(s.tripPace)
		}
		eng.SetSpawner(s.spawnerFor(scoped, scope, eng))
		eng.SetDeadlineMover(s.moveDeadline)
		sctx, cancel := context.WithCancel(s.ctx)
		// Per-trigger observation: no root watch. Each scoped trigger watches exactly the
		// (system,kind) subtree it matches, scoped to this scope. cancel stops them.
		eng.ObserveScoped(sctx)
		se = &scopeEngine{eng: eng, ctx: sctx, cancel: cancel, susp: susp}
		s.scopeEngs[scope] = se
	}
	s.scopeMu.Unlock()
	// OFF THE LOCK, and once. Restoring reads the store and opens a watch per rule — network
	// work — and holding scopeMu across it would stall every other scope's lookup behind one
	// dev connecting. The sync.Once is what makes that safe to do unlocked: a second caller
	// arriving mid-restore waits for the first rather than installing the charter twice.
	//
	// The scope's PERMS MIRROR is loaded and watched here too, on the scope's own view of the
	// store: a scope's checks read its own mirror (entity.Attribution), which holds baseline's
	// grants through the overlay and the scope's own on top. Until it loads — and if it cannot —
	// the scope's checks read the store per key, which is right and slower, so a mirror that
	// did not load is logged rather than fatal, as the charter is.
	//
	// The scope's SUSPENSIONS load before the charter arms a rule, and unlike the mirror they
	// FAIL CLOSED: a table that has not loaded holds everything, so a load that did not happen
	// is asked again until it does (loadScopeSuspensions) rather than logged and left.
	se.restore.Do(func() {
		if err := entity.Track(se.ctx, s.Store.Scope(scope)); err != nil {
			fmt.Fprintf(os.Stderr, "verse: perms mirror (scope %s): %v\n", scope, err)
		}
		s.loadScopeSuspensions(se, scope)
		s.restoreScopeCharter(se.eng, scope)
	})
	return se.eng
}

// restoreScopeCharter reinstalls the scope's own strata and rules, in that order and for the
// reason Start gives: a rule names the stratum it lives in, and one restored before its
// stratum is held rather than installed.
//
// Failures are logged rather than returned, as Start's are: the caller is a request that has
// something else to do (installing a trigger, listing one), and a scope whose charter came
// back incomplete is still a scope worth serving.
func (s *Server) restoreScopeCharter(eng *trigger.Engine, scope string) {
	if err := eng.LoadStrata(); err != nil {
		fmt.Fprintf(os.Stderr, "verse: strata (scope %s): %v\n", scope, err)
	}
	if err := eng.LoadCharter(); err != nil {
		fmt.Fprintf(os.Stderr, "verse: charter (scope %s): %v\n", scope, err)
	}
}

// SeedError is what the permission seed refused with, nil when it landed. A server holding one
// has no policy and must not serve; `verse up` says so and stops.
func (s *Server) SeedError() error { return s.seedErr }

// StartServing is Start for a server that is about to serve: it takes the lifetime context
// synchronously — scoped engines need it, and a request may arrive immediately — and runs the
// restore behind it.
//
// It exists because `verse up` served nothing until Start returned. The restore is several
// store-shaped phases and there is no reason the API should be absent for them: /healthz
// answers `started: false` with a 503 until they finish, so readiness gates exactly as it did
// while an operator (and the probe) gets an answer instead of a refused connection.
func (s *Server) StartServing(ctx context.Context) {
	s.ctx = ctx
	s.susp.Unload() // before the first request can be served, which Start's goroutine is not
	go s.Start(ctx)
}

// Start begins the engine's reactive loop (observing the store). It returns
// immediately; the loop runs until ctx is cancelled. It first reconciles orphans —
// task:instance entities a previous run left running — since this is the monolith
// (the task runtime restarts with the engine, so those tasks are gone).
func (s *Server) Start(ctx context.Context) {
	s.ctx = ctx // scoped engines (created later, per scope) start under this
	// Nothing is suspended that the records do not say, and nothing runs until they have been
	// read: the table holds everything from here until phaseSuspensions loads it.
	if s.susp.Loaded() {
		s.susp.Unload()
	}
	// WHERE THE TIME GOES, said out loud, and A READ THAT DID NOT HAPPEN IS ASKED AGAIN: each
	// phase reads the store, and one whose read hit its deadline says so and is retried until
	// it answers, rather than restoring nothing for good (startup.go).
	phase := s.phase
	defer func() { s.restored.Store(true) }()
	// The perms mirror, loaded and watched, so the check every write and read makes costs no
	// round trip: on a busy store the per-write read hit its deadline and refused the write.
	phase(phasePerms, func() error { return entity.Track(ctx, s.Store) })
	// The suspensions before anything they could hold is re-attached or restored: until they
	// have loaded they hold everything, and a store that cannot serve them holds every rule and
	// task, since a suspension that failed open on a restart would resume what somebody stopped.
	phase(phaseSuspensions, s.restoreSuspensions)
	phase(phaseTasks, s.reconcileTasks) // resume remote-hosted tasks; orphan local-dead ones
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
	// And why it follows ObserveScoped, which is what binds ctx to the engine: anything
	// restored before that runs under a context nothing cancels, so it would outlive the
	// shutdown that was supposed to stop it.
	//
	// A rule that could not be restored is reported and skipped, so one bad entity does not
	// cost the rest of the charter.
	//
	// This is BASELINE's charter. A scope's own is restored when its engine is built, which
	// happens on demand — the first scoped request, i.e. the dev reconnecting (see
	// scopeEngine). It cannot happen here, because a restart cannot enumerate the scopes that
	// have one: a scope's layer is invisible from baseline and nothing registers a scope's
	// existence. So a scope nobody has come back to sits inert, which is the remaining
	// half of the gap scoped resume also has (see reconcileClaims).
	// The ORDERING first: a rule names the stratum it lives in, and one restored before its
	// stratum is held rather than installed — correct, and it would then only start firing when
	// something happened to touch the stratum again. Loading strata first makes the restart what
	// it should be, an install over a verse that has moved on.
	// Two phases rather than one, so that each is asked again on its own: a rule restored
	// before its level is held and installs when the level arrives, so the order between the
	// two holds whichever of them the store answers first.
	phase(phaseStrata, func() error {
		if err := s.awaiting(phaseSuspensions); err != nil {
			return err
		}
		return s.loaded(s.Eng.LoadStrata)
	})
	phase(phaseCharter, func() error {
		if err := s.awaiting(phaseSuspensions); err != nil {
			return err
		}
		return s.loaded(s.Eng.LoadCharter)
	})
	// The charter PACKAGES, each into an engine of its own: a package is a stamp on records
	// anyone can list, so unlike a scope a restart can bring every one of them back rather than
	// waiting for somebody to reconnect. It follows the baseline load because the two split the
	// same listing between them — baseline takes the unstamped records, a package its own.
	phase(phasePackages, s.restorePackages)
	// And the charter the deployment itself stands behind, as the system. After the restore,
	// so that it is installed over what stood rather than under it, and every time, since an
	// install of what already stands is a no-op with a revision attached and the file is the
	// truth about what the system's charter is.
	phase(phaseSystem, func() error {
		// Over what stood, so it waits for the restore to have answered; a restore still
		// being asked again is a charter this must not install under.
		if err := s.awaiting(phaseCharter, phasePackages); err != nil {
			return err
		}
		s.installSystemCharter()
		return nil
	})

	// And the docket. A selection's question is durable and the goroutine awaiting it is
	// not, so one left by the previous verse has nobody reading its ballots: this gives each
	// its awaiter back, and settles the ones no charter can answer any more.
	//
	// AFTER the charter, which is what it navigates back into to find what a ballot is
	// holding up; and after reconcileTasks, so a resumed selection sees the live registry
	// rather than starting a second run beside work that outlived us.
	phase(phaseSelections, s.resumeSelections) // baseline AND every package's own engine
	// And the clocks. A question's deadline is durable and the timer that fires it is not.
	phase(phaseDeadlines, s.armDeadlines)
	// And the schedules, for the same reason: the record holds `next`, this process holds the
	// timer.
	phase(phaseSchedules, s.armSchedules)
	// And the asks' expiries, for the same reason: the ask holds `expires`, this process the timer.
	phase(phaseAsks, s.armAsks)
	// And a person's asks: each that waits is waited on again, since the wait was the last
	// process's, and a yes whose `then` had not started starts.
	phase(phaseAsksWait, s.resumeAsks)
	// And the sources, last: each one's first pass is a write the restored charter should be
	// standing to react to. A level-triggered pass does not repeat itself, so what a rule
	// missed by being late it would miss for good.
	phase(phaseSources, s.restoreSources)
	// And the charters verse ships at the system level, rendered by `verse up` from the config
	// (Server.SystemCharters). After the sources: such a charter declares outboxes to the places
	// the ios read, and the declaration is checked against the ios the restore registered. Over
	// what stood, as the system charter file is, and every time, for the same reason.
	phase(phaseSystemTexts, func() error {
		if err := s.awaiting(phaseSystem, phaseSources); err != nil {
			return err
		}
		s.installSystemCharters()
		return nil
	})
	s.reportActingAsNobody()
}

// Handler returns the HTTP surface. Observation (read plane) and control are
// deliberately distinct routes.
func (s *Server) Handler() http.Handler {
	rt := &router{mux: http.NewServeMux()}
	// observation (read plane). The response of every route is declared as one/list/stream
	// (or nothing), which is what the published table needs to be right about a listing and
	// what the root schema is derived from — see rootSignature.
	rt.handle("GET /status", s.status, "", one("verse-status"))
	rt.handle("GET /healthz", s.healthz, "", nothing)
	rt.handle("GET /watch", s.watch, "", stream("verse-event"))
	rt.handle("GET /state", s.state, "", one("verse-state"))
	rt.handle("GET /entity", s.getEntity, "", one("verse-entity"))
	// A node's permissions, as its owner reads and sets them. The mirror itself is the
	// system's and not readable over the entity routes; this is the owner's window onto their
	// own record, and the one verb by which a person writes to it.
	rt.handle("GET /perms", s.getPerms, "", one("verse-perms-record"))
	rt.handle("GET /perms/records", s.listPerms, "", list("verse-perms-record"))
	// Groups: a named set of principals, a grantee whose address admits its members.
	rt.handle("GET /groups", s.listGroups, "", nothing)
	rt.handle("POST /groups", s.putGroup, "verse-group", nothing)
	rt.handle("GET /groups/{name}", s.getGroup, "", nothing)
	rt.handle("DELETE /groups/{name}", s.removeGroup, "", nothing)
	// THE ASKS, both sides of each: the asker raises, changes and withdraws one and lists theirs;
	// whom it asks lists the questions waiting on them and answers. Every question is an ask at
	// verse.ask.<id>, so there is one tree of routes, and `verse ask` and `verse question` are its
	// two readings.
	rt.handle("GET /asks", s.listAsks, "", nothing)
	rt.handle("POST /asks", s.postAsk, "verse-ask", one("verse-asked"))
	rt.handle("GET /asks/{id}", s.getAsk, "", one("verse-entity"))
	rt.handle("DELETE /asks/{id}", s.deleteAsk, "", nothing)
	rt.handle("PATCH /asks/{id}", s.patchAsk, "verse-ask-change", one("verse-asked"))
	rt.handleControl("POST /asks/{id}/answer", s.answerAsk, "verse-resolve", nothing)
	rt.handleControl("POST /asks/{id}/say", s.sayOnAsk, "verse-gate-say", nothing)
	rt.handle("PUT /perms", s.putPerms, "verse-perms-edit", one("verse-perms-record"))
	// The live tasks and the suspensions a selection names (flows.go): the work in flight and
	// the work held from now on, one listing narrowed by the flags an act takes.
	rt.handle("GET /flows", s.listFlows, "", list("verse-flow-row"))
	rt.handle("GET /schedules", s.listSchedules, "", list("verse-schedule-row"))
	rt.handle("GET /flow/tree", s.flowTree, "", list("verse-flow-tree"))
	// ONE ROUTE FOR THE CHARTER: the tree at an address, and one rule or one level by name
	// inside it. `/triggers` and `/strata` were two surfaces over one idea — see versed/charter.go.
	rt.handle("GET /charter", s.getCharter, "", nothing)
	rt.handle("GET /charter/{name}", s.getCharterItem, "", one("verse-trigger"))
	// the API describing itself: the route table, and the schema its entries name
	rt.handle("GET /api", s.serveAPI, "", one("verse-routes"))
	rt.handle("GET /schema", s.serveSchema, "", nothing)
	rt.handle("GET /schema/{name}", s.serveDefinition, "", nothing)
	// mutation + control (out-of-band from observation; priority-serviced)
	rt.handle("POST /entity", s.putEntity, "verse-put-entity", one("verse-entity"))
	rt.handle("DELETE /entity", s.deleteEntity, "", nothing)
	rt.handle("POST /charter", s.addCharter, "verse-trigger", nothing)
	// The charter check: what an install of the same documents would do, with nothing installed
	// (chartercheck.go). A question beside the install, and never part of it.
	rt.handle("POST /charter/check", s.checkCharter, "verse-trigger", one("verse-charter-check"))
	rt.handle("POST /schedules/{id}/tick", s.tickSchedule, "verse-schedule-tick", nothing)
	rt.handle("DELETE /charter/{name}", s.deleteCharterItem, "", nothing)
	rt.handle("DELETE /charter", s.deleteCharter, "", nothing)
	// resolvers — who answers a question. A resolver is installed rather than declared in a
	// charter, because it outlives any one rule and is managed by whoever supervises rather
	// than by whoever wrote the work.
	rt.handle("GET /resolvers", s.listResolvers, "", nothing)
	rt.handle("POST /resolvers", s.addResolver, "", nothing)
	rt.handle("GET /resolvers/{name}", s.getResolver, "", nothing)
	rt.handle("DELETE /resolvers/{name}", s.removeResolver, "", nothing)
	rt.handle("POST /resolvers/{name}/grant", s.grantResolver, "", nothing)
	rt.handle("GET /resolvers/{name}/asks", s.servedAsks, "", nothing)
	rt.handle("GET /asks/{id}/served", s.askServed, "", nothing)
	rt.handle("POST /resolvers/{name}/revoke", s.revokeResolver, "", nothing)
	// runners — WHERE work runs, as against who answers for it. Installed rather than chosen
	// at startup, so a drive can add one for work this verse does not otherwise need to know
	// about.
	// sources — what REFLECTS an external system into the verse, as against where work runs.
	// Addressed by the slice reflected, because that is a question verse already asks: who
	// writes `git:*`. See docs/io.md.
	rt.handle("GET /io", s.listIOs, "", nothing)
	rt.handle("POST /io", s.addIO, "", nothing)
	rt.handle("GET /io/{name}", s.getIO, "", nothing)
	rt.handle("GET /io/{name}/check", s.checkIO, "", nothing)
	rt.handle("DELETE /io/{name}", s.removeIO, "", nothing)
	// What a package declared it sends to an io's out place (outbox.go), asked by that io.
	rt.handle("GET /outboxes", s.listOutboxes, "", nothing)
	// The hint: a delivery that carries WHICH SLICE and nothing else, so it can cost at most
	// a reconcile of a slice its sender does not choose the contents of.
	rt.handleUnattributed("POST /io/{name}/hint", s.hintIO, "", nothing)
	// And the out half's counterpart. Verse dials a reflection and therefore knows how it is
	// doing; it never dials an out half, so the only way it can know is to be told.
	rt.handleUnattributed("POST /io/{name}/pass", s.passIO, "", nothing)
	// accounts — which account in an external system is which principal (identity.go). A person
	// claims an account and proves it; the system binds it; an io looks a comment's author up.
	// Verse as an issuer: the discovery document and the key set a relying party verifies its
	// tokens against (issuer.go). Reads that name nobody, answered in JSON as OIDC writes them.
	rt.handle("GET "+issuerDiscoveryPath, s.serveIssuerDiscovery, "", nothing)
	rt.handle("GET "+issuerKeysPath, s.serveIssuerKeys, "", nothing)
	// Who the request is from, and how versed knows: the provider that verified them, or their
	// own say-so (auth.go).
	rt.handle("GET /whoami", s.whoami, "", nothing)
	rt.handle("GET /accounts", s.listAccounts, "", nothing)
	rt.handle("POST /accounts/{provider}", s.claimAccount, "", nothing)
	rt.handle("POST /accounts/github/verify", s.verifyAccount, "", nothing)
	rt.handle("POST /accounts/{provider}/heard", s.heardAccount, "", nothing)
	rt.handle("POST /accounts/{provider}/link", s.linkAccount, "", nothing)
	rt.handle("GET /accounts/{provider}/who", s.whoIsAccount, "", nothing)
	rt.handle("DELETE /accounts/{provider}/{sub}", s.revokeAccount, "", nothing)
	rt.handle("GET /runners", s.listRunners, "", nothing)
	rt.handle("POST /runners", s.addRunner, "", nothing)
	rt.handle("GET /runners/{name}", s.getRunner, "", nothing)
	rt.handle("GET /runners/{name}/check", s.checkRunner, "", nothing)
	rt.handle("DELETE /runners/{name}", s.removeRunner, "", nothing)
	// A package's own declaration: what it reads outside its address. It carries `?at=` like
	// the two above, and it is posted BEFORE them, because a rule is checked against it.

	rt.handle("POST /scope/delete", s.deleteScope, "", nothing)
	// Holding a run or a suspension, and lifting it; saying to live work, and cancelling a run
	// (flows.go).
	rt.handle("POST /flow/suspend", s.flowSuspend, "verse-flow-select", one("verse-flow-row"))
	rt.handle("POST /flow/resume", s.flowResume, "verse-flow-select", one("verse-flow-row"))
	rt.handleControl("POST /flow/say", s.flowSay, "verse-flow-select", one("verse-steered"))
	rt.handle("POST /flow/cancel", s.flowCancel, "verse-flow-select", one("verse-steered"))
	// task runtime (task) — mounted; serves /tasks[/{id}[/…]]. versed intercepts the
	// two READ routes to union the local runtime with the active runner's own tasks
	// (e.g. a broker's), stamping each with its runner; everything else (spawn, events,
	// control) falls through to the local runtime.
	ph := s.Local.Handler()
	rt.handle("GET /tasks", s.listTasks, "", list("verse-instance"))
	rt.handle("POST /tasks", s.spawnTask, "verse-spawn", one("verse-spawned"))
	rt.handle("GET /tasks/{id}", s.getTask, "", one("verse-instance"))
	rt.handle("GET /tasks/{id}/dial", s.taskDial, "", one("verse-dial"))
	rt.handle("GET /tasks/{id}/inbox", s.taskInbox, "", list("verse-inbox-item"))
	rt.handle("GET /tasks/{id}/events", func(w http.ResponseWriter, r *http.Request) {
		s.taskEvents(w, r, ph)
	}, "", stream("verse-frame"))
	// cancel/say are runner-agnostic: local tasks go to the runtime, brokered/remote
	// ones to the runner's handle — so `task cancel`/`say` work wherever a task runs.
	rt.handle("POST /tasks/{id}/cancel", func(w http.ResponseWriter, r *http.Request) {
		s.taskControl(w, r, ph, "cancel")
	}, "", nothing)
	rt.handleControl("POST /tasks/{id}/say", func(w http.ResponseWriter, r *http.Request) {
		s.taskControl(w, r, ph, "say")
	}, "verse-say", nothing)
	// pause/resume the same way: held at the task's next boundary wherever it runs, and asked
	// of the task's scope and of control first, as every control verb is.
	rt.handle("POST /tasks/{id}/pause", func(w http.ResponseWriter, r *http.Request) {
		s.taskControl(w, r, ph, "pause")
	}, "", nothing)
	rt.handle("POST /tasks/{id}/resume", func(w http.ResponseWriter, r *http.Request) {
		s.taskControl(w, r, ph, "resume")
	}, "", nothing)
	// Answering is the ONE path (answerQuestion), not a case of taskControl: this route
	// differs from `/asks/{id}/answer` only in being TOLD which task holds the question
	// instead of working it out. Everything that follows — attribution, delivery, the record,
	// a stale answer — is the same act and must not have two implementations.
	rt.handleControl("POST /tasks/{id}/resolve", func(w http.ResponseWriter, r *http.Request) {
		var body api.Resolve
		if err := codec.Read(r, &body); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		s.answerQuestion(w, r, body, r.PathValue("id"), s.scopedStore(r))
	}, "verse-resolve", nothing)
	// re-vector (slide autonomy on a running task) — runner-agnostic like cancel/say: a
	// local task is served by the runtime mount (runnerd's setVector), a brokered/remote
	// one goes to the runner handle's SetVector.
	rt.handle("PUT /tasks/{id}/vector", func(w http.ResponseWriter, r *http.Request) {
		s.taskControl(w, r, ph, "vector")
	}, "verse-set-vector", nothing)
	rt.mount("/tasks", ph)
	rt.mount("/tasks/", ph)
	s.routes = rt.routes
	s.unattributed = rt.unattributed
	s.control = rt.control
	// Every answer says which verse gave it, and a client that cannot be talking to this one
	// is turned away before any of the above runs. See versioned.
	return versioned(s.principals(rt.mux, rt.unattributed))
}

// principals notes every principal that presents itself, once: `verse.principal.<kind>.<id>`
// is the record that a party has acted here, which is what `verse d verse principal` lists. Written
// under an absence precondition so the first sighting is the one on record, and remembered
// per process so that a listing costs one write per party rather than one per request.
//
// It is a middleware rather than a line in scopedStore because reads present a principal too,
// and a party that only ever looked is still a party the verse has met.
//
// WHO A REQUEST IS FROM is its bearer's, where it carries one: an id_token from an identity
// provider the deployment names is verified, and the principal its account is bound to is set on
// the request, so every write and check below reads it where it reads the header (auth.go).
//
// WHAT A REQUEST MAY PRESENT without one is `user:`, `agent:` or `runner:`, self-asserted within
// the three, and a bare email, which is respelled `user:` on the request itself so every write and
// check below reads the one form. Where the deployment admits people only verified (the mode
// `required`), a `user:` presented this way is refused 401. People are verified first: `agent:`
// and `runner:` are next, each under its own block of `auth`, and are taken as asserted until
// that is built. A name the grammar does not read is refused. So are the names versed
// makes — `via:` when a rule fires, a source reflects or a resolver answers, `system:` when versed
// acts — since a header saying either is precisely the assertion that was fine while nothing was
// refused and is a way to hold what it names once something is. What installs a system-owned
// charter is versed itself, from the file its deployment names (Server.SystemCharter). `group:`
// does not parse as a principal, since nobody acts as a group.
//
// AND A REQUEST THAT IS NOT A READ PRESENTS ONE. A write naming nobody is not a write by nobody:
// somewhere behind the route it reaches a part of verse whose own store is the system's, and an
// empty name there means "mine" — a charter installed with no header is persisted by the engine,
// as the engine, and its rules fire as the system. Refusing `system:` in the header is worth
// nothing while leaving the header off says the same thing. So it is refused HERE, before any
// route runs and by default: every method but GET and HEAD, on every pattern the mux holds,
// mounted ones included, except the routes registered as acting for a party the route itself
// names (router.handleUnattributed). A route added tomorrow refuses until somebody decides
// otherwise.
func (s *Server) principals(mux *http.ServeMux, unattributed map[string]bool) http.Handler {
	var h http.Handler = mux
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// A bearer is who the request is from, verified, and it is read ahead of the header: the
		// header then names the same principal or nobody (auth.go).
		if token := bearerOf(r); token != "" {
			_, pattern := mux.Handler(r)
			// A read, a registered control act, or no route at all, which the router answers.
			ctl := pattern == "" || r.Method == http.MethodGet || r.Method == http.MethodHead || s.control[pattern]
			who, provider, via, refused := s.authenticate(r.Context(), token, principalOf(r),
				r.Header.Get(api.ForPrincipalHeader), act{method: r.Method, pattern: pattern, control: ctl})
			if refused != nil {
				if refused.code == http.StatusUnauthorized {
					w.Header().Set("WWW-Authenticate", "Bearer")
				}
				http.Error(w, refused.msg, refused.code)
				return
			}
			r = authenticated(r, provider)
			if via != "" {
				r = relayed(r, via)
			}
			r.Header.Set(api.PrincipalHeader, who)
		}
		p := principalOf(r)
		if p == "" && r.Method != http.MethodGet && r.Method != http.MethodHead {
			if _, pattern := mux.Handler(r); !unattributed[pattern] {
				http.Error(w, fmt.Sprintf("%s is required: a request that writes names who makes it — "+
					"user:, agent: or runner: (the CLI sends its config's `user:` or `agent:`, and "+
					"`-as` names somebody else)", api.PrincipalHeader), http.StatusBadRequest)
				return
			}
		}
		if p != "" {
			g, err := entity.ParsePrincipal(p)
			if err != nil {
				http.Error(w, api.PrincipalHeader+": "+err.Error(), http.StatusBadRequest)
				return
			}
			switch g.Kind {
			case entity.ViaKind, entity.SystemKind:
				http.Error(w, fmt.Sprintf("%s: %q is a name versed makes — via: when a rule fires, a "+
					"source reflects or a resolver answers, system: when versed acts — and not one a "+
					"request presents; a request is user:, agent: or runner:. A charter the system "+
					"stands behind is installed from the file the deployment names (systemCharter "+
					"in its config)", api.PrincipalHeader, p), http.StatusBadRequest)
				return
			}
			// Where people are admitted only verified, a person's name with no proof beside it is
			// refused and not served as nobody: an answer with nothing in it reads as an empty
			// verse, and a refusal says to log in.
			if g.Kind == entity.UserKind && s.verifiesPeople() && authenticatedBy(r) == claimantAsserted {
				w.Header().Set("WWW-Authenticate", "Bearer")
				http.Error(w, fmt.Sprintf("this verse admits a person only from an identity provider's "+
					"token (auth.principal.mode is %s), and this request names %s with none: log in "+
					"(`verse account login`). People are verified first; an agent or a runner is taken as "+
					"given until its own authentication is built, and a request that names nobody "+
					"still reads what everyone may", AuthRequired, g.String()),
					http.StatusUnauthorized)
				return
			}
			if p = g.String(); p != principalOf(r) {
				r.Header.Set(api.PrincipalHeader, p)
			}
			s.notePrincipal(p)
		}
		if name := r.Header.Get(api.ResolverHeader); name != "" {
			via, err := s.actAsResolver(p, name)
			if err != nil {
				http.Error(w, api.ResolverHeader+": "+err.Error(), http.StatusForbidden)
				return
			}
			r.Header.Set(api.PrincipalHeader, via)
		}
		h.ServeHTTP(w, r)
	})
}

// actAsResolver is who a request presented as p acts as when it names a resolver: `via:` the
// resolver, where p installed it, and a refusal otherwise.
func (s *Server) actAsResolver(p, name string) (string, error) {
	if p == "" {
		return "", fmt.Errorf("a resolver is acted as by whoever installed it, and this request names nobody")
	}
	ref := resolver.Ref(strings.TrimPrefix(name, entity.VerseRoot+"."+resolver.System+"."))
	owner, err := entity.OwnerOf(s.Store, ref)
	if err != nil {
		return "", err
	}
	if owner == "" || owner != p {
		return "", fmt.Errorf("%s acts as %s only if it installed it", p, ref)
	}
	return entity.Via(ref), nil
}

// notePrincipal records p under the kind its name carries, the first time this process sees
// them.
func (s *Server) notePrincipal(p string) {
	if _, seen := s.users.Load(p); seen {
		return
	}
	if err := entity.Note(s.Store, p); err != nil {
		return // the store did not answer, or the name is none to meet; the next request asks again
	}
	s.users.Store(p, struct{}{})
}

// ScriptedBuilder is verserunner.ScriptedBuilder, where the scripted driver now lives with the
// verse runner. It stays here for the in-process runner and its tests until that runner is
// removed.
var ScriptedBuilder = verserunner.ScriptedBuilder

// deleteScope drops the ?scope copy-on-write overlay and all its data —
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
	se, ok := s.scopeEngs[name]
	delete(s.scopeEngs, name)
	s.scopeMu.Unlock()
	var eng *trigger.Engine
	if ok {
		se.cancel()
		eng = se.eng
	}
	// And its live work, which the shared runtime would otherwise go on running for a scope that
	// no longer exists, with no records and no store to reflect into. After the engine stops, so
	// no rule starts more of it.
	s.cancelScopeTasks(name, eng)
	// And its perms mirror: the grants the overlay held go with it.
	if p := entity.PolicyOf(s.Store); p != nil {
		p.DropScope(name)
	}
	if err := s.Store.DeleteScope(name); err != nil {
		http.Error(w, "scope delete: "+err.Error(), http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// cancelScopeTasks cancels every live task a scope owns (docs/reference/scopes.md#what-a-scope-owns):
// what its engine tracks — its rules' tasks and `task run --scope`, wherever they run — and
// what the local runtime hosts under the scope that no engine does. The engine's go without
// their completion hooks, which would act inside the scope being deleted (Engine.CancelLive).
func (s *Server) cancelScopeTasks(scope string, eng *trigger.Engine) {
	reason := "scope " + scope + " was deleted"
	cancelled := map[string]bool{}
	if eng != nil {
		for _, id := range eng.CancelLive(reason) {
			cancelled[id] = true
		}
	}
	for _, inst := range s.Local.Snapshots() {
		if sc, ok := s.Local.ScopeOf(inst.ID); !ok || sc != scope || cancelled[inst.ID] {
			continue
		}
		if t := s.Local.Lookup(inst.ID); t != nil {
			t.Cancel(reason)
		}
	}
}

// scopedStore returns the store view for the request's ?scope (baseline if absent).
// The seam for a sandbox — the logd-backed store isolates the scope (see
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

// scopedStore is the store a request writes through: the scope it named, or baseline, acting as
// the principal the request carried. Every write a handler makes through it is attributed to
// that principal, and every entity one creates is owned by them — which is what makes a raw
// `verse d -put` the same kind of write as a `task run`, with no exception to remember.
func (s *Server) scopedStore(r *http.Request) entity.Store {
	store := s.Store
	if sc := r.URL.Query().Get("scope"); sc != "" {
		store = s.Store.Scope(sc)
	}
	return entity.As(store, principalOf(r))
}

// asDocket is the store the docket's OWN acts go through on a request's behalf, in the
// request's scope: the clock settling an overdue question, the layer rule handing one on when a
// reader arrives or a resolver is removed. Whoever read the docket did not decide those, and a
// write carrying their name would say they did.
func (s *Server) asDocket(r *http.Request) entity.Store {
	return entity.As(s.scopedStore(r), entity.SystemDocket)
}

// actor is who an act that records a `by` is BY: the principal the request is made as, which is
// the one the check admitted. A body may say `by` too — the CLI sends both — and then it names
// the same principal or the request is refused: checked as one party and recorded as another,
// one permitted answerer cast every ballot a threshold needed under three names, the last of
// them `system:docket`, and the record said the system decided.
func actor(w http.ResponseWriter, store entity.Store, by string) (string, bool) {
	who := entity.PrincipalOf(store)
	if who == "" {
		http.Error(w, api.PrincipalHeader+" is required: an act names who made it", http.StatusBadRequest)
		return "", false
	}
	if by == "" || by == who {
		return who, true
	}
	if g, err := entity.ParsePrincipal(by); err == nil && g.String() == who {
		return who, true
	}
	http.Error(w, fmt.Sprintf("by names %s and the request is made as %s: what an act records is who "+
		"made it, so the two are one name (`-as` sets both)", by, who), http.StatusBadRequest)
	return "", false
}

// principalOf is who a request is made by (api.PrincipalHeader), and "" for a request that did
// not say. Past the door it is the principal a verified bearer proved, where the request carried
// one, and otherwise what the request asserted (authenticatedBy says which).
func principalOf(r *http.Request) string { return r.Header.Get(api.PrincipalHeader) }

// queryPattern reads the PAYLOAD half of the selector a read-plane route was given: ?if is a
// tony match over the payload, rooted at the field path in ?if-at (one parameter per segment,
// so a field name is never re-split here).
//
// Which nodes a read is about is the PATH (?at) — see querySelector, which pairs the two halves.
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

// queryPaths reads the listing half of a read's query: whether the caller wants ADDRESSES rather
// than the document, and how far to walk. depth is counted FROM THE ADDRESS the path names — 0
// is that address alone, 1 its children — and absent is everything beneath it, so the same
// number means the same thing at every level.
//
// A DEPTH WITHOUT A LISTING IS REFUSED rather than ignored: it bounds a walk, a document read
// does not walk, and a parameter that quietly does nothing is how a caller comes to believe it
// asked for something it did not.
func queryPaths(q url.Values) (listing bool, depth int, err error) {
	d := q.Get("depth")
	if q.Get("paths") == "" {
		if d != "" {
			return false, 0, fmt.Errorf("?depth=%s without ?paths: a depth bounds a listing, "+
				"and this read answers a document", d)
		}
		return false, entity.Unbounded, nil
	}
	depth = entity.Unbounded
	if d != "" {
		n, err := strconv.Atoi(d)
		if err != nil || n < 0 {
			return false, 0, fmt.Errorf("?depth=%s: a depth is how far BENEATH the address to "+
				"walk — 0 is the address alone — and it is never negative", d)
		}
		depth = n
	}
	return true, depth, nil
}

// selector is what a read-plane route was asked for: a path reaching nodes, and a predicate over
// each node it reached.
//
// It is ONE type because the streaming face and the listing face must name the SAME set —
// `verse watch --if X demo pr '*'` and `verse data --if X demo pr '*'` differ in when they
// answer, not in what they pick. That is not visible by eye, so TestStateAndWatchNameTheSameSet
// says it instead.
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

// matchRef is whether a change's path is one the selector's path reaches.
func (s selector) matchRef(ref entity.Ref) bool { return entity.Reaches(s.at, ref) }

// matchNode is the whole question about a node a read reached, and matchEvent the whole question
// about a change.
func (s selector) matchNode(n *ir.Node) (*ir.Node, bool, error) {
	return n, s.pat.MatchesPayload(n), nil
}

func (s selector) matchEvent(ev entity.Event) bool {
	return s.matchRef(ev.Ref) && s.pat.MatchesChange(ev)
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
	if refused(w, readableAt(store, principalOf(r), sel)) {
		return
	}
	// ABSENT MEANS FROM NOW, and it is not the same as `since=0`. A watch with no cursor is
	// somebody saying "show me what happens", and answering that with a replay of the whole
	// retained log — or, when the store cannot replay from 0, with a REFUSAL telling them to
	// "drop --since" they never passed — is answering a question nobody asked. `since=0` still
	// means from the beginning, for a caller that means it.
	// The client's answer to what a watch on a path holding nothing means: refuse it, as a read
	// of the same path does, or wait for a value to arrive. Refusing is the default because the
	// client here is a person who typed the address.
	waitIfAbsent := q.Get("wait-if-absent") == "true"
	since, catchUp, err := watchCursor(q.Get("since"))
	if err != nil {
		codec.HTTPError(w, r, http.StatusBadRequest, err.Error())
		return
	}
	// `init` opens the stream with what stands at the path, as the substrate's opening state
	// diffed against nothing: a set per node, at the revision the watch opened at. Refused where
	// it cannot mean that rather than dropped — a cursor has already said what comes first, and
	// a watch with no literal prefix is the verse-wide stream, which has no opening state.
	withInit := q.Get("init") == "true"
	if withInit && catchUp {
		codec.HTTPError(w, r, http.StatusBadRequest,
			"init and since both say what comes first — what stands now, or history from a revision; ask one")
		return
	}
	match := sel.matchEvent

	// Subscribe BEFORE the catch-up snapshot, so an event landing between the two is
	// captured live rather than lost in the gap. The catch-up and the live stream
	// overlap; dedup by revision (send only strictly-ascending revs).
	// AT THE PATH when one was named, and at the root only when none was. A watch on a path
	// nothing stands at is refused by the substrate unless the client asked to wait, so a person
	// who typed an address wrong is told so instead of holding a stream that will never carry
	// anything. It also narrows what crosses: observing the root and filtering here means every
	// commit in the verse is delivered to a watch on one slice.
	ch, unsub, err := watchFor(store, sel, waitIfAbsent, withInit)
	if errors.Is(err, errNoOpeningState) {
		codec.HTTPError(w, r, http.StatusBadRequest, err.Error())
		return
	}
	if err != nil {
		// The substrate refuses a watch on a path holding nothing unless the client asked to
		// wait, which is the same answer a read of it gives — so this needs no read of its own
		// to know, and says the same thing `verse data` would about the same address.
		notFoundAt(w, r, sel.at)
		return
	}
	defer unsub()

	// A cursor the store cannot answer from is REFUSED, before NewSSE commits the response.
	//
	// `Since` returns a non-nil error when `since` is below what the store still retains, and it does
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
	// AT THE SAME PATH THE LIVE HALF WATCHES. A catch-up costs what the slice it reads holds —
	// the base document at the starting commit, and every patch in the range that touches it —
	// so a watch on one slice that replayed the WHOLE VERSE paid for the store's size on every
	// request. Both halves take `prefix` now, and a selector that names no path still asks at
	// the root, because that is what it is asking about.
	var replay []entity.Commit
	var replayErr error
	if catchUp {
		replay, replayErr = store.Since(since, sel.at)
	}
	if replayErr != nil {
		// WHAT ACTUALLY HAPPENED, said in the store's own words. This asked a second question
		// to guess with — `store.Since(0)`, whose answer was one int64, the oldest commit still
		// retained — and that probe is gone. It could not work: it is the LONGEST replay the
		// store can be asked for, so on any verse with a history it failed exactly as the
		// cheap one just had, and the handler then asserted "it is not the cursor's age"
		// whatever the cause. Before it learned to say it had failed at all, its zero value
		// went out as a measurement: `the store still has commits from 0`, which is what
		// git-issue 3qf2z59dh12ksdazg9n0 reports seeing. It was also expensive enough to
		// matter — on staging it materialised the whole log, and the substrate was evicted for
		// memory four times in the fifteen minutes somebody spent retrying a refused --since.
		//
		// `Since` answers with its own error now, so the guess is unnecessary: the one failure
		// that means the history is gone says so, and everything else names the store's
		// condition rather than the caller's cursor.
		if errors.Is(replayErr, entity.ErrReplayTooOld) {
			// THE STORE SAYS FROM WHICH COMMIT IT CAN STILL SERVE, in the refusal itself, so
			// the advice names a cursor that works rather than the one just refused. That is
			// what the deleted probe was reaching for and could not reach.
			codec.HTTPError(w, r, http.StatusConflict, fmt.Sprintf(
				"since=%d is below what the store still retains, so that history is gone: %v. "+
					"Ask from the commit it names, or with --ago N for the last N commits, "+
					"which the store clamps to what it kept, or drop --since to stream from now",
				since, replayErr))
			return
		}
		// NOT A CONFLICT. The cursor is fine and the store could not answer — a 409 told a
		// person their cursor was the problem while their verse was unreachable, and they
		// retried the one call that made it worse.
		codec.HTTPError(w, r, http.StatusServiceUnavailable, fmt.Sprintf(
			"the store could not replay from since=%d: %v — the cursor stands, so the same "+
				"request is worth repeating once the store answers again", since, replayErr))
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
	for _, c := range replay {
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

// storeUnreadable reports the store's last failed read, or nil.
//
// A LISTING CANNOT SAY THIS ITSELF: `Store.List` answers a slice, so a store that cannot be read
// answers the empty one, indistinguishable from a verse holding nothing. That is how a store
// with an unreplayable patch in its log presents — `verse data` empty, `verse charter` a
// charter of no rules, every write still reporting success. Asking the store turns it into an
// answer somebody can act on.
func storeUnreadable(st entity.Reader) error {
	if h, ok := st.(interface{ ReadError() error }); ok {
		return h.ReadError()
	}
	return nil
}

// unreadable answers 503 when THIS request's read did not happen, and reports whether it did.
//
// 503 rather than 500: the request is fine and the verse may be fine again in a moment — what
// failed is this server's ability to read its own store, which is what Service Unavailable
// says. The body names the cause, because "cannot read the store" with no cause sends an
// operator back to the log-reading this exists to replace.
//
// IT TAKES THE READ'S OWN ERROR, where it used to ask the store for the last one it remembered.
// That flag is one field shared by every reader — the engine, the sources, and each request in
// flight — so whether a request noticed its own failure depended on who else read in between:
// a neighbour's success hid it and the handler answered 200 with an empty document, and a
// neighbour's failure refused a request whose read was fine. Both directions were wrong at the
// same line (git-issue tyr7f6dhh12ksdd4gnn0), and the window is microseconds, which is why it
// read as `--paths` disagreeing with the document form rather than as a race.
// notFoundAt says nothing stands at the address asked for, in the address the caller wrote —
// which is the whole point of answering it rather than an empty document: a reader learns their
// path is wrong instead of that the verse is empty.
//
// IN THE ASKER'S VIEW, and it says so: what a read answers is what its reader may read, so
// "nothing stands" is a statement about that and not about the store (view.go).
func notFoundAt(w http.ResponseWriter, r *http.Request, at *kpath.KPath) {
	spelled := r.URL.Query().Get("at")
	if spelled == "" {
		spelled = at.String()
	}
	if spelled == "" {
		// The whole verse, on a verse nobody has written to. Naming the address would name
		// nothing, and "the verse is empty" is the fact a reader wants on their first read.
		codec.HTTPError(w, r, http.StatusNotFound, "in your view, the verse is empty: nothing has been written yet")
		return
	}
	codec.HTTPError(w, r, http.StatusNotFound, nothingStands(spelled))
}

func unreadable(w http.ResponseWriter, r *http.Request, err error) bool {
	if err == nil {
		return false
	}
	// A read the check turned away is not a store that could not be read: it is answered,
	// with why, and nothing about the store is wrong.
	if refused(w, err) {
		return true
	}
	codec.HTTPError(w, r, http.StatusServiceUnavailable, fmt.Sprintf(
		"the store could not be read, so this answer would be an empty verse rather than the "+
			"verse: %v — nothing is missing until it reads again, and writes may still be "+
			"appending", err))
	return true
}

// state answers the verse AT A PATH: the node the path reaches in the store, with everything
// beneath it. It is narrowed by the same selector /watch
// streams, so a read and a stream never disagree about what a filter names.
//
// `--if` is asked of each node the path reached — `demo.pr` reaches one, `demo.pr.*` each pr —
// and never prunes inside one it keeps. (`--if-at` says where inside the node the predicate is
// evaluated, which is a different question again.)
//
// A path that reaches nothing is a 404 at every length; a path whose nodes `--if` kept none of is
// `{}`, which is an answer about the filter rather than the path.
func (s *Server) state(w http.ResponseWriter, r *http.Request) {
	sel, err := querySelector(r.URL.Query())
	if err != nil {
		codec.HTTPError(w, r, http.StatusBadRequest, err.Error())
		return
	}
	store := s.scopedStore(r)
	if refused(w, readableAt(store, principalOf(r), sel)) {
		return
	}
	// AT THE LITERAL PREFIX, which is what the substrate routes on: a read of one registration
	// costs what that registration holds, not what the verse does. The rest of the path — from
	// its first wildcard, index or key — is answered over the node that came back.
	prefix, rest := entity.LiteralPrefix(sel.at)
	// A LISTING IS ANSWERED BY THE STORE, which knows the names without reading the nodes: it
	// walks its own levels and answers addresses, so listing a wide container costs the walk
	// rather than the container — and answers one no read could return at all, being past the
	// read budget. That is every listing: a place, a path with a wildcard or a descent, and one
	// with a predicate, each handed over with the depth as it was given. The depth is the
	// store's own, so nothing here counts segments, and a path with more than one `..` means
	// what logd says it means.
	if listing, depth, err := queryPaths(r.URL.Query()); err != nil {
		codec.HTTPError(w, r, http.StatusBadRequest, err.Error())
		return
	} else if listing && rest == nil && sel.pat.Predicate == nil {
		out, err := store.PathsAt(prefix, depth)
		if unreadable(w, r, err) {
			return
		}
		// Asked without reading it: the place listed is as often as not a container.
		if ok, err := store.StandsAt(prefix); unreadable(w, r, err) {
			return
		} else if !ok {
			// A listing of a path nothing stands at is not an empty listing: `--paths | wc -l`
			// answering 0 must never be how a reader learns the address was wrong.
			notFoundAt(w, r, sel.at)
			return
		}
		writeOut(w, r, http.StatusOK, entity.PathsDoc(out))
		return
	} else if listing {
		out, reached, err := store.PathsMatching(sel.at, depth, sel.pat.StorePattern(), nil)
		if unreadable(w, r, err) {
			return
		}
		// A PATH THAT REACHES NOTHING NAMES NOTHING, and one that reached nodes `--if` kept none
		// of is an answer about the filter: the empty listing.
		if !reached {
			notFoundAt(w, r, sel.at)
			return
		}
		writeOut(w, r, http.StatusOK, entity.PathsDoc(out))
		return
	}
	// What is left reads the node: a read that wants the document.
	node, ok, err := store.ReadAt(prefix)
	if unreadable(w, r, err) {
		return
	}
	if !ok {
		notFoundAt(w, r, sel.at)
		return
	}
	var keep func(*ir.Node) (*ir.Node, bool, error)
	if sel.pat.Predicate != nil {
		keep = sel.matchNode
	}
	out, reached, err := entity.Reach(node, rest, keep)
	if err != nil {
		codec.HTTPError(w, r, http.StatusBadRequest, err.Error())
		return
	}
	// A PATH THAT REACHES NOTHING NAMES NOTHING — a wildcard over a node with no children
	// included — and a path that reached nodes `--if` kept none of is an answer about the
	// filter: the empty document.
	if !reached {
		notFoundAt(w, r, sel.at)
		return
	}
	if out == nil {
		out = ir.FromMap(map[string]*ir.Node{})
	}
	writeOut(w, r, http.StatusOK, entity.Doc{Node: out})
}

// errNoOpeningState refuses `init` on a watch that has no opening state to give.
var errNoOpeningState = errors.New("init needs a path with a literal prefix: without one the " +
	"watch joins the verse-wide stream, which has no opening state")

// watchFor opens the narrowest watch the selector allows: at its literal prefix, reporting at the
// depth the path names, or at the root when it names no prefix. withInit opens it with what
// stands at the path, which only the watch at a prefix has.
func watchFor(store entity.Store, sel selector, waitIfAbsent, withInit bool) (<-chan entity.Commit, func(), error) {
	prefix, _ := entity.LiteralPrefix(sel.at)
	narrow, hasNarrow := store.(interface {
		WatchPattern([]string, *kpath.KPath, bool, bool) (<-chan entity.Commit, func(), error)
	})
	if len(prefix) == 0 || !hasNarrow {
		if withInit {
			return nil, nil, errNoOpeningState
		}
		ch, unsub := store.Observe(sel.at)
		return ch, unsub, nil
	}
	// WITH THE PATH ITSELF: what is reported is what the path reaches, whether that is one
	// depth or — under a `..` — every depth beneath it.
	return narrow.WatchPattern(prefix, sel.at, waitIfAbsent, !withInit)
}

func (s *Server) getEntity(w http.ResponseWriter, r *http.Request) {
	ref, err := addressAt(r.URL.Query().Get("at"))
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	e, ok, err := s.scopedStore(r).Get(ref)
	if refused(w, err) {
		return // may not be read, which is neither "absent" nor "the store is down"
	}
	if err != nil {
		// A read that could not be performed is NOT a 404. Reporting it as one tells the
		// caller the entity does not exist, which is a different — and actionable —
		// statement from "the store is unreachable", and the caller acts on it: an agent
		// creates the file it was told is missing. 502: verse is up, its store is not.
		http.Error(w, "entity read failed: "+err.Error(), http.StatusBadGateway)
		return
	}
	if !ok {
		codec.HTTPError(w, r, http.StatusNotFound, nothingStands(ref.String()))
		return
	}
	// The entity itself. Its Ref carries MarshalText, so it still renders as the address
	// string this always sent — and the route table's `verse-entity` now names the type
	// actually written rather than a map that happened to match it.
	writeOut(w, r, http.StatusOK, e)
}

// deleteEntity removes whatever stands at `?at=` — an entity, or a place inside one, the
// address saying which by how deep it goes. Removing what is not there is not an error: the
// request says what should stand there afterwards, and nothing already does.
// reservedWrite refuses a client write into verse's own half of the tree.
//
// The entity routes are the GENERIC write, and before the boundary had a name anyone who could
// reach them could write a rule, stamp a task record, or answer a question on verse's behalf —
// with no route saying no, because there was nothing in an address to say no about. Now there
// is: everything under `verse` is what it writes about its own running, and it is written by the engine,
// not through here.
//
// It is not a lock on the DATA. A reader still reads it (that is the point of it being
// entities), the engine still writes it, and a scope's own copy still layers over it. What is
// refused is one path — the generic write — because every legitimate change to verse's own
// state has a verb: `verse c -a`, `verse resolver add`, `verse question approve`. A ballot
// is not among them, deliberately: `vote.*` is the world's half, since casting one is an act
// somebody takes rather than a record verse keeps.
func reservedWrite(ref entity.Ref) error {
	if !ref.Reserved() {
		return nil
	}
	return fmt.Errorf("%s is verse's own: the entity write is for what the world is about, and "+
		"what verse writes about its own running is written by the verbs that own it "+
		"(charter -a, resolver add, runner add, source add, question approve)", ref)
}

func (s *Server) deleteEntity(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	ref, err := addressAt(q.Get("at"))
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if err := reservedWrite(ref); err != nil {
		http.Error(w, err.Error(), http.StatusForbidden)
		return
	}
	if _, _, err := s.scopedStore(r).Write(entity.Write{
		At: entity.Path{Ref: ref, Access: q.Get("sub")},
	}); refused(w, err) {
		return
	} else if err != nil {
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
	ref, err := addressAt(body.At)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if err := reservedWrite(ref); err != nil {
		http.Error(w, err.Error(), http.StatusForbidden)
		return
	}
	when, err := conditions(body.When)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	var grants []entity.Grant
	if body.Grants != nil {
		if grants, err = grantsAt(ref, body.Grants); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
	}
	store := s.scopedStore(r)
	committed, _, err := store.Write(entity.Write{
		At: entity.Path{Ref: ref, Access: body.Sub}, Value: body.Payload, When: when, Grants: grants,
	})
	// GRANTS ARE GIVEN AT A CREATION: a node that stands is 409 beside a refused condition, and
	// grants nobody could give there are the caller's to fix.
	if errors.Is(err, entity.ErrStands) {
		http.Error(w, err.Error(), http.StatusConflict)
		return
	}
	if errors.Is(err, entity.ErrBadGrants) {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	// A WRITE THAT WOULD REPLACE WHAT IT DESCENDS THROUGH IS THE CALLER'S, not the gateway's:
	// nothing failed downstream and a retry changes nothing, so it answers 409 beside the
	// refused-precondition case rather than 502 (git-issue mbjpdr6sh12ksa5zgsn0).
	if errors.Is(err, entity.ErrNotDescendable) {
		http.Error(w, err.Error(), http.StatusConflict)
		return
	}
	if refused(w, err) {
		return
	}
	if err != nil {
		http.Error(w, "entity write failed: "+err.Error(), http.StatusBadGateway)
		return
	}
	if !committed {
		http.Error(w, "write refused: a condition did not hold", http.StatusConflict)
		return
	}
	// The ENTITY as it now stands, which is what makes a merge visible: a write that named one
	// field answers with the whole thing, so a caller sees what it did not send. The address it
	// answers about is the one the write named — a path of any depth, now that a client can say
	// one — rather than a three-segment ancestor of it.
	e, ok, err := store.Get(ref)
	if err != nil || !ok {
		w.WriteHeader(http.StatusNoContent) // it landed; we just cannot show it back
		return
	}
	writeOut(w, r, http.StatusOK, e)
}

// putGroup writes a group — its maker's, by the seed's grant beneath verse.principal.group —
// or replaces the members of one its requester owns. A member is a grantee in the grammar — a
// principal, `<kind>:*`, `*`, `group:<name>` or a place acting — stored as the grammar spells
// it; membership is read when it is checked, so a group may name a member nobody has met.
func (s *Server) putGroup(w http.ResponseWriter, r *http.Request) {
	var body api.Group
	if err := codec.Read(r, &body); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if body.Name == "" {
		http.Error(w, "a group has a name", http.StatusBadRequest)
		return
	}
	if g, err := entity.ParseGrantee(entity.GroupKind + ":" + body.Name); err != nil || g.Kind != entity.GroupKind {
		http.Error(w, fmt.Sprintf("group %q: a group's name is what follows group: in a grantee, and "+
			"holds no space or `*`", body.Name), http.StatusBadRequest)
		return
	}
	members := make([]any, 0, len(body.Members))
	for _, m := range body.Members {
		g, err := entity.ParseGrantee(m)
		if err != nil {
			http.Error(w, "member "+err.Error(), http.StatusBadRequest)
			return
		}
		members = append(members, g.String())
	}
	ref := entity.GroupRef(body.Name)
	_, _, err := s.scopedStore(r).Write(entity.Write{At: ref.At(), Value: entity.Obj{entity.MembersField: members}.Payload()})
	if refused(w, err) {
		return
	} else if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	writeOut(w, r, http.StatusCreated, map[string]any{"at": ref.String(), "members": members})
}

func (s *Server) listGroups(w http.ResponseWriter, r *http.Request) {
	store := s.scopedStore(r)
	ents, err := entity.BelowErr(store, func(ref entity.Ref) bool { return entity.IsGroup(ref) },
		1, entity.VerseRoot, entity.PrincipalSystem, entity.GroupKind)
	if unreadable(w, r, err) {
		return
	}
	out := make([]map[string]any, 0, len(ents))
	for _, e := range ents {
		out = append(out, map[string]any{"at": e.Ref.String(), "name": e.Ref.Name(), "members": entity.Strs(e.Payload, entity.MembersField)})
	}
	writeOut(w, r, http.StatusOK, out)
}

func (s *Server) getGroup(w http.ResponseWriter, r *http.Request) {
	ref := entity.GroupRef(r.PathValue("name"))
	e, ok, err := s.scopedStore(r).Get(ref)
	if unreadable(w, r, err) {
		return
	}
	if !ok {
		http.NotFound(w, r)
		return
	}
	writeOut(w, r, http.StatusOK, map[string]any{"at": ref.String(), "name": ref.Name(), "members": entity.Strs(e.Payload, entity.MembersField)})
}

func (s *Server) removeGroup(w http.ResponseWriter, r *http.Request) {
	ref := entity.GroupRef(r.PathValue("name"))
	if _, _, err := s.scopedStore(r).Write(entity.Write{At: ref.At()}); refused(w, err) {
		return
	} else if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// getPerms answers a node's perms record to somebody who may READ the node: who owns what you
// can read, and what it holds out, is yours to know, the way a file you can read shows its
// mode. Nobody reads the mirror as a whole; setting a record is its owner's (putPerms).
func (s *Server) getPerms(w http.ResponseWriter, r *http.Request) {
	ref, err := addressOrRoot(r.URL.Query().Get("at"))
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if refused(w, entity.Check(s.scopedStore(r), principalOf(r), entity.Read, ref, "reading its record")) {
		return
	}
	s.answerPerms(w, r, ref)
}

// listPerms is bare `verse perms`: every record whose node the requester may read, in address
// order — who owns what they can see, and what each place holds out.
//
// THE MIRROR IS LISTED AS THE SYSTEM and filtered here, node by node, with the same check
// getPerms makes for one: the mirror itself is the system's with nothing held out on it, so
// listed through the request's own view it would answer nothing, and a person reads their
// records through this verb rather than through `verse data verse perms` for exactly that
// reason. The check per record costs a walk through the cache and no read.
func (s *Server) listPerms(w http.ResponseWriter, r *http.Request) {
	ents, err := entity.BelowErr(entity.As(s.scopedStore(r), entity.SystemPerms), nil, 1,
		entity.VerseRoot, entity.PermsSystem, entity.PermsKind)
	if unreadable(w, r, err) {
		return
	}
	store, who := s.scopedStore(r), principalOf(r)
	out := make([]api.PermsRecord, 0, len(ents)+1)
	// THE ROOT'S FIRST, since it is what every other record hangs from — and it stands beside
	// the mirror rather than in it, so the listing above cannot carry it.
	if e, ok, err := entity.As(store, entity.SystemPerms).Get(entity.PermsRef(entity.RefAt())); err == nil && ok {
		if entity.Check(store, who, entity.Read, entity.RefAt(), "reading its record") == nil {
			out = append(out, permsRecordOf(entity.RefAt(), e.Payload))
		}
	}
	for _, e := range ents {
		ref, err := entity.ParseAddr(e.Ref.Name())
		if err != nil {
			continue // a record keyed by something that is not an address is nobody's to see
		}
		if entity.Check(store, who, entity.Read, ref, "reading its record") != nil {
			continue
		}
		out = append(out, permsRecordOf(ref, e.Payload))
	}
	sort.Slice(out, func(i, j int) bool { return out[i].At < out[j].At })
	writeOut(w, r, http.StatusOK, out)
}

// putPerms takes a place and sets what it holds out. The requester must hold `owner` at the
// node already: this is a place being taken by whoever may take it, never a way to take one
// from its owner — and a record already somebody else's refuses the edit the same way.
func (s *Server) putPerms(w http.ResponseWriter, r *http.Request) {
	var body api.PermsEdit
	if err := codec.Read(r, &body); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	ref, err := addressOrRoot(body.At)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	who := principalOf(r)
	if who == "" {
		http.Error(w, "a place is taken by somebody: set `user:` in the CLI's config, or --as", http.StatusBadRequest)
		return
	}
	// What the edit would hand down is read before the place is taken, so a refused one takes
	// nothing.
	var childLevels entity.Levels
	if body.ChildGrants != nil && ir.Uncomment(body.ChildGrants).Type != ir.NullType {
		var perr error
		if childLevels, _, perr = entity.ParseLevels(entity.Obj{entity.ChildGrantsField: body.ChildGrants}.Payload(), entity.ChildGrantsField); perr != nil {
			http.Error(w, perr.Error(), http.StatusBadRequest)
			return
		}
		if childLevels == nil {
			childLevels = entity.Levels{}
		}
		if err := entity.CheckChildGrants(childLevels); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
	}
	store := s.scopedStore(r)
	if refused(w, entity.CheckTake(store, who, ref, "setting its record")) {
		return
	}
	if err := entity.Claim(store, ref, who); refused(w, err) {
		return
	} else if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	// Taking the place may have found a record standing that is somebody else's; the check
	// above passed on co-ownership, so they are a co-owner of it and may edit it. Owner
	// records never move: what the edit touches is what the node holds out.
	if body.ChildGrants != nil {
		if err := entity.SetChildGrants(store, ref, childLevels); err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
	}
	if body.Grants != nil {
		gs, err := grantsAt(ref, body.Grants)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if err := entity.SetGrants(store, ref, gs); err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
	}
	if body.Grant != nil {
		levels, _, err := entity.ParseLevels(entity.Obj{"grant": body.Grant}.Payload(), "grant")
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if len(levels) == 0 {
			http.Error(w, "a grant holds out a level: read, read-write, "+entity.ChildOwnerKey+" or control", http.StatusBadRequest)
			return
		}
		for _, g := range entity.GrantsOf(ref.Segs(), body.Below, levels) {
			if err := entity.AddGrant(store, ref, g); err != nil {
				http.Error(w, err.Error(), http.StatusBadGateway)
				return
			}
		}
	}
	if body.Revoke != nil {
		levels, _, err := entity.ParseLevels(entity.Obj{"revoke": body.Revoke}.Payload(), "revoke")
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if len(levels) == 0 {
			http.Error(w, "a revoke names grantees by level: {read: [who]}", http.StatusBadRequest)
			return
		}
		if err := entity.Revoke(store, ref, levels); err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
	}
	s.answerPerms(w, r, ref)
}

// grantsAt reads a `--grants` list as the grants of the record at ref: what an owner sets on
// their node's record (putPerms), and what a creator gives the node they create (putEntity) —
// one reading, so a creator may say exactly what an owner may. `[]` is a list holding nothing,
// never the absence that would mean the default.
func grantsAt(ref entity.Ref, list entity.Payload) ([]entity.Grant, error) {
	gs, err := entity.ParseGrants(entity.SegsOf(ref), entity.Obj{entity.GrantsField: list}.Payload())
	if err != nil {
		return nil, err
	}
	if gs == nil {
		gs = []entity.Grant{}
	}
	return gs, nil
}

// answerPerms writes a node's record back, read as the system since the mirror is not the
// requester's to read; the caller has established that the requester owns the node.
//
// THROUGH THE SCOPE THE EDIT WENT THROUGH. A scope's mirror is its own, layered as the store is,
// so a record written in a scope stands in the overlay — and reading it back from baseline
// answered 404 for a record that had just been written, or worse, baseline's older one.
func (s *Server) answerPerms(w http.ResponseWriter, r *http.Request, ref entity.Ref) {
	rec, ok, err := entity.As(s.scopedStore(r), entity.SystemPerms).Get(entity.PermsRef(ref))
	if unreadable(w, r, err) {
		return
	}
	if !ok {
		codec.HTTPError(w, r, http.StatusNotFound, fmt.Sprintf("%s has no record of its own: it is "+
			"owned by whoever owns the node above it, and holds out what that node does", ref))
		return
	}
	writeOut(w, r, http.StatusOK, permsRecordOf(ref, rec.Payload))
}

// permsRecordOf is a record as the surface shows it. The grants are spelled for the node they
// are on — a line about the node itself says no `at` — whatever spelling the store holds, so
// a record written before that was the spelling reads the same as one written after; a
// grants field that will not parse is shown as it is, since what is wrong with it is the
// thing to see.
func permsRecordOf(ref entity.Ref, payload entity.Payload) api.PermsRecord {
	grants := entity.Get(payload, entity.GrantsField)
	if gs, err := entity.ParseGrants(entity.SegsOf(ref), payload); err == nil && grants != nil {
		grants = entity.GrantsPayload(ref.Segs(), gs)
	}
	return api.PermsRecord{
		At: ref.String(), Owner: entity.Str(payload, entity.OwnerField),
		Grants: grants, ChildGrants: entity.Get(payload, entity.ChildGrantsField),
	}
}

// readableAt is the check a listing or a watch AT a path makes before reading beneath it: the
// path itself must be the reader's to read, as it would be for a Get. A listing at a readable
// path then leaves out what may not be seen beneath it; a listing at a path that is not
// readable at all is refused, and says so, rather than answered empty. A selector naming no
// path reads at the root, which is everyone's.
func readableAt(store entity.Store, principal string, sel selector) error {
	prefix, _ := entity.LiteralPrefix(sel.at)
	if len(prefix) == 0 {
		return nil
	}
	return entity.CheckInto(store, principal, entity.RefAt(prefix...))
}

// refused answers a write the permission check turned away: 403, with the check's own words,
// which name who wanted what and the record that decided. It is the caller's answer and not
// the store's — nothing failed downstream and a retry changes nothing — so it is not the 502
// a store that could not be reached gets.
func refused(w http.ResponseWriter, err error) bool {
	if !errors.Is(err, entity.ErrForbidden) {
		return false
	}
	http.Error(w, err.Error(), http.StatusForbidden)
	return true
}

// addressOrRoot is addressAt where the ROOT is a legal answer: the empty path names it, and the
// perms routes are where a caller says so — the top's own record is set like any other node's.
// The entity routes keep addressAt, since a write with no address is a write of the whole
// document and is a typo far more often than it is a request.
func addressOrRoot(at string) (entity.Ref, error) {
	if at == "" {
		return entity.RefAt(), nil
	}
	return addressAt(at)
}

// addressAt reads an address off the wire: one kinded path, at whatever depth, as the segments
// it stands at.
//
// PathSegs is the reader rather than a dot split, because the quoting is what makes the path a
// spelling at all — an id may hold a dot (`demo.pr."a.b"`), and splitting would make that entity
// two. It refuses a WILDCARD, which is right for every caller here: this is where something is
// read or written, and a `*` names no one place.
func addressAt(at string) (entity.Ref, error) {
	segs, err := entity.PathSegs(at)
	if err != nil {
		return entity.Ref{}, fmt.Errorf("address %q: %w", at, err)
	}
	ref, ok := entity.RefOfAny(segs)
	if !ok {
		return entity.Ref{}, fmt.Errorf("address %q does not name a place: a segment is a name, "+
			"and none of them may be empty", at)
	}
	return ref, nil
}

// conditions maps the wire's preconditions onto the store's. A condition that asks nothing
// is refused rather than silently dropped: sending one means intending a guard, and a guard
// that is quietly not applied is worse than a rejected request.
func conditions(cs []api.Condition) ([]entity.Require, error) {
	out := make([]entity.Require, 0, len(cs))
	for _, c := range cs {
		var at entity.Path
		if c.At != "" {
			ref, err := addressAt(c.At)
			if err != nil {
				return nil, fmt.Errorf("condition: %w", err)
			}
			at = ref.At()
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

// listDocket is the unified pending-gate view: engine (trigger) gates raised by a firing AND
// per-task gates (an agent blocked on write/shell/delegate). Both are published as
// `gate:question:<id>` records, and both are resolvable by id.
func (s *Server) listDocket(w http.ResponseWriter, r *http.Request) {
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
	out := make([]map[string]any, 0)
	ents, readErr := pendingQuestionsErr(s.scopedStore(r))
	if s.settleOverdueIn(s.asDocket(r), ents) {
		ents, readErr = pendingQuestionsErr(s.scopedStore(r))
	}
	// WHAT A RESOLVER SERVES FOR YOU is its to answer, so the docket leaves it out; `?served`
	// puts it back, saying which resolver serves it (9ce3bva0, stage 5).
	withServed := r.URL.Query().Get("served") == "true" || r.URL.Query().Get("all") == "true"
	// THE QUESTIONS PUT TO YOU, with `?waiting=me`: what waits on the caller rather than every
	// pending ask they may read, which is the whole docket (`verse question` against `--all`).
	waiting := r.URL.Query().Get("waiting") == "me"
	for _, ent := range ents {
		if waiting && !waitsOn(s.scopedStore(r), principalOf(r), ent.Payload) {
			continue
		}
		servedBy, err := s.servedFor(principalOf(r), ent.Payload)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		if servedBy != "" && !withServed {
			continue
		}
		if entity.Str(ent.Payload, mirror.SaidToField) != "" {
			continue // words to a task, which asks no person
		}
		p := entity.ToObj(ent.Payload)
		rec := map[string]any{}
		if servedBy != "" {
			rec["servedBy"] = servedBy
		}
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
	// A FAILED READ IS NOT AN EMPTY LIST. List answers nil when the read did not happen, so
	// without this a store that could not be read renders as "nothing is installed" — and
	// `verse io` answered `[]` on a verse holding three sources, which is the one shape an
	// operator acts on. Same guard /state has carried all along, for the same reason.
	if unreadable(w, r, readErr) {
		return
	}
	writeOut(w, r, http.StatusOK, out)
}

// waitsOn reports whether a question waits on who: its waitingOn names them as the answer line it
// was copied from would (entity.Names) — themselves, a whole kind, everyone, a place they may
// write — and it is not their own ask, which its asker answers only where they named themselves
// (askedBy). A line that cannot be read waits on nobody here, as the docket's other readers
// would take it.
func waitsOn(store entity.Store, who string, q entity.Payload) bool {
	names := entity.Strs(q, mirror.WaitingOnField)
	if s := entity.Str(q, mirror.WaitingOnField); s != "" {
		names = append(names, s)
	}
	if len(names) == 0 || askedBy(q, who) {
		return false
	}
	ok, err := entity.Names(store, names, entity.ReadWrite, who)
	return err == nil && ok
}

// relaysControl reports whether an answer to this task's gates is QUEUED for a worker
// rather than applied. A local task's registry takes it on the spot and a remote runtime
// takes it over the wire that carried it; a broker holds it until somebody dials in.
func (s *Server) relaysControl(taskID string) bool {
	if taskID == "" {
		return false // a firing's gate: the registry answers it here, now
	}
	if _, ok := s.Local.SnapshotOf(taskID); ok {
		return false
	}
	// The same reach the answer itself will take, so this cannot say "applied" about a task
	// the answer is then queued for: whether control is relayed is a property of the runtime
	// HOLDING the task, and a broker holds one it was named for without being the default.
	if h := s.attach(taskID); h != nil {
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
// One selector, because `verse question --all` and `verse status` must name the same set. They did not
// before — status counted the engine registry, which never held a task's questions and
// holds nothing at all after a restart, so it could report no gates beside a docket listing
// several.
func pendingQuestions(store entity.Store) []entity.Entity {
	out, _ := pendingQuestionsErr(store)
	return out
}

// pendingQuestionsErr is the same read, reporting the error IT failed with — see entity.ListErr.
func pendingQuestionsErr(store entity.Store) ([]entity.Entity, error) {
	var out []entity.Entity
	// At the questions subtree. This is on the READINESS path (`/status` reports the count),
	// and a List(pred) is a Match on the root — so a probe every five seconds read the entire
	// verse to count what was pending.
	ents, err := entity.BelowErr(store, mirror.IsQuestion, 1,
		entity.VerseRoot, mirror.AskSystem)
	if err != nil {
		return nil, err
	}
	for _, ent := range ents {
		if p := entity.ToObj(ent.Payload); p != nil && p["status"] == mirror.QuestionPending {
			out = append(out, ent)
		}
	}
	return out, nil
}

func (s *Server) answerAsk(w http.ResponseWriter, r *http.Request) {
	var body api.Resolve
	if err := codec.Read(r, &body); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if !pathID(w, r, &body.ID) {
		return
	}
	s.answerQuestion(w, r, body, "", s.scopedStore(r))
}

// pathID puts the ask the path names into a body's id, and refuses a body naming another: the path
// is which ask a request is about.
func pathID(w http.ResponseWriter, r *http.Request, id *string) bool {
	path := r.PathValue("id")
	if *id != "" && *id != path {
		http.Error(w, fmt.Sprintf("the path names ask %s and the body %s", path, *id), http.StatusBadRequest)
		return false
	}
	*id = path
	return true
}

// getAsk is one ask, read as its record is (getEntity): what the caller may not read is refused,
// and what does not stand is 404.
func (s *Server) getAsk(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	q.Set("at", mirror.AskRef(r.PathValue("id")).KPath())
	r.URL.RawQuery = q.Encode()
	s.getEntity(w, r)
}

// listAsks is GET /asks, as each side reads the asks. Bare, the caller's own, whatever became of
// them (`verse ask`); `?waiting=me`, the questions waiting on them (`verse question`); `?all=true`,
// every pending ask they may read, the docket (`verse question --all`).
func (s *Server) listAsks(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	waiting, all := q.Get("waiting"), q.Get("all") == "true"
	switch {
	case waiting != "" && waiting != "me":
		http.Error(w, "waiting names whom the questions wait on, and only `me` is asked for", http.StatusBadRequest)
		return
	case waiting != "" && all:
		http.Error(w, "waiting=me and all=true are two listings; ask for one", http.StatusBadRequest)
		return
	case waiting != "" || all:
		s.listDocket(w, r)
		return
	}
	who := principalOf(r)
	ents, err := entity.BelowErr(s.scopedStore(r), mirror.IsAsk, 1, entity.VerseRoot, mirror.AskSystem)
	if unreadable(w, r, err) {
		return
	}
	out := make([]map[string]any, 0)
	for _, e := range ents {
		if entity.Str(e.Payload, "asker") != who {
			continue
		}
		rec := map[string]any(entity.ToObj(e.Payload))
		rec["id"] = e.Ref.Name()
		out = append(out, rec)
	}
	writeOut(w, r, http.StatusOK, out)
}

// sayOnAsk records what somebody said ABOUT a question, answering nothing.
//
// Admitted to whoever may READ the question, which is wider than who may answer it on purpose: the
// person who cannot decide this is often the one who knows why it is waiting, and a docket that
// took words only from its answerers would lose them. The words are written by versed, as the
// docket, so a reader who holds no write on the question can still leave a line on it — and cannot
// write anything else there.
func (s *Server) sayOnAsk(w http.ResponseWriter, r *http.Request) {
	var body api.GateSay
	if err := codec.Read(r, &body); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if !pathID(w, r, &body.ID) {
		return
	}
	if strings.TrimSpace(body.Say) == "" {
		http.Error(w, "say is required: a say with no words is nothing said", http.StatusBadRequest)
		return
	}
	store := s.scopedStore(r)
	var ok bool
	if body.By, ok = actor(w, store, body.By); !ok {
		return
	}
	// Read as the sayer, which is the permission check: a question they cannot see is one they
	// cannot leave a line on, and the refusal is the store's own words.
	if _, found, err := store.Get(mirror.QuestionRef(body.ID)); err != nil {
		codec.HTTPError(w, r, http.StatusForbidden, err.Error())
		return
	} else if !found {
		http.Error(w, "no question "+body.ID+" stands", http.StatusNotFound)
		return
	}
	mirror.SayOn(s.asDocket(r), body.ID, body.By, body.Say)
	w.WriteHeader(http.StatusNoContent)
}

// posed reads a question, and says whether it is POSED: its own perms record stands, which says
// who may answer it, and so does the question, which says what may be chosen and how many it
// takes. Until both do, an answer is checked against the docket's row — every person — and
// tallied with the default two options at one apiece, so a stranger's single ballot settles a
// gate that named five people and needed three of them. The record is written first
// (mirror.PostQuestion), so this asks the record and then the question, and waits a moment for a
// question somebody is known to have raised: the gap is two writes wide, and whoever answers
// inside it is better served by a wait than by a refusal to retry.
//
// WHAT THE RECORD SAYS, not what a registry says about it. Who raised a question and how — the
// engine's registry, a hosted task's own, a worker's frame — decides nothing here: every one of
// them poses through the mirror, and a question none of them has finished posing is not
// answerable, whatever it would have restricted.
//
// raised is whether anybody is known to be asking it, so that an id nobody raised is told so at
// once rather than after the wait.
func (s *Server) posed(store entity.Store, id string, raised func() bool) (q entity.Entity, posed bool, err error) {
	ref := mirror.QuestionRef(id)
	// AND IT STANDS PENDING, where somebody is waiting on it. A question asked again under the
	// id it had — one withdrawn and asked again, or a poser posting over its own id — has a
	// record standing from the asking before until this one lands: settled, with that asking's
	// options. Answered against those, one ballot releases a waiter this asking says needs
	// three. A settled question nobody is waiting on is simply answered already, and is told so
	// below.
	read := func() (entity.Entity, bool, error) {
		if ok, err := entity.Recorded(store, ref); err != nil || !ok {
			return entity.Entity{}, false, err
		}
		q, ok, err := store.Get(ref)
		if err != nil || !ok {
			return q, false, err
		}
		if entity.Str(q.Payload, "status") != mirror.QuestionPending && raised() {
			return q, false, nil
		}
		return q, true, nil
	}
	if q, posed, err = read(); err != nil || posed || !raised() {
		return q, posed, err
	}
	deadline := time.Now().Add(2 * time.Second)
	for !posed && err == nil && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
		q, posed, err = read()
	}
	return q, posed, err
}

// answerQuestion is the ONE answering path. Both routes reach it — `/asks/{id}/answer`, which
// finds the owner from the id, and `/tasks/{id}/resolve`, which is told it — because the
// two differ in how the question is addressed and in nothing else. Having had two
// implementations is how `decisionId`/`proceed` drifted from `id`/`approve` unnoticed, and an
// answer's attribution and its record are exactly the things that must not drift.
//
// taskID is the task the answer is addressed to, empty when the caller did not say and the
// id must be read for it.
//
// store is the view the answerer acts through: the request's principal, or a resolver answering
// for a party it serves (answeringFor), which then answers as that party. Whether they may answer
// is asked of the store BEFORE the answer is delivered, since the record is written only
// afterwards and a refusal then would be an answer that reached the work with no record of it.
// The answer is theirs to give when they co-own the question — the task's owner, or the
// charter's — or hold `read-write` on it.
func (s *Server) answerQuestion(w http.ResponseWriter, r *http.Request, body api.Resolve, taskID string, store entity.Store) {
	if body.ID == "" {
		http.Error(w, "id is required: an answer names the question it answers", http.StatusBadRequest)
		return
	}
	// Attribution is required, not defaulted: a default is a name on the record that belongs to
	// nobody, and an answer is a durable entry on the question, so an unattributable one is a
	// permanent unattributable one. A decision is an act; acts have actors.
	//
	// AND THE ACTOR IS WHOEVER THE REQUEST IS MADE AS — the person, or the resolver a verdict
	// came through — which is who the check below admits (actor).
	var ok bool
	if body.By, ok = actor(w, store, body.By); !ok {
		return
	}
	if body.Choice == "" {
		http.Error(w, "choice is required: an answer names the option it chooses", http.StatusBadRequest)
		return
	}
	res := task.Choose(body.Choice, body.By)
	res.Steer = body.Say
	res.Edit = body.Edit
	// How the answerer was authenticated, from the door (auth.go): what a ballot guarantees is
	// no more than the name on it, so it says how that name was known — and through which io,
	// where a trusted io relayed them (iod.go).
	res.Authenticated = authenticatedBy(r)
	res.Via = relayedBy(r)

	// THE SCOPE'S OWN, first. The awaiter is delivered to out-of-band and the record is written
	// through `store` afterwards, and the two are in different scopes the moment a question is
	// answered from a scope that did not pose it: delivered to baseline's awaiter, recorded as
	// answered in the overlay, baseline's record still pending. A scope owns what it creates
	// (owns.go), so that answer is refused naming whose the question is.
	holder, found, err := s.questionScope(body.ID, r.URL.Query().Get("scope"))
	if !s.ownHere(w, r, "question "+body.ID, holder, found, err) {
		return
	}
	// NOT UNTIL IT IS POSED (posed): a question is answered against its own record and its own
	// options, and one that has neither yet is not answered against the docket's row and the
	// default two.
	// Whether anybody is WAITING on this id now: the engine's registry, or a task holding it as
	// a pending gate. Not whether the id looks like a task's — every id on the task route does,
	// and a question nobody raised would be waited for and then called "still being posed".
	raised := func() bool {
		for _, p := range s.Reg.Pending() {
			if p.Question.ID == body.ID {
				return true
			}
		}
		return s.taskHoldingGate(body.ID) != ""
	}
	q, found, err := s.posed(store, body.ID, raised)
	if unreadable(w, r, err) {
		return
	}
	if !found {
		if !raised() {
			http.Error(w, "no such question", http.StatusNotFound)
			return
		}
		http.Error(w, "question "+body.ID+" is still being posed: it was raised a moment ago, and "+
			"who may answer it and how many it takes are not written down yet — ask again",
			http.StatusServiceUnavailable)
		return
	}
	// A RESOLVER ANSWERS FOR A PARTY (9ce3bva0, stage 4): made as a resolver with a match, the
	// answer is the party's — whoever it serves the ask for, as they granted it — and is held
	// from here on to everything that party's own answer would be. The ballot names both.
	if party, acting, err := s.answeringFor(entity.PrincipalOf(store), q.Payload, body.For); err != nil {
		http.Error(w, fmt.Sprintf("%s: %v", body.ID, err), http.StatusForbidden)
		return
	} else if acting {
		res.Via = body.By
		body.By, res.By = party, party
		// The party authenticated nothing: the resolver answered for them, and `via` says so.
		res.Authenticated = ""
		// And made on their authority from here: the ballot and the answer are the party's, so
		// what the resolver may not write — it reads the ask, and no more — they may.
		store = entity.As(store, party)
	} else if body.For != "" {
		http.Error(w, "`for` names whom a resolver answers for, and this answer is not made as one", http.StatusBadRequest)
		return
	} else if !body.Force {
		// THE PARTY CHOSE ITS RESOLVER (9ce3bva0, stage 5): answering as yourself what one
		// serves for you is said, and done only when you say so.
		if by, err := s.servedFor(body.By, q.Payload); err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		} else if by != "" {
			http.Error(w, fmt.Sprintf("%s answers %s for you; answer with force (--force) to answer "+
				"it yourself", by, body.ID), http.StatusConflict)
			return
		}
	}
	if refused(w, entity.Check(store, body.By, entity.ReadWrite, mirror.QuestionRef(body.ID), "answering it")) {
		return
	}
	// AN ANSWER IS TO THE ASK AS IT STANDS. One naming an earlier edit of a person's ask consented
	// to words no longer asked; one naming none answers the edit standing now. The registry
	// checks again at delivery, which is where the race with an edit is closed (Registry.Deliver).
	if current := mirror.EditOf(q.Payload); current != 0 {
		if res.Edit != 0 && res.Edit != current {
			http.Error(w, fmt.Sprintf("ask %s was asked again: you answered edit %d, and it now stands at edit %d (%s); read it and answer again",
				body.ID, res.Edit, current, entity.Str(q.Payload, "pose")), http.StatusConflict)
			return
		}
		res.Edit = current
	}
	// AN ASK IS ANSWERED BY WHOM IT ASKS. A person's ask is its asker's record, so the asker holds
	// it at owner and would pass the check above; answering it is still not theirs, unless they
	// named themselves among whom it asks.
	if askedBy(q.Payload, body.By) {
		http.Error(w, fmt.Sprintf("%s asked %s, and an ask is answered by whom it asks, not by its asker",
			body.By, body.ID), http.StatusForbidden)
		return
	}
	// NOBODY ANSWERS AN ASK ABOUT SOMETHING THEY CANNOT READ (9ce3bva0, D3), asked again here and
	// not only when it was posed: the subject's reading may have narrowed since.
	if err := mirror.CheckAudience(store, entity.Str(q.Payload, "subject"), []string{body.By}); err != nil {
		http.Error(w, fmt.Sprintf("%s: %v", body.ID, err), http.StatusForbidden)
		return
	}
	// The options are the RECORD's, and a choice that is not one of them is refused before
	// anything is delivered or recorded: a ballot for an option nobody offered is not dissent,
	// it is a typo, and the refusal lists what could have been chosen.
	opts := mirror.OptionsOf(q.Payload)
	need, isOption := task.Need(opts, body.Choice)
	if !isOption {
		http.Error(w, fmt.Sprintf("%q is not an option here; the options are %s",
			body.Choice, strings.Join(task.OptionIDs(opts), ", ")), http.StatusBadRequest)
		return
	}
	owner := taskID
	if owner == "" {
		owner = s.ownerOf(store, body.ID)
	}

	// A threshold above one is a TALLY. The ballot is recorded and the question stays open
	// until the choice has as many as it needs; only the ballot that reaches the threshold
	// goes on to settle, and that one is written by the settle itself, so the record never
	// shows a threshold reached without saying so. Counted forward from the read for the
	// common case, then read back after the write for the race two ballots landing at once
	// would otherwise lose (see mirror.CastBallot).
	if need > 1 && entity.Str(q.Payload, "status") == mirror.QuestionPending {
		count := mirror.Tally(q.Payload)[body.Choice]
		if entity.Str(entity.Get(entity.Get(q.Payload, "ballots"), body.By), "choice") != body.Choice {
			count++ // this ballot is new, or changes its answerer's mind
		}
		if count < need {
			count, err = mirror.CastBallot(store, body.ID, res)
			if unreadable(w, r, err) {
				return
			}
			if count < need {
				w.WriteHeader(http.StatusAccepted)
				fmt.Fprintf(w, "recorded: %d of %d for %s\n", count, need, body.Choice)
				return
			}
		}
	}

	err = s.settle(store, body.ID, res, owner)
	if err == nil {
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
	if errors.Is(err, task.ErrStaleEdit) {
		http.Error(w, fmt.Sprintf("ask %s was asked again while you answered: read it and answer again", body.ID),
			http.StatusConflict)
		return
	}
	// Somebody was here first. The loser is recorded rather than dropped: two people at a
	// docket answering at once is normal, and a docket that keeps only the winner cannot
	// show that the question was contested.
	won, settled, readErr := mirror.SettledBy(store, body.ID)
	// Not folded into "nobody was here first": telling an answerer they won a race that could
	// not be read is the one answer there is no recovering from.
	if unreadable(w, r, readErr) {
		return
	}
	if settled || errors.Is(err, task.ErrAlreadyAnswered) {
		// Not stale if it COUNTED: two ballots that reached a threshold together both tried
		// to settle, one did, and the other's ballot is on the record under the outcome it
		// chose. That answerer decided the same thing the question settled on, and telling
		// them they lost a race would report a decision they were part of as one they missed.
		if mirror.Counted(store, body.ID, res) {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		if won == "" {
			won = "an earlier answer"
		}
		mirror.StaleAnswer(store, body.ID, res)
		http.Error(w, "already answered by "+won+"; your answer was recorded as stale", http.StatusConflict)
		return
	}
	http.Error(w, "no such question", http.StatusNotFound)
}

// settle delivers a resolution to whatever is waiting on a question and records it. It is the
// one place a question SETTLES, whoever decided: a person or a resolver through
// answerQuestion, or the clock through settleByDeadline. owner is the task holding the
// question, empty for a firing's gate, which the engine's registry answers here.
//
// Delivery first, the record after: the waiter is the arbiter of who settled a question, and
// a record written ahead of a delivery that then failed would say a decision reached a party
// it never reached. The error is the delivery's, for the caller to say in its own terms.
func (s *Server) settle(store entity.Store, id string, res task.Resolution, owner string) error {
	var err error
	if owner != "" {
		err = s.resolveTaskDecision(owner, id, res)
	} else {
		err = s.Reg.Deliver(id, res)
	}
	if err != nil {
		return err
	}
	// Accepted is not the same as delivered. A broker takes an answer for a worker that
	// must dial in and pull it, so the record says so — `delivered: false` — until the
	// worker's acknowledgement clears it. Everywhere else the answer has already landed
	// on the party that must act, and the record is simply settled.
	if s.relaysControl(owner) {
		mirror.QueuedAnswer(store, id, res)
	} else {
		mirror.AnswerQuestion(store, id, res)
	}
	return nil
}

// armDeadline starts the clock on a question posed with a deadline: the instant is stamped on
// the record once (a stamp already standing wins, so a re-pose after a restart arms against
// the original instant rather than a fresh one), and a timer fires the settle at it.
//
// A timer rather than a sweep, because a deadline SETTLES: it releases a firing or a task
// loop that is blocked, and those do not read the docket. The docket read still settles an
// overdue question it finds (settleOverdueIn), for the daemon whose timer died with it.
func (s *Server) armDeadline(store entity.Store, id, within string) {
	d, err := time.ParseDuration(within)
	if err != nil || d < 0 {
		return // refused where the question was written; nothing to arm here
	}
	s.armAt(store, id, s.now().Add(d))
}

// armQuestion arms a posed question's clock: at the date it carries where the gate read one from
// its subject, and `within` from now otherwise.
//
// THE DATE IS READ AGAIN HERE. The firing resolved it before the question was written, and an
// edit landing in between is followed by the gate's watch before there is a record to move — so
// the move is refused, and the question would stand at the date it was fired with. Read at the
// arming, the subject's current date is the one stamped; an edit after this is the watch's.
func (s *Server) armQuestion(store entity.Store, q task.GateQuestion) {
	d := q.Deadline
	if d.At == "" {
		s.armDeadline(store, q.ID, d.Within)
		return
	}
	want, err := time.Parse(time.RFC3339Nano, d.At)
	if err != nil {
		return
	}
	if d.AtFrom != "" && q.Subject != "" {
		if ref, perr := entity.ParseAddr(q.Subject); perr == nil {
			if e, ok, gerr := store.Get(ref); gerr == nil && ok {
				if at, derr := trigger.DateAt(e.Payload, d.AtFrom); derr == nil {
					want = at
				}
			}
		}
	}
	s.armAt(store, q.ID, want)
}

// armAt stamps want as the question's instant, unless one stands already, and sets a timer for
// whichever instant the record then holds.
func (s *Server) armAt(store entity.Store, id string, want time.Time) {
	at, err := mirror.StampDeadline(store, id, want)
	if err != nil {
		return
	}
	fires, err := time.Parse(time.RFC3339Nano, at)
	if err != nil {
		return
	}
	s.at(fires, func() { s.settleByDeadline(store, id) })
}

// armDeadlines re-arms, at startup, every pending question that carries a stamped deadline.
// A firing's question is orphaned by the reconcile and re-posed by the charter's replay,
// which arms it again through the ordinary path; a surviving task's question is re-posed by
// nobody, so it is armed from the record here.
//
// A docket that did not answer armed nothing, and says so (entity.IsUnread) so the phase asks
// again: a clock that is never set is a question that never settles.
func (s *Server) armDeadlines() error {
	if err := s.awaiting(phaseTasks, phaseCharter, phasePackages); err != nil {
		return err
	}
	ents, err := pendingQuestionsErr(s.Store)
	if err != nil {
		return entity.Unread(fmt.Errorf("the docket could not be read, so no deadline was armed: %w", err))
	}
	for _, ent := range ents {
		// A STAMPED instant is armed as it stands — a dated question's date, or a moved one —
		// and only a question armed nowhere yet starts its clock from `within` now.
		within, _, at := mirror.DeadlineOf(ent.Payload)
		if stamped, err := time.Parse(time.RFC3339Nano, at); err == nil {
			s.armAt(s.Store, ent.Ref.Name(), stamped)
		} else if within != "" {
			s.armDeadline(s.Store, ent.Ref.Name(), within)
		}
	}
	return nil
}

// moveDeadline is the engines' way to move a standing question's clock, when a gate reading its
// date from the subject sees the date edited: the record moves if the question is still pending,
// and a timer is set for the new instant.
func (s *Server) moveDeadline(scope, id string, at time.Time) error {
	store := s.storeForScope(scope)
	moved, err := mirror.MoveDeadline(store, id, at)
	if err != nil || !moved {
		return err
	}
	s.at(at, func() { s.settleByDeadline(store, id) })
	return nil
}

// settleByDeadline settles one question whose deadline has passed, on its default option, as
// `deadline`. A question already settled, or whose stamped instant is still ahead, is left
// alone — the timer that fired may be one an earlier arming set, and the record is what says
// whether the clock has run out.
func (s *Server) settleByDeadline(store entity.Store, id string) {
	e, ok, err := store.Get(mirror.QuestionRef(id))
	if err != nil || !ok || entity.Str(e.Payload, "status") != mirror.QuestionPending {
		return
	}
	_, dflt, at := mirror.DeadlineOf(e.Payload)
	fires, err := time.Parse(time.RFC3339Nano, at)
	if err != nil || s.now().Before(fires) {
		return
	}
	if dflt == "" {
		dflt = task.NoOption
	}
	res := task.Choose(dflt, task.ByDeadline)
	res.Note = "the deadline passed with no decision"
	// A settle nobody can take — the waiter is gone, the runner is down — is left for the
	// next timer or the next reader; the record still says pending, which is the truth.
	_ = s.settle(store, id, res, s.ownerOf(store, id))
}

// settleOverdueIn settles the questions in a docket read whose deadline has passed, and
// reports whether it settled any, so the reader can list what is true now rather than what
// was true a moment ago.
func (s *Server) settleOverdueIn(store entity.Store, pending []entity.Entity) bool {
	now := s.now()
	var any bool
	for _, ent := range pending {
		_, _, at := mirror.DeadlineOf(ent.Payload)
		if at == "" {
			continue
		}
		if fires, err := time.Parse(time.RFC3339Nano, at); err == nil && !now.Before(fires) {
			s.settleByDeadline(store, ent.Ref.Name())
			any = true
		}
	}
	return any
}

// reflect mirrors a task, filling in the provenance every call site shares rather than
// asking each to remember it.
func (s *Server) reflect(store entity.Store, id string, h task.Handle, m mirror.Meta) {
	m.Arm = func(id, within string) { s.armDeadline(store, id, within) }
	m.SlidBy = func() (by, mode string) {
		v, _ := s.slides.Load(id)
		slide, _ := v.([2]string)
		return slide[0], slide[1]
	}
	// Who the runner acts as, when it is one reached by url: a spawn and a resume both name
	// the runner entity, and the entity names the principal.
	if m.As == "" {
		m.As = runnerIdentity(store, m.RunnerName)
	}
	mirror.Reflect(store, id, h, m)
}

// ── resolvers: who answers ───────────────────────────────────────────────────

// addResolver installs a resolver at `verse.resolvers.<name>`.
//
// A VERB rather than a raw write: installing one is where a new match is held to every place
// the resolver already answers for (answerConflict), and a raw write has nowhere to put that.
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
	ref := resolver.Ref(res.Name)
	method := entity.Obj{"driver": res.Method.Driver}
	if res.Method.URL != "" {
		method["url"] = res.Method.URL
	}
	body := entity.Obj{"name": res.Name, "method": method}
	if res.Match != nil {
		m := entity.Obj{}
		if !res.Match.About.IsEmpty() {
			about, err := res.Match.About.ToTonyIR()
			if err != nil {
				http.Error(w, "match.about: "+err.Error(), http.StatusBadRequest)
				return
			}
			m["about"] = about
		}
		if len(res.Match.Labels) > 0 {
			labels := entity.Obj{}
			for k, v := range res.Match.Labels {
				labels[k] = v
			}
			m["labels"] = labels
		}
		body["match"] = m
		// Installed again with a new match, it is held to what a grant is held to, at every
		// place it already answers for.
		ats, err := entity.AnswerGrantsOf(s.Store, entity.Via(resolver.Ref(res.Name)))
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		for _, at := range ats {
			if err := s.answerConflict(s.scopedStore(r), res, at); err != nil {
				http.Error(w, err.Error(), http.StatusConflict)
				return
			}
		}
	}
	store := s.scopedStore(r)
	before, err := entity.ActsAs(store, ref)
	if unreadable(w, r, err) {
		return
	}
	_, stood, err := store.Get(ref)
	if unreadable(w, r, err) {
		return
	}
	if _, _, err := store.Write(entity.Write{At: ref.At(), Value: body.Payload()}); refused(w, err) {
		return
	} else if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	// A resolver answers as whoever its record says (actsAsInstaller).
	if err := actsAsInstaller(store, ref, principalOf(r), before); err != nil {
		s.takeBackOut(r, ref, stood)
		http.Error(w, err.Error(), http.StatusConflict)
		return
	}
	writeOut(w, r, http.StatusCreated, map[string]any{"at": ref.String()})
}

func (s *Server) listResolvers(w http.ResponseWriter, r *http.Request) {
	out := make([]map[string]any, 0)
	resolvers, readErr := entity.BelowErr(s.scopedStore(r), resolver.IsResolver, 1, entity.VerseRoot, resolver.System)
	for _, ent := range resolvers {
		rec := entity.ToObj(ent.Payload)
		if rec == nil {
			continue
		}
		rec["at"] = ent.Ref.String()
		out = append(out, rec)
	}
	// A FAILED READ IS NOT AN EMPTY LIST. List answers nil when the read did not happen, so
	// without this a store that could not be read renders as "nothing is installed" — and
	// `verse io` answered `[]` on a verse holding three sources, which is the one shape an
	// operator acts on. Same guard /state has carried all along, for the same reason.
	if unreadable(w, r, readErr) {
		return
	}
	writeOut(w, r, http.StatusOK, out)
}

func (s *Server) getResolver(w http.ResponseWriter, r *http.Request) {
	ref := resolver.Ref(r.PathValue("name"))
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

// grantResolver lets a resolver answer for the caller, or for a place the caller owns: an `answer`
// grant naming it, at `verse.for.<caller>` or at the place (entity.Answer). revokeResolver takes
// it back.
//
// A PERSON'S NODE IS MADE FOR THEM. Verse's half holds nothing out, so nobody can take
// `verse.for.<anyone>`; the first grant a person makes creates theirs, owned by them, and from
// then on it is theirs as any place is, `verse perms` included.
func (s *Server) grantResolver(w http.ResponseWriter, r *http.Request) {
	s.answerFor(w, r, true)
}

func (s *Server) revokeResolver(w http.ResponseWriter, r *http.Request) {
	s.answerFor(w, r, false)
}

func (s *Server) answerFor(w http.ResponseWriter, r *http.Request, grant bool) {
	var body api.AnswerFor
	if err := codec.Read(r, &body); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	who, ok := actor(w, s.scopedStore(r), "")
	if !ok {
		return
	}
	ref := resolver.Ref(r.PathValue("name"))
	res, found := resolver.Find(s.scopedStore(r), ref.String())
	// A revoke needs no resolver standing: a grant to one since removed is still yours to take back.
	if !found && grant {
		http.Error(w, fmt.Sprintf("no resolver %s", ref), http.StatusNotFound)
		return
	}
	if grant && res.Match == nil {
		http.Error(w, fmt.Sprintf("%s has no `match`: it stands from before resolvers watched asks, "+
			"and answers for nobody by grant; install it again with a match", ref), http.StatusConflict)
		return
	}
	at := entity.ForRef(who)
	if body.For != "" {
		var err error
		if at, err = entity.ParseAddr(body.For); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
	} else if grant {
		if owner, err := entity.OwnerOf(s.Store, at); err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		} else if owner == "" {
			if err := entity.Establish(s.Store, at, who, entity.Ref{}, []entity.Grant{}); err != nil {
				http.Error(w, err.Error(), http.StatusBadGateway)
				return
			}
		}
	}
	if held, err := entity.Holds(s.Store, who, entity.OwnerLevel, at); err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	} else if !held {
		http.Error(w, fmt.Sprintf("%s does not own %s, so it lets nobody answer for it", who, at), http.StatusForbidden)
		return
	}
	// A GRANT IS ITS PARTY'S: a resolver's answer for a place is given as the place's owner on
	// record (partyPrincipal), so only they grant it. An owner above co-owns the place and may do
	// anything there, but a grant of theirs would record answers as somebody who never gave them.
	// Taking one back is any owner's, as cleaning up is.
	if grant {
		if owner, err := entity.OwnerOf(s.Store, at); err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		} else if owner == "" {
			http.Error(w, fmt.Sprintf("%s is nobody's yet: a resolver answers for a place as its owner on record, "+
				"so a grant there waits until somebody's it is", at), http.StatusForbidden)
			return
		} else if owner != who {
			http.Error(w, fmt.Sprintf("%s is %s's: a resolver answers for it as its owner on record, so only %s "+
				"grants one there", at, owner, owner), http.StatusForbidden)
			return
		}
	}
	if grant {
		if err := s.answerConflict(s.scopedStore(r), res, at); err != nil {
			http.Error(w, err.Error(), http.StatusConflict)
			return
		}
	}
	names, err := entity.LevelAt(s.Store, at, entity.Answer)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	via := entity.Via(ref)
	names = slices.DeleteFunc(slices.Clone(names), func(n string) bool { return n == via })
	if grant {
		names = append(names, via)
	}
	if err := entity.SetLevel(s.Store, at, entity.Answer, names); err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// answerConflict refuses res answering for at where another resolver already does, on an ask both
// could match: TWO RESOLVERS NEVER ANSWER FOR ONE PARTY ON ONE ASK (9ce3bva0, Q2). Refused on
// the party's side only, so a match declared anywhere costs nobody anything, and none can be
// claimed ahead of the resolver the party wants.
func (s *Server) answerConflict(store entity.Store, res resolver.Resolver, at entity.Ref) error {
	names, err := entity.AnswerersMeeting(s.Store, at)
	if err != nil {
		return err
	}
	self := entity.Via(resolver.Ref(res.Name))
	for _, n := range names {
		g, err := entity.ParseGrantee(n)
		if n == self || err != nil || g.Kind != entity.ViaKind {
			continue
		}
		other, ok := resolver.Find(store, entity.RefAt(g.Path...).String())
		if !ok || other.Match == nil || !other.Match.Overlaps(res.Match) {
			continue
		}
		return fmt.Errorf("%s and %s would both answer for %s on an ask both match; narrow one's "+
			"match (its about or its labels), or revoke the other", resolver.Ref(res.Name), n, at)
	}
	return nil
}

func (s *Server) removeResolver(w http.ResponseWriter, r *http.Request) {
	ref := resolver.Ref(r.PathValue("name"))
	if _, ok, err := s.scopedStore(r).Get(ref); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	} else if !ok {
		http.NotFound(w, r)
		return
	}
	// WHAT IT WAS GRANTED GOES FIRST. A grant names the resolver's address, not whoever installed
	// it, so one left standing would pass to whoever installs that name next: their service would
	// answer as everyone who granted this one. Taken back before the record goes, so there is no
	// moment when the name is free and the grants are not.
	if err := s.revokeAnswering(entity.Via(ref)); err != nil {
		http.Error(w, "the grants to it were not taken back, so it stands: "+err.Error(), http.StatusBadGateway)
		return
	}
	if _, _, err := s.scopedStore(r).Write(entity.Write{At: ref.At(), Value: nil}); err != nil {
		if refused(w, err) {
			return
		}
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	// Nothing it serves moves: the asks are their askers', and whoever granted it answers for
	// themselves again. What it raised goes with it — an onward ask nobody is left to carry back.
	s.withdrawRaisedBy(entity.Via(ref), fmt.Sprintf("the resolver that raised it, %s, was removed", ref))
	w.WriteHeader(http.StatusNoContent)
}

// revokeAnswering takes via off every `answer` line that names it: each person's and each place's
// grant to a resolver being removed.
func (s *Server) revokeAnswering(via string) error {
	ats, err := entity.AnswerGrantsOf(s.Store, via)
	if err != nil {
		return err
	}
	for _, at := range ats {
		names, err := entity.LevelAt(s.Store, at, entity.Answer)
		if err != nil {
			return err
		}
		if err := entity.SetLevel(s.Store, at, entity.Answer,
			slices.DeleteFunc(slices.Clone(names), func(n string) bool { return n == via })); err != nil {
			return err
		}
	}
	return nil
}

// engForRequest is the engine a LISTING is about: baseline, a scope's, or a charter package's.
//
// `?at=` was accepted by the install and ignored by the listing, so the rules of a package
// answered with baseline's rules — an empty list, for a package that had three, which reads as
// "the package installed nothing" rather than as "you asked the wrong engine". A listing and an
// install have to mean the same thing by the same query parameter.
//
// It does NOT create an engine: a listing is a question, and a package nobody has installed
// anything into has no rules to answer with rather than an engine to bring into being.
func (s *Server) engForRequest(r *http.Request) (*trigger.Engine, error) {
	at := r.URL.Query().Get("at")
	if at == "" {
		return s.engFor(r.URL.Query().Get("scope")), nil
	}
	confine, err := trigger.ParseAt(at)
	if err != nil {
		return nil, err
	}
	s.scopeMu.Lock()
	defer s.scopeMu.Unlock()
	if pe, ok := s.scopeEngs["at:"+confine.Stamp()]; ok {
		return pe.eng, nil
	}
	return nil, nil
}

// watchCursor reads the `since` parameter: the cursor to replay from, whether to replay at all,
// and why not if it cannot be read.
//
// A NEGATIVE CURSOR IS RELATIVE — `since=-20` is the last twenty commits — and it is PASSED
// THROUGH rather than resolved here. The store resolves it against its own watermark at the
// moment the watch is established, clamped to its retained floor (go-tony v0.0.176/177), and
// neither of those is knowable from this end: verse could subtract from the head it last saw,
// but it could not clamp to a floor it cannot see, so a deep `-N` on a compacted store would
// come back refused where the store would have answered from what it kept.
//
// It is also why this takes no head: resolving here meant reading one, and a head read a moment
// before the watch starts is a head something may have moved. The store reads its own.
//
// N COMMITS, NOT N EVENTS. A watch narrowed to a path sees only the changes under it, so `-20`
// asks for the span of the last twenty commits and delivers however many events live in it —
// which is usually fewer and never more. A cursor stays a bookmark; the relative form makes the
// bookmark relative rather than making it a count.
//
// ABSENT MEANS FROM NOW, and it is not the same as `since=0`. A watch with no cursor is
// somebody saying "show me what happens", and answering that with a replay of the whole
// retained log — or, when the store cannot replay from 0, with a REFUSAL telling them to "drop
// --since" they never passed — is answering a question nobody asked. `catchUp` is what says
// which, and it has to: the sentinel used to be `since = -1`, which is a real cursor now.
func watchCursor(raw string) (since int64, catchUp bool, err error) {
	if raw == "" {
		return 0, false, nil
	}
	n, err := strconv.ParseInt(raw, 10, 64)
	if err != nil {
		return 0, false, fmt.Errorf("since=%q is not a revision: a cursor is a number, and an "+
			"absent one streams from now", raw)
	}
	return n, true, nil
}

// status is a one-shot summary of the running verse: the store's revision and the
// live counts across the planes — the charter size, in-flight flows, pending gates,
// hosted tasks, and total firings.
// healthz answers from THIS PROCESS ALONE — no store call, at any load.
//
// It exists because `/status` was the only endpoint to probe, and `/status` is a census: it
// counts gates, runners, sources and tasks, and each of those is a read. So a kubelet asking
// "is versed up" every five seconds was asking docd for the whole verse, and the answer's
// latency was a function of the STORE's load rather than of whether this process was alive.
// On staging that made readiness unsatisfiable while the store was busy — versed sat 0/1 for
// half an hour with logd perfectly healthy underneath — and every attempt left a watch behind
// (Store.Rev's sync), which is what made the store busy in the first place.
//
// versed-deployment.yaml names this endpoint as the thing that would earn a liveness probe
// back: "an endpoint that answers from this process alone, without asking docd anything. Then
// 'is it alive' and 'is the store fast' stop being the same question." This is that.
//
// `head` is what the process has been TOLD the store's commit is — its own writes and any
// commit a live watch delivered — read out of memory. It does not move while nothing is
// happening, which is correct: quiet is not unready.
func (s *Server) healthz(w http.ResponseWriter, r *http.Request) {
	// STARTING IS NOT READY, and it is not wedged either. versed serves before its engine is
	// restored — the charter, the packages, the motions and the sources are several
	// store-shaped phases, and staging spent 93 seconds in them — so a probe that waited for
	// them was hitting a closed socket the whole time and could not tell which it was.
	//
	// 503 keeps readiness meaning exactly what it meant: no traffic until the engine is up.
	// What changes is that the answer exists, so `curl /healthz` says starting, and the
	// kubelet's failures count against a reply rather than a refused connection.
	code := http.StatusOK
	if !s.restored.Load() {
		code = http.StatusServiceUnavailable
	}
	writeOut(w, r, code, s.Health())
}

// Health is this process's own facts, read WITHOUT TOUCHING THE STORE: /healthz answers with
// it, and so does the admin face's index (adminface).
//
// Both callers need the same property for the same reason — the load that makes you ask is the
// load that would make a store-reading answer never arrive — so they share the one function
// rather than each promising it. TestHealthzReadsNothing pins the promise.
func (s *Server) Health() map[string]any {
	return map[string]any{"ok": true, "started": s.restored.Load(), "head": s.Store.Head()}
}

func (s *Server) status(w http.ResponseWriter, r *http.Request) {
	out := api.Status{
		// Head, which no longer costs a sync and no longer lies when nothing is happening.
		//
		// This was Rev, deliberately, for one reason: Head used to be what this PROCESS had
		// been told, so a verse where nothing had committed since it started reported 0 —
		// staging, sources scaled off, answered `head: 0` over a store holding 7262 commits.
		// An operator reads storeRev to judge whether anything is moving, and 0 about a full
		// store is worse than a slow answer.
		//
		// go-tony v0.0.165 removes that: the ping carries the store's head, and libctl raises
		// its mark from every answer that carries a commit — reads, writes, watch events, and
		// the heartbeat. So an idle verse tracks the head from a request it was making anyway,
		// and this handler stops opening a root watch per call to find out.
		StoreRev: s.Store.Head(),
		Flows:    s.flowCount(r.URL.Query().Get("scope")),
		Gates:    len(pendingQuestions(s.scopedStore(r))),
		// In force in the scope asked about; a scope nobody has opened holds none.
		Suspensions: s.suspensionCount(r.URL.Query().Get("scope")),
		Tasks:       taskCount(s.scopedStore(r)),
		Fired:       s.firedTotal(),
		Firings:     s.firingsStatus(),
		Runners:     runnerRefs(s.scopedStore(r)),
		IOs:         ioRefs(s.scopedStore(r)),
		Charter:     s.charterTop(),
		Runner:      s.runnerStatus(), // how tasks run: kind + resume + dial legs
		LLM:         s.LLM,            // secrets-safe: are agent creds configured on THIS server
		Perms:       s.permsStatus(),
		Restoring:   s.Restoring(),
	}
	// When docd-backed, publish the client face, where store clients connect.
	if s.DocdClient != "" {
		out.Docd = &api.Docd{Client: s.DocdClient}
	}
	// This verse's own listen addresses (operator API, and the broker face when served).
	if s.Addr != "" || s.BrokerAddr != "" || s.AdminAddr != "" {
		out.Listen = &api.Listen{API: s.Addr, Broker: s.BrokerAddr, Admin: s.AdminAddr}
	}
	writeOut(w, r, http.StatusOK, out)
}

// permsStatus is the permission check's census: what it has refused. Every store verse writes
// through is a view over the one attribution, so one count covers a request's write, a
// firing's and a source's alike.
func (s *Server) permsStatus() *api.Perms {
	pol := entity.PolicyOf(s.Store)
	if pol == nil {
		return nil
	}
	refused, unreadable := pol.Refusals()
	if len(refused) == 0 {
		refused = nil
	}
	return &api.Perms{Refused: refused, Unreadable: unreadable, Hidden: pol.Hidden(), Unparsed: pol.Unparsed()}
}

// baseCharter is the charter every verse stands on, in the binary: the standard schedules and
// the calendar (system.tony). Installed at every start, before the deployment's own.
//
//go:embed system.tony
var baseCharter []byte

// installSystemCharter applies the system's charter: the embedded base, then the file named by
// SystemCharter when the deployment names one. Their rules are the system's, their firings act
// as the system, and a question one of them poses is co-owned by the system alone. A file that
// cannot be read or installed is reported and the rest of the start goes on; a verse whose
// system charter is missing is a verse with fewer rules, not one that will not start.
func (s *Server) installSystemCharter() {
	s.installAsSystem("the base charter", baseCharter)
	if s.SystemCharter != "" {
		if data, err := os.ReadFile(s.SystemCharter); err != nil {
			fmt.Fprintf(os.Stderr, "verse: system charter: %v\n", err)
		} else {
			s.installAsSystem("system charter "+s.SystemCharter, data)
		}
	}
}

// installSystemCharters applies the charters handed to the server as text (SystemCharters), as
// the system, after the sources are restored: one that sends to an io's place is checked against
// the ios registered, and a text that cannot be installed is reported and the rest go on.
//
// ONE REFUSED FOR WANT OF ITS IOS WAITS FOR THEM. A verse started on an empty store (a wipe) has
// no ios at start, and its ios are registered afterwards (deploy/repopulate.sh), so such a
// charter is refused here and would stay missing until the next restart. It is kept, and tried
// again each time an io with an out half is registered (installWaitingSystemCharters).
func (s *Server) installSystemCharters() {
	s.systemMu.Lock()
	defer s.systemMu.Unlock()
	s.systemWaiting = nil
	for _, c := range s.SystemCharters {
		if err := s.installAsSystem("system charter "+c.Name, c.Text); err != nil {
			s.systemWaiting = append(s.systemWaiting, c)
		}
	}
}

// installWaitingSystemCharters tries again each system charter refused so far, as an io's
// registration may be what it was waiting for, and keeps those refused again.
func (s *Server) installWaitingSystemCharters() {
	s.systemMu.Lock()
	defer s.systemMu.Unlock()
	var still []SystemCharter
	for _, c := range s.systemWaiting {
		if err := s.installAsSystem("system charter "+c.Name, c.Text); err != nil {
			still = append(still, c)
			continue
		}
		fmt.Fprintf(os.Stderr, "verse: system charter %s: installed, once an io it sends to stood\n", c.Name)
	}
	s.systemWaiting = still
}

// SystemCharter is one charter a deployment installs as the system beside the file it names: a
// text, and the name the log says it by.
type SystemCharter struct {
	Name string
	Text []byte
}

// installAsSystem installs one charter's text as the system, saying what it was when it cannot,
// and answering why.
func (s *Server) installAsSystem(what string, data []byte) error {
	docs, err := codec.ParseNodes(data, codec.Tony)
	if err != nil {
		fmt.Fprintf(os.Stderr, "verse: %s: %v\n", what, err)
		return err
	}
	release, err := s.lockCharter(s.ctxOrBackground())
	if err != nil {
		fmt.Fprintf(os.Stderr, "verse: %s: %v\n", what, err)
		return err
	}
	defer release()
	if _, _, err := s.installCharterAs(entity.As(s.Store, entity.SystemCharter), "", "", docs); err != nil {
		fmt.Fprintf(os.Stderr, "verse: %s: %v\n", what, err)
		return err
	}
	return nil
}

// firedTotal is how many entities this verse's triggers have acted on: every engine running a
// charter here, baseline's and each package's, added up.
//
// EVERY ENGINE, because a rule fires in the engine that holds it and a package's charter runs
// in one of its own. Baseline's counter alone is therefore the count of a charter installed at
// the root, and says nothing about one installed at an address — staging's rules live
// under `verse-dev.{drift,merge,triage}`, so `fired` read 0 beside a census listing them
// and a docket of 73 tasks they had spawned. This is the scoping the census is a tree for,
// applied to the number next to it.
//
// CUMULATIVE SINCE THIS PROCESS STARTED, which is what the counter is: countFire only ever
// increments, and nothing persists it, so a restart is the one thing that takes it back to 0.
// Read it as "has this verse ever acted", not as a gauge of what is in flight — `flows` is the
// live one.
//
// Scopes are left out for the reason the census leaves them out: a scope is one party's
// overlay, and status answers for this verse.
// firingsStatus counts what has gone wrong at the rules' firings, over baseline and every
// package, and is nil when nothing has: a status with no `firings` is one with none in trouble.
func (s *Server) firingsStatus() *api.Firings {
	var out api.Firings
	for _, eng := range append([]*trigger.Engine{s.Eng}, s.packageEngines()...) {
		failed, unread := eng.TroubleCounts()
		out.Failed += failed
		out.Unread += unread
	}
	if out == (api.Firings{}) {
		return nil
	}
	return &out
}

func (s *Server) firedTotal() int {
	n := s.Eng.Fired()
	for _, eng := range s.packageEngines() {
		n += eng.Fired()
	}
	return n
}

// charterTop is the charter as a CENSUS: how much is installed, and where.
//
//	rules: 2          # baseline's own, at the root
//	strata: 1
//	children:
//	  verse-dev:      # a PLACE: nothing installed here, so no counts
//	    children:
//	      drift: {rules: 4, strata: 2}
//
// COUNTED PER NODE, because the count this replaces was BASELINE's alone while every number
// beside it in status is the whole verse's. A verse whose program is packaged reported
// `rules: 0` next to a charter tree listing the packages holding them — the scoping was the
// bug rather than the zero, and a shape that says whose is the fix for it.
//
// It is `verse charter`'s shape with the lists counted: `rules`, `strata` and `children` mean
// what they mean there, and a node with no charter carries only `children` — so `rules: 0`
// appears exactly where a charter holds none, which is a state worth seeing (a package whose
// levels installed and whose rules were refused).
//
// The counts come from the ENGINES, which hold the installed charter in memory, so this costs
// no read beyond the one packagesSeen already makes. An address with records but no engine
// counts zero rather than creating one: a status must not install anything to describe it.
func (s *Server) charterTop() *ir.Node {
	root := &charterCensus{}
	root.count(s.Eng)
	for _, at := range s.packagesSeen() {
		node := root
		for _, sg := range strings.Split(at, ".") {
			node = node.child(sg)
		}
		node.count(s.packageEngineIfRunning(at))
	}
	if root.empty() {
		return nil
	}
	return root.node()
}

// charterCensus is one node of the tree: what is installed here, and what is under it.
type charterCensus struct {
	here          bool // a charter is installed AT this address
	rules, strata int
	arming        []string // rules counted above whose watch is still being opened
	suspended     []string // rules counted above that a suspension holds about every subject
	kids          map[string]*charterCensus
	order         []string
}

func (c *charterCensus) child(seg string) *charterCensus {
	if c.kids == nil {
		c.kids = map[string]*charterCensus{}
	}
	if k, ok := c.kids[seg]; ok {
		return k
	}
	k := &charterCensus{}
	c.kids[seg] = k
	c.order = append(c.order, seg)
	return k
}

// count records what an engine holds. A nil engine is a charter whose records are in the store
// and whose engine is not — it is installed, and it holds nothing this verse is running.
func (c *charterCensus) count(eng *trigger.Engine) {
	c.here = true
	if eng == nil {
		return
	}
	c.rules, c.strata = len(eng.Triggers()), len(eng.Strata())
	c.arming = eng.Arming()
	for _, t := range eng.Triggers() {
		if len(eng.SuspendedWhole(t.Name)) > 0 {
			c.suspended = append(c.suspended, t.Name)
		}
	}
}

// empty reports whether this node has children.
func (c *charterCensus) empty() bool {
	return !c.here && len(c.order) == 0
}

func (c *charterCensus) node() *ir.Node {
	var fields, values []*ir.Node
	add := func(key string, v *ir.Node) {
		fields = append(fields, ir.FromString(key))
		values = append(values, v)
	}
	// A PLACE CARRIES ONLY ITS CHILDREN, as it does in `verse charter`: `rules: 0` where no
	// charter is installed reads as a charter holding none, which is a different fact and one
	// somebody goes looking for.
	if c.here {
		add("rules", ir.FromInt(int64(c.rules)))
		add("strata", ir.FromInt(int64(c.strata)))
		// Named, not counted: a rule that is in the charter and fires on nothing is one
		// somebody may be waiting on, and the count above says it is installed.
		if len(c.arming) > 0 {
			add("arming", strNodes(c.arming))
		}
		// And the ones a suspension holds whole: installed, counted, and firing on nothing until
		// somebody resumes them.
		if len(c.suspended) > 0 {
			add("suspended", strNodes(c.suspended))
		}
	}
	if len(c.order) > 0 {
		sort.Strings(c.order)
		var kf, kv []*ir.Node
		for _, seg := range c.order {
			kf = append(kf, ir.FromString(seg))
			kv = append(kv, c.kids[seg].node())
		}
		add("children", &ir.Node{Type: ir.ObjectType, Fields: kf, Values: kv})
	}
	return &ir.Node{Type: ir.ObjectType, Fields: fields, Values: values}
}

// strataNames is the installed levels, low to high — Strata() already sorts, and the order is
// what a reader is after: which level a derive may land in, relative to the rest.
func strataNames(ss []trigger.Stratum) []string {
	out := make([]string, 0, len(ss))
	for _, s := range ss {
		out = append(out, s.Name)
	}
	return out
}

// runnerRefs is the runners a spawn may name. Refs rather than records, because status is an
// orientation and `verse runner` is the surface that answers what one IS.
func runnerRefs(store entity.Store) []string {
	es := runner.List(store)
	out := make([]string, 0, len(es))
	for _, e := range es {
		out = append(out, runner.Ref(e.Name).String())
	}
	return out
}

// ioRefs is what reaches the world for this verse. Refs rather than records, for runnerRefs'
// reason — but it earns its line here for one of its own: every other counter measures what
// verse is doing with what it was given, and none of them can tell "nothing is happening" from
// "nothing is arriving". A verse whose only io was dropped reports a steady store rev and an
// idle charter, which is what a healthy quiet one reports too. `verse io` carries the health.
func ioRefs(store entity.Store) []string {
	es := verseio.List(store)
	out := make([]string, 0, len(es))
	for _, e := range es {
		out = append(out, verseio.Ref(e.Name).String())
	}
	return out
}

// runnerStatus describes the runner along its two independent legs, so the two are not
// confused: `resume` is the VERSE-facing control/re-attach address (what a restart needs),
// `dial` is the WORKER-facing address (where a dial-in worker connects). They coincide for
// a remote runnerd (one URL, no separate worker leg) and diverge for a broker (verse drives
// it in-process — ephemeral — while a worker dials a separate face). See docs/task-control.md.
//
// `kind` is about the DEFAULT — where a spawn that names nobody runs — and `dial` is about
// the broker, which is a name a spawn may use rather than the place it lands by default. So
// the two are reported independently: a verse serving a broker still answers `kind: local`,
// and `dial` being present is what says work can be put in front of a person here.
func (s *Server) runnerStatus() *api.RunnerStatus {
	pol := &api.RunnerStatus{Resume: "ephemeral"}
	switch {
	case s.runner.Ref() == "local":
		pol.Kind = "local"
	default: // a remote runner base URL — the Ref IS the re-attach address
		pol.Kind = "remote"
		pol.Resume = s.runner.Ref()
	}
	if f, ok := s.broker.(interface{ Face() string }); ok {
		pol.Dial = f.Face()
	}
	return pol
}

func hasFace(p task.Runner) bool {
	_, ok := p.(interface{ Face() string })
	return ok
}

// taskLister is task.Lister: a runner that keeps its OWN task registry (a broker),
// distinct from the local runtime. localRunner does NOT implement it — it delegates to the
// same runnerd the union already lists — so brokered tasks are added without
// double-counting local ones.
//
// The declaration moved to the task package so both implementations assert against it and
// a signature change is a compile error where the implementation is. Asserting it structurally
// here, as this did, meant a widened signature made the assertion report false rather than
// fail to build — which is how the same mistake took out gate resolution once already.
type taskLister = task.Lister

// registries are the runtimes whose OWN task list this verse has to ask, beside the Taskd
// snapshots every caller reads first: the default runner when it keeps one (a remote runnerd
// does), and the dial-in broker, which a spawn may name without it being the default.
//
// It is a list rather than a field because a runtime can be named without being the default,
// so there is no single one to ask. Consulting only the default omits the other silently, and
// a task missing from `verse task`, from the gate-holder search and from the snapshot route
// while running perfectly well is the shape that costs an afternoon.
//
// The local supervisor is deliberately absent: it delegates to the same Taskd the callers list
// first, so including it would double-count rather than add anything.
func (s *Server) registries() []task.Runner {
	var out []task.Runner
	if _, ok := s.runner.(taskLister); ok {
		out = append(out, s.runner)
	}
	if _, ok := s.broker.(taskLister); ok {
		out = append(out, s.broker)
	}
	return out
}

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
	Resolve(decision, choice, say, by string) error
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
	// WHO IS ANSWERABLE FOR THE RUN is whoever the request is made as (actor). The name travels
	// — to a remote runner, and onto the commits a coderunner delivers, as their
	// `Verse-Principal:` trailer — so a body naming somebody else is a run signed by a party
	// who never asked for it.
	var ok bool
	if body.Principal, ok = actor(w, s.scopedStore(r), body.Principal); !ok {
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
	// NOT UNDER SOMEBODY ELSE'S ID: an id names one party's work, and a run under one that
	// stands writes over the record the last run left.
	if owner, other, err := entity.OwnedByOther(s.scopedStore(r), mirror.Ref(id), body.Principal); unreadable(w, r, err) {
		return
	} else if other {
		http.Error(w, fmt.Sprintf("task id %q stands as %s's: an id names one party's work, so run "+
			"this under another id (or a `name`, and the id is derived from it)", id, owner), http.StatusConflict)
		return
	}
	// Honor --scope: mirror the task:instance into that scope's copy-on-write view, not
	// baseline — else a scoped run leaks into baseline (a baseline watch would see it). On the
	// memory store Scope is a passthrough, so isolation only applies to the docd/logd store.
	// And whose it is: the record of a task somebody typed is owned by whoever typed it.
	scope := r.URL.Query().Get("scope")
	store := s.scopedStore(r)
	vec, err := task.SpawnVector(body.Vector, body.Gates)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	// Whom it acts for: whoever ran it, where that is a person or an agent. Its gates name
	// nobody to ask the docket, and $principal to ask them.
	actsFor := mirror.ActsFor(body.Principal)
	if err := mirror.CheckPrincipal(body.Gates, actsFor); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	// The second spawn path, refusing the same way the first does. A misspelled runner that
	// quietly ran the work in-process says one thing in the deployment and does another in
	// the verse. Refused BEFORE the spawn, so nothing is started and nothing is mirrored.
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
	// The credential this run spends, read as WHOEVER ASKED — the same rule a charter's spawn
	// gets, and the reason typing an address buys nothing a person could not already read. The
	// refusal is a 400 rather than a task that fails, because somebody is standing here.
	credential, err := credentialAt(store, body.Principal, body.VirtualKey)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	h := place.Spawn(task.Launch{
		ID: id, Prompt: body.Prompt, Driver: body.Driver, Retain: body.Retain, Converse: body.Converse,
		Vector: vec, Environment: body.Environment,
		VirtualKey: credential,
		Answer:     body.Answer, Calls: body.Calls,
		// And the same scope the record above goes to, so this run's gates reach the
		// docket of whoever asked for it rather than everybody's.
		Scope: scope,
		// Somebody typed this, and the principal is theirs; a firing's is the rule that
		// fired — see launchOf.
		Provenance: task.Provenance{Principal: body.Principal, Subject: body.Subject},
	})
	eng := s.engineOf(scope)
	s.reflect(store, id, h, mirror.Meta{
		Runner: place.Ref(), RunnerName: placeName,
		// What it was asked and what ran it, on the record as a rule's spawn puts them: the
		// two facts a reader of this task wants beside its status, and for a task on a named
		// runner the record is the only place they can be read without that runner.
		Prompt: body.Prompt, Driver: body.Driver,
		Subject: body.Subject, Gates: body.Gates, Asker: actsFor,
		HeldBy: func() []string { return eng.HeldBy(id) },
		// No Occurrence: nothing caused this one, so there is no commit to name and the
		// mirror stamps the revision its own first write landed at instead. Reading
		// Store.Rev() here looked obvious and is a store ROUND TRIP — it opens a watch when
		// none is live — which put a network call in front of the record write and let the
		// task's first gate question arrive before the record it is about.
	})
	// A FLOW, like a rule's task: tracked by the engine of the scope that spawned it, so `verse
	// flow` lists it, the flow verbs reach it, and what holds a subject's work holds this too. A
	// flow is a query over live tasks' subjects, and a task about no subject is the flow of its
	// own id, the one address it has.
	flow := body.Subject
	if flow == "" {
		flow = id
	}
	eng.Adopt(flow, id, h)
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
// bare list would leak a scope's tasks into baseline (and across
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
	for _, inst := range s.Local.Snapshots() {
		inst.Runner = "local"
		live[inst.ID] = inst
	}
	for _, p := range s.registries() {
		// Best effort, deliberately: an error here means this runner's DETAIL is missing, and
		// the row survives without it. Failing the list on it would be trading a complete
		// answer for no answer.
		if insts, err := p.(taskLister).ListTasks(); err == nil {
			ref := p.Ref()
			for _, inst := range insts {
				inst.Runner = ref
				live[inst.ID] = inst
			}
		}
	}
	out := make([]task.Instance, 0)
	for _, ent := range taskRows(store) {
		rec := entity.ToObj(ent.Payload)
		if rec == nil {
			continue
		}
		inst, ok := live[ent.Ref.Name()]
		if !ok {
			inst = instanceFromRecord(ent.Ref.Name(), rec)
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

// taskRows is the docket as this view sees it: the task:instance mirror, which is the record
// written at spawn and complete without asking any runtime. Both the list and the count come
// through here, so that they are the same rows by construction rather than by agreement.
func taskRows(store entity.Store) []entity.Entity {
	return entity.Below(store, mirror.IsTask,
		1, entity.VerseRoot, mirror.System, mirror.InstanceKind)
}

// taskCount is how many tasks this verse has a record of — the number `verse task` lists.
//
// It counts the mirror rather than the in-process runtime, and the difference is the whole
// point: the runtime's map holds the tasks THIS PROCESS hosts, so a verse that runs its work
// on a remote runner hosts none of them and would report 0 over a full docket. `status` is
// read as the cheap orientation and `verse task` as the detail, in that order, by somebody
// deciding whether anything is happening at all — so the two must not be able to disagree.
//
// It skips a row whose payload is not an object for the reason the list does: such a row is
// not a task anybody can be told about, and counting it would promise a line that never comes.
func taskCount(store entity.Store) int {
	n := 0
	for _, ent := range taskRows(store) {
		if entity.ToObj(ent.Payload) != nil {
			n++
		}
	}
	return n
}

// instanceFromRecord is what verse can say about a task it is not currently holding a
// handle on — one on a named runner, or one whose runtime has not been asked. It is the
// durable account rather than a live one, which is exactly what the mirror is for.
func instanceFromRecord(id string, rec map[string]any) task.Instance {
	str := func(k string) string { v, _ := rec[k].(string); return v }
	num := func(k string) int64 { v, _ := rec[k].(int64); return v }
	return task.Instance{
		ID: id, Prompt: str("prompt"), Driver: str("driver"), Status: task.Status(str("status")),
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
	for _, e := range entity.Below(store, func(ref entity.Ref) bool {
		return mirror.IsTask(ref)
	}, 1, entity.VerseRoot, mirror.System, mirror.InstanceKind) {
		ids[e.Ref.Name()] = true
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
	if _, ok := s.Local.SnapshotOf(id); ok {
		return true
	}
	for _, p := range s.registries() {
		if _, err := p.(taskLister).TaskSnapshot(id); err == nil {
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
	// `local` means in-process, which the runnerd branch above already covers; equal to the
	// default runner means the branch above covered it too, and rebuilding a second client
	// for the same place would lose whatever the configured one knows about itself — a
	// broker's handle relays control, and an HTTP client reconstructed from its url does not
	// say so.
	if ref == "" || ref == "local" || ref == s.runner.Ref() {
		return nil
	}
	// The broker is named, not addressed. Handing back the runtime itself is the whole of why
	// this is a branch: `broker` is a reserved name rather than a url, so building an HTTP
	// client from it dials a host by that name — and control for a brokered task has to reach
	// the object holding the claim, which relays it to a worker that dialled in.
	if ref == brokerName {
		return s.broker // nil when this verse serves none, which reads as "not here"
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
	for _, inst := range s.Local.Snapshots() {
		if holds(inst) {
			return inst.ID
		}
	}
	for _, p := range s.registries() {
		// A runner that did not answer is not a runner holding no gate, and this does not
		// distinguish them — deliberately, because it is already the fallback: the caller
		// consults the mirror's question record first and only lands here when no record
		// names a task. There is nothing better to say from here, and saying it would
		// mean widening every caller between this and the resolve.
		insts, _ := p.(taskLister).ListTasks()
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
	if _, ok := s.Local.SnapshotOf(taskID); ok {
		if s.Local.Resolve(taskID, decisionID, res) {
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
			return rh.Resolve(decisionID, res.Chosen(), strings.Join(res.Steer, " "), res.By)
		}
		return task.ErrUnsupported
	}
	return task.ErrNoSuchTask
}

// attach reaches a task wherever it actually runs: the runner its own task:instance records
// first, the dial-in broker second, this verse's default runner last.
//
// The order is not a preference. An HTTP runner's Attach hands back a handle for ANY id without
// asking, so consulting the default first would reach whichever place happens to be configured
// rather than the one holding the task — silently, and only for work sent somewhere by name.
// The broker goes ahead of it for the opposite reason: it answers only for ids it actually
// holds, so asking costs nothing and skipping it would lose a brokered task that never got a
// record — one spawned on the broker directly rather than through a firing.
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
	if h := attachOn(s.broker, id); h != nil {
		return h
	}
	return s.runner.Attach(id)
}

// getTask resolves one task across backends: the local runtime first (live detail),
// then the runner's own registry (a brokered task) — so `task get <id>` works whether
// the task runs locally or behind a broker.
// mayReadTask asks the permission check whether the request may read a task, and answers the
// request when it may not: refused in the check's words, or the store's error when the record
// could not be read.
//
// A TASK IS READ AS ITS RECORD IS. The snapshot, the event stream and the inbox are the task's
// prompt, its transcript, its result and the answers queued for its worker; the record
// (`verse.task.instance.<id>`) holds less than any of them, and is the one of the four the
// check reaches by itself. So each is asked through the record, in the scope the request names.
//
// A worker that has claimed nothing holds nothing on the record, and reads none of these: what
// it needs before the claim is where to dial, which taskDial answers.
func (s *Server) mayReadTask(w http.ResponseWriter, r *http.Request, id string) bool {
	_, _, err := s.scopedStore(r).Get(mirror.Ref(id))
	return !unreadable(w, r, err)
}

// taskDial answers where a worker dials in to run a task, and nothing else about it.
//
// NOT CHECKED, which is its reason for being: a worker is granted the task at its claim, the
// claim is made at the address this answers, and a check here would ask for the grant before
// the act that makes it. What it gives away is an address the broker serves to anybody who
// can reach it.
//
// ONE ANSWER for a task that is not brokered and for one that is not there, so it says where
// to dial and never whether a task exists.
func (s *Server) taskDial(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if url := s.dialOf(r, id); url != "" {
		writeOut(w, r, http.StatusOK, api.Dial{DialURL: url})
		return
	}
	codec.HTTPError(w, r, http.StatusNotFound, fmt.Sprintf(
		"no broker to dial in to for task %q: it is not a brokered task, or it is not there", id))
}

// dialOf is a task's dial address, from whichever holds the task: a registry that serves
// workers, else the task's record, read as the system since the asker may hold nothing on it.
func (s *Server) dialOf(r *http.Request, id string) string {
	if _, ok := s.Local.SnapshotOf(id); ok {
		return "" // hosted here: there is no worker to dial in
	}
	for _, p := range s.registries() {
		if inst, err := p.(taskLister).TaskSnapshot(id); err == nil && inst.DialURL != "" {
			return inst.DialURL
		}
	}
	store := s.Store
	if sc := r.URL.Query().Get("scope"); sc != "" {
		store = s.Store.Scope(sc)
	}
	if e, ok, _ := entity.As(store, entity.SystemDefault).Get(mirror.Ref(id)); ok {
		return entity.Str(e.Payload, "dialURL")
	}
	return ""
}

func (s *Server) getTask(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if !s.mayReadTask(w, r, id) {
		return
	}
	if inst, ok := s.Local.SnapshotOf(id); ok {
		inst.Runner = "local"
		writeOut(w, r, http.StatusOK, inst)
		return
	}
	for _, p := range s.registries() {
		inst, err := p.(taskLister).TaskSnapshot(id)
		switch {
		case err == nil:
			inst.Runner = p.Ref()
			writeOut(w, r, http.StatusOK, inst)
			return
		case errors.Is(err, task.ErrNoSuchTask):
			// This one answered and has never heard of it. Not a 404 yet: another registry
			// may hold it, and a task on a NAMED runner is in none of them. Keep asking, and
			// the record is asked before anyone gives up.
		default:
			// A runner that did not answer is not a runner without this task. Reporting
			// 404 sent an operator to look for a typo in an id that was never wrong, for a
			// task that is running somewhere they cannot currently see.
			codec.HTTPError(w, r, http.StatusBadGateway,
				fmt.Sprintf("runner %s: %s", p.Ref(), err))
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
// The clause about subjects is there because a subject and a task id both carry `@` and `:`, so
// nothing about their shape tells them apart, which is exactly why the answer has to.
func noSuchTask(w http.ResponseWriter, r *http.Request, id string) {
	codec.HTTPError(w, r, http.StatusNotFound, fmt.Sprintf(
		"no task %q — not in this verse's runtime, not on its runner, and no record of one. "+
			"A SUBJECT is not a task id: a trigger names its task `<chain>@<subject>`. "+
			"`verse flow` lists live tasks by id; "+
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
	if !s.mayReadTask(w, r, r.PathValue("id")) {
		return
	}
	items := []api.InboxItem{}
	// Every runtime that could hold a worker inbox, not just the default: a broker serves one
	// whether or not unnamed work lands on it, and asking only the default answers an empty
	// list about a task with control queued — which reads as "nothing waiting" rather than
	// "asked the wrong place".
	for _, p := range s.registries() {
		ir, ok := p.(inboxReader)
		if !ok {
			continue
		}
		if got, ok := ir.TaskInbox(r.PathValue("id")); ok {
			items = got
			break
		}
	}
	// The bare list, not {items: [...]}. Every other collection this server serves is a
	// bare array — triggers, gates, flows, tasks, flow trees — and the wrapper
	// here was a JSON habit rather than a decision. Nothing decoded it structurally:
	// `verse task inbox` re-renders the document.
	writeOut(w, r, http.StatusOK, items)
}

// taskEvents bridges `attach` across backends: a locally-hosted task streams from the
// runtime (its native SSE); a brokered/remote one is bridged here — Subscribe the runner
// handle (which replays the merged log then goes live) and re-emit each event as SSE.
func (s *Server) taskEvents(w http.ResponseWriter, r *http.Request, runtime http.Handler) {
	id := r.PathValue("id")
	// Asked before the stream is handed to whoever serves it: a hosted task's is the runtime's
	// own handler, which knows nothing of who is reading.
	if !s.mayReadTask(w, r, id) {
		return
	}
	if _, ok := s.Local.SnapshotOf(id); ok {
		runtime.ServeHTTP(w, r) // local runtime owns it
		return
	}
	s.streamEvents(w, r, id)
}

// streamEvents serves one task's read plane over SSE, from `?since=` if it carries one.
//
// It is factored out because an answerer reads THIS stream, not a projection of it: the design
// requires that an answerer sees what an attached observer sees, and the moment it is a
// different stream there are two orders to reconcile and a supervised run that reads
// differently depending on who is looking.
func (s *Server) streamEvents(w http.ResponseWriter, r *http.Request, id string) {
	// Wherever it runs, not wherever the default runner is: `verse task attach` on work sent
	// to a per-spawn runner answered 404, and so — per the note above, since an answerer reads
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
// so the same command works wherever a task runs. Pause and resume are the operator's hold
// (holdTask), taken and lifted as `flow suspend|resume --task` take them.
//
// CONTROL IS HELD, not assumed: the task's owner holds it by owning the task, and the runner
// working it by the grant its claim made. Asked of the store before anything is relayed, the
// way an answer is, since what a control call does happens elsewhere and leaves no record here
// to refuse.
func (s *Server) taskControl(w http.ResponseWriter, r *http.Request, runtime http.Handler, op string) {
	id := r.PathValue("id")
	if op == "pause" || op == "resume" {
		if _, ok := s.holdTask(w, r, id, op == "pause"); ok {
			w.WriteHeader(http.StatusNoContent)
		}
		return
	}
	// THE SCOPE'S OWN, first: the runtime below hosts baseline's tasks and every scope's, and
	// found by id, a task is reached whatever scope asked. A scope owns what it creates
	// (owns.go), so a control write addressed from the wrong scope is refused naming the right
	// one, before anything is relayed and before whether the asker may control it is asked.
	if _, ok := s.controlledTask(w, r, id); !ok {
		return
	}
	// SAY AND VECTOR GO THROUGH THE HANDLE WHEREVER THE TASK RUNS, the runtime's own included:
	// both record who did them, and that is whoever the request is made as (actor). The
	// runtime's own handler takes the request as it stands and records the body's `by`, so the
	// party checked for control would not be the party written down.
	if _, ok := s.Local.SnapshotOf(id); ok && op != "say" && op != "vector" {
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
		if err := codec.Read(r, &body); err != nil || body.Instruction == "" {
			http.Error(w, "instruction required", http.StatusBadRequest)
			return
		}
		by, ok := actor(w, s.scopedStore(r), body.By)
		if !ok {
			return
		}
		ask, err := s.sayTo(r, id, h, body.Instruction, by)
		if refused(w, err) {
			return
		} else if errors.Is(err, task.ErrEnded) {
			http.Error(w, err.Error(), http.StatusConflict)
			return
		} else if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		writeOut(w, r, http.StatusOK, api.Asked{ID: ask})
		return
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
		if err := body.Vector.Validate(); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		// $principal is whom the task acts for, which its record says (`asker:`).
		if task.GatesNamePrincipal(body.Vector.Gates) {
			e, _, err := s.scopedStore(r).Get(mirror.Ref(id))
			if unreadable(w, r, err) {
				return
			}
			if err := mirror.CheckPrincipal(body.Vector.Gates, entity.Str(e.Payload, "asker")); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
		}
		by, ok := actor(w, s.scopedStore(r), body.By)
		if !ok {
			return
		}
		// Who slid it is handed to the projection HERE, where it was checked, and not read back
		// off the worker's echo of the slide, which says whatever the worker wrote
		// (mirror.Meta.SlidBy). Keyed by the task's id, which is what both ends have.
		s.slides.Store(id, [2]string{by, body.Vector.Default.String()})
		h.SetVector(body.Vector, by)
	}
	w.WriteHeader(http.StatusNoContent)
}

// flowTree renders a subject's work as a SHAPE — the trigger that acted on it, the task
// that ran, and the completion hooks branching off it, including the ones that have not run
// yet. A read, so the address rides the query string rather than a body (steering is POST).
//
// It is derived per request from the charter, the mirror and the live registry (see
// trigger.FlowTreesAcross), over every engine the scope may see. Nothing about the tree is stored: a materialized one would be the
// flows[subject] index that a flow-as-a-query deliberately replaced.
func (s *Server) flowTree(w http.ResponseWriter, r *http.Request) {
	engines := s.flowEngines(r.URL.Query().Get("scope"))
	addr := r.URL.Query().Get("subject")
	if addr == "" {
		http.Error(w, "subject required (an entity address, e.g. demo.pr.1 or demo.pr.*)", http.StatusBadRequest)
		return
	}
	m, err := trigger.ParseExpr(addr)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	var active *bool
	switch r.URL.Query().Get("active") {
	case "":
	case "true", "false":
		a := r.URL.Query().Get("active") == "true"
		active = &a
	default:
		http.Error(w, "active is true, false, or absent for every tree", http.StatusBadRequest)
		return
	}
	writeOut(w, r, http.StatusOK, trigger.FlowTreesAcross(engines, m, active))
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
