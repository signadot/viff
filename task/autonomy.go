package task

import (
	"errors"
	"fmt"
	"maps"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/signadot/tony-format/go-tony/gomap"
	"github.com/signadot/tony-format/go-tony/ir"

	"github.com/signadot/verse/systems"
)

// AN AUTONOMY IS ONE ORDERED LIST OVER EVERY CALL A TASK MAKES. Each row names a call by its
// system, its verb and the step's fields as patterns (package systems), and says what happens
// to it: `deny` refuses it with the row's name and asks nobody, `ask` poses the question a
// gate poses and the answer decides, and `allow` lets it go. The first row that matches a call
// decides it, and the last row, which names no call, decides every call no row above matched:
// the task's dial, `ask` when there is no autonomy.
//
// THE AUTONOMY DECIDES, AND DOES NOT REACH. What a task may do at all is its environment's
// (systems.Environment): a step outside it is refused before any row is read, and a row only
// decides among the steps the environment gives. So an autonomy is how much of that a task does
// unasked, and the default row is the one place a run is made unattended.
//
//	autonomy:
//	- {match: {system: git, verb: push, ref: refs/heads/main}, then: deny}
//	- {match: {system: git, verb: push, ref: refs/heads/verse/auto/*}, then: allow}
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

// Match is the calls a row is about, written flat:
// `{system: git, verb: push, ref: refs/heads/main}`. Every row but the default names its system;
// a verb left out matches any of the system's; a param is a pattern over the call's field of
// that name, `*` within a segment and `**` across, and a command matching as a whole string. A
// param the verb does not have is refused, and so is a `$`: a match is a pattern over what the
// task does, not bound by the firing.
//
//tony:schemagen=verse-task-policy-match,notag,codec=custom
type Match struct {
	System string
	Verb   string
	Params map[string]string
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
			nr.Match = &Match{System: r.Match.System, Verb: r.Match.Verb, Params: maps.Clone(r.Match.Params)}
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
// Two matches could decide the same call where their systems and verbs agree, or one names none,
// and each param they both name has patterns that meet (systems.Intersect); one covers the other
// where it names no more than the other does and each of its patterns contains the other's
// (systems.PatternContains). Reading patterns against patterns, not against a call, is what makes
// this sound for a `*` on either side.
func (p Autonomy) Within(outer Autonomy, env systems.Environment) error {
	outer = outer.OrDefault()
	inner := p.OrDefault()
	for i, r := range inner {
		covered := false
		for _, o := range outer {
			if !o.Match.meets(r.Match) || decidedAbove(inner[:i], o.Match) || !reaches(env, o.Match) {
				continue
			}
			if modeRank(r.Then.Mode) > modeRank(o.Then.Mode) {
				return fmt.Errorf("[%d]: `then: %s` on %s, where the autonomy it must lie within says %s on %s",
					i, r.Then.Mode, r.Match.words(), o.Then.Mode, o.Match.words())
			}
			if o.Match.encloses(r.Match) {
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

// decidedAbove is whether a row among rows covers every call m matches, so none of them reaches a
// row below.
func decidedAbove(rows Autonomy, m *Match) bool {
	for _, q := range rows {
		if q.Match.encloses(m) {
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
	return env.Reaches(m.System, m.Verb, m.Params)
}

// meets is whether some call could be matched by both m and n: their systems and verbs agree
// where both name one, and each param both name has patterns that meet.
func (m *Match) meets(n *Match) bool {
	if m.Any() || n.Any() {
		return true
	}
	if (m.System != "" && n.System != "" && m.System != n.System) || (m.Verb != "" && n.Verb != "" && m.Verb != n.Verb) {
		return false
	}
	for name, mp := range m.Params {
		if np, ok := n.Params[name]; ok && !systems.Intersect(mp, np, m.sep(name)) {
			return false
		}
	}
	return true
}

// encloses is whether every call n matches, m matches too: m names no system, verb or param n does
// not, and each pattern of m's contains n's for the same param.
func (m *Match) encloses(n *Match) bool {
	if m.Any() {
		return true
	}
	if n.Any() {
		return false
	}
	if (m.System != "" && m.System != n.System) || (m.Verb != "" && m.Verb != n.Verb) {
		return false
	}
	for name, mp := range m.Params {
		np, ok := n.Params[name]
		if !ok || !systems.PatternContains(mp, np, m.sep(name)) {
			return false
		}
	}
	return true
}

// sep is what separates a param's segments, as the match's step says of it (systems.Sep); a
// match naming no verb reads the param as a plain pattern, segmented by `/`.
func (m *Match) sep(name string) byte {
	if m.System == "" || m.Verb == "" {
		return systems.SepPath
	}
	return systems.Sep(systems.StepName(m.System, m.Verb), name)
}

// words is a match in words, for a refusal that names it: `every call` for one naming nothing.
func (m *Match) words() string {
	if m.Any() {
		return "every call"
	}
	parts := []string{}
	if m.System != "" {
		s := m.System
		if m.Verb != "" {
			s = systems.StepName(m.System, m.Verb)
		}
		parts = append(parts, s)
	} else if m.Verb != "" {
		parts = append(parts, "verb "+m.Verb)
	}
	for _, k := range slices.Sorted(maps.Keys(m.Params)) {
		parts = append(parts, k+"="+m.Params[k])
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
	return m == nil || (m.System == "" && m.Verb == "" && len(m.Params) == 0)
}

// Matches reports whether the row's match covers a call.
func (m *Match) Matches(s Step) bool {
	if m.Any() {
		return true
	}
	system, verb, _ := systems.Split(s.Name)
	if (m.System != "" && m.System != system) || (m.Verb != "" && m.Verb != verb) {
		return false
	}
	for name, pattern := range m.Params {
		if !systems.Match(pattern, s.Fields[name], systems.Sep(s.Name, name)) {
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
	return (m.System == "" || m.System == system) && (m.Verb == "" || m.Verb == verb)
}

// Decide is the row that decides a call: the first that matches it, and its place. An autonomy
// whose rows name nothing that matches decides as DefaultAutonomy does. Decide reads rows alone,
// as a ceiling's are read.
func (p Autonomy) Decide(s Step) (Row, int) {
	for i, r := range p {
		if r.Match.Matches(s) {
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
			fs = append(fs, k+"="+r.Match.Params[k])
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

// Check is what a match has to satisfy: a system the documents describe, a verb it has, and
// fields of the steps it covers (package systems: a block's attributes and a verb's fields). A verb is named within its system: `close` is a different call
// in each tracker, and a row on one says which.
func (m *Match) Check() error {
	if m.Any() {
		return nil
	}
	var covered []systems.Verb
	var d systems.Document
	if m.System != "" {
		var ok bool
		d, ok = systems.Get(m.System)
		if !ok {
			return fmt.Errorf("match.system: %q is not a system (they are: %s)", m.System, strings.Join(systemNames(), ", "))
		}
		if m.Verb != "" {
			v, ok := d.Verb(m.Verb)
			if !ok {
				return fmt.Errorf("match.verb: %s has no verb %q (it has: %s)", m.System, m.Verb, strings.Join(verbNames(d), ", "))
			}
			covered = []systems.Verb{v}
		} else {
			covered = d.Verbs
		}
	} else {
		return errors.New("match: a row names its system, as `{system: git, verb: push}` does; only the " +
			"default row, last, names none")
	}
	params := map[string][]systems.Field{}
	for _, v := range covered {
		fields, _ := d.StepFields(v.Name)
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
		if strings.Contains(m.Params[name], "$") {
			return fmt.Errorf("match.%s: %q: a match is a pattern over what the task does and is not bound, "+
				"so a `$` in it would never match", name, m.Params[name])
		}
		// A pattern no call's field could be spelled as never matches, which in a deny row lets
		// through what it was written to stop: refused, unless one of the calls takes it.
		var refused error
		for _, p := range forms {
			if refused = p.Accepts(m.Params[name]); refused == nil {
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
	if m.Verb != "" {
		kvs = append(kvs, ir.KeyVal{Key: ir.FromString(matchVerb), Val: ir.FromString(m.Verb)})
	}
	for _, name := range slices.Sorted(maps.Keys(m.Params)) {
		kvs = append(kvs, ir.KeyVal{Key: ir.FromString(name), Val: ir.FromString(m.Params[name])})
	}
	return ir.FromKeyVals(kvs), nil
}

// FromTonyIR reads the flat form. A param's value is a string, or a boolean or a number as
// one would spell it (`delete: true`).
func (m *Match) FromTonyIR(node *ir.Node, opts ...gomap.UnmapOption) error {
	node = ir.Uncomment(node)
	if node == nil || node.Type == ir.NullType {
		*m = Match{}
		return nil
	}
	if node.Type != ir.ObjectType {
		return errors.New("match: an object, `{system: git, verb: push, ref: refs/heads/main}`")
	}
	out := Match{}
	for i, key := range node.Fields {
		key = ir.Uncomment(key)
		if key == nil || key.Type != ir.StringType {
			return errors.New("match: a key is a name")
		}
		val := ir.Uncomment(node.Values[i])
		var s string
		switch {
		case val == nil || val.Type == ir.NullType:
			return fmt.Errorf("match.%s: null is no pattern; leave the key out to match any value", key.String)
		case val.Type == ir.StringType:
			s = val.String
		case val.Type == ir.BoolType:
			s = fmt.Sprint(val.Bool)
		case val.Type == ir.NumberType:
			s = numberText(val)
		default:
			return fmt.Errorf("match.%s: a pattern is a string", key.String)
		}
		switch key.String {
		case matchSystem:
			out.System = s
		case matchVerb:
			out.Verb = s
		default:
			if out.Params == nil {
				out.Params = map[string]string{}
			}
			out.Params[key.String] = s
		}
	}
	*m = out
	return nil
}

// numberText is a number as the digits it was written with: an integer the parser read into
// Int64 and a float into Float64 leave Number empty.
func numberText(n *ir.Node) string {
	switch {
	case n.Number != "":
		return n.Number
	case n.Int64 != nil:
		return strconv.FormatInt(*n.Int64, 10)
	case n.Float64 != nil:
		return strconv.FormatFloat(*n.Float64, 'g', -1, 64)
	}
	return ""
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
