package versed

import (
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strings"

	"github.com/signadot/tony-format/go-tony/ir"

	"github.com/signadot/verse/codec"
	"github.com/signadot/verse/entity"
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
//	rules: [ … ]
//	strata: [ … ]
//	children:
//	  verse-dev:                   # nothing is installed HERE, so no `writes`
//	    rules: []
//	    strata: []
//	    children:
//	      drift:
//	        writes: verse-dev.drift.*
//	        reads: [git]
//	        rules: [ … ]
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

// fillCharter reads one charter — its confinement, what it declares it reads, its rules and its levels —
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
		node.reads = trigger.StampReads(eng.Reads())
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
	reads    []string
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
	// `writes` and `reads` say what a charter INSTALLED here may do, so they appear only where
	// one is. `rules` and `strata` appear at every node, empty included: a reader who learns
	// the shape at one level has learned it at all of them, and an address that holds nothing of
	// its own says so rather than leaving the reader to infer it from two absent keys.
	if n.here {
		add("writes", ir.FromString(n.writes))
		if n.reads != nil {
			add("reads", strNodes(n.reads))
		}
	}
	add("rules", orEmptyList(n.triggers))
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
	// `?path=` is the address AND the reach into the node at it, split here (charterpath.go).
	// An explicit `?at=` keeps its meaning and settles the split, which is what a package whose
	// address ends in a document key needs.
	rest := []string{}
	if p := r.URL.Query().Get("path"); p != "" {
		// THROUGH THE KPATH PARSER, not strings.Split: a segment may contain a dot
		// (`verse-dev.drift.rules."v1.2-check"`), so the only correct split is the one that
		// knows the quoting — which is the same reason entity.PathSegs exists and the same
		// reason `verse state` writes an address as steps when an id holds a delimiter.
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
	if unreadable(w, r, s.Store) {
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

// addCharter installs a charter — a rule, a level, a package's `reads:` declaration, or a whole
// file of them — into the charter at an address. The classification is the SERVER's, so a client
// posting a charter file does not have to know which of three things each document is, nor which
// order the three have to be installed in.
//
// A REQUEST IS ONE STEP. Either every definition in it installs or none does, which is what makes
// `charter add` atomic per package: the CLI posts a file's worth of one address in one request
// (cmd/verse/charter.go), and a refusal anywhere puts back what stood before it. Applied one
// document at a time, a file whose first document installed and whose second was refused left the
// first standing — a charter nobody wrote down, and (once the levels were in and the rules were
// not) a package an operator could not remove by correcting the file.
func (s *Server) addCharter(w http.ResponseWriter, r *http.Request) {
	release, ok := s.lockCharterHTTP(w, r)
	if !ok {
		return
	}
	defer release()
	docs, err := codec.ReadNodes(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	defs := charterDocs(docs)
	if len(defs) == 0 {
		http.Error(w, "there is no charter in the request: a rule, a level, an `reads:` "+
			"declaration, or a `---` separated file of them", http.StatusBadRequest)
		return
	}
	if code, err := s.installCharter(r, defs); err != nil {
		http.Error(w, err.Error(), code)
		return
	}
	w.WriteHeader(http.StatusCreated)
}

// charterDocs flattens a request's documents into the definitions they hold: one apiece, or a LIST
// of them, which is how a charter file spells several rules under one `---`.
//
// UNWRAPPED TO LOOK AT, KEPT AS IT WAS TO INSTALL: a comment wraps the definition it precedes, and
// that comment is the author's reason for the rule — the record is meant to be the rule as they
// left it (trigger.Trigger's Doc field). A `!key(name)` tag on a list is carried through and
// ignored: it is an array either way, and the tag is what makes re-sending an edited file merge by
// name rather than by position.
func charterDocs(docs []*ir.Node) []*ir.Node {
	var out []*ir.Node
	for _, doc := range docs {
		inner := codec.Unwrap(doc)
		if inner == nil || inner.Type == ir.NullType {
			continue // a trailing `---`, or an empty document
		}
		if inner.Type != ir.ArrayType {
			out = append(out, doc)
			continue
		}
		for _, v := range inner.Values {
			if el := codec.Unwrap(v); el != nil && el.Type != ir.NullType {
				out = append(out, v)
			}
		}
	}
	return out
}

// installCharter applies a charter file's worth of one address as ONE STEP, answering the status
// code a refusal should carry.
//
// THE ORDER IS THE SERVER'S, whatever order the file wrote them in: the declaration first, because
// a rule is checked against what its package imports as it installs; then the levels, because a
// rule naming a stratum that is not installed yet is HELD rather than refused, and a file whose
// author wrote both should not leave half its rules dormant because of how it was laid out; then
// the rules.
func (s *Server) installCharter(r *http.Request, defs []*ir.Node) (int, error) {
	var imports, strata, rules []*ir.Node
	for _, def := range defs {
		switch charterKind(def) {
		case "reads":
			imports = append(imports, def)
		case "stratum":
			strata = append(strata, def)
		default:
			rules = append(rules, def)
		}
	}
	at := r.URL.Query().Get("at")
	// THE DECLARATION'S ADDRESS FIRST, before any engine is asked for, because asking creates one:
	// `reads:` belongs to a package, and a refusal reached on the way there would leave an engine
	// running for a charter that was never installed.
	if len(imports) > 0 && at == "" {
		return http.StatusBadRequest, errors.New("reads: a charter with no address has nothing " +
			"to import into — `reads:` belongs to a package, installed with " +
			"`verse c -a --at <address>`")
	}
	// FRESH BEFORE THE ENGINE, for the same reason: an install that is wholly refused must leave
	// the address as it found it, and a package engine standing at an address nothing is installed
	// at is a charter that lists as nothing and runs as nothing.
	tx := &charterTx{s: s, at: at, fresh: at != "" && !s.packagesSeenAt(at)}
	eng, err := s.charterEngine(r)
	if err != nil {
		return http.StatusBadRequest, err
	}
	tx.eng = eng
	for _, def := range imports {
		if code, err := tx.imports(def); err != nil {
			return tx.refuse(code, err)
		}
	}
	for _, def := range strata {
		if code, err := tx.stratum(def); err != nil {
			return tx.refuse(code, err)
		}
	}
	for _, def := range rules {
		if code, err := tx.rule(def); err != nil {
			return tx.refuse(code, err)
		}
	}
	return http.StatusCreated, nil
}

// charterTx is one request's worth of installs and how to put back what each replaced. It is not
// a transaction over the store — the writes have committed — but an UNDO of them in reverse, which
// is as much as an engine holding live watches can offer and is what "the file is either the
// charter or it is not" needs.
type charterTx struct {
	s     *Server
	at    string
	eng   *trigger.Engine
	fresh bool // no charter stood at this address before this request
	undo  []func() error
}

func (tx *charterTx) push(f func() error) { tx.undo = append(tx.undo, f) }

// imports installs a package's declaration of what it reads, remembering the one it replaced.
func (tx *charterTx) imports(def *ir.Node) (int, error) {
	subs, err := trigger.ReadsFromNode(def)
	if err != nil {
		return http.StatusBadRequest, err
	}
	before := tx.eng.Reads()
	prev, had, err := tx.s.packageDecl(tx.at)
	if err != nil {
		return http.StatusServiceUnavailable, err
	}
	if err := tx.s.setPackageImports(tx.at, subs); err != nil {
		return http.StatusBadRequest, err
	}
	tx.push(func() error {
		tx.eng.SetReads(before)
		return tx.s.putPackageDecl(tx.at, prev, had)
	})
	return http.StatusCreated, nil
}

// stratum installs one level, remembering the one it replaced.
func (tx *charterTx) stratum(def *ir.Node) (int, error) {
	var st trigger.Stratum
	if err := codec.FromNode(def, &st); err != nil {
		return http.StatusBadRequest, err
	}
	old, had := tx.eng.Stratum(st.Name)
	if err := tx.eng.InstallStratum(st); err != nil {
		return charterCode(err), err
	}
	tx.push(func() error {
		if had {
			return tx.eng.InstallStratum(old)
		}
		return tx.eng.RemoveStratum(st.Name)
	})
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
	old, had := tx.eng.Rule(t.Name)
	if err := tx.eng.InstallTrigger(t); err != nil {
		return charterCode(err), err
	}
	tx.push(func() error {
		tx.eng.RemoveTrigger(t.Name)
		if had {
			return tx.eng.AddTrigger(old)
		}
		return nil
	})
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
	// and a later install at the same address would find it rather than make one.
	if tx.fresh {
		tx.s.dropPackageEngine(tx.at)
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
	if err := s.removePackage(at, r.URL.Query().Get("recurse") == "true"); err != nil {
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
	if len(rest) != 2 || (rest[0] != "rules" && rest[0] != "strata") {
		http.Error(w, "a removal names one rule or one level — `<address> rules <name>` or "+
			"`<address> strata <name>` — and a path to an address alone takes the whole "+
			"package down. "+strings.Join(rest, ".")+" is neither", http.StatusBadRequest)
		return
	}
	kind, name := rest[0], rest[1]
	eng, err := s.engineAt(r, at)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if eng == nil {
		http.Error(w, s.charterPathNotFound(at, rest), http.StatusNotFound)
		return
	}
	if kind == "rules" {
		if eng.RemoveTrigger(name) {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		http.Error(w, s.charterPathNotFound(at, rest), http.StatusNotFound)
		return
	}
	switch err := eng.RemoveStratum(name); {
	case err == nil:
		w.WriteHeader(http.StatusNoContent)
	case errors.Is(err, trigger.ErrNoStratum):
		http.Error(w, s.charterPathNotFound(at, rest), http.StatusNotFound)
	default:
		http.Error(w, err.Error(), http.StatusBadRequest)
	}
}

// engineAt is the engine holding the charter at an address — a scope's when one is asked for,
// since a scope's charter is its own copy of the verse rather than a place in this one.
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
	case has["reads"] || has["at"]:
		return "reads"
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
