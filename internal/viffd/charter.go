package viffd

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/signadot/tony-format/go-tony/ir"

	"go.viff.dev/viff/internal/trigger"
	"go.viff.dev/viff/internal/trigger/charter"
	"go.viff.dev/viff/pkg/api"
	"go.viff.dev/viff/pkg/codec"
	"go.viff.dev/viff/pkg/entity"
)

// THE CHARTER IS ONE THING AT ONE ADDRESS, and this is the one route that serves it.
//
// It replaces `/triggers` and `/strata`, which were two surfaces over one idea: a rule and a
// level are both parts of a charter, they are installed from one file, they are confined by one
// address, and they come and go together. Two endpoints meant every question had to be asked
// twice and assembled by whoever asked — and it meant `viff charter` and `viff charter` each
// answered a slice of a charter with no way to see the charter.
//
// So: one document, addressed like the viff itself, with the same shape at every node.
//
//	writes: anywhere               # baseline: no address, so it may write anywhere
//	rules: [ … ]
//	strata: [ … ]
//	children:
//	  viff-dev:                   # a PLACE: nothing is installed here, so it carries
//	    children:                  # nothing a charter would — only what is under it
//	      drift:
//	        writes: viff-dev.drift.*
//	        reads: [git]
//	        rules: [ … ]
//	        strata: [ … ]
//
// A NODE IS A PLACE, not a package: `viff-dev` above holds no charter of its own and is there
// because two charters live under it. That is what makes the tree navigable — `viff c
// viff-dev` is a real question with a real answer, and so is every prefix of an address. What
// it answers WITH is its children alone, since every other key here is a fact about a charter.

// charterTree builds the charter at an address: what stands there, and what stands under it.
//
// depth < 0 is everything below; 0 is that address alone. Answers nil when nothing is installed
// at the address or under it, which the handler reports as a 404 — a question about a place, and
// "nothing is there" is worth saying rather than answering with an empty document.
func (s *Server) charterTree(at string, depth int) (*ir.Node, error) {
	want, err := trigger.ParseAt(at)
	if err != nil {
		return nil, err
	}
	segs := want.Path()
	root := &charterNode{}
	found := at == ""
	// The schedules are read ONCE for the tree and handed to each charter by its address: read
	// per charter, a listing of every installed charter cost one read of every schedule record
	// per charter, in a row.
	schedules := map[string][]api.ScheduleRow{}
	rows, err := s.scheduleRows(s.Store, func(api.ScheduleRow) bool { return true })
	if err != nil {
		return nil, fmt.Errorf("schedules: %w", err)
	}
	for _, row := range rows {
		schedules[row.At] = append(schedules[row.At], row)
	}
	for _, addr := range append([]string{""}, s.packagesSeen()...) {
		rest, under := belowAddr(addr, segs)
		if !under {
			continue
		}
		found = true
		if depth >= 0 && len(rest) > depth {
			continue
		}
		node := root
		for _, seg := range rest {
			node = node.child(seg)
		}
		if err := s.fillCharter(node, addr, schedules[addr]); err != nil {
			return nil, err
		}
	}
	if !found {
		return nil, nil
	}
	return root.node(), nil
}

// fillCharter reads one charter — its confinement, what it declares it reads, its rules and its levels —
// off the engine running it, with the schedules declared at its address.
func (s *Server) fillCharter(node *charterNode, at string, rows []api.ScheduleRow) error {
	// BASELINE IS s.Eng, not a package engine at the empty address: packageEngine would create
	// a second, empty engine keyed on no address and answer from it — a charter that installs
	// fine and lists as nothing, which is exactly what this is here to prevent.
	eng := s.Eng
	if at != "" {
		var err error
		if eng, err = s.packageEngine(at); err != nil {
			return err
		}
	}
	var err error
	node.here = true
	node.writes = "anywhere"
	if at != "" {
		node.writes = at + ".*"
		node.reads = trigger.StampReads(eng.Reads())
		// The declaration once, for what it sends and when it goes.
		if decl, had, err := s.packageDecl(at); err == nil && had {
			if sends := trigger.SendsOf(decl); len(sends) > 0 {
				node.sends = relativeSends(sends, at)
			}
			node.ttl, node.expires = entity.Str(decl, charterTTLField), entity.Str(decl, charterExpiresField)
		}
	}
	// THE RULES THAT STAND, the pending ones among them: a pending rule is written down and
	// occupies its name, so a listing without it shows a charter its author did not install,
	// and the rule they are looking for is the one that is missing. Each is marked with what it
	// waits for.
	listed := append(eng.Triggers(), eng.Held()...)
	node.triggers, err = codec.ToNode(listed)
	if err != nil {
		return fmt.Errorf("charter at %q: %w", at, err)
	}
	markTaskRules(listed, node.triggers)
	markSuspendedRules(eng, node.triggers)
	markHeldRules(eng.Pendings(), node.triggers)
	if err := markTroubledRules(eng, node.triggers); err != nil {
		return fmt.Errorf("charter at %q: %w", at, err)
	}
	if node.strata, err = codec.ToNode(eng.Strata()); err != nil {
		return err
	}
	// The schedules are the daemon's records rather than the engine's, declared at this address.
	if len(rows) > 0 {
		if node.schedules, err = codec.ToNode(rows); err != nil {
			return err
		}
	}
	return nil
}

// belowAddr reports whether a charter's address is at or under the address asked for, and what
// is left of it once the asked-for part is taken off — which is where it hangs in the answer.
//
// SEGMENT BY SEGMENT, over the kinded path each address is: a package at `viff-dev.drift` is
// two segments, and the one segment `viff-dev.drift` — a place whose name holds a dot — is a
// different address that reaches it not at all.
func belowAddr(at string, want []string) ([]string, bool) {
	addr, err := entity.PathSegs(at)
	if err != nil {
		return nil, false
	}
	if len(addr) < len(want) {
		return nil, false
	}
	for i, seg := range want {
		if addr[i] != seg {
			return nil, false
		}
	}
	return addr[len(want):], true
}

// charterNode is one place in the address tree: the charter installed there (or none, for a
// segment that only leads somewhere) and whatever is installed below it.
type charterNode struct {
	here   bool // a charter is installed AT this address, rather than only under it
	writes string
	reads  []string
	sends  []trigger.Send // `from` relative to the package's address, as the charter wrote it
	// ttl is how long the charter stands, as its file wrote it, and expires the instant it goes
	// at; both empty for one with no ttl (charterttl.go).
	ttl, expires string
	triggers     *ir.Node
	strata       *ir.Node
	schedules    *ir.Node // the schedules declared here, as `viff schedule` lists them; nil for none
	kids         map[string]*charterNode
	kidOrder     []string
}

func (n *charterNode) child(seg string) *charterNode {
	if n.kids == nil {
		n.kids = map[string]*charterNode{}
	}
	if c, ok := n.kids[seg]; ok {
		return c
	}
	c := &charterNode{}
	n.kids[seg] = c
	n.kidOrder = append(n.kidOrder, seg)
	return c
}

// node renders the subtree in a DELIBERATE field order — what it may write, what it reads, what
// it holds, then what is under it. Alphabetical (which is what a map gives) would put `children`
// first and `writes` last, so the shallowest fact about a charter would be the one furthest down
// every entry.
func (n *charterNode) node() *ir.Node {
	var fields, values []*ir.Node
	add := func(key string, v *ir.Node) {
		fields = append(fields, ir.FromString(key))
		values = append(values, v)
	}
	// EVERY KEY HERE IS A FACT ABOUT A CHARTER, so a node with no charter carries none of them.
	// A place — `viff-dev`, which exists because `viff-dev.drift` is under it — answers with
	// its `children` and nothing else.
	//
	// This used to add `rules: []` and `strata: []` at every node, empty included, on the
	// argument that a reader who learns the shape at one level has learned it at all of them.
	// What that reader learned was false: `rules: []` at a place reads as a charter with no
	// rules, which is a state a package really can be in (its levels installed and its rules
	// refused), and the only thing telling the two apart was the ABSENCE of `writes`. A fact
	// distinguished from its opposite by a missing key is not a shape worth being uniform
	// about.
	if !n.here {
		if len(n.kidOrder) > 0 {
			sort.Strings(n.kidOrder)
			var kf, kv []*ir.Node
			for _, seg := range n.kidOrder {
				kf = append(kf, ir.FromString(seg))
				kv = append(kv, n.kids[seg].node())
			}
			add("children", &ir.Node{Type: ir.ObjectType, Fields: kf, Values: kv})
		}
		return &ir.Node{Type: ir.ObjectType, Fields: fields, Values: values}
	}
	add("writes", ir.FromString(n.writes))
	if n.reads != nil {
		add("reads", strNodes(n.reads))
	}
	if len(n.sends) > 0 {
		add(trigger.SendsField, sendNodes(n.sends))
	}
	if n.ttl != "" {
		add(charterTTLField, ir.FromString(n.ttl))
		add(charterExpiresField, ir.FromString(n.expires))
	}
	add("rules", orEmptyList(n.triggers))
	add("strata", orEmptyList(n.strata))
	// Only where one is declared: a charter with no schedules is the ordinary case, and a key
	// saying so at every node would be the `rules: []` mistake again.
	if n.schedules != nil {
		add("schedules", n.schedules)
	}
	if len(n.kidOrder) > 0 {
		sort.Strings(n.kidOrder)
		var kf, kv []*ir.Node
		for _, seg := range n.kidOrder {
			kf = append(kf, ir.FromString(seg))
			kv = append(kv, n.kids[seg].node())
		}
		add("children", &ir.Node{Type: ir.ObjectType, Fields: kf, Values: kv})
	}
	return &ir.Node{Type: ir.ObjectType, Fields: fields, Values: values}
}

func orEmptyList(n *ir.Node) *ir.Node {
	if n == nil {
		return ir.FromSlice(nil)
	}
	return n
}

func strNodes(in []string) *ir.Node {
	out := make([]*ir.Node, 0, len(in))
	for _, s := range in {
		out = append(out, ir.FromString(s))
	}
	return ir.FromSlice(out)
}

// ── the routes ───────────────────────────────────────────────────────────────

// getCharter answers the charter at an address — the tree above.
func (s *Server) getCharter(w http.ResponseWriter, r *http.Request) {
	at := r.URL.Query().Get("at")
	depth := -1
	if d := r.URL.Query().Get("depth"); d != "" {
		if _, err := fmt.Sscanf(d, "%d", &depth); err != nil {
			http.Error(w, "depth: "+err.Error(), http.StatusBadRequest)
			return
		}
	}
	// A SCOPE IS A CHARTER TOO, and it is not in the tree: a scope's rules are its own
	// copy of the viff rather than a place in this one. Asked for by name, answered whole.
	if scope := r.URL.Query().Get("scope"); scope != "" {
		eng := s.engFor(scope)
		if eng == nil {
			writeOut(w, r, http.StatusOK, map[string]any{})
			return
		}
		node := &charterNode{here: true, writes: "the " + scope + " scope"}
		var err error
		// Armed and pending alike, each pending one marked with what it waits for, as the tree's
		// packages list theirs.
		if node.triggers, err = codec.ToNode(append(eng.Triggers(), eng.Held()...)); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		markHeldRules(eng.Pendings(), node.triggers)
		if node.strata, err = codec.ToNode(eng.Strata()); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		writeNode(w, r, node.node())
		return
	}
	// `?path=` is the address AND the reach into the node at it, split here (charterpath.go).
	// An explicit `?at=` keeps its meaning and settles the split, which is what a package whose
	// address ends in a document key needs.
	rest := []string{}
	if p := r.URL.Query().Get("path"); p != "" {
		// THROUGH THE KPATH PARSER, not strings.Split: a segment may contain a dot
		// (`viff-dev.drift.rules."v1.2-check"`), so the only correct split is the one that
		// knows the quoting — which is the same reason entity.PathSegs exists and the same
		// reason `viff data` writes an address as steps when an id holds a delimiter.
		segs, err := entity.PathSegs(p)
		if err != nil {
			http.Error(w, "path: "+err.Error(), http.StatusBadRequest)
			return
		}
		if at != "" {
			rest = segs
		} else {
			at, rest = s.splitCharterPath(segs)
		}
		// A reach INTO one node does not need the tree under it built — and building it is a
		// read per package. `children` is the exception, being the tree under it.
		if len(rest) > 0 && rest[0] != "children" && r.URL.Query().Get("depth") == "" {
			depth = 0
		}
	}

	tree, err := s.charterTree(at, depth)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	// THE STORE'S FLAG, not a read's own, and this is the one caller for which that is the
	// right question: a charter tree is assembled from the ENGINES' state, not from a listing
	// made here, so there is no read of this request's to report. What it is guarding against
	// is the same thing either way — a charter that renders as no rules because the store could
	// not be read. See unreadable, and entity.ReadFailed for the distinction.
	if unreadable(w, r, storeUnreadable(s.Store)) {
		return
	}
	if tree == nil {
		http.Error(w, s.charterPathNotFound(at, rest), http.StatusNotFound)
		return
	}
	if len(rest) > 0 {
		node := charterReach(tree, rest)
		if node == nil {
			http.Error(w, s.charterPathNotFound(at, rest), http.StatusNotFound)
			return
		}
		writeNode(w, r, node)
		return
	}
	writeNode(w, r, tree)
}

// getCharterItem answers one rule or one level by name, at an address.
//
// ONE NAME SPACE from here, because a charter is one thing: a rule and a level cannot share a
// name within a charter without this being ambiguous, and that is a fair thing to forbid — they
// are both named parts of the same program, and a reader seeing `stratum: reflected` beside a
// rule called `reflected` learns nothing good either.
func (s *Server) getCharterItem(w http.ResponseWriter, r *http.Request) {
	eng, err := s.engForRequest(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if eng == nil {
		http.NotFound(w, r)
		return
	}
	name := r.PathValue("name")
	if t, ok := eng.Trigger(name); ok {
		writeOut(w, r, http.StatusOK, t)
		return
	}
	for _, st := range eng.Strata() {
		if st.Name == name {
			writeOut(w, r, http.StatusOK, st)
			return
		}
	}
	http.NotFound(w, r)
}

// deleteCharterItem removes one rule or one level by name, at an address.
func (s *Server) deleteCharterItem(w http.ResponseWriter, r *http.Request) {
	release, ok := s.lockCharterHTTP(w, r)
	if !ok {
		return
	}
	defer release()
	eng, err := s.engForRequest(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	name := r.PathValue("name")
	if eng == nil {
		// A scope nobody has opened holds nothing — and the name may still be baseline's.
		s.charterNotHere(w, r, name, func() { http.NotFound(w, r) })
		return
	}
	// AS WHOEVER ASKED. The engine removes through its own store, which is the system's, so it is
	// told who is asking and refuses for them (trigger.Engine.MayRemoveTrigger): a removal
	// nobody checked is the system deleting somebody's rule, its owner record with it, and
	// leaving the name free for the stranger who asked.
	if err := s.lockedAt(r.URL.Query().Get("scope"), r.URL.Query().Get("at")); err != nil {
		charterError(w, err.Error(), http.StatusTooManyRequests)
		return
	}
	if eng.Closing() {
		charterError(w, trigger.ErrClosing.Error(), http.StatusTooManyRequests)
		return
	}
	who := entity.PrincipalOf(s.scopedStore(r))
	if removed, err := eng.RemoveTriggerAs(r.Context(), who, name); refused(w, err) {
		return
	} else if removed {
		w.WriteHeader(http.StatusNoContent)
		return
	} else if err != nil {
		charterRefused(w, err) // the engine closed meanwhile, or the delete did not land
		return
	}
	// A schedule is the daemon's record rather than the engine's, and is removed by the
	// declared name in the charter the path names.
	if r.URL.Query().Get("scope") == "" {
		removed, err := s.removeScheduleNamed(s.scopedStore(r), r.URL.Query().Get("at"), name)
		if errors.Is(err, entity.ErrForbidden) {
			http.Error(w, err.Error(), http.StatusForbidden)
			return
		}
		if err != nil {
			http.Error(w, err.Error(), http.StatusServiceUnavailable)
			return
		}
		if removed {
			w.WriteHeader(http.StatusNoContent)
			return
		}
	}
	switch err := eng.RemoveStratumAs(r.Context(), who, name); {
	case err == nil:
		w.WriteHeader(http.StatusNoContent)
	case errors.Is(err, entity.ErrForbidden):
		http.Error(w, err.Error(), http.StatusForbidden)
	case errors.Is(err, trigger.ErrNoStratum):
		// Not in this scope's charter. A scope owns what it creates (owns.go): an inherited
		// rule is baseline's to remove, and is said to be, rather than "not found" over a rule
		// the same scope lists through the overlay.
		s.charterNotHere(w, r, name, func() { http.NotFound(w, r) })
	default:
		http.Error(w, err.Error(), http.StatusBadRequest)
	}
}

// addCharter installs charters: one fielded charter, several of them (a directory's worth), or
// the shorthand — a bare definition or a list of them, installed at the query's address. The
// classification is the SERVER's, so a client posting a charter does not have to know which kind
// each definition is, nor which order the kinds have to be installed in.
//
// A REQUEST IS ONE STEP — every charter in it, or none. The client posts a whole file, or a whole
// directory (cmd/viff/charter.go), and the split happens here, so a refusal anywhere puts back
// what stood before it wherever it stood. Applied one document at a time, a request whose first
// document installed and whose second was refused left the first standing — a charter nobody
// wrote down, and (once the levels were in and the rules were not) a package an operator could
// not remove by correcting the file.
func (s *Server) addCharter(w http.ResponseWriter, r *http.Request) {
	release, ok := s.lockCharterHTTP(w, r)
	if !ok {
		return
	}
	// RELEASED BEFORE THE WAIT: the lock is one charter operation at a time, and a `wait` is
	// the store's pace, not an operation; held through it, an install waiting on a busy store
	// would refuse every other install, removal and sweep for as long as it waited.
	released := false
	defer func() {
		if !released {
			release()
		}
	}()
	docs, err := codec.ReadNodes(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	hold, err := s.taskCharterOf(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusForbidden)
		return
	}
	q := r.URL.Query()
	// `wait=<duration>` holds the answer until the request's rules are armed, or that long,
	// at most maxInstallWait: an install answers once accepted, and arming is the store's pace
	// (api.InstalledPackage). This is the one reader of the duration; the command and the
	// tools send it as written, so a bad one has one answer.
	var wait time.Duration
	if v := q.Get("wait"); v != "" {
		d, err := time.ParseDuration(v)
		if err != nil || d < 0 || d > maxInstallWait {
			http.Error(w, fmt.Sprintf("wait=%q: a duration, as 30s, up to %s", v, maxInstallWait), http.StatusBadRequest)
			return
		}
		wait = d
	}
	store, scope, at, prune := s.scopedStore(r), q.Get("scope"), q.Get("at"), q.Get("prune")
	// A PRUNE UNINSTALLS FIRST (charterprune.go): the packages the request leaves out are locked
	// under the charter lock and gone, their tasks ended off it, before anything is installed.
	var gone []string
	if prune != "" {
		var code int
		if gone, code, err = s.pruneLeftOut(store, scope, at, prune, docs); err != nil {
			charterError(w, err.Error(), code)
			return
		}
		if own := hold.ownPackageAmong(gone); own != "" {
			http.Error(w, fmt.Sprintf("prune %s: the directory leaves out %s, whose task is asking: uninstalling it "+
				"cancels the task before the install it is making. Nothing was changed", prune, own), http.StatusBadRequest)
			return
		}
	}
	if len(gone) > 0 {
		// EVERY ONE IS LOCKED BEFORE THE CHARTER LOCK COMES OFF, and they are uninstalled
		// together. One locked only as its turn came would stand open while those before it
		// ended their tasks, and an install there would be taken and then uninstalled unasked.
		us, errs := make([]*uninstalling, len(gone)), make([]error, len(gone))
		for i, t := range gone {
			us[i], errs[i] = s.beginUninstall(store, t)
		}
		release()
		failed := false
		for i, u := range us {
			if u != nil {
				errs[i] = u.wait(r.Context(), gone[i])
			}
			failed = failed || errs[i] != nil
		}
		if failed {
			charterError(w, pruneStopped(prune, gone, errs), http.StatusServiceUnavailable)
			return
		}
		again, err := s.lockCharter(r.Context())
		if err != nil {
			charterError(w, pruneUninstalled(err, gone).Error(), http.StatusServiceUnavailable)
			return
		}
		release = again
	}
	in, code, err := s.installCharterPruning(r.Context(), store, scope, at, prune, docs, hold, gone)
	if err != nil {
		if len(gone) > 0 {
			err = pruneUninstalled(err, gone)
		}
		charterError(w, err.Error(), code)
		return
	}
	release()
	released = true
	groups := in.settle(r.Context(), wait)
	out := api.Installed{Installed: installedReport(groups), Pruned: in.pruned}
	writeOut(w, r, http.StatusCreated, out)
}

// installCharterPruning applies a whole request: every charter in it, or none of them.
//
// ONE charterTx SPANS THE REQUEST, whether it carries one charter or a directory's worth, so the
// promise an install prints — that nothing was installed when anything was refused — is true of
// what was sent, not of each charter in it.
//
// REQUEST ORDER, for installing as well as for reporting. A rule's reads are checked against its
// own package's declaration, and a declared read of an address no package is installed at adds
// no edge, so a package that reads another installs whichever comes first.
//
// WITHIN a package the order is the SERVER's, whatever order the file wrote them in: the
// declaration first, because a rule is checked against what its package declares it reads as it
// installs; then the levels, because a rule naming a stratum that is not installed yet is HELD
// rather than refused, and a file whose author wrote both should not leave half its rules
// dormant because of how it was laid out; then the rules.
//
// With a PRUNE ROOT it is also a removal: see charterprune.go. With a HOLD it is a task's,
// and every rule's spawns are held within the task (chartertask.go).
//
// WHAT IT ANSWERS IS SETTLED AFTERWARDS (installed.settle), off the charter lock: a wait for
// the rules to arm, if asked for, and then what the install has to say of each rule, which is
// how things stand when the request ends.
//
// gone is the packages a prune uninstalled ahead of this, for what it answers.
func (s *Server) installCharterPruning(ctx context.Context, store entity.Store, scope, at, prune string, docs []*ir.Node, hold *taskCharter, gone []string) (*charterInstall, int, error) {
	groups, err := charterFilesPruning(docs, at, prune)
	if err != nil {
		return nil, http.StatusBadRequest, err
	}
	// AN INSTALL NAMES ITS INSTALLER. The engine persists a rule through its own store, which
	// is the system's, and an empty name there means the engine's: a charter installed by
	// nobody would be installed by the system, and fire as it. The door refuses the request
	// (Server.principals); this refuses the call, whoever comes to make it — before a prune
	// asks what nobody may remove, which an empty name would pass.
	if entity.PrincipalOf(store) == "" {
		return nil, http.StatusBadRequest, errors.New("a charter names who installs it: its rules act as their installer")
	}
	if prune != "" {
		// What the request leaves out went ahead of it (pruneLeftOut). A package standing there
		// now was installed since, and is not this request's to take down unasked.
		left, code, err := s.pruneTargets(store, scope, prune, groups)
		if err != nil {
			return nil, code, err
		}
		if len(left) > 0 {
			return nil, http.StatusTooManyRequests, fmt.Errorf("prune %s: a charter is installed at %s, "+
				"which the request leaves out; ask again, to take it down too", prune, strings.Join(left, ", "))
		}
	}
	tx := &charterTx{s: s, ctx: ctx, store: store, scope: scope, hold: hold, dropped: map[string][]string{}}
	if code, err := tx.install(groups); err != nil {
		return nil, code, err
	}
	if prune != "" {
		if code, err := tx.pruneKept(groups); err != nil {
			code, err := tx.refuse(code, err)
			return nil, code, err
		}
	}
	// A package an uninstall locked while this installed at it is not installed at: asked as
	// each package's install begins, and here once every one is in.
	for _, g := range groups {
		if err := s.lockedAt(scope, g.at); err != nil {
			code, err := tx.refuse(http.StatusTooManyRequests, err)
			return nil, code, err
		}
	}
	takePackageAddresses(store, entity.PrincipalOf(store), groups)
	return &charterInstall{tx: tx, groups: groups, pruned: tx.prunedReport(gone)}, http.StatusCreated, nil
}

// charterInstall is a request's worth of charters, in: what to answer for it is settled after.
type charterInstall struct {
	tx     *charterTx
	groups []charterGroup
	pruned []api.Pruned
}

// settle answers the groups with what the install has to say of each rule, after waiting that
// long, at most, for the rules it placed to arm (waitArmed).
func (in *charterInstall) settle(ctx context.Context, wait time.Duration) []charterGroup {
	in.tx.waitArmed(ctx, wait)
	for i := range in.groups {
		in.groups[i].said = in.tx.notes(in.groups[i].at)
	}
	return in.groups
}

// installCharterAs is the install with the request already read off: the store the installer
// acts through, the scope and the address the file is being applied at. The system's own
// charter comes through here from a file rather than a request (installSystemCharter).
func (s *Server) installCharterAs(store entity.Store, scope, at string, docs []*ir.Node) ([]charterGroup, int, error) {
	in, code, err := s.installCharterPruning(s.ctxOrBackground(), store, scope, at, "", docs, nil, nil)
	if err != nil {
		return nil, code, err
	}
	return in.settle(s.ctx, 0), code, nil
}

// maxInstallWait is the longest an install's answer is held for its rules to arm: a caller
// gone meanwhile ends the wait through its request's context, and this bounds one that stays.
const maxInstallWait = time.Hour

// waitArmed holds until every rule this request placed has its watches open (Engine.Ready),
// or until wait has passed, or ctx — the request's — ends. Nothing is waited for with no wait.
// A rule held for a stratum or a runner does not arm by waiting, so it is not waited on: the
// answer says what it waits for. The channel is fetched by name once every install has
// returned, so a rule replaced by this very install is waited on as the rule it is now.
func (tx *charterTx) waitArmed(ctx context.Context, wait time.Duration) {
	if wait <= 0 || len(tx.placed) == 0 {
		return
	}
	deadline := time.NewTimer(wait)
	defer deadline.Stop()
	for _, p := range tx.placed {
		ready := p.eng.Ready(p.rule.Name)
		if ready == nil {
			continue
		}
		select {
		case <-ready:
		case <-deadline.C:
			return
		case <-ctx.Done():
			return
		}
	}
}

// install applies every group, or refuses and puts back what it had applied.
func (tx *charterTx) install(groups []charterGroup) (int, error) {
	s, store, scope := tx.s, tx.store, tx.scope
	installer := entity.PrincipalOf(store)
	for i := range groups {
		g := &groups[i]
		// THE ADDRESS IS THE INSTALLER'S TO TAKE, asked before anything is installed at it: a
		// package writes beneath its address, and what it writes is owned through the address
		// (takePackageAddresses), so installing at a place somebody else owns is refused here
		// with the check's own words rather than at the first firing that writes there.
		if code, err := checkPackageAddress(store, installer, g.at); err != nil {
			return tx.refuse(code, err)
		}
		// FRESH BEFORE THE ENGINE, because asking for one creates it: an install that is wholly
		// refused must leave the address as it found it, and a package engine standing where
		// nothing is installed is a charter that lists as nothing and runs as nothing.
		if err := s.lockedAt(scope, g.at); err != nil {
			return tx.refuse(http.StatusTooManyRequests, err)
		}
		// AND ONLY FROM A LISTING THAT WAS READ: a package taken for fresh is stopped with a
		// refusal, its tasks cancelled with no hook, so one missed by a read that did not happen
		// would lose its work to a refusal of something else.
		if g.at != "" {
			seen, err := s.packagesSeenErr()
			if err != nil {
				return tx.refuse(http.StatusServiceUnavailable, err)
			}
			if !slices.Contains(seen, g.at) {
				tx.fresh = append(tx.fresh, g.at)
			}
		}
		eng, err := s.charterEngineFor(scope, g.at)
		if errors.Is(err, errStopping) {
			return tx.refuse(http.StatusTooManyRequests, err)
		}
		if err != nil {
			return tx.refuse(http.StatusBadRequest, err)
		}
		tx.at, tx.eng = g.at, eng
		for _, def := range g.reads {
			if code, err := tx.imports(def); err != nil {
				return tx.refuse(code, err)
			}
		}
		// The ttl with the declaration it is part of, which only a fielded charter writes.
		if len(g.reads) > 0 {
			if code, err := tx.expiry(g); err != nil {
				return tx.refuse(code, err)
			}
		}
		for _, def := range g.strata {
			if code, err := tx.stratum(def); err != nil {
				return tx.refuse(code, err)
			}
		}
		// The schedules after the levels and before the rules: a rule reads a schedule's
		// ticks, and a tick that lands before its reader is armed is one the reader's own
		// replay picks up, so the order is a courtesy rather than a need.
		for _, def := range g.schedules {
			if code, err := tx.schedule(def); err != nil {
				return tx.refuse(code, err)
			}
		}
		for _, def := range g.rules {
			if code, err := tx.rule(def); err != nil {
				return tx.refuse(code, err)
			}
		}
	}
	return http.StatusCreated, nil
}

// checkPackageAddress refuses an install at an address the installer does not hold `owner` at.
// Baseline's charter has no address.
func checkPackageAddress(store entity.Store, installer, at string) (int, error) {
	if at == "" {
		return 0, nil
	}
	ref, err := entity.ParseAddr(at)
	if err != nil {
		return http.StatusBadRequest, err
	}
	return checkPlace(store, installer, ref, "installing a charter at it")
}

// takePackageAddresses gives each package's address an owner record naming its installer, once
// the whole file has installed.
//
// ONE RECORD FOR THE PACKAGE, NOT ONE PER ROW. A rule acts as its record, which resolves to the
// installer, and a record is written only where ownership changes down the tree: with nothing
// recorded at the address, the owner above a derived row was the system, so every row a package
// derived got a record of its own naming the installer — the triage drive's thirty rows, the
// drift ledger's findings, each one a record saying what the address could say once. Taken
// here, every row beneath is owned through the address and needs none.
//
// AFTER the install rather than before it, so a file refused part-way leaves no place taken that
// nothing was installed at.
func takePackageAddresses(store entity.Store, installer string, groups []charterGroup) {
	for _, g := range groups {
		if g.at == "" {
			continue
		}
		ref, err := entity.ParseAddr(g.at)
		if err != nil {
			continue // refused before anything installed; unreachable here
		}
		takePlace(store, installer, ref, "charter package "+g.at)
	}
}

// installedReport is what a successful install answers with: one entry per package the file
// held, in file order.
//
// IT IS THE SERVER'S TO SAY, because the grouping is. The CLI used to split the file itself to
// know what to print, which meant two graders of one question — and the one that printed was
// not the one that installed.
//
// AND WHAT THE INSTALL HAS TO SAY, which is the author's to read and nobody else's to act on:
// `held`, the rules written down and not armed, each with what it waits for — its watches,
// which the store opens at its own pace, or a stratum or a runner that is not there yet;
// `notes`, the advisories, each with its rule. `rules` counts the rules accepted to fire, the
// ones still arming among them, so a file whose one rule is held for a stratum answers 0 and
// says why, and the count does not turn on the store's pace.
func installedReport(groups []charterGroup) []api.InstalledPackage {
	out := make([]api.InstalledPackage, 0, len(groups))
	for _, g := range groups {
		e := api.InstalledPackage{Strata: len(g.strata), At: g.at, Expires: g.expires}
		held := 0
		for _, n := range g.said {
			if n.Waits != "" {
				e.Held = append(e.Held, api.HeldRule{Rule: n.rule, Waits: n.Waits, Arming: n.Arming})
				if !n.Arming {
					held++
				}
			}
			for _, a := range n.Advice {
				e.Notes = append(e.Notes, api.RuleNote{Rule: n.rule, Note: a})
			}
		}
		e.Rules = len(g.rules) - held
		out = append(out, e)
	}
	return out
}

// charterGroup is one package's worth of a file: where it goes, and what it installs there.
type charterGroup struct {
	at        string
	reads     []*ir.Node
	strata    []*ir.Node
	schedules []*ir.Node
	rules     []*ir.Node
	// ttl is how long a fielded charter stands, as its file wrote it (ttlText), or zero for as
	// long as it is not removed; expires is the instant its install gave it (charterttl.go).
	ttl              time.Duration
	ttlText, expires string
	// said is what installing the rules had to say, filled once the file is in.
	said []ruleNotes
}

func (g charterGroup) empty() bool {
	return len(g.reads) == 0 && len(g.strata) == 0 && len(g.schedules) == 0 && len(g.rules) == 0
}

// fileKeys are the keys of a fielded charter, and the only ones it may carry. The file is the
// node `viff c` prints for one address, so what the install reads is what the listing wrote.
var fileKeys = []string{"at", charter.ReadsField, trigger.SendsField, charterTTLField, "rules", "schedules", "strata"}

// charterFiles splits a request into the charters it installs, in request order.
//
// A DOCUMENT IS ONE OF TWO THINGS. A FIELDED CHARTER — an object whose keys are fileKeys — is a
// whole charter: its `at:` says where (or the query's, when it says nothing), and it is a
// package's declaration as well, so an `at:` with no `reads:` declares a package that reads
// nothing outside itself. Anything else is the SHORTHAND: a rule, a level, a schedule, or a list
// of them, installed at the query's address alongside what stands there. The shorthand declares
// nothing; saying what a package reads is what the fielded form is for.
//
// ONE CHARTER PER ADDRESS PER REQUEST: two with no order between them have nothing to settle
// which one stands.
func charterFiles(docs []*ir.Node, at string) ([]charterGroup, error) {
	var out []charterGroup
	seen := map[string]bool{}
	claim := func(addr string) error {
		if seen[addr] {
			where := "baseline"
			if addr != "" {
				where = strconv.Quote(addr)
			}
			return fmt.Errorf("the request installs two charters at %s: one charter per address, "+
				"since two have no order that would settle which one stands", where)
		}
		seen[addr] = true
		return nil
	}
	bare := -1 // the shorthand's group, once one is open
	some := false
	for _, doc := range docs {
		inner := codec.Unwrap(doc)
		if inner == nil || inner.Type == ir.NullType {
			continue // an empty document
		}
		some = true
		if charterKind(doc) == "charter" {
			g, err := fieldedCharter(doc, at)
			if err != nil {
				return nil, err
			}
			if err := claim(g.at); err != nil {
				return nil, err
			}
			out = append(out, g)
			continue
		}
		defs := []*ir.Node{doc}
		if inner.Type == ir.ArrayType {
			defs = defs[:0]
			for _, v := range inner.Values {
				if el := codec.Unwrap(v); el != nil && el.Type != ir.NullType {
					defs = append(defs, v)
				}
			}
		}
		if bare < 0 {
			if err := claim(at); err != nil {
				return nil, err
			}
			out = append(out, charterGroup{at: at})
			bare = len(out) - 1
		}
		g := &out[bare]
		for _, def := range defs {
			switch charterKind(def) {
			case "stratum":
				g.strata = append(g.strata, def)
			case "schedule":
				g.schedules = append(g.schedules, def)
			case "charter":
				return nil, errors.New("a charter inside a list: a list installs rules, levels " +
					"and schedules at the path, and a charter — `at:`, `reads:`, `rules:` … — " +
					"is a document of its own (one file, or one file per charter in a directory)")
			default:
				g.rules = append(g.rules, def)
			}
		}
	}
	if !some {
		return nil, errors.New("there is no charter in the request: a charter (`at:`, `reads:`, " +
			"`strata:`, `schedules:`, `rules:`), or a rule, a level, or a list of them")
	}
	// NOT BOTH IN ONE REQUEST. A charter beside bare definitions is how a file used to open a
	// package — `at:` and `reads:`, then its rules in documents of their own — and read now it
	// would put those rules at the path rather than in the package: unconfined, at baseline,
	// when the path is empty. Which of the two its author meant is not the server's to guess.
	if bare >= 0 && len(out) > 1 {
		return nil, errors.New("a charter beside bare rules or levels: a charter holds its own, " +
			"under `rules:`, `strata:` and `schedules:` — move them there, or install the bare " +
			"ones on their own at the path they belong to")
	}
	return out, nil
}

// fieldedCharter reads one fielded charter: where it goes, and what it installs there.
func fieldedCharter(doc *ir.Node, at string) (charterGroup, error) {
	n := codec.Unwrap(doc)
	lists := map[string][]*ir.Node{}
	declared, hasReads, hasSends := "", false, false
	var ttl *ir.Node
	for i, key := range n.Fields {
		if key == nil || key.Type != ir.StringType {
			continue
		}
		v := codec.Unwrap(n.Values[i])
		switch key.String {
		case "at":
			if v == nil || v.Type != ir.StringType || v.String == "" {
				return charterGroup{}, errors.New("a charter's `at:` is an address (`drift`, `demo.pr`)")
			}
			declared = v.String
		case charter.ReadsField:
			hasReads = true
		case trigger.SendsField:
			hasSends = true
		case charterTTLField:
			ttl = v
		case "rules", "schedules", "strata":
			if v == nil || v.Type == ir.NullType {
				continue
			}
			if v.Type != ir.ArrayType {
				return charterGroup{}, fmt.Errorf("a charter's `%s:` is a list", key.String)
			}
			for _, el := range v.Values {
				if e := codec.Unwrap(el); e != nil && e.Type != ir.NullType {
					lists[key.String] = append(lists[key.String], el)
				}
			}
			if defs := lists[key.String]; len(defs) > 0 {
				defs[0] = headOnFirst(n.Values[i], defs[0])
			}
		default:
			return charterGroup{}, fmt.Errorf("a charter's keys are %s; %q is none of them",
				strings.Join(fileKeys, ", "), key.String)
		}
	}
	switch {
	case declared == "":
		declared = at
	case at != "" && declared != at:
		return charterGroup{}, fmt.Errorf("the file installs a charter at %q and the path says %q: "+
			"the `at:` is the one that travels with the charter, so drop the path", declared, at)
	}
	g := charterGroup{at: declared}
	if declared == "" {
		// A `reads:` belongs to a package; baseline may write anywhere and has no graph to be
		// an edge in.
		if hasReads {
			return charterGroup{}, errors.New("reads: a charter with no address has nothing to " +
				"read INTO — `reads:` belongs to a package: give it `at: <address>`, or install " +
				"the file at a path (`viff c -a <address> -f …`)")
		}
		if hasSends {
			return charterGroup{}, errors.New("sends: a charter with no address has nothing to " +
				"send FROM — `sends:` belongs to a package: give it `at: <address>`, or install " +
				"the file at a path (`viff c -a <address> -f …`)")
		}
		// Nor a ttl: baseline is not removed whole (removePackage), so there is nothing for
		// one to end.
		if ttl != nil {
			return charterGroup{}, errors.New("ttl: a charter with no address is baseline's, " +
				"which is not removed whole — `ttl:` belongs to a package: give it `at: <address>`, " +
				"or install the file at a path (`viff c -a <address> -f …`)")
		}
	} else {
		// THE DOCUMENT IS THE DECLARATION: trigger.ReadsFromNode reads its `reads:`, and one
		// that has none declares a package that reads nothing outside itself.
		g.reads = []*ir.Node{doc}
	}
	for _, kw := range [][2]string{{"strata", "stratum"}, {"schedules", "schedule"}, {"rules", "trigger"}} {
		kind, want := kw[0], kw[1]
		for _, def := range lists[kind] {
			if got := charterKind(def); got != want {
				return charterGroup{}, fmt.Errorf("a charter's `%s:` holds %s, and this one is %s",
					kind, kindNoun(want), kindNoun(got))
			}
		}
	}
	if ttl != nil {
		d, err := charterTTL(ttl)
		if err != nil {
			return charterGroup{}, err
		}
		g.ttl, g.ttlText = d, ttl.String
	}
	g.strata, g.schedules, g.rules = lists["strata"], lists["schedules"], lists["rules"]
	return g, nil
}

// headOnFirst gives a list's first entry the comment written above it.
//
// A BLOCK LIST BEGINS AT ITS FIRST ELEMENT, so the parser hangs the lines above `- name: first`
// on the list rather than on the entry — the same lines as a comment above `rules:` itself. In a
// charter those lines are the first rule's reason, and the record is meant to be the rule as its
// author left it (trigger.Trigger's Doc), so they go onto the entry, ahead of any it has.
func headOnFirst(list, first *ir.Node) *ir.Node {
	var lines []string
	for n := list; n != nil && n.Type == ir.CommentType && len(n.Values) > 0; n = n.Values[0] {
		lines = append(lines, n.Lines...)
	}
	if len(lines) == 0 {
		return first
	}
	if first.Type == ir.CommentType {
		first.Lines = append(lines, first.Lines...)
		return first
	}
	return &ir.Node{Type: ir.CommentType, Lines: lines, Values: []*ir.Node{first}}
}

// kindNoun is a definition's kind as the refusal names it.
func kindNoun(kind string) string {
	switch kind {
	case "stratum":
		return "levels"
	case "schedule":
		return "schedules"
	case "charter":
		return "a charter"
	}
	return "rules"
}

// charterEngineAt is the engine a group installs into: a scope's when one is asked for, else
// the package's at this address, else baseline's.
func (s *Server) charterEngineAt(r *http.Request, at string) (*trigger.Engine, error) {
	return s.charterEngineFor(r.URL.Query().Get("scope"), at)
}

// charterEngineFor is charterEngineAt with the request read off: a scope's engine, baseline's,
// or a package's.
func (s *Server) charterEngineFor(scope, at string) (*trigger.Engine, error) {
	if scope != "" {
		if s.ctx == nil {
			return nil, errors.New("engine not started")
		}
		return s.scopeEngine(scope), nil
	}
	if at == "" {
		return s.Eng, nil
	}
	if s.ctx == nil {
		return nil, errors.New("engine not started")
	}
	return s.packageEngine(at)
}

// charterTx is one request's worth of installs and how to put back what each replaced. It is not
// a transaction over the store — the writes have committed — but an UNDO of them in reverse, which
// is as much as an engine holding live watches can offer and is what "the file is either the
// charter or it is not" needs.
type charterTx struct {
	s *Server
	// ctx bounds each wait on the engine to hold what the request wrote (trigger.Engine.InstallTriggerAs).
	ctx   context.Context
	store entity.Store
	scope string
	at    string
	eng   *trigger.Engine
	// hold is the task installing, where a runner carries the request for one: what each
	// rule's spawns are held within (chartertask.go). nil for a person's or the system's.
	hold *taskCharter
	// fresh is every address this REQUEST brought into being. A refusal drops each one's
	// engine: an engine left running at an address with nothing installed observes on behalf
	// of no charter, and a later install there would find it rather than make one.
	fresh []string
	undo  []func() error
	// placed is every rule this request installed, in the order it did, with the engine that
	// holds it: what each has to say is asked once the whole file is in (notes).
	placed []placedRule
	// kept is what each package's file holds, and dropped what a prune took out of the
	// packages the request keeps (charterprune.go).
	kept    map[string]map[string]bool
	dropped map[string][]string
}

// placedRule is a rule a request installed, the package it went to, and the engine there.
type placedRule struct {
	at   string
	eng  *trigger.Engine
	rule trigger.Trigger
}

// ruleNotes is what one rule's install has to say (trigger.Notes).
type ruleNotes struct {
	rule string
	trigger.Notes
}

// notes is what the installs at one package have to say, in the order the file held them.
//
// ASKED ONCE THE WHOLE FILE IS IN, not as each rule lands: a rule is held until its stratum
// arrives, and a file may carry the stratum after the rule. What the author is told is how
// things stand when the request ends.
func (tx *charterTx) notes(at string) []ruleNotes {
	var out []ruleNotes
	for _, p := range tx.placed {
		if p.at != at {
			continue
		}
		if n := p.eng.NotesOn(p.rule); n.Waits != "" || len(n.Advice) > 0 {
			out = append(out, ruleNotes{rule: p.rule.Name, Notes: n})
		}
	}
	return out
}

func (tx *charterTx) push(f func() error) { tx.undo = append(tx.undo, f) }

// undoCtx bounds an undo's wait on the engine: the request's lifetime is not it, since what is
// put back is put back whether or not the requester is still there to hear it.
func (tx *charterTx) undoCtx() context.Context { return context.WithoutCancel(tx.ctx) }

// as is who this install is by: the request's principal, which the rule and level records are
// written as, so that the charter acts as its installer from then on.
func (tx *charterTx) as() string { return entity.PrincipalOf(tx.store) }

// imports installs a package's declaration of what it reads, remembering the one it replaced.
func (tx *charterTx) imports(def *ir.Node) (int, error) {
	subs, err := trigger.ReadsFromNode(def)
	if err != nil {
		return http.StatusBadRequest, err
	}
	// CAPTURED, not read at undo time: one transaction now spans several packages, so `tx.at`
	// and `tx.eng` have moved on by the time an unwind runs. An undo that read them would put
	// a later package's declaration back over an earlier package's.
	at, eng := tx.at, tx.eng
	before := eng.Reads()
	prev, had, err := tx.s.packageDecl(at)
	if err != nil {
		return http.StatusServiceUnavailable, err
	}
	sends, err := trigger.SendsFromNode(def, at)
	if err != nil {
		return http.StatusBadRequest, err
	}
	was := trigger.SendsOf(prev)
	if err := tx.s.setPackageImports(tx.store, at, subs); err != nil {
		if entity.IsUnread(err) {
			return http.StatusServiceUnavailable, err
		}
		return http.StatusBadRequest, err
	}
	tx.push(func() error {
		eng.SetReads(before)
		return tx.s.putPackageDecl(tx.store, at, prev, had)
	})
	// The OUTBOXES with it, from the same document: what the package reads and where it asks iods
	// for things are both its declaration (outbox.go). Undone before the record is put back, so
	// the io is held out exactly the outboxes the restored record names.
	installer := tx.as()
	if err := tx.s.setPackageSends(tx.store, installer, at, sends, was); err != nil {
		return http.StatusBadRequest, err
	}
	tx.push(func() error {
		return tx.s.setPackageSends(tx.store, installer, at, was, sends)
	})
	return http.StatusCreated, nil
}

// stratum installs one level, remembering the one it replaced.
func (tx *charterTx) stratum(def *ir.Node) (int, error) {
	var st trigger.Stratum
	if err := codec.FromNode(def, &st); err != nil {
		return http.StatusBadRequest, err
	}
	eng := tx.eng // captured: the transaction spans packages (see imports)
	tx.keep("strata", st.Name)
	old, had := eng.Stratum(st.Name)
	// WHOSE it stood as, kept beside what stood: see rule.
	whose, err := entity.SnapshotRecord(tx.store, eng.StratumRef(st.Name))
	if err != nil {
		return http.StatusServiceUnavailable, err
	}
	err = eng.InstallStratumAs(tx.ctx, tx.as(), st)
	if err == nil || errors.Is(err, trigger.ErrNotReflected) { // written either way: see rule
		tx.push(func() error {
			if had {
				if err := eng.RestoreStratum(tx.undoCtx(), old); err != nil {
					return err
				}
				return entity.RestoreRecord(tx.store, whose)
			}
			return eng.RemoveStratum(st.Name)
		})
	}
	if err != nil {
		return charterCode(err), err
	}
	return http.StatusCreated, nil
}

// rule installs one rule, remembering the one it replaced — held or installed, since either
// occupies the name.
func (tx *charterTx) rule(def *ir.Node) (int, error) {
	if err := trigger.CheckFields(def); err != nil {
		return http.StatusBadRequest, err
	}
	var t trigger.Trigger
	if err := codec.FromNode(def, &t); err != nil {
		return http.StatusBadRequest, err
	}
	if t.Name == "" {
		return http.StatusBadRequest, errors.New("a rule needs a name")
	}
	if err := tx.hold.holds(t); err != nil {
		return http.StatusForbidden, err
	}
	if tx.hold != nil {
		env := tx.hold.environment
		t.Task, t.TaskEnvironment = tx.hold.task, &env
	}
	// A TASK ACTING AS WHOEVER WROTE A RECORD selects only in its installer's place. It runs on
	// the record owner's authority (entity.Pair), which is safe where writing the record is
	// writing into the charter's own place — an ask under mirror.ask, the system's — and is
	// harvesting anywhere else: a rule over Victor's space would run its author's task as Victor.
	if place, ok := t.ActsAsSubjectOver(); ok {
		if code, err := checkPlace(tx.store, tx.as(), place,
			fmt.Sprintf("rule %q acting as the owner of what it selects under it", t.Name)); err != nil {
			return code, err
		}
	}
	eng := tx.eng // captured: the transaction spans packages (see imports)
	tx.keep("rules", t.Name)
	old, had := eng.Rule(t.Name)
	before, err := entity.ActsAs(tx.store, eng.RuleRef(t.Name))
	if err != nil {
		return http.StatusServiceUnavailable, err
	}
	// THE SYSTEM REAPPLYING A RULE THAT STANDS AS IT IS leaves it standing, with nothing
	// written. A system charter is applied over what stood at every start, after the restore
	// has brought the same rules back: the same text standing as the system is what the
	// install would leave, so there is nothing to apply. A rule whose words moved while its
	// meaning stayed — the comments its author writes above it are part of its record, and
	// not of what it does — is written, and the engine's copy follows the record with nothing
	// placed again (trigger.Engine.InstallTriggerAs).
	//
	// Decided before anything is read from the store: whose a rule stands as is the perms
	// mirror's (ActsAs), and the record snapshot below is only for undoing an install.
	if had && tx.as() == entity.SystemCharter && before == tx.as() && trigger.SameText(old, t) {
		return http.StatusCreated, nil
	}
	// AND WHOSE IT STOOD AS, which is its perms record and not anything in the rule. An install
	// may move that — the system takes a name it installs at — so an undo that puts back content
	// alone leaves a person's rule standing under the system's record, firing as the system.
	// The record is kept beside the content and both go back.
	whose, err := entity.SnapshotRecord(tx.store, eng.RuleRef(t.Name))
	if err != nil {
		return http.StatusServiceUnavailable, err
	}
	// THE UNDO IS PUSHED FOR WHAT WAS WRITTEN, not for what was answered: an install whose record
	// landed and whose wait for the engine to show it did not (trigger.ErrNotReflected) is a
	// failure the transaction unwinds, and the record it wrote is part of what it unwinds.
	err = eng.InstallTriggerAs(tx.ctx, tx.as(), t)
	if err == nil || errors.Is(err, trigger.ErrNotReflected) {
		tx.push(func() error {
			if had {
				if err := eng.RestoreTrigger(tx.undoCtx(), old); err != nil {
					return err
				}
				return entity.RestoreRecord(tx.store, whose)
			}
			eng.RemoveTrigger(t.Name)
			return nil
		})
	}
	if err != nil {
		return charterCode(err), err
	}
	// A rule fires as whoever its record says, so one that stands without its installer's is
	// refused here, with the undo above already in place to take it back out.
	if err := actsAsInstaller(tx.store, eng.RuleRef(t.Name), tx.as(), before); err != nil {
		return charterCode(err), err
	}
	tx.placed = append(tx.placed, placedRule{at: tx.at, eng: eng, rule: t})
	return http.StatusCreated, nil
}

// refuse unwinds what this request installed and dresses the refusal that caused it.
//
// The unwind's own failures are REPORTED alongside it rather than swallowed. The operator asked
// for a file to be applied and is being told it was not; the only thing worse than a half-applied
// charter is a half-applied charter nobody mentioned, which is the state this whole path exists to
// retire.
func (tx *charterTx) refuse(code int, err error) (int, error) {
	err = fmt.Errorf("%w\nnothing was installed: a charter file is applied as one step, so a "+
		"refusal anywhere puts back what stood before it", err)
	for i := len(tx.undo) - 1; i >= 0; i-- {
		if back := tx.undo[i](); back != nil {
			err = fmt.Errorf("%w\n— except this, which could not be put back: %v", err, back)
		}
	}
	// The ENGINE last, and only when this request is what brought the address into being: an
	// engine left running at an address with nothing installed observes on behalf of no charter,
	// and a later install at the same address would find it rather than make one. What the
	// refused rules started meanwhile is stopped with it (stopPackage).
	for _, at := range tx.fresh {
		tx.s.stopPackage(at)
	}
	return code, err
}

// deleteCharter uninstalls the charter package at an address — see Server.removePackage.
func (s *Server) deleteCharter(w http.ResponseWriter, r *http.Request) {
	release, ok := s.lockCharterHTTP(w, r)
	if !ok {
		return
	}
	defer release()
	at := r.URL.Query().Get("at")
	// ONE PATH FOR REMOVING TOO, resolved the way a read resolves it (charterpath.go): the
	// address, then what to take out of what stands there. A path that ends at an address takes
	// the PACKAGE down; one that goes on to name a rule or a level takes that out. Both were
	// verbs of their own — `uninstall <address>` and `remove <name>` — which is two commands
	// differing in what they remove rather than in the word, over two coordinate systems.
	if p := r.URL.Query().Get("path"); p != "" {
		segs, err := entity.PathSegs(p)
		if err != nil {
			http.Error(w, "path: "+err.Error(), http.StatusBadRequest)
			return
		}
		var rest []string
		if at != "" {
			rest = segs
		} else {
			at, rest = s.splitCharterPath(segs)
		}
		if len(rest) > 0 {
			s.deleteCharterAt(w, r, at, rest)
			return
		}
	}
	if at == "" {
		http.Error(w, "uninstall: a path is required — it names the charter to remove",
			http.StatusBadRequest)
		return
	}
	if s.ctx == nil {
		http.Error(w, "engine not started", http.StatusServiceUnavailable)
		return
	}
	if !s.packagesSeenAt(at) && len(s.packagesUnder(at)) == 0 {
		http.NotFound(w, r)
		return
	}
	// The lock is let go here: an uninstall waits on its package's tasks, and takes the lock
	// itself for the removal (uninstall.go).
	release()
	if err := s.removePackage(r.Context(), s.scopedStore(r), at, r.URL.Query().Get("recurse") == "true"); err != nil {
		if refused(w, err) {
			return
		}
		if r.Context().Err() != nil || entity.IsUnread(err) || errors.Is(err, errCharterBusy) {
			charterError(w, err.Error(), http.StatusServiceUnavailable)
			return
		}
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// deleteCharterAt takes one rule or one level out of the charter at an address.
//
// The path has to END at a name: `rules watcher` and `strata base` are the two shapes, and
// anything else is a reach INTO something — `rules watcher action` reads fine and removing it
// would mean editing a rule through a URL, which is what re-installing the file is for.
func (s *Server) deleteCharterAt(w http.ResponseWriter, r *http.Request, at string, rest []string) {
	// A FIRST SEGMENT THAT IS NOT A KEY means no address resolved, so this is a question about
	// a PLACE — `viff c -r nosuchpkg` — and answering "nosuchpkg is neither a rule nor a
	// level" describes something nobody asked. Same split the read makes (charterPathNotFound).
	if !charterKeys[rest[0]] {
		http.Error(w, s.charterPathNotFound(at, rest), http.StatusNotFound)
		return
	}
	if len(rest) != 2 || (rest[0] != "rules" && rest[0] != "strata" && rest[0] != "schedules") {
		http.Error(w, "a removal names one rule or one level or one schedule — `<address> rules "+
			"<name>`, `<address> strata <name>` or `<address> schedules <name>` — and a path to "+
			"an address alone takes the whole package down. "+strings.Join(rest, ".")+" is none of "+
			"those", http.StatusBadRequest)
		return
	}
	kind, name := rest[0], rest[1]
	if err := s.lockedAt(r.URL.Query().Get("scope"), at); err != nil {
		charterError(w, err.Error(), http.StatusTooManyRequests)
		return
	}
	if kind == "schedules" {
		// The daemon's record rather than the engine's, and not a scope's.
		if r.URL.Query().Get("scope") != "" {
			http.Error(w, "a schedule is not a scope's: it ticks for the viff", http.StatusBadRequest)
			return
		}
		removed, err := s.removeScheduleNamed(s.scopedStore(r), at, name)
		switch {
		case errors.Is(err, entity.ErrForbidden):
			http.Error(w, err.Error(), http.StatusForbidden)
		case err != nil:
			http.Error(w, err.Error(), http.StatusServiceUnavailable)
		case !removed:
			http.Error(w, s.charterPathNotFound(at, rest), http.StatusNotFound)
		default:
			w.WriteHeader(http.StatusNoContent)
		}
		return
	}
	eng, err := s.engineAt(r, at)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	// Not in this charter: at an address, that is the package's not-found; at the viff itself,
	// it may be another scope's to remove (owns.go), and is said to be.
	notHere := func() {
		notFound := func() { http.Error(w, s.charterPathNotFound(at, rest), http.StatusNotFound) }
		if at != "" {
			notFound()
			return
		}
		s.charterNotHere(w, r, name, notFound)
	}
	if eng == nil {
		notHere() // a scope nobody has opened holds nothing — and the name may be baseline's
		return
	}
	if eng.Closing() {
		charterError(w, at+": "+trigger.ErrClosing.Error(), http.StatusTooManyRequests)
		return
	}
	who := entity.PrincipalOf(s.scopedStore(r)) // see deleteCharterItem: the engine refuses for them
	if kind == "rules" {
		if removed, err := eng.RemoveTriggerAs(r.Context(), who, name); refused(w, err) {
			return
		} else if removed {
			w.WriteHeader(http.StatusNoContent)
			return
		} else if err != nil {
			charterRefused(w, err) // see deleteCharterItem
			return
		}
		notHere()
		return
	}
	switch err := eng.RemoveStratumAs(r.Context(), who, name); {
	case err == nil:
		w.WriteHeader(http.StatusNoContent)
	case errors.Is(err, entity.ErrForbidden):
		http.Error(w, err.Error(), http.StatusForbidden)
	case errors.Is(err, trigger.ErrNoStratum):
		notHere()
	default:
		http.Error(w, err.Error(), http.StatusBadRequest)
	}
}

// engineAt is the engine holding the charter at an address — a scope's when one is asked for,
// since a scope's charter is its own copy of the viff rather than a place in this one.
func (s *Server) engineAt(r *http.Request, at string) (*trigger.Engine, error) {
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

// charterEngine is the engine an install writes into: a scope's, a package's, or baseline's.
func (s *Server) charterEngine(r *http.Request) (*trigger.Engine, error) {
	if scope := r.URL.Query().Get("scope"); scope != "" {
		if s.ctx == nil {
			return nil, fmt.Errorf("engine not started")
		}
		return s.scopeEngine(scope), nil
	}
	if at := r.URL.Query().Get("at"); at != "" {
		if s.ctx == nil {
			return nil, fmt.Errorf("engine not started")
		}
		return s.packageEngine(at)
	}
	return s.Eng, nil
}

// charterKind tells the three definitions a charter file holds apart, by the fields they carry:
// a rule has a `condition`, a level has a `level` and a `match`, a package declaration has
// `imports` or `at`. The CLI classified these; the server does now, so any client posting a
// charter says what it has by writing it rather than by choosing a route.
func charterKind(node *ir.Node) string {
	n := codec.Unwrap(node)
	if n == nil || n.Type != ir.ObjectType {
		return "trigger"
	}
	has := map[string]bool{}
	for _, key := range n.Fields {
		if key != nil && key.Type == ir.StringType {
			has[key.String] = true
		}
	}
	switch {
	case has["condition"]:
		return "trigger"
	case has["level"] && has["match"]:
		return "stratum"
	case has["every"]:
		return "schedule"
	}
	for _, k := range fileKeys {
		if has[k] {
			return "charter"
		}
	}
	return "trigger"
}

// charterCode tells a rule viff would not RECORD from one it would not accept: the first is
// the store being unwell (503) and the second is the charter being wrong (400), and reporting
// either as the other sends somebody to debug the wrong thing.
func charterCode(err error) int {
	// A refusal is a refusal, whichever layer made it: the engine refusing a replacement, or the
	// store refusing the write that records it (which arrives wrapped as ErrCharter).
	if errors.Is(err, entity.ErrForbidden) {
		return http.StatusForbidden
	}
	if errors.Is(err, errActsAsNobody) {
		return http.StatusConflict
	}
	// The package is being uninstalled, or the one that was here is still being stopped, and
	// the same request is taken once it is: the refusals here that asking again mends, and the
	// status an iod asks again on.
	if errors.Is(err, trigger.ErrClosing) || errors.Is(err, errStopping) {
		return http.StatusTooManyRequests
	}
	if errors.Is(err, trigger.ErrCharter) || errors.Is(err, errCharterBusy) {
		return http.StatusServiceUnavailable
	}
	return http.StatusBadRequest
}

// writeNode answers a document this server BUILT, in the format the request asked for.
func writeNode(w http.ResponseWriter, r *http.Request, node *ir.Node) {
	out := codec.RequestFormat(r)
	b, err := codec.NodeBytes(node, out)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", codec.MediaType(out))
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(b)
}
