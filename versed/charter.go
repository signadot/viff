package versed

import (
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strings"

	"github.com/signadot/tony-format/go-tony/ir"

	"github.com/signadot/verse/codec"
	"github.com/signadot/verse/trigger"
)

// THE CHARTER IS ONE THING AT ONE ADDRESS, and this is the one route that serves it.
//
// It replaces `/triggers` and `/strata`, which were two surfaces over one idea: a rule and a
// level are both parts of a charter, they are installed from one file, they are confined by one
// address, and they come and go together. Two endpoints meant every question had to be asked
// twice and assembled by whoever asked — and it meant `verse charter` and `verse charter` each
// answered a slice of a charter with no way to see the charter.
//
// So: one document, addressed like the verse itself, with the same shape at every node.
//
//	writes: anywhere               # baseline: no address, so it may write anywhere
//	triggers: [ … ]
//	strata: [ … ]
//	children:
//	  verse-dev:                   # nothing is installed HERE, so no `writes`
//	    triggers: []
//	    strata: []
//	    children:
//	      drift:
//	        writes: verse-dev.drift.*
//	        imports: [git]
//	        triggers: [ … ]
//	        strata: [ … ]
//
// A NODE IS A PLACE, not a package: `verse-dev` above holds no charter of its own and is there
// because two charters live under it. That is what makes the tree navigable — `verse charter
// verse-dev` is a real question with a real answer, and so is every prefix of an address.

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
		if err := s.fillCharter(node, addr); err != nil {
			return nil, err
		}
	}
	if !found {
		return nil, nil
	}
	return root.node(), nil
}

// fillCharter reads one charter — its confinement, what it imports, its rules and its levels —
// off the engine running it.
func (s *Server) fillCharter(node *charterNode, at string) error {
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
		node.imports = trigger.StampImports(eng.Imports())
	}
	node.triggers, err = codec.ToNode(eng.Triggers())
	if err != nil {
		return fmt.Errorf("charter at %q: %w", at, err)
	}
	node.strata, err = codec.ToNode(eng.Strata())
	return err
}

// belowAddr reports whether a charter's address is at or under the address asked for, and what
// is left of it once the asked-for part is taken off — which is where it hangs in the answer.
func belowAddr(at string, want []string) ([]string, bool) {
	var addr []string
	if at != "" {
		addr = strings.Split(at, ".")
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
	here     bool // a charter is installed AT this address, rather than only under it
	writes   string
	imports  []string
	triggers *ir.Node
	strata   *ir.Node
	kids     map[string]*charterNode
	kidOrder []string
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
	// `writes` and `imports` say what a charter INSTALLED here may do, so they appear only where
	// one is. `triggers` and `strata` appear at every node, empty included: a reader who learns
	// the shape at one level has learned it at all of them, and an address that holds nothing of
	// its own says so rather than leaving the reader to infer it from two absent keys.
	if n.here {
		add("writes", ir.FromString(n.writes))
		if n.imports != nil {
			add("imports", strNodes(n.imports))
		}
	}
	add("triggers", orEmptyList(n.triggers))
	add("strata", orEmptyList(n.strata))
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
	// A SCOPE IS A CHARTER TOO, and it is not in the tree: a federation's rules are its own
	// copy of the verse rather than a place in this one. Asked for by name, answered whole.
	if scope := r.URL.Query().Get("scope"); scope != "" {
		eng := s.engFor(scope)
		if eng == nil {
			writeOut(w, r, http.StatusOK, map[string]any{})
			return
		}
		node := &charterNode{here: true, writes: "the " + scope + " scope"}
		var err error
		if node.triggers, err = codec.ToNode(eng.Triggers()); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if node.strata, err = codec.ToNode(eng.Strata()); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		writeNode(w, r, node.node())
		return
	}
	tree, err := s.charterTree(at, depth)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if unreadable(w, r, s.Store) {
		return
	}
	if tree == nil {
		http.NotFound(w, r)
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
	if eng.RemoveTrigger(name) {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	switch err := eng.RemoveStratum(name); {
	case err == nil:
		w.WriteHeader(http.StatusNoContent)
	case errors.Is(err, trigger.ErrNoStratum):
		http.NotFound(w, r)
	default:
		http.Error(w, err.Error(), http.StatusBadRequest)
	}
}

// addCharter installs one definition — a rule, a level, or a package's `imports:` declaration —
// into the charter at an address. The classification is the SERVER's, so a client posting a
// charter file does not have to know which of three things each document is.
func (s *Server) addCharter(w http.ResponseWriter, r *http.Request) {
	node, err := codec.ReadNode(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	at := r.URL.Query().Get("at")
	switch charterKind(node) {
	case "imports":
		subs, err := trigger.ImportsFromNode(node)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if err := s.setPackageImports(at, subs); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
	case "stratum":
		var st trigger.Stratum
		if err := codec.FromNode(node, &st); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		eng, err := s.charterEngine(r)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if err := eng.InstallStratum(st); err != nil {
			http.Error(w, err.Error(), charterCode(err))
			return
		}
	default:
		if err := trigger.CheckFields(node); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		var t trigger.Trigger
		if err := codec.FromNode(node, &t); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if t.Name == "" {
			http.Error(w, "a rule needs a name", http.StatusBadRequest)
			return
		}
		eng, err := s.charterEngine(r)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if err := eng.InstallTrigger(t); err != nil {
			http.Error(w, err.Error(), charterCode(err))
			return
		}
	}
	w.WriteHeader(http.StatusCreated)
}

// deleteCharter uninstalls the charter package at an address — see Server.removePackage.
func (s *Server) deleteCharter(w http.ResponseWriter, r *http.Request) {
	at := r.URL.Query().Get("at")
	if at == "" {
		http.Error(w, "uninstall: `?at=` is required — it names the charter to remove",
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
	if err := s.removePackage(at, r.URL.Query().Get("recurse") == "true"); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	w.WriteHeader(http.StatusNoContent)
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
	case has["imports"] || has["at"]:
		return "imports"
	}
	return "trigger"
}

// charterCode tells a rule verse would not RECORD from one it would not accept: the first is
// the store being unwell (503) and the second is the charter being wrong (400), and reporting
// either as the other sends somebody to debug the wrong thing.
func charterCode(err error) int {
	if errors.Is(err, trigger.ErrCharter) {
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
