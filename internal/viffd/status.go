package viffd

import (
	"net/http"
	"sort"
	"strings"

	"github.com/signadot/tony-format/go-tony/ir"

	"go.viff.dev/viff/internal/trigger"
	"go.viff.dev/viff/pkg/api"
	"go.viff.dev/viff/pkg/entity"
	viffio "go.viff.dev/viff/pkg/io"
)

// status is a one-shot summary of the running viff: the store's revision and the
// live counts across the planes — the charter size, in-flight flows, pending gates,
// hosted tasks, and total firings.
// healthz answers from THIS PROCESS ALONE — no store call, at any load.
//
// It exists because `/status` was the only endpoint to probe, and `/status` is a census: it
// counts gates, runners, sources and tasks, and each of those is a read. So a kubelet asking
// "is viffd up" every five seconds was asking docd for the whole viff, and the answer's
// latency was a function of the STORE's load rather than of whether this process was alive.
// On staging that made readiness unsatisfiable while the store was busy — viffd sat 0/1 for
// half an hour with logd perfectly healthy underneath — and every attempt left a watch behind
// (Store.Rev's sync), which is what made the store busy in the first place.
//
// viffd-deployment.yaml names this endpoint as the thing that would earn a liveness probe
// back: "an endpoint that answers from this process alone, without asking docd anything. Then
// 'is it alive' and 'is the store fast' stop being the same question." This is that.
//
// `head` is what the process has been TOLD the store's commit is — its own writes and any
// commit a live watch delivered — read out of memory. It does not move while nothing is
// happening, which is correct: quiet is not unready.
func (s *Server) healthz(w http.ResponseWriter, r *http.Request) {
	// STARTING IS NOT READY, and it is not wedged either. viffd serves before its engine is
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
func (s *Server) Health() api.Health {
	return api.Health{OK: true, Started: s.restored.Load(), Head: s.Store.Head()}
}

func (s *Server) status(w http.ResponseWriter, r *http.Request) {
	out := api.Status{
		// Head, which no longer costs a sync and no longer lies when nothing is happening.
		//
		// This was Rev, deliberately, for one reason: Head used to be what this PROCESS had
		// been told, so a viff where nothing had committed since it started reported 0 —
		// staging, sources scaled off, answered `head: 0` over a store holding 7262 commits.
		// An operator reads storeRev to judge whether anything is moving, and 0 about a full
		// store is worse than a slow answer.
		//
		// go-tony v0.0.165 removes that: the ping carries the store's head, and libctl raises
		// its mark from every answer that carries a commit — reads, writes, watch events, and
		// the heartbeat. So an idle viff tracks the head from a request it was making anyway,
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
		Runner:      s.localRunnerStatus(), // the local runner, asked now
		LLM:         s.LLM,                 // secrets-safe: are agent creds configured on THIS server
		Perms:       s.permsStatus(),
		Restoring:   s.Restoring(),
	}
	// When docd-backed, publish the client face, where store clients connect.
	if s.DocdClient != "" {
		out.Docd = &api.Docd{Client: s.DocdClient}
	}
	if s.issuer != nil {
		out.Issuer = &api.Issuer{URL: s.issuer.url, Key: s.issuer.from}
	}
	// This viff's own listen addresses (the operator API, and the admin face).
	if s.Addr != "" || s.AdminAddr != "" {
		out.Listen = &api.Listen{API: s.Addr, Admin: s.AdminAddr}
	}
	writeOut(w, r, http.StatusOK, out)
}

// permsStatus is the permission check's census: what it has refused. Every store viff writes
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

// firedTotal is how many entities this viff's triggers have acted on: every engine running a
// charter here, baseline's and each package's, added up.
//
// EVERY ENGINE, because a rule fires in the engine that holds it and a package's charter runs
// in one of its own. Baseline's counter alone is therefore the count of a charter installed at
// the root, and says nothing about one installed at an address — staging's rules live
// under `viff-dev.{drift,merge,triage}`, so `fired` read 0 beside a census listing them
// and a docket of 73 tasks they had spawned. This is the scoping the census is a tree for,
// applied to the number next to it.
//
// CUMULATIVE SINCE THIS PROCESS STARTED, which is what the counter is: countFire only ever
// increments, and nothing persists it, so a restart is the one thing that takes it back to 0.
// Read it as "has this viff ever acted", not as a gauge of what is in flight — `flows` is the
// live one.
//
// Scopes are left out for the reason the census leaves them out: a scope is one party's
// overlay, and status answers for this viff.
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
//	  viff-dev:      # a PLACE: nothing installed here, so no counts
//	    children:
//	      drift: {rules: 4, strata: 2}
//
// COUNTED PER NODE, because the count this replaces was BASELINE's alone while every number
// beside it in status is the whole viff's. A viff whose program is packaged reported
// `rules: 0` next to a charter tree listing the packages holding them — the scoping was the
// bug rather than the zero, and a shape that says whose is the fix for it.
//
// It is `viff charter`'s shape with the lists counted: `rules`, `strata` and `children` mean
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
	pending       []trigger.Pending // rules of the charter not armed, each with why (not in rules)
	arming        []trigger.Pending // armed rules whose watch the store is still opening, each with what it is doing (in rules)
	suspended     []string          // rules counted above that a suspension holds about every subject
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
// and whose engine is not — it is installed, and it holds nothing this viff is running.
func (c *charterCensus) count(eng *trigger.Engine) {
	c.here = true
	if eng == nil {
		return
	}
	trigs := eng.Triggers()
	c.pending, c.arming = eng.Pendings(), eng.Arming()
	c.rules, c.strata = len(trigs), len(eng.Strata())
	for _, t := range trigs {
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
	// A PLACE CARRIES ONLY ITS CHILDREN, as it does in `viff charter`: `rules: 0` where no
	// charter is installed reads as a charter holding none, which is a different fact and one
	// somebody goes looking for.
	if c.here {
		add("rules", ir.FromInt(int64(c.rules)))
		add("strata", ir.FromInt(int64(c.strata)))
		// Named with why: a rule that is in the charter and fires on nothing is one somebody
		// may be waiting on. `arming` is the armed ones whose watch the store is still
		// opening, each with what it is doing — counted in `rules`, as an install counts
		// them, so the count does not turn on the store's pace; `pending` is the held ones,
		// not counted, each with what it waits for.
		var af, av, pf, pv []*ir.Node
		for _, p := range c.arming {
			af = append(af, ir.FromString(p.Rule))
			av = append(av, ir.FromString(p.Cause))
		}
		for _, p := range c.pending {
			pf = append(pf, ir.FromString(p.Rule))
			pv = append(pv, ir.FromString(p.Cause))
		}
		if len(af) > 0 {
			add("arming", &ir.Node{Type: ir.ObjectType, Fields: af, Values: av})
		}
		if len(pf) > 0 {
			add("pending", &ir.Node{Type: ir.ObjectType, Fields: pf, Values: pv})
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
// orientation and `viff runner` is the surface that answers what one IS.
func runnerRefs(store entity.Store) []string {
	es := installedRunners(store)
	out := make([]string, 0, len(es))
	for _, e := range es {
		out = append(out, runnerEntityRef(e.Name).String())
	}
	return out
}

// ioRefs is what reaches the world for this viff. Refs rather than records, for runnerRefs'
// reason — but it earns its line here for one of its own: every other counter measures what
// viff is doing with what it was given, and none of them can tell "nothing is happening" from
// "nothing is arriving". A viff whose only io was dropped reports a steady store rev and an
// idle charter, which is what a healthy quiet one reports too. `viff iod` carries the health.
func ioRefs(store entity.Store) []string {
	es := viffio.List(store)
	out := make([]string, 0, len(es))
	for _, e := range es {
		out = append(out, viffio.Ref(e.Name).String())
	}
	return out
}
