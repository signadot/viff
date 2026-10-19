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

// A POLICY IS ONE ORDERED LIST OVER EVERY CALL A TASK MAKES. Each row names a call by its
// system, its verb and the verb's params as patterns (package systems), and says what happens
// to it: `deny` refuses it with the row's name and asks nobody, `ask` poses the question a
// gate poses and the answer decides, `allow` lets it go, and `withhold`, on a system or a
// verb and never a call, takes the verb out of the run: the tool is not offered, so the call
// cannot be made, and nothing brings it back while the task runs, whatever row stands above
// it. Otherwise the first row that matches a call decides it, and the last row, which names no
// call, is what the calls of the verbs named above get when no row's params matched them: the
// task's dial.
//
// THE POLICY IS THE TASK'S CEILING. A verb is the task's only if a row names it, by its system
// alone (every verb of it) or with the verb (Provides). A verb no row names is not provided: its
// tool is not offered and a call of it is refused, whatever the default row says. A task with no
// policy, or with the default row alone, is given no verb of any system.
//
//	policy:
//	- {match: {system: git, verb: push, ref: refs/heads/main}, then: deny}
//	- {match: {system: git, verb: push, ref: refs/heads/verse/auto/*}, then: allow}
//	- {match: {system: git, verb: push}, then: {ask: {need: 2, answerers: [group:release], within: 4h}}}
//	- {then: ask}
//
// It is the launch's, and it can be replaced while the task runs (PUT /tasks/{id}/policy); the
// change applies from the next call.
type Policy []Row

// Row is one line of a policy: which calls, and what happens to them.
//
//tony:schemagen=verse-task-policy-row,notag
type Row struct {
	// Name names the row in the questions it poses and on the record, and is what a hook's
	// `onDenied` refers to. Absent, the row is named by its place: `policy[2]`.
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

// Then is what a row does with a call: `deny`, `ask`, `allow` or `withhold`, or `{ask: {…}}`,
// an ask with its shape (Ask).
//
//tony:schemagen=verse-task-policy-then,notag,codec=custom
type Then struct {
	Mode string
	// Ask is the ask's shape, with `then: {ask: {…}}`; nil with a bare `then: ask`.
	Ask *Ask
}

// The four things a row does with a call. The first three are decisions, made at each call
// and the policy's to change while the task runs; a withhold is the verb's absence, fixed
// when the run starts (KeepsWithheld).
const (
	ThenDeny     = "deny"
	ThenAsk      = "ask"
	ThenAllow    = "allow"
	ThenWithhold = "withhold"
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
func (p Policy) Clone() Policy {
	if len(p) == 0 {
		return nil
	}
	out := make(Policy, 0, len(p))
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

// DefaultPolicy is what a task with no policy runs under: the default row alone, `then: ask`,
// which provides no verb.
func DefaultPolicy() Policy { return Policy{{Then: Then{Mode: ThenAsk}}} }

// OrDefault is the policy, or DefaultPolicy where there is none.
func (p Policy) OrDefault() Policy {
	if len(p) == 0 {
		return DefaultPolicy()
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
		if !fieldMatch(pattern, s.Fields[name], name == "command") {
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

// Decide is the row that decides a call: the first that matches it, and its place. A policy
// whose rows name nothing that matches decides as DefaultPolicy does. Decide reads rows alone,
// as a ceiling's are read; a task's policy refuses first what it does not provide (Provides).
func (p Policy) Decide(s Step) (Row, int) {
	for i, r := range p {
		if r.Match.Matches(s) {
			return r, i
		}
	}
	return DefaultPolicy()[0], -1
}

// NameOf is what the row at i is called in a question: its name, or its place, `policy[i]`. The
// decision no row made (Decide's -1) is DefaultPolicy's, and is called `policy[default]`, which no
// row can be named (a name has no brackets).
func (p Policy) NameOf(i int) string {
	if i < 0 {
		return defaultRowName
	}
	if i < len(p) && p[i].Name != "" {
		return p[i].Name
	}
	return fmt.Sprintf("policy[%d]", i)
}

// defaultRowName is what the decision no row made is called (NameOf, Named).
const defaultRowName = "policy[default]"

// Named is the row by the name a question carries (NameOf), if the policy has it; the decision
// no row made resolves to DefaultPolicy's row.
func (p Policy) Named(name string) (Row, bool) {
	if name == defaultRowName {
		return DefaultPolicy()[0], true
	}
	for i, r := range p {
		if p.NameOf(i) == name {
			return r, true
		}
	}
	return Row{}, false
}

// Default is what the policy's last row does: the task's dial.
func (p Policy) Default() string {
	if len(p) == 0 {
		return ThenAsk
	}
	return p[len(p)-1].Then.Mode
}

// MayAsk reports whether a call of a step may be stopped: some row that could match it is not
// an `allow`. Whether it is stopped this time depends on the call's fields.
func (p Policy) MayAsk(step string) bool {
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

// Possible reports whether some call of a step could go ahead under a task's policy, given what
// is known of it: its name, and the fields it carries, the rest unknown. False means every such
// call is refused, whatever else it carries: the verb is not provided (Provides), or a row
// refuses it (Refuses).
func (p Policy) Possible(s Step) bool {
	_, _, refused := p.Refuses(s)
	return p.Provides(s.Name) && !refused
}

// Refuses is the row that refuses every call of a step, given what is known of it, and its
// place. The first row that could match decides: a deny whose every param is among the known
// fields refuses every such call; a deny on a field not known might not match, and is passed
// over; anything else could let a call go. A field's known value may itself be a pattern (a
// row of another policy being held against this one), matched as the text it is: a pattern
// within a pattern reads as the narrower written inside the wider. A withheld verb is refused
// wherever its row stands, as the supervisor refuses it.
func (p Policy) Refuses(s Step) (Row, int, bool) {
	if r, i, ok := p.withholding(s.Name); ok {
		return r, i, true
	}
	rows := p.OrDefault()
	for i, r := range rows {
		if !r.Match.covers(s.Name) {
			continue
		}
		known := true
		matches := true
		var params map[string]string
		if r.Match != nil {
			params = r.Match.Params
		}
		for name, pattern := range params {
			v, ok := s.Fields[name]
			if !ok {
				known = false
				continue
			}
			if !fieldMatch(pattern, v, name == "command") {
				matches = false
			}
		}
		if !matches {
			continue
		}
		if r.Then.Mode != ThenDeny {
			return Row{}, -1, false
		}
		if known {
			return r, i, true
		}
	}
	return Row{}, -1, false
}

// Provides reports whether a task's policy gives it a step's verb: some row other than the
// default names the verb's system, alone or with the verb, and no `withhold` row takes it out.
// A verb not provided has no tool, and a call of it is refused; the default row decides only
// the calls of provided verbs that no row above it matched.
func (p Policy) Provides(step string) bool {
	if p.Withholds(step) {
		return false
	}
	return slices.ContainsFunc(p, func(r Row) bool { return !r.Match.Any() && r.Match.covers(step) })
}

// Withholds reports whether a step's verb is withheld: some `withhold` row names its system, or
// its system and verb, wherever it stands. Such a step cannot occur: the tool is not offered.
func (p Policy) Withholds(step string) bool {
	_, _, ok := p.withholding(step)
	return ok
}

// withholding is the first `withhold` row that names a step's verb, and its place.
func (p Policy) withholding(step string) (Row, int, bool) {
	for i, r := range p {
		if r.Then.Mode == ThenWithhold && r.Match.covers(step) {
			return r, i, true
		}
	}
	return Row{}, -1, false
}

// Withheld is what the policy withholds, each as `<system>` or `<system>.<verb>`, sorted.
func (p Policy) Withheld() []string {
	var out []string
	for _, r := range p {
		if r.Then.Mode != ThenWithhold || r.Match == nil {
			continue
		}
		name := r.Match.System
		if r.Match.Verb != "" {
			name = systems.StepName(r.Match.System, r.Match.Verb)
		}
		if !slices.Contains(out, name) {
			out = append(out, name)
		}
	}
	sort.Strings(out)
	return out
}

// KeepsWithheld is whether the policy withholds exactly what prev did: a replacement while the
// task runs may change what is decided, never what is absent. A verb withheld at the start was
// never offered and cannot be brought back; one offered cannot be taken away from a doer
// that holds it.
func (p Policy) KeepsWithheld(prev Policy) error {
	was, now := prev.Withheld(), p.Withheld()
	if slices.Equal(was, now) {
		return nil
	}
	return fmt.Errorf("the policy withholds %s, and the task started withholding %s: what is withheld "+
		"is fixed when the run starts", words(now), words(was))
}

// Verbs is the steps the policy's rows name, `<system>.<verb>`, sorted: a row naming a system
// names each of its verbs, one naming a verb names it in each system that has it, and the
// default row names none. They are what the policy asks of the runner, under its ceilings.
func (p Policy) Verbs() []string {
	set := map[string]bool{}
	for _, r := range p {
		if r.Match.Any() {
			continue
		}
		for _, d := range systems.All() {
			if r.Match.System != "" && r.Match.System != d.Name {
				continue
			}
			for _, v := range d.Verbs {
				if r.Match.Verb == "" || r.Match.Verb == v.Name {
					set[systems.StepName(d.Name, v.Name)] = true
				}
			}
		}
	}
	return slices.Sorted(maps.Keys(set))
}

// KeepsVerbs is whether the policy names no verb prev did not: a replacement while the task runs
// re-decides the verbs the task started with, and adds none, since what a policy names is what
// it asked of the runner when the run started.
func (p Policy) KeepsVerbs(prev Policy) error {
	was := prev.Verbs()
	var added []string
	for _, v := range p.Verbs() {
		if _, found := slices.BinarySearch(was, v); !found {
			added = append(added, v)
		}
	}
	if len(added) == 0 {
		return nil
	}
	return fmt.Errorf("the policy names %s, which the task's policy did not: a replacement re-decides "+
		"the verbs the task started with, and adds none", words(added))
}

func words(names []string) string {
	if len(names) == 0 {
		return "nothing"
	}
	return strings.Join(names, ", ")
}

// WithSystem is the policy with each row's match naming system where it names none, the default
// row apart: how a ceiling written under a system's key is read as rows of that system.
func (p Policy) WithSystem(system string) Policy {
	out := p.Clone()
	for i := range out {
		if out[i].Match.Any() {
			continue // the default row stays the default
		}
		if out[i].Match.System == "" {
			out[i].Match.System = system
		}
	}
	return out
}

// About is the rows that could decide a call of a step, in words, for a brief: each row's
// params and what it does, in order, ending with the row every other call gets.
func (p Policy) About(step string) string {
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
		case ThenWithhold:
			what = "withheld"
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

// Summary is the policy in one line, for the event that says it was replaced: how many rows,
// and the dial.
func (p Policy) Summary() string {
	p = p.OrDefault()
	return fmt.Sprintf("%d row(s), default %s", len(p), p.Default())
}

// NamesPrincipal reports whether any row's ask names PrincipalAnswerer.
func (p Policy) NamesPrincipal() bool {
	return slices.ContainsFunc(p, func(r Row) bool { return r.Then.Ask != nil && NamesPrincipal(r.Then.Ask.Answerers) })
}

// Check is what a policy has to satisfy to be installed: each row's shape against the systems'
// documents, and a last row that names no call, so every call is decided. A row before it that
// matches every call would decide for the rows after it, and is refused as the mistake it is.
func (p Policy) Check() error {
	named := map[string]int{}
	for i, r := range p {
		if err := r.Check(); err != nil {
			return fmt.Errorf("policy[%d]: %w", i, err)
		}
		// A question carries its row's name, and verse reads the row's ask by it: two rows of
		// one name would pose one's question under the other's ask.
		if r.Name != "" {
			if at, dup := named[r.Name]; dup {
				return fmt.Errorf("policy[%d]: the name %q is policy[%d]'s; a row's name is its own", i, r.Name, at)
			}
			named[r.Name] = i
		}
		if r.Match.Any() && i != len(p)-1 {
			return fmt.Errorf("policy[%d]: matches every call, so the rows after it never decide; the row "+
				"with no match is the default, and it is last", i)
		}
	}
	if len(p) > 0 && !p[len(p)-1].Match.Any() {
		return errors.New("policy: the last row names a call; a policy ends with its default row, one " +
			"with no match (`{then: ask}`), which decides the calls of the verbs named above that no row matched")
	}
	return nil
}

// notRowGates is the gates of the questions that are no row's: a conversation's turn (the loop),
// codex's own question, and the `question-ask` tool's. A question carries its row's name as its
// gate, and verse reads the row's ask by it, so no row takes one of these.
var notRowGates = []string{"turn", "ask", systems.Tool("question", "ask")}

// Check is what a row has to satisfy: a `then`, an ask's shape only on an ask, a withhold on a
// system or a verb alone, and a match the documents describe.
func (r Row) Check() error {
	switch r.Then.Mode {
	case ThenDeny, ThenAsk, ThenAllow:
	case ThenWithhold:
		if r.Match.Any() || r.Match.System == "" || len(r.Match.Params) > 0 {
			return errors.New("then: withhold names a system or a verb, not a call: `{match: {system: commands}, then: withhold}`")
		}
	case "":
		return errors.New("then: a row says what happens to the calls it matches: deny, ask, allow or withhold")
	default:
		return fmt.Errorf("then: %q is not one of deny, ask, allow, withhold", r.Then.Mode)
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
// params of the verbs it covers. A verb is named within its system: `close` is a different call
// in each tracker, and a row on one says which.
func (m *Match) Check() error {
	if m.Any() {
		return nil
	}
	var covered []systems.Verb
	if m.System != "" {
		d, ok := systems.Get(m.System)
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
	params := map[string][]systems.Param{}
	for _, v := range covered {
		for _, p := range v.Params {
			params[p.Name] = append(params[p.Name], p)
		}
	}
	for _, name := range slices.Sorted(maps.Keys(m.Params)) {
		forms, ok := params[name]
		if !ok {
			return fmt.Errorf("match.%s: not a param of the calls the row names (they have: %s)",
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
	node = unwrapNode(node)
	if node == nil || node.Type == ir.NullType {
		*m = Match{}
		return nil
	}
	if node.Type != ir.ObjectType {
		return errors.New("match: an object, `{system: git, verb: push, ref: refs/heads/main}`")
	}
	out := Match{}
	for i, key := range node.Fields {
		key = unwrapNode(key)
		if key == nil || key.Type != ir.StringType {
			return errors.New("match: a key is a name")
		}
		val := unwrapNode(node.Values[i])
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

// ToTonyIR writes `deny`, `ask`, `allow` or `withhold`, or `{ask: {…}}` for an ask with a shape.
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
	node = unwrapNode(node)
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
			return errors.New("then: `deny`, `ask`, `allow`, `withhold`, or `{ask: {need, answerers, within, default, labels}}`")
		}
		key := unwrapNode(node.Fields[0])
		if key == nil || key.Type != ir.StringType || key.String != ThenAsk {
			return errors.New("then: only `ask` takes a shape: `{ask: {need, answerers, within, default, labels}}`")
		}
		// The ask is read strictly wherever the policy is read: a misspelled `need` or
		// `answerers` dropped would ask fewer people, or other ones, than the row says.
		var a Ask
		if val := unwrapNode(node.Values[0]); val != nil && val.Type != ir.NullType {
			if err := gomap.FromTonyIR(val, &a, append(slices.Clone(opts), gomap.Strict())...); err != nil {
				return fmt.Errorf("then.ask: %w", err)
			}
		}
		*t = Then{Mode: ThenAsk, Ask: &a}
		return nil
	}
	return errors.New("then: `deny`, `ask`, `allow`, `withhold`, or `{ask: {…}}`")
}

// unwrapNode sees through the comments a parser keeps around a value.
func unwrapNode(n *ir.Node) *ir.Node {
	for n != nil && n.Type == ir.CommentType && len(n.Values) > 0 {
		n = n.Values[0]
	}
	return n
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
