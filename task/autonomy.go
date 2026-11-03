package task

import (
	"errors"
	"fmt"
	"maps"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/signadot/tony-format/go-tony/gomap"
	"github.com/signadot/tony-format/go-tony/ir"

	"github.com/signadot/verse/systems"
)

// AN AUTONOMY IS ONE ORDERED LIST OVER EVERY CALL A TASK MAKES. Each row names a call by its
// system, its verb, and a tony match pattern over each field it names, read by the field's form
// (systems.MatchField), and says what happens to it: `deny` refuses it with the row's name and asks nobody, `ask` poses the question a
// gate poses and the answer decides, and `allow` lets it go. The first row that matches a call
// decides it, and the last row, which names no call, decides every call no row above matched:
// the task's dial, `ask` when there is no autonomy.
//
// AN ADDRESS IS READ TOWARD REFUSING. A call at an address reaches everything under it, so a row
// that allows matches one whose reach lies within its `path`, and a row that denies or asks one
// whose reach touches it (Row.reading). With `deny [play, secret]` above `allow [play]`, a read of
// `play` or `play.*` is denied, and with `allow [scratch]` first, a read of the root is not
// allowed.
//
// THE AUTONOMY DECIDES, AND DOES NOT REACH. What a task may do at all is its environment's
// (systems.Environment): a step outside it is refused before any row is read, and a row only
// decides among the steps the environment gives. So an autonomy is how much of that a task does
// unasked, and the default row is the one place a run is made unattended.
//
//	autonomy:
//	- {match: {system: git, verb: push, ref: refs/heads/main}, then: deny}
//	- {match: {system: git, verb: push, ref: !glob "refs/heads/verse/auto/*"}, then: allow}
//	- {match: {system: git, verb: push}, then: {ask: {need: 2, answerers: [group:release], within: 4h}}}
//	- {then: ask}
//
// It is the launch's, and it can be replaced while the task runs (PUT /tasks/{id}/autonomy); the
// change applies from the next call.
type Autonomy []Row

// Row is one line of an autonomy: which calls, and what happens to them.
//
//tony:schemagen=verse-task-policy-row,notag
type Row struct {
	// Name names the row in the questions it poses and on the record, and is what a hook's
	// `onDenied` refers to. Absent, the row is named by its place: `autonomy[2]`.
	Name string `tony:"field=name,omitzero"`
	// Match is the calls the row is about. Absent, every call: the default row, which is last.
	Match *Match `tony:"field=match,omitzero"`
	// Then is what happens to a call the row matches.
	Then Then `tony:"field=then"`
}

// Match is the calls a row is about, a tony match over the step written flat:
// `{system: git, verb: push, ref: refs/heads/main}`. Every row but the default names its
// system; a verb left out matches any of the system's, and one may be a pattern over their names
// (`verb: !or [data-put, data-rm]`); a param is a pattern over the step's field of that name, in
// tony's grammar (`ref: !glob "refs/heads/verse/*"`, `force: true`, `command: !regexp "git
// push"`; systems.MatchField), an absent field meeting none of them. A param the verbs do not
// have is refused, and so is an expression, `$[…]`: a match is a pattern over what the task does,
// not bound by the firing.
//
//tony:schemagen=verse-task-policy-match,notag,codec=custom
type Match struct {
	System string
	// Verb is the verb the row names by name; empty where it names any, or a pattern.
	Verb string
	// VerbPattern is the verbs the row names by a pattern over their names, nil where it names
	// one or any.
	VerbPattern *ir.Node
	Params      map[string]*ir.Node
}

// Then is what a row does with a call: `deny`, `ask` or `allow`, or `{ask: {…}}`,
// an ask with its shape (Ask).
//
//tony:schemagen=verse-task-policy-then,notag,codec=custom
type Then struct {
	Mode string
	// Ask is the ask's shape, with `then: {ask: {…}}`; nil with a bare `then: ask`.
	Ask *Ask
}

// The three things a row does with a call, decided at each call and the autonomy's to change
// while the task runs.
const (
	ThenDeny  = "deny"
	ThenAsk   = "ask"
	ThenAllow = "allow"
)

// Ask is how an `ask` row asks: what the question takes to settle, who is asked, how long it
// stands, and what verse adds to it. THE RUNNER CARRIES THE ROW WHOLE AND READS THREE OF THESE:
// `need`, `answerers` and `within`. `default` and `labels` are verse's, which keeps the clock
// and the docket: a runner poses the question without them, and verse adds them when it
// records it. One shape, in the charter, on the wire, on the record and in a slide.
//
//tony:schemagen=verse-task-policy-ask,notag
type Ask struct {
	// Need is how many approvals the call takes. Absent is one.
	Need int `tony:"field=need,omitzero"`
	// Answerers are who is asked, in the grammar a rule's gate uses (`user:…`, `group:…`,
	// `$principal`). Absent is the docket.
	Answerers []string `tony:"field=answerers,omitzero"`
	// Within is how long the question stands, as a duration; after it, Default.
	Within string `tony:"field=within,omitzero"`
	// Default is what silence settles on when Within passes: `deny`, `proceed`. Verse's: the
	// runner never reads it.
	Default string `tony:"field=default,omitzero"`
	// Labels are what the question says about itself for whoever watches asks: `team:
	// payments`, `slack: C0C3KHG9B0W`. Verse's: the runner never reads them.
	Labels map[string]string `tony:"field=labels,omitzero"`
}

// Clone is a copy that aliases nothing of the original.
func (p Autonomy) Clone() Autonomy {
	if len(p) == 0 {
		return nil
	}
	out := make(Autonomy, 0, len(p))
	for _, r := range p {
		nr := Row{Name: r.Name, Then: Then{Mode: r.Then.Mode}}
		if r.Match != nil {
			nr.Match = r.Match.clone()
		}
		if a := r.Then.Ask; a != nil {
			nr.Then.Ask = &Ask{Need: a.Need, Answerers: slices.Clone(a.Answerers), Within: a.Within, Default: a.Default, Labels: maps.Clone(a.Labels)}
		}
		out = append(out, nr)
	}
	return out
}

// Within is why p lets a task do more than outer lets its own, over the calls env gives, or nil.
// Each row of p is held to every row of outer that could decide a call p's row matches, in
// outer's order, until one of outer's covers p's row whole: each must be at least as strict, deny
// below ask below allow. A row of p no row of outer covers is held to outer's default too. Left
// out are outer's rows a row of p above this one covers whole, since no call of theirs reaches
// this row, and outer's rows on calls env does not give, since no call of theirs is this task's.
//
// Two rows could decide the same call where their systems agree, their verbs share one, and
// each param they both name has patterns that meet (systems.FieldMeetsAs); one covers the other
// where it names no param the other does not, its verbs hold the other's, and each of its
// patterns holds the other's (systems.FieldContainsAs), each read as its row reads an address.
// Reading patterns against patterns, not against a call, is what makes this sound for any
// operator on either side: one the analysis does not read is held to be within another only
// where the two are the same.
func (p Autonomy) Within(outer Autonomy, env systems.Environment) error {
	outer = outer.OrDefault()
	inner := p.OrDefault()
	for i, r := range inner {
		covered := false
		for _, o := range outer {
			if !o.meets(r) || decidedAbove(inner[:i], o) || !reaches(env, o.Match) {
				continue
			}
			if modeRank(r.Then.Mode) > modeRank(o.Then.Mode) {
				return fmt.Errorf("[%d]: `then: %s` on %s, where the autonomy it must lie within says %s on %s",
					i, r.Then.Mode, r.Match.words(), o.Then.Mode, o.Match.words())
			}
			if o.encloses(r) {
				covered = true
				break
			}
		}
		if !covered && modeRank(r.Then.Mode) > modeRank(outer.Default()) {
			return fmt.Errorf("[%d]: `then: %s` on %s, where the autonomy it must lie within says %s",
				i, r.Then.Mode, r.Match.words(), outer.Default())
		}
	}
	return nil
}

// modeRank orders a row's modes by what they let through: deny, then ask, then allow. A mode
// that is none of these is read as allow, so it is never the one that passes a check it should not.
func modeRank(mode string) int {
	switch mode {
	case ThenDeny:
		return 0
	case ThenAsk:
		return 1
	}
	return 2
}

// decidedAbove is whether a row among rows covers every call o matches, so none of them reaches a
// row below.
func decidedAbove(rows Autonomy, o Row) bool {
	for _, q := range rows {
		if q.encloses(o) {
			return true
		}
	}
	return false
}

// reaches is whether env gives some call m matches: every call, for a match naming no system.
func reaches(env systems.Environment, m *Match) bool {
	if m.Any() || m.System == "" {
		return true
	}
	return env.Reaches(m.System, m.VerbNames(), m.Params)
}

// reading is how the row reads an address (systems.Reading): within its pattern for a row that
// allows, touching it for one that denies or asks, so each errs toward refusing. A mode that is
// none of these is read as allow, as modeRank reads it.
func (r Row) reading() systems.Reading {
	if r.Then.Mode == ThenDeny || r.Then.Mode == ThenAsk {
		return systems.Touches
	}
	return systems.Within
}

// meets is whether some call could be matched by both r and o: their systems and verbs agree
// where both name one, and each param both name has patterns that meet.
func (r Row) meets(o Row) bool {
	m, n := r.Match, o.Match
	if m.Any() || n.Any() {
		return true
	}
	if m.System != "" && n.System != "" && m.System != n.System {
		return false
	}
	if mv, nv := m.VerbNames(), n.VerbNames(); mv != nil && nv != nil && !slices.ContainsFunc(mv, func(v string) bool { return slices.Contains(nv, v) }) {
		return false
	}
	verbs := m.VerbNames()
	for name, mp := range m.Params {
		if np, ok := n.Params[name]; ok && !systems.FieldMeetsAs(m.formIn(verbs, name), r.reading(), mp, o.reading(), np) {
			return false
		}
	}
	return true
}

// encloses is whether every call o matches, r matches too: r names no system or param o does not,
// its verbs hold o's, and each pattern of r's holds o's for the same param.
func (r Row) encloses(o Row) bool {
	m, n := r.Match, o.Match
	if m.Any() {
		return true
	}
	if n.Any() {
		return false
	}
	if m.System != "" && m.System != n.System {
		return false
	}
	if mv := m.VerbNames(); mv != nil {
		nv := n.VerbNames()
		if nv == nil || slices.ContainsFunc(nv, func(v string) bool { return !slices.Contains(mv, v) }) {
			return false
		}
	}
	verbs := m.VerbNames()
	for name, mp := range m.Params {
		np, ok := n.Params[name]
		if !ok || !systems.FieldContainsAs(m.formIn(verbs, name), r.reading(), mp, o.reading(), np) {
			return false
		}
	}
	return true
}

// form is a param's form, as the first of the match's verbs that carries it says (systems.FieldOf);
// empty, any text, where none does.
func (m *Match) form(name string) string { return m.formIn(m.VerbNames(), name) }

// formIn is form, the match's verbs given.
func (m *Match) formIn(verbs []string, name string) string {
	for _, v := range verbs {
		if f, ok := systems.FieldOf(systems.StepName(m.System, v), name); ok {
			return f.Form
		}
	}
	return ""
}

// VerbNames is the verbs of its system the match names: its verb, those its pattern matches, or
// nil for any.
func (m *Match) VerbNames() []string {
	switch {
	case m == nil || m.System == "":
		return nil
	case m.Verb != "":
		return []string{m.Verb}
	case m.VerbPattern == nil:
		if d, ok := systems.Get(m.System); ok {
			return verbNames(d)
		}
		return nil
	}
	d, _ := systems.Get(m.System)
	var out []string
	for _, v := range d.Verbs {
		if systems.MatchField("", m.VerbPattern, ir.FromString(v.Name)) {
			out = append(out, v.Name)
		}
	}
	if out == nil {
		out = []string{}
	}
	return out
}

// clone is a copy that aliases nothing of m.
func (m *Match) clone() *Match {
	out := &Match{System: m.System, Verb: m.Verb, VerbPattern: cloneNode(m.VerbPattern)}
	if m.Params != nil {
		out.Params = make(map[string]*ir.Node, len(m.Params))
		for k, v := range m.Params {
			out.Params[k] = cloneNode(v)
		}
	}
	return out
}

// cloneNode is a copy of n, nil for nil.
func cloneNode(n *ir.Node) *ir.Node {
	if n == nil {
		return nil
	}
	return n.Clone()
}

// words is a match in words, for a refusal that names it: `every call` for one naming nothing.
func (m *Match) words() string {
	if m.Any() {
		return "every call"
	}
	parts := []string{}
	switch {
	case m.System != "" && m.Verb != "":
		parts = append(parts, systems.StepName(m.System, m.Verb))
	case m.System != "" && m.VerbPattern != nil:
		parts = append(parts, m.System, "verb="+systems.Words(m.VerbPattern))
	case m.System != "":
		parts = append(parts, m.System)
	}
	for _, k := range slices.Sorted(maps.Keys(m.Params)) {
		parts = append(parts, k+"="+systems.Words(m.Params[k]))
	}
	return strings.Join(parts, " ")
}

// DefaultAutonomy is what a task with no autonomy runs under: the default row alone, `then: ask`, so
// every step its environment gives is asked about.
func DefaultAutonomy() Autonomy { return Autonomy{{Then: Then{Mode: ThenAsk}}} }

// OrDefault is the autonomy, or DefaultAutonomy where there is none.
func (p Autonomy) OrDefault() Autonomy {
	if len(p) == 0 {
		return DefaultAutonomy()
	}
	return p
}

// Any reports whether the match covers every call: no system, no verb, no param.
func (m *Match) Any() bool {
	return m == nil || (m.System == "" && m.Verb == "" && m.VerbPattern == nil && len(m.Params) == 0)
}

// Matches reports whether the row covers a call, reading an address as the row does (reading).
func (r Row) Matches(s Step) bool { return r.Match.matches(s, r.reading()) }

// matches is whether the match covers a call, an address read as rd.
func (m *Match) matches(s Step, rd systems.Reading) bool {
	if m.Any() {
		return true
	}
	if !m.covers(s.Name) {
		return false
	}
	for name, pattern := range m.Params {
		if !systems.MatchFieldAs(systems.FormOf(s.Name, name), rd, pattern, s.Field(name)) {
			return false
		}
	}
	return true
}

// covers reports whether the match's system and verb cover a call's name, params aside.
func (m *Match) covers(step string) bool {
	if m.Any() {
		return true
	}
	system, verb, _ := systems.Split(step)
	if m.System != "" && m.System != system {
		return false
	}
	if m.Verb != "" {
		return m.Verb == verb
	}
	return m.VerbPattern == nil || systems.MatchField("", m.VerbPattern, ir.FromString(verb))
}

// Decide is the row that decides a call: the first that matches it, and its place. An autonomy
// whose rows name nothing that matches decides as DefaultAutonomy does. Decide reads rows alone,
// as a ceiling's are read.
func (p Autonomy) Decide(s Step) (Row, int) {
	for i, r := range p {
		if r.Matches(s) {
			return r, i
		}
	}
	return DefaultAutonomy()[0], -1
}

// NameOf is what the row at i is called in a question: its name, or its place, `autonomy[i]`. The
// decision no row made (Decide's -1) is DefaultAutonomy's, and is called `autonomy[default]`, which no
// row can be named (a name has no brackets).
func (p Autonomy) NameOf(i int) string {
	if i < 0 {
		return defaultRowName
	}
	if i < len(p) && p[i].Name != "" {
		return p[i].Name
	}
	return fmt.Sprintf("autonomy[%d]", i)
}

// defaultRowName is what the decision no row made is called (NameOf, Named).
const defaultRowName = "autonomy[default]"

// Named is the row by the name a question carries (NameOf), if the autonomy has it; the decision
// no row made resolves to DefaultAutonomy's row.
func (p Autonomy) Named(name string) (Row, bool) {
	if name == defaultRowName {
		return DefaultAutonomy()[0], true
	}
	for i, r := range p {
		if p.NameOf(i) == name {
			return r, true
		}
	}
	return Row{}, false
}

// Default is what the autonomy's last row does: the task's dial.
func (p Autonomy) Default() string {
	if len(p) == 0 {
		return ThenAsk
	}
	return p[len(p)-1].Then.Mode
}

// MayAsk reports whether a call of a step may be stopped: some row that could match it is not
// an `allow`. Whether it is stopped this time depends on the call's fields.
func (p Autonomy) MayAsk(step string) bool {
	for _, r := range p.OrDefault() {
		if !r.Match.covers(step) {
			continue
		}
		if r.Then.Mode != ThenAllow {
			return true
		}
		if r.Match.Any() || len(r.Match.Params) == 0 {
			return false // allowed whatever its fields
		}
	}
	return true
}

// About is the rows that could decide a call of a step, in words, for a brief: each row's
// params and what it does, in order, ending with the row every other call gets.
func (p Autonomy) About(step string) string {
	var parts []string
	for i, r := range p.OrDefault() {
		if !r.Match.covers(step) {
			continue
		}
		what := r.Then.Mode
		switch what {
		case ThenAsk:
			what = "asks first"
		case ThenAllow:
			what = "allowed"
		default:
			what = "refused"
		}
		if r.Match.Any() {
			parts = append(parts, "the rest "+what)
			if r.Match.Any() {
				break
			}
			continue
		}
		var fs []string
		for _, k := range r.sortedParams() {
			fs = append(fs, k+"="+systems.Words(r.Match.Params[k]))
		}
		parts = append(parts, strings.Join(fs, " ")+": "+what+" ("+p.NameOf(i)+")")
	}
	return strings.Join(parts, "; ")
}

func (r Row) sortedParams() []string { return r.Match.sortedParams() }

// Summary is the autonomy in one line, for the event that says it was replaced: how many rows,
// and the dial.
func (p Autonomy) Summary() string {
	p = p.OrDefault()
	return fmt.Sprintf("%d row(s), default %s", len(p), p.Default())
}

// NamesPrincipal reports whether any row's ask names PrincipalAnswerer.
func (p Autonomy) NamesPrincipal() bool {
	return slices.ContainsFunc(p, func(r Row) bool { return r.Then.Ask != nil && NamesPrincipal(r.Then.Ask.Answerers) })
}

// Check is what an autonomy has to satisfy to be installed: each row's shape against the systems'
// documents, and a last row that names no call, so every call is decided. A row before it that
// matches every call would decide for the rows after it, and is refused as the mistake it is.
//
// A refusal names a row by its place alone, `[2]: …`, or the whole as `: …`, for the caller to
// say whose rows they are: `autonomy[2]: …`, `systems.git.ceiling[2]: …`.
func (p Autonomy) Check() error {
	named := map[string]int{}
	for i, r := range p {
		if err := r.Check(); err != nil {
			return fmt.Errorf("[%d]: %w", i, err)
		}
		// A question carries its row's name, and verse reads the row's ask by it: two rows of
		// one name would pose one's question under the other's ask.
		if r.Name != "" {
			if at, dup := named[r.Name]; dup {
				return fmt.Errorf("[%d]: the name %q is row %d's; a row's name is its own", i, r.Name, at)
			}
			named[r.Name] = i
		}
		if r.Match.Any() && i != len(p)-1 {
			return fmt.Errorf("[%d]: matches every call, so the rows after it never decide; the row "+
				"with no match is the default, and it is last", i)
		}
	}
	if len(p) > 0 && !p[len(p)-1].Match.Any() {
		return errors.New(": the last row names a call; the list ends with its default row, one " +
			"with no match (`{then: ask}`), which decides the calls of the verbs named above that no row matched")
	}
	return nil
}

// notRowGates is the gates of the questions that are no row's: a conversation's turn (the loop),
// codex's own question, the `question-ask` tool's, and a step the environment refused
// (EnvironmentGate). A question carries its row's name as its gate, and verse reads the row's ask
// by it, so no row takes one of these.
var notRowGates = []string{"turn", "ask", systems.Tool("question", "ask"), EnvironmentGate}

// EnvironmentGate is the gate a step outside the environment is refused under: no row decided
// it, and nobody was asked (ByEnvironment).
const EnvironmentGate = "environment"

// Check is what a row has to satisfy: a `then`, an ask's shape only on an ask, and a match the
// documents describe.
func (r Row) Check() error {
	switch r.Then.Mode {
	case ThenDeny, ThenAsk, ThenAllow:
	case "":
		return errors.New("then: a row says what happens to the calls it matches: deny, ask or allow")
	default:
		return fmt.Errorf("then: %q is not one of deny, ask, allow", r.Then.Mode)
	}
	// A connection does not wait for a person: the proxy decides it as it is opened, and the
	// client on the other end gives up long before an answer could come.
	if r.Match != nil && r.Match.System == "network" && r.Then.Mode == ThenAsk {
		return errors.New("then: a row on network allows or denies; a connection can't wait for an answer")
	}
	if a := r.Then.Ask; a != nil {
		if r.Then.Mode != ThenAsk {
			return fmt.Errorf("then: an ask's shape (need, answerers, within) goes with `ask`, not %s", r.Then.Mode)
		}
		if a.Need < 0 {
			return fmt.Errorf("then.ask.need: %d is not a number of approvals", a.Need)
		}
		if a.Within != "" {
			if d, err := time.ParseDuration(a.Within); err != nil || d <= 0 {
				return fmt.Errorf("then.ask.within: %q is not a duration", a.Within)
			}
		}
		if a.Default != "" && a.Default != Proceed && a.Default != Deny {
			return fmt.Errorf("then.ask.default: %q is not one of the question's answers, %s or %s", a.Default, Proceed, Deny)
		}
		if err := CheckLabels(a.Labels); err != nil {
			return fmt.Errorf("then.ask.%w", err)
		}
	}
	if strings.ContainsFunc(r.Name, func(c rune) bool { return c == ' ' || c == '[' || c == ']' }) {
		return fmt.Errorf("name: %q: a row's name is a word", r.Name)
	}
	if slices.Contains(notRowGates, r.Name) {
		return fmt.Errorf("name: %q names a question that is no row's (%s); name the row otherwise",
			r.Name, strings.Join(notRowGates, ", "))
	}
	return r.Match.Check()
}

// Check is what a match has to satisfy: a system the documents describe, a verb it has or a
// pattern naming some of them, and fields of the steps it covers (package systems: a block's
// attributes and a verb's fields), each pattern one a value of the field could meet
// (systems.Field.Accepts). A verb is named within its system: `close` is a different call in
// each tracker, and a row on one says which.
func (m *Match) Check() error {
	if m.Any() {
		return nil
	}
	if m.System == "" {
		return errors.New("match: a row names its system, as `{system: git, verb: push}` does; only the " +
			"default row, last, names none")
	}
	d, ok := systems.Get(m.System)
	if !ok {
		return fmt.Errorf("match.system: %q is not a system (they are: %s)", m.System, strings.Join(systemNames(), ", "))
	}
	if m.Verb != "" {
		if _, ok := d.Verb(m.Verb); !ok {
			return fmt.Errorf("match.verb: %s has no verb %q (it has: %s)", m.System, m.Verb, strings.Join(verbNames(d), ", "))
		}
	}
	if m.VerbPattern != nil {
		names := systems.Field{Name: "verb", Form: systems.FormText, Values: verbNames(d)}
		if err := names.Accepts(m.VerbPattern); err != nil {
			return fmt.Errorf("match.verb: %w", err)
		}
		if len(m.VerbNames()) == 0 {
			return fmt.Errorf("match.verb: %s names none of %s's verbs (%s), so the row would never match",
				systems.Words(m.VerbPattern), m.System, strings.Join(verbNames(d), ", "))
		}
	}
	params := map[string][]systems.Field{}
	for _, v := range m.VerbNames() {
		fields, _ := d.StepFields(v)
		for _, f := range fields {
			params[f.Name] = append(params[f.Name], f)
		}
	}
	for _, name := range slices.Sorted(maps.Keys(m.Params)) {
		forms, ok := params[name]
		if !ok {
			return fmt.Errorf("match.%s: not a field of the calls the row names (they have: %s)",
				name, strings.Join(slices.Sorted(maps.Keys(params)), ", "))
		}
		if name == matchSystem || name == matchVerb {
			return fmt.Errorf("match.%s: %s and %s name the call, and no field is called either", name, matchSystem, matchVerb)
		}
		pattern := m.Params[name]
		if systems.Binds(pattern) {
			return fmt.Errorf("match.%s: %s: a match is a pattern over what the task does and is not bound, "+
				"so an expression in it would never match", name, systems.Words(pattern))
		}
		// A pattern no call's field could be spelled as never matches, which in a deny row lets
		// through what it was written to stop: refused, unless one of the calls takes it.
		var refused error
		for _, f := range forms {
			if refused = f.Accepts(pattern); refused == nil {
				break
			}
		}
		if refused != nil {
			return fmt.Errorf("match.%s: %w, so the row would never match", name, refused)
		}
	}
	return nil
}

func systemNames() []string {
	var names []string
	for _, d := range systems.All() {
		names = append(names, d.Name)
	}
	return names
}

func verbNames(d systems.Document) []string {
	var names []string
	for _, v := range d.Verbs {
		names = append(names, v.Name)
	}
	return names
}

// question is what an ask row asks about a call, as the runner poses it: the row's name, the
// call, and the three things it reads of the ask.
func (r Row) question(name string, s Step) GateQuestion {
	q := GateQuestion{Gate: name, Reason: s.String()}
	if a := r.Then.Ask; a != nil {
		q.Answerers = a.Answerers
		if a.Need > 1 {
			q.Options = []Option{{ID: Proceed, Need: a.Need}, {ID: Deny, Need: 1}}
		}
		if a.Within != "" {
			q.Deadline = &Deadline{Within: a.Within}
		}
	}
	return q
}

// ── the wire ──────────────────────────────────────────────────────────────────────────────

// The keys a match carries beside its params.
const (
	matchSystem = "system"
	matchVerb   = "verb"
)

// ToTonyIR writes the match flat: system, verb, then the params by name.
func (m Match) ToTonyIR(opts ...gomap.MapOption) (*ir.Node, error) {
	var kvs []ir.KeyVal
	if m.System != "" {
		kvs = append(kvs, ir.KeyVal{Key: ir.FromString(matchSystem), Val: ir.FromString(m.System)})
	}
	switch {
	case m.Verb != "":
		kvs = append(kvs, ir.KeyVal{Key: ir.FromString(matchVerb), Val: ir.FromString(m.Verb)})
	case m.VerbPattern != nil:
		kvs = append(kvs, ir.KeyVal{Key: ir.FromString(matchVerb), Val: m.VerbPattern.Clone()})
	}
	for _, name := range slices.Sorted(maps.Keys(m.Params)) {
		v := m.Params[name]
		if v == nil {
			v = ir.Null()
		}
		kvs = append(kvs, ir.KeyVal{Key: ir.FromString(name), Val: v.Clone()})
	}
	return ir.FromKeyVals(kvs), nil
}

// FromTonyIR reads the flat form: `system` a name, `verb` a name or a pattern, and every other
// key a param, a pattern as written, `null` any value the step carries.
func (m *Match) FromTonyIR(node *ir.Node, opts ...gomap.UnmapOption) error {
	node = systems.Plain(ir.Uncomment(node))
	if node == nil || node.Type == ir.NullType {
		*m = Match{}
		return nil
	}
	if node.Type != ir.ObjectType || node.Tag != "" {
		return errors.New("match: an object, `{system: git, verb: push, ref: refs/heads/main}`")
	}
	out := Match{}
	for i, key := range node.Fields {
		key = ir.Uncomment(key)
		if key == nil || key.Type != ir.StringType {
			return errors.New("match: a key is a name")
		}
		val := ir.Uncomment(node.Values[i])
		if val == nil {
			val = ir.Null()
		}
		switch key.String {
		case matchSystem:
			if val.Tag != "" || val.Type != ir.StringType {
				return errors.New("match.system: a system by its name, `git`")
			}
			out.System = val.String
		case matchVerb:
			switch {
			case val.Tag == "" && val.Type == ir.StringType:
				out.Verb = val.String
			case val.Tag == "" && val.Type == ir.NullType:
				return errors.New("match.verb: null is any verb; leave the key out to match any")
			default:
				out.VerbPattern = systems.Plain(val)
			}
		default:
			if out.Params == nil {
				out.Params = map[string]*ir.Node{}
			}
			out.Params[key.String] = systems.Plain(val)
		}
	}
	*m = out
	return nil
}

// ToTonyIR writes `deny`, `ask` or `allow`, or `{ask: {…}}` for an ask with a shape.
func (t Then) ToTonyIR(opts ...gomap.MapOption) (*ir.Node, error) {
	if t.Ask == nil {
		return ir.FromString(t.Mode), nil
	}
	ask, err := gomap.ToTonyIR(*t.Ask)
	if err != nil {
		return nil, err
	}
	return ir.FromKeyVals([]ir.KeyVal{{Key: ir.FromString(ThenAsk), Val: ask}}), nil
}

// FromTonyIR reads a word, or `{ask: {…}}`, the ask strictly.
func (t *Then) FromTonyIR(node *ir.Node, opts ...gomap.UnmapOption) error {
	node = ir.Uncomment(node)
	if node == nil || node.Type == ir.NullType {
		*t = Then{}
		return nil
	}
	switch node.Type {
	case ir.StringType:
		*t = Then{Mode: node.String}
		return nil
	case ir.ObjectType:
		if len(node.Fields) != 1 {
			return errors.New("then: `deny`, `ask`, `allow`, or `{ask: {need, answerers, within, default, labels}}`")
		}
		key := ir.Uncomment(node.Fields[0])
		if key == nil || key.Type != ir.StringType || key.String != ThenAsk {
			return errors.New("then: only `ask` takes a shape: `{ask: {need, answerers, within, default, labels}}`")
		}
		// The ask is read strictly wherever the autonomy is read: a misspelled `need` or
		// `answerers` dropped would ask fewer people, or other ones, than the row says.
		var a Ask
		if val := ir.Uncomment(node.Values[0]); val != nil && val.Type != ir.NullType {
			if err := gomap.FromTonyIR(val, &a, append(slices.Clone(opts), gomap.Strict())...); err != nil {
				return fmt.Errorf("then.ask: %w", err)
			}
		}
		*t = Then{Mode: ThenAsk, Ask: &a}
		return nil
	}
	return errors.New("then: `deny`, `ask`, `allow`, or `{ask: {…}}`")
}

// sortedParams is a match's params by name, for a reader.
func (m *Match) sortedParams() []string {
	if m == nil {
		return nil
	}
	keys := slices.Collect(maps.Keys(m.Params))
	sort.Strings(keys)
	return keys
}
