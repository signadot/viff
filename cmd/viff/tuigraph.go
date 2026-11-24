package main

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/signadot/tony-format/go-tony/ir"

	"go.viff.dev/viff/pkg/entity"
)

// A graph, as a charter's graph view and the Tasks section's draw it: the places a charter's
// rules watch, each rule under the place it watches, the places a rule derives into under the
// rule, and on every rule the work it has in the runtime: the tasks running, the questions its
// gates wait on, and how its other tasks ended.
//
// IT KNOWS NO CHARTER. It is drawn from what every charter says of itself (a rule's name, its
// subject's path, what its action does and where it derives, its gates) and from how the engine
// names a rule's work: a task is `<charter>:<rule>@<subject>`, and a gate's question names its
// gate and its subject. A charter that names nothing more than that is drawn whole.

// graphRule is one rule as the graph draws it.
type graphRule struct {
	name, does, on, level string
	into                  []string // the places it derives into
	gates                 []string
}

// graphRules is a charter's rules, in the order it lists them.
func graphRules(rec *ir.Node) []graphRule {
	var out []graphRule
	for _, r := range listOf(entity.Get(rec, "rules")) {
		a := entity.Get(r, "action")
		g := graphRule{name: entity.Str(r, "name"), does: ruleDoes(a), level: entity.Str(r, "stratum"),
			on: entity.Str(entity.Get(entity.Get(r, "condition"), "subject"), "path")}
		derives := listOf(entity.Get(a, "derive"))
		if d := entity.Get(a, "derive"); d != nil && d.Type == ir.ObjectType {
			derives = []*ir.Node{d}
		}
		for _, d := range derives {
			if p := entity.Str(d, "path"); p != "" && !contains(g.into, p) {
				g.into = append(g.into, p)
			}
		}
		// Gates are written on the rule, or under its gating.
		for _, gs := range [][]*ir.Node{listOf(entity.Get(r, "gates")), listOf(entity.Get(entity.Get(r, "gating"), "gates"))} {
			for _, gate := range gs {
				if n := entity.Str(gate, "name"); n != "" {
					g.gates = append(g.gates, n)
				}
			}
		}
		out = append(out, g)
	}
	return out
}

func contains(xs []string, s string) bool {
	for _, x := range xs {
		if x == s {
			return true
		}
	}
	return false
}

// graphWork is what one rule has in the runtime.
type graphWork struct {
	running             []tuiItem
	waiting             []tuiQuestion
	done, failed, other int
}

func (w *graphWork) live() int {
	if w == nil {
		return 0
	}
	return len(w.running) + len(w.waiting)
}

// taskOf is a rule's task id taken apart: the charter it is of, the rule, and the subject as
// the id carries it. A task run by hand (`run-…`, a name and a hash) is of no charter.
func taskOf(id string) (charter, rule, subj string, ok bool) {
	head, subj, _ := strings.Cut(id, "@")
	i := strings.LastIndex(head, ":")
	if i < 0 {
		return "", "", "", false
	}
	charter, rule = head[:i], head[i+1:]
	// A scope comes first: `me::pkg:rule`.
	if j := strings.LastIndex(charter, "::"); j >= 0 {
		charter = charter[j+2:]
	}
	return charter, rule, subj, charter != "" && rule != ""
}

// pathMatches is whether the address at is one a rule's subject path names: segment by
// segment, a `*` standing for any one.
func pathMatches(pattern, at string) bool {
	if pattern == "" || at == "" {
		return false
	}
	p, ok1 := segsOf(pattern)
	a, ok2 := segsOf(at)
	if !ok1 || !ok2 {
		if rest, ok := strings.CutSuffix(pattern, ".*"); ok {
			return parentOf(at) == rest
		}
		return pattern == at
	}
	if len(p) != len(a) {
		return false
	}
	for i := range p {
		if p[i] != "*" && p[i] != a[i] {
			return false
		}
	}
	return true
}

// graphIndex is the work in the runtime by the charter and the rule it is of, the tasks of no
// charter, and the questions no rule's gate posed.
type graphIndex struct {
	work  map[string]map[string]*graphWork
	hand  []tuiItem // running tasks of no charter
	asks  []tuiQuestion
	rules map[string][]graphRule // each installed charter's rules, by its address
}

func (x *graphIndex) at(charter, rule string) *graphWork {
	if x.work[charter] == nil {
		x.work[charter] = map[string]*graphWork{}
	}
	if x.work[charter][rule] == nil {
		x.work[charter][rule] = &graphWork{}
	}
	return x.work[charter][rule]
}

// live is how many tasks run and questions wait in a charter.
func (x *graphIndex) live(charter string) (running, waiting int) {
	for _, w := range x.work[charter] {
		running, waiting = running+len(w.running), waiting+len(w.waiting)
	}
	return
}

// charterAt is a charter row's address: the root's is "".
func charterAt(it tuiItem) string {
	if it.name == "(root)" {
		return ""
	}
	return it.name
}

// index reads the snapshot as the graph draws it.
func (m tuiModel) index() *graphIndex {
	x := &graphIndex{work: map[string]map[string]*graphWork{}, rules: map[string][]graphRule{}}
	for _, c := range m.snap.charters {
		x.rules[charterAt(c)] = graphRules(c.record)
	}
	for _, t := range m.snap.everyTask {
		st := firstWord(t.state)
		charter, rule, _, ok := taskOf(t.name)
		if !ok {
			if !isTerminalStatus(st) {
				x.hand = append(x.hand, t)
			}
			continue
		}
		w := x.at(charter, rule)
		switch {
		case !isTerminalStatus(st):
			w.running = append(w.running, t)
		case st == "done":
			w.done++
		case st == "failed" || st == "orphaned":
			w.failed++
		default:
			w.other++
		}
	}
	// A question is its gate's rule's: the rule with a gate of its name whose subject path
	// names what the question is about; failing that, the one such rule of a charter the
	// subject is under.
	charters := make([]string, 0, len(x.rules))
	for c := range x.rules {
		charters = append(charters, c)
	}
	sort.Slice(charters, func(i, j int) bool { return len(charters[i]) > len(charters[j]) })
	for _, q := range m.snap.questions {
		if c, r, ok := x.gateRule(charters, q); ok {
			w := x.at(c, r)
			w.waiting = append(w.waiting, q)
		} else {
			x.asks = append(x.asks, q)
		}
	}
	return x
}

func (x *graphIndex) gateRule(charters []string, q tuiQuestion) (charter, rule string, ok bool) {
	if q.gate == "" || q.subject == "" {
		return "", "", false
	}
	for _, exact := range []bool{true, false} {
		for _, c := range charters {
			if !exact && !under(q.subject, c) {
				continue
			}
			for _, r := range x.rules[c] {
				if contains(r.gates, q.gate) && (!exact || pathMatches(r.on, q.subject)) {
					return c, r.name, true
				}
			}
		}
	}
	return "", "", false
}

// subjectShort is a task's subject as a row names it: its last segment, without the hash a
// lossy token carries.
func subjectShort(subj string) string {
	subj, _, _ = strings.Cut(subj, entity.HashSep)
	if i := strings.LastIndex(subj, "."); i >= 0 {
		subj = subj[i+1:]
	}
	return subj
}

// since is how long ago an RFC 3339 time was, "" where it does not parse.
func since(s string) string {
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return ""
	}
	return roundDur(time.Since(t))
}

// workNote is a rule's work in a line: what runs, what waits, and how the rest ended.
func workNote(w *graphWork) string {
	if w == nil {
		return ""
	}
	var parts []string
	if n := len(w.running); n > 0 {
		parts = append(parts, glyph("running")+tuiWarn.Render(fmt.Sprintf(" %d running", n)))
	}
	if n := len(w.waiting); n > 0 {
		parts = append(parts, tuiWarn.Render(fmt.Sprintf("◐ %d waiting", n)))
	}
	if w.done > 0 {
		parts = append(parts, tuiGood.Render(fmt.Sprintf("✓ %d", w.done)))
	}
	if w.failed > 0 {
		parts = append(parts, tuiBad.Render(fmt.Sprintf("✗ %d", w.failed)))
	}
	if w.other > 0 {
		parts = append(parts, tuiFaint.Render(fmt.Sprintf("· %d", w.other)))
	}
	return strings.Join(parts, tuiFaint.Render(" · "))
}

// workLines is a rule's live work, a line each: the tasks running, then the questions waiting.
func workLines(w *graphWork, width int) []string {
	if w == nil {
		return nil
	}
	var out []string
	for _, t := range w.running {
		_, _, subj, _ := taskOf(t.name)
		name := subjectShort(subj)
		if name == "" {
			name = t.name
		}
		line := fmt.Sprintf("%s %s", glyph("running"), short(name, max(width-30, 12)))
		if d := since(t.note); d != "" {
			line += tuiFaint.Render("  " + d)
		}
		if r := strings.TrimPrefix(entity.Str(t.record, "runnerName"), "mirror.runners."); r != "" {
			line += tuiFaint.Render("  " + r)
		}
		out = append(out, line)
	}
	for _, q := range w.waiting {
		gate := questionGate(q)
		out = append(out, tuiWarn.Render("◐ ")+gateStyle(gate).Render(gate)+"  "+short(q.pose, max(width-len(gate)-6, 16)))
	}
	return out
}

// charterGraph is one charter as a graph: its lines, for a pane width wide.
func charterGraph(at string, rec *ir.Node, x *graphIndex, width int) []string {
	rules := graphRules(rec)
	if len(rules) == 0 {
		return []string{tuiFaint.Render("this charter has no rules")}
	}
	// A place is drawn as the pattern a rule watches it by, where one does: a rule deriving
	// into `x.head` feeds the rules on `x.head.*`.
	place := func(into string) string {
		for _, r := range rules {
			if r.on == into || (r.on != "" && parentOf(r.on) == into) {
				return r.on
			}
		}
		return into + ".*"
	}
	fed := map[string]bool{}
	for _, r := range rules {
		for _, p := range r.into {
			if q := place(p); q != r.on {
				fed[q] = true
			}
		}
	}
	// What a charter sends on, by the place it sends from: an address under the charter's.
	sends := map[string]string{}
	for _, s := range listOf(entity.Get(rec, "sends")) {
		from := entity.Str(s, "from")
		if at != "" && !under(from, at) {
			from = at + "." + from
		}
		sends[from] = entity.Str(s, "to")
	}
	// Addresses under the charter's own are named from it.
	name := func(p string) string {
		if at != "" && p != at && strings.HasPrefix(p, at+".") {
			return "…" + strings.TrimPrefix(p, at)
		}
		if p == "" {
			return "(no subject)"
		}
		return p
	}
	placeLine := func(p string) string {
		line := tuiSection.Render(name(p))
		for from, to := range sends {
			if p == from || parentOf(p) == from {
				line += tuiFaint.Render("  ↗ sent to " + to)
			}
		}
		return line
	}

	var lines []string
	drawn := map[string]bool{}
	var draw func(p, prefix string)
	draw = func(p, prefix string) {
		drawn[p] = true
		var on []graphRule
		for _, r := range rules {
			if r.on == p {
				on = append(on, r)
			}
		}
		for i, r := range on {
			branch, rest := "├─▶ ", "│   "
			if i == len(on)-1 {
				branch, rest = "└─▶ ", "    "
			}
			line := tuiFaint.Render(prefix+branch) + tuiKey.Render(r.name) + "  " + r.does
			if r.level != "" {
				line += tuiFaint.Render(" · level " + r.level)
			}
			if len(r.gates) > 0 {
				line += tuiWarn.Render(" · gate " + strings.Join(r.gates, ", "))
			}
			w := x.work[at][r.name]
			if note := workNote(w); note != "" {
				line += "   " + note
			}
			lines = append(lines, line)
			for _, l := range workLines(w, width-len([]rune(prefix))-6) {
				lines = append(lines, tuiFaint.Render(prefix+rest)+"  "+l)
			}
			for _, into := range r.into {
				q := place(into)
				if q == p {
					continue // it derives into the place it watches
				}
				if drawn[q] {
					lines = append(lines, tuiFaint.Render(prefix+rest)+placeLine(q)+tuiFaint.Render("  (above)"))
					continue
				}
				lines = append(lines, tuiFaint.Render(prefix+rest)+placeLine(q))
				draw(q, prefix+rest)
			}
		}
	}
	root := func(p string) {
		if drawn[p] {
			return
		}
		if len(lines) > 0 {
			lines = append(lines, "")
		}
		lines = append(lines, placeLine(p))
		draw(p, "")
	}
	// From the places nothing in the charter derives into, then whatever a loop left undrawn.
	for _, r := range rules {
		if !fed[r.on] {
			root(r.on)
		}
	}
	for _, r := range rules {
		root(r.on)
	}
	// Work under a rule's name the charter no longer has.
	var gone []string
	for rule := range x.work[at] {
		known := false
		for _, r := range rules {
			known = known || r.name == rule
		}
		if !known {
			gone = append(gone, rule)
		}
	}
	sort.Strings(gone)
	if len(gone) > 0 {
		lines = append(lines, "", tuiFaint.Render("rules the charter no longer has"))
		lines = append(lines, flatRules(x.work[at], gone, width)...)
	}
	return lines
}

// flatRules is rules by name alone, with their work: what a charter the tui cannot read (it is
// not installed, or its rules have changed) still shows.
func flatRules(work map[string]*graphWork, names []string, width int) []string {
	var lines []string
	for _, n := range names {
		lines = append(lines, "  "+tuiKey.Render(n)+"   "+workNote(work[n]))
		for _, l := range workLines(work[n], width-6) {
			lines = append(lines, "      "+l)
		}
	}
	return lines
}

// charterGraphText is a charter row's graph view.
func (m tuiModel) charterGraphText(it tuiItem, w int) string {
	x := m.index()
	running, waiting := x.live(charterAt(it))
	head := tuiTitle.Render(it.name) + "  " + tuiFaint.Render(fmt.Sprintf("%s · %d running · %d waiting on you", it.state, running, waiting))
	return head + "\n\n" + strings.Join(charterGraph(charterAt(it), it.record, x, w), "\n")
}

// flightText is the Tasks section's graph view: every charter with work running or waiting, as
// its graph; the tasks run by hand; and the charters whose work has all ended, a line each.
func (m tuiModel) flightText(w int) string {
	x := m.index()
	recs := map[string]tuiItem{}
	for _, c := range m.snap.charters {
		recs[charterAt(c)] = c
	}
	var busy, idle []string
	running, waiting := len(x.hand), len(x.asks)
	for c := range x.work {
		r, q := x.live(c)
		running, waiting = running+r, waiting+q
		if r+q > 0 {
			busy = append(busy, c)
		} else {
			idle = append(idle, c)
		}
	}
	sort.Strings(busy)
	sort.Strings(idle)

	var b strings.Builder
	b.WriteString(tuiTitle.Render("in flight") + "  " + tuiFaint.Render(fmt.Sprintf("%d tasks running · %d waiting on you · %d charters at work", running, waiting, len(busy))) + "\n")
	if running+waiting == 0 {
		b.WriteString("\n" + tuiGood.Render("✓ nothing is running, and nothing waits on you") + "\n")
	}
	for _, c := range busy {
		r, q := x.live(c)
		b.WriteString("\n" + sectionRule(fmt.Sprintf("%s · %d running · %d waiting", addrText(c), r, q), w) + "\n")
		if it, ok := recs[c]; ok {
			b.WriteString(strings.Join(charterGraph(c, it.record, x, w), "\n") + "\n")
			continue
		}
		// Its rules are not among the charters read: its work is listed by rule.
		b.WriteString(strings.Join(flatRules(x.work[c], sortedRules(x.work[c]), w), "\n") + "\n")
	}
	if len(x.hand) > 0 {
		b.WriteString("\n" + sectionRule(fmt.Sprintf("run by hand · %d running", len(x.hand)), w) + "\n")
		for _, t := range x.hand {
			line := fmt.Sprintf("%s %s", glyph("running"), short(t.name, max(w-30, 12)))
			if d := since(t.note); d != "" {
				line += tuiFaint.Render("  " + d)
			}
			if r := strings.TrimPrefix(entity.Str(t.record, "runnerName"), "mirror.runners."); r != "" {
				line += tuiFaint.Render("  " + r)
			}
			b.WriteString(line + "\n")
		}
	}
	if len(x.asks) > 0 {
		b.WriteString("\n" + sectionRule(fmt.Sprintf("asked, not by a rule's gate · %d", len(x.asks)), w) + "\n")
		for _, q := range x.asks {
			gate := questionGate(q)
			b.WriteString(tuiWarn.Render("◐ ") + gateStyle(gate).Render(gate) + "  " + short(q.pose, max(w-len(gate)-6, 16)) + "\n")
		}
	}
	if len(idle) > 0 {
		b.WriteString("\n" + sectionRule(fmt.Sprintf("ended · %d charters with tasks in the runtime", len(idle)), w) + "\n")
		nameW := min(44, max(w/2, 16))
		for _, c := range idle {
			var parts []string
			for _, r := range sortedRules(x.work[c]) {
				parts = append(parts, r+" "+workNote(x.work[c][r]))
			}
			b.WriteString(fmt.Sprintf("%-*s %s\n", nameW, short(addrText(c), nameW), strings.Join(parts, tuiFaint.Render(" · "))))
		}
	}
	return strings.TrimRight(b.String(), "\n")
}

func sortedRules(work map[string]*graphWork) []string {
	out := make([]string, 0, len(work))
	for r := range work {
		out = append(out, r)
	}
	sort.Strings(out)
	return out
}

// flightRecent is how long a task that has ended stays on the Tasks section's timeline.
const flightRecent = 30 * time.Minute

// flightTimeline is the rows of the Tasks section's timeline: every task running, of a charter
// or run by hand, and those that ended a little while ago, in the order they started.
func (m tuiModel) flightTimeline() []timelineRow {
	var out []timelineRow
	now := time.Now()
	for _, t := range m.snap.everyTask {
		r, ok := timelineRowOf(t, now)
		if !ok || (!r.running && now.Sub(r.end) > flightRecent) {
			continue
		}
		out = append(out, r)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].start.Before(out[j].start) })
	return out
}
