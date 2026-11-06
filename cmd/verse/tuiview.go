package main

import (
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"
	"github.com/signadot/tony-format/go-tony/ir"

	"github.com/signadot/verse/pkg/codec"
	"github.com/signadot/verse/pkg/entity"
)

// The tui's drawing, in the shape of a mission control: a command line on top, the explorer on
// the left (each section, its rows under it), the content on the right (a section as a table,
// or one row: a question, a task, a project with its views), a rule, and the keys.
//
// THE SCREEN NEVER OUTGROWS THE TERMINAL: each pane is cut to the width it is drawn in and to
// the height it has, and the whole is cut to the terminal's height again, so the command line
// stays on the first line whatever a record holds. A terminal is redrawn from the top, and a
// frame one line too tall scrolls the top away. Each pane says where it is when it holds more
// than it shows ("1-40 of 97").

// tuiColor is whether the screen is drawn in colour (`--color`, as every command decides it):
// set once, before the program starts.
var tuiColor = true

var (
	cAccent = lipgloss.AdaptiveColor{Light: "#3B5BFD", Dark: "#7B93FF"}
	cAmber  = lipgloss.AdaptiveColor{Light: "#b45309", Dark: "#fbbf24"}
	cRed    = lipgloss.AdaptiveColor{Light: "#b91c1c", Dark: "#f87171"}
	cGreen  = lipgloss.AdaptiveColor{Light: "#15803d", Dark: "#4ade80"}
	cCyan   = lipgloss.AdaptiveColor{Light: "#0e7490", Dark: "#67e8f9"}
	cMuted  = lipgloss.AdaptiveColor{Light: "#6b7280", Dark: "#9ca3af"}
	cRule   = lipgloss.AdaptiveColor{Light: "#9ca3af", Dark: "#4b5563"}
	cSelBg  = lipgloss.AdaptiveColor{Light: "#e0e7ff", Dark: "#1e2a5a"}

	tuiTitle   = lipgloss.NewStyle().Bold(true)
	tuiBrand   = lipgloss.NewStyle().Bold(true).Foreground(cAccent)
	tuiSelRow  = lipgloss.NewStyle().Bold(true).Background(cSelBg)
	tuiFaint   = lipgloss.NewStyle().Foreground(cMuted)
	tuiRule    = lipgloss.NewStyle().Foreground(cRule)
	tuiWarn    = lipgloss.NewStyle().Bold(true).Foreground(cAmber)
	tuiBad     = lipgloss.NewStyle().Foreground(cRed)
	tuiGood    = lipgloss.NewStyle().Foreground(cGreen)
	tuiKey     = lipgloss.NewStyle().Bold(true).Foreground(cAccent)
	tuiSection = lipgloss.NewStyle().Bold(true).Foreground(cCyan)
	tuiColHead = lipgloss.NewStyle().Bold(true).Foreground(cMuted)
)

// statusStyle is how a status word is drawn: what failed red, what runs amber, what is done green.
func statusStyle(s string) lipgloss.Style {
	switch s {
	case "failed", "cancelled", "orphaned", "rejected", "denied", "deny":
		return tuiBad
	case "running", "pending", "paused", "waiting":
		return tuiWarn
	case "done", "answered", "proceed", "finished":
		return tuiGood
	}
	return tuiFaint
}

// gateStyle is a question's gate badge.
func gateStyle(string) lipgloss.Style {
	return lipgloss.NewStyle().Bold(true).Foreground(cAccent)
}

// spinner is a running thing's mark, turning with the clock.
func spinner() string {
	frames := []rune("⠋⠙⠹⠸⠼⠴⠦⠧⠇⠏")
	return string(frames[int(time.Now().UnixMilli()/250)%len(frames)])
}

// glyph is a status as one mark, coloured: what runs turns, what is done is ticked, what failed
// is crossed, what waits on a person is half full.
func glyph(status string) string {
	switch status {
	case "running", "pending":
		return tuiWarn.Render(spinner())
	case "done", "finished", "answered", "proceed":
		return tuiGood.Render("✓")
	case "failed", "cancelled", "orphaned", "rejected", "denied", "deny":
		return tuiBad.Render("✗")
	case "paused", "waiting":
		return tuiWarn.Render("◐")
	}
	return tuiFaint.Render("·")
}

// layout is the explorer's width, the content's text width, and the rows each pane draws: the
// terminal less the command line and the rule under it, and the rule and the two lines under
// the panes.
func (m tuiModel) layout() (exW, contentW, bodyH int) {
	bodyH = max(m.height-5, 3)
	exW = min(34, max(m.width/4, 22))
	contentW = max(m.width-exW-2, 16)
	return exW, contentW, bodyH
}

// contentRows is how many lines the content pane's text has: its body less the line of tabs
// on top and the line that says where it is at the bottom.
func (m tuiModel) contentRows() int {
	_, _, bodyH := m.layout()
	return max(bodyH-2, 1)
}

func (m tuiModel) View() string {
	if !m.loaded {
		return "reading the verse…"
	}
	exW, contentW, bodyH := m.layout()
	ex := m.explorerLines(exW, bodyH)
	content := m.contentLines(contentW, bodyH)
	bar := tuiRule.Render("┃")
	var b strings.Builder
	b.WriteString(m.commandLine() + "\n")
	b.WriteString(m.rule(exW, "EXPLORER", m.focus == paneList) + tuiRule.Render("╋") +
		m.rule(m.width-exW-1, strings.ToUpper(m.contentTitle()), m.focus == paneDetail) + "\n")
	for i := range bodyH {
		l, r := "", ""
		if i < len(ex) {
			l = ex[i]
		}
		if i < len(content) {
			r = content[i]
		}
		b.WriteString(pad(l, exW) + bar + " " + lipgloss.NewStyle().MaxWidth(contentW).Render(r) + "\n")
	}
	b.WriteString(tuiRule.Render(strings.Repeat("━", exW)+"╋"+strings.Repeat("━", max(m.width-exW-1, 0))) + "\n")
	b.WriteString(m.footer())
	frame := b.String()
	// The last guard: whatever drew too tall or too wide is cut, at the bottom and the right.
	lines := strings.Split(frame, "\n")
	if len(lines) > m.height && m.height > 0 {
		lines = lines[:m.height]
	}
	for i, l := range lines {
		if lipgloss.Width(l) > m.width {
			lines[i] = lipgloss.NewStyle().MaxWidth(m.width).Render(l)
		}
	}
	return strings.Join(lines, "\n")
}

// rule is a heavy rule w wide with a title in it, braced and in the accent where its pane has
// the focus.
func (m tuiModel) rule(w int, title string, focused bool) string {
	t := " " + title + " "
	style := tuiRule
	if focused {
		t, style = " { "+title+" } ", tuiBrand
	}
	t = short(t, max(w-2, 0))
	return tuiRule.Render("━") + style.Render(t) + tuiRule.Render(strings.Repeat("━", max(w-1-lipgloss.Width(t), 0)))
}

// commandLine is the first line: the command line, where `:` types, and who reads what when.
func (m tuiModel) commandLine() string {
	left := tuiBrand.Render("verse") + " " + tuiFaint.Render("cmd ▸") + " "
	if m.mode == modeCommand {
		return lipgloss.NewStyle().MaxWidth(m.width).Render(left + m.cmd.View())
	}
	left += tuiFaint.Render("▊  : command · tab completes · ? help")
	who := tuiFaint.Render(fmt.Sprintf("%s · read %s", m.me, m.read.Format("15:04:05")))
	if gap := m.width - lipgloss.Width(left) - lipgloss.Width(who); gap >= 2 {
		left += strings.Repeat(" ", gap) + who
	}
	return lipgloss.NewStyle().MaxWidth(m.width).Render(left)
}

// count is what a section's line says of it, beside its name.
func (m tuiModel) count(t tuiTab) string {
	switch t {
	case tabQuestions:
		if n := len(m.snap.questions); n > 0 {
			return tuiWarn.Render(fmt.Sprintf("%d need you", n))
		}
		return tuiFaint.Render("0")
	case tabTasks:
		if n := m.running(); n > 0 {
			return tuiWarn.Render(fmt.Sprintf("%d run", n))
		}
		return tuiFaint.Render(fmt.Sprint(len(m.snap.tasks)))
	case tabData:
		return tuiFaint.Render(fmt.Sprint(len(m.itemsOf(tabData))))
	case tabCharters:
		return tuiFaint.Render(fmt.Sprint(len(m.snap.charters)))
	}
	return tuiFaint.Render(fmt.Sprint(len(m.snap.runners)))
}

// exStart is the explorer's first line shown, so the cursor stays on screen: above the last
// line while rows follow it, since that line then says how many more there are.
func (m tuiModel) exStart(n, h int) int {
	return exStartAt(m.exAt(), n, h)
}

func exStartAt(cur, n, h int) int {
	if n <= h || cur < h-1 {
		return 0
	}
	return min(cur-h+2, n-h)
}

// explorerLines is the explorer as drawn, h lines at most.
func (m tuiModel) explorerLines(w, h int) []string {
	ex := m.explorer()
	cur := m.exAt()
	// Each section's rows once, not once a row: a wide place lists thousands.
	var lists exLists
	lists.qs = m.questions()
	for t := tuiTab(0); t < tabCount; t++ {
		lists.items[t] = m.itemsOf(t)
	}
	errLine := m.snap.errs[m.tab]
	n := len(ex)
	if errLine != "" {
		n += 2
	}
	start := exStartAt(cur, n, h)
	end := min(start+h, n)
	var lines []string
	// Only the rows on screen are drawn.
	for i := start; i < min(end, len(ex)); i++ {
		r := ex[i]
		sel := i == cur
		var text string
		if r.item < 0 {
			mark := " "
			if sel {
				mark = "▶"
			}
			name := strings.ToUpper(r.tab.String())
			if r.tab == tabData && m.dataAt != "" {
				// Where the listing is, as much of it as fits: its end is what tells it apart.
				at := m.dataAt
				if room := w - 10 - lipgloss.Width(name); len([]rune(at)) > room && room > 3 {
					at = "…" + string([]rune(at)[len([]rune(at))-room+1:])
				}
				name += " " + at
			}
			if m.folded[r.tab] {
				name += " +"
			}
			right := m.count(r.tab)
			gap := max(w-1-lipgloss.Width(name)-lipgloss.Width(right)-1, 1)
			if sel {
				text = tuiSelRow.Width(w).Render(cut(mark+name+strings.Repeat(" ", gap), w-lipgloss.Width(right)) + right)
			} else {
				text = tuiTitle.Render(mark+name) + strings.Repeat(" ", gap) + right
			}
		} else {
			branch := "┣━ "
			if i+1 >= len(ex) || ex[i+1].tab != r.tab {
				branch = "┗━ "
			}
			mark, name := m.exItem(r, &lists)
			name = strings.ReplaceAll(screenSafe(name), "\n", " ")
			body := cut(name, max(w-8, 4)) // after "  ┗━ ◐ ": a selected row that overran its width wrapped
			if sel {
				text = tuiSelRow.Width(w).Render("  " + branch + mark + " " + body)
			} else {
				text = tuiRule.Render("  "+branch) + mark + " " + body
			}
		}
		lines = append(lines, text)
	}
	if errLine != "" {
		for _, l := range []string{"", tuiBad.Render(cut("✗ "+strings.ReplaceAll(screenSafe(errLine), "\n", " "), w))} {
			if len(lines) < end-start {
				lines = append(lines, l)
			}
		}
	}
	if end < n && len(lines) > 0 {
		lines[len(lines)-1] = tuiFaint.Render(fmt.Sprintf(" ↓ %d more", n-end))
	}
	return lines
}

// exLists is each section's rows as the filter keeps them, made once per frame.
type exLists struct {
	qs    []tuiQuestion
	items [tabCount][]tuiItem
}

// exItem is a row's mark and name in the explorer.
func (m tuiModel) exItem(r exRow, l *exLists) (string, string) {
	if r.tab == tabQuestions {
		q := l.qs[r.item]
		gate := questionGate(q)
		return tuiWarn.Render("◐"), gateStyle(gate).Render(gate) + " " + strings.Join(strings.Fields(q.pose), " ")
	}
	return m.itemMark(r.tab, l.items[r.tab][r.item])
}

// itemMark is a row's mark and name, as the explorer and the section's table show it.
func (m tuiModel) itemMark(t tuiTab, it tuiItem) (string, string) {
	switch t {
	case tabTasks:
		return glyph(firstWord(it.state)), taskShortName(it.name)
	case tabData:
		if len(m.waitingUnder(it.note)) > 0 {
			return tuiWarn.Render("◐"), it.name
		}
		return tuiFaint.Render("▸"), it.name
	case tabCharters:
		return tuiKey.Render("§"), it.name
	}
	running := entity.Get(it.record, "running")
	if running != nil && nodeText(running) != "0" {
		return tuiGood.Render("▣"), it.name
	}
	return tuiFaint.Render("▣"), it.name
}

// questionGate is a question's gate as its labels name it, or its gate, or "ask".
func questionGate(q tuiQuestion) string {
	gate := q.gate
	if lb := entity.Str(entity.Get(q.record, "labels"), "gate"); lb != "" {
		gate = lb
	}
	if gate == "" {
		gate = "ask"
	}
	return gate
}

// waitingUnder is the questions waiting on you about at or anything under it.
func (m tuiModel) waitingUnder(at string) []tuiQuestion {
	var out []tuiQuestion
	for _, q := range m.snap.questions {
		if q.subject != "" && under(q.subject, at) {
			out = append(out, q)
		}
	}
	return out
}

func (m tuiModel) running() int {
	n := 0
	for _, t := range m.snap.tasks {
		if !isTerminalStatus(firstWord(t.state)) {
			n++
		}
	}
	return n
}

// contentTitle is what the rule over the content pane names.
func (m tuiModel) contentTitle() string {
	if m.mode == modeHelp {
		return "commands"
	}
	if m.onHeader {
		if m.tab == tabData {
			return "data · " + addrText(m.dataAt)
		}
		return m.tab.String()
	}
	switch m.tab {
	case tabData:
		if it, ok := m.item(); ok {
			return "data · " + it.note
		}
		return "data · " + addrText(m.dataAt)
	case tabCharters:
		if it, ok := m.item(); ok {
			return "charter · " + it.name
		}
	case tabQuestions:
		if q, ok := m.question(); ok {
			return "question · " + questionGate(q)
		}
	case tabTasks:
		if it, ok := m.item(); ok {
			return "task · " + taskShortName(it.name)
		}
	case tabRunners:
		if it, ok := m.item(); ok {
			return "runner · " + it.name
		}
	}
	return m.tab.String()
}

// tabsLine is the line on top of the content: a project's views, the chosen one bracketed, or
// what the pane shows.
func (m tuiModel) tabsLine() string {
	if m.mode == modeHelp {
		return tuiFaint.Render("any key closes · : runs one")
	}
	if vs := m.views(); len(vs) > 0 {
		var parts []string
		for _, v := range vs {
			name := strings.ToUpper(v.String())
			if v == m.shownView() {
				parts = append(parts, tuiBrand.Render("[ "+name+" ]"))
			} else {
				parts = append(parts, tuiFaint.Render("  "+name+"  "))
			}
		}
		return strings.Join(parts, "  ") + tuiFaint.Render("   tab / v switches")
	}
	if m.onHeader {
		return tuiBrand.Render("[ TABLE ]") + tuiFaint.Render("  "+strings.ToLower(m.tab.String())+m.filterNote())
	}
	return tuiBrand.Render("[ DETAIL ]") + tuiFaint.Render(m.filterNote())
}

func (m tuiModel) filterNote() string {
	if m.filter == "" {
		return ""
	}
	return fmt.Sprintf("  /%s", m.filter)
}

// contentLines is the content pane as drawn: its tabs, h-2 lines of its text from the scroll,
// and where it is.
func (m tuiModel) contentLines(w, h int) []string {
	text, wrap := m.contentText(w)
	text = screenSafe(text)
	var lines []string
	if wrap {
		lines = strings.Split(lipgloss.NewStyle().Width(w).Render(text), "\n")
	} else {
		for _, l := range strings.Split(text, "\n") {
			lines = append(lines, lipgloss.NewStyle().MaxWidth(w).Render(l))
		}
	}
	rows := max(h-2, 1)
	scroll := min(m.scroll, max(len(lines)-rows, 0))
	end := min(scroll+rows, len(lines))
	out := []string{m.tabsLine()}
	out = append(out, lines[scroll:end]...)
	for len(out) < h-1 {
		out = append(out, "")
	}
	where := m.summary()
	if len(lines) > rows {
		where = fmt.Sprintf("scroll ↑/↓ J/K · %d-%d of %d", scroll+1, end, len(lines))
	}
	return append(out, tuiFaint.Render(where))
}

// summary is the verse in one line, under the content when it fits.
func (m tuiModel) summary() string {
	s := fmt.Sprintf("%d need you · %d tasks running · %d charters · %d runners",
		len(m.snap.questions), m.running(), len(m.snap.charters), len(m.snap.runners))
	return s
}

// contentText is the content's text, and whether it is prose to wrap (a record, a question)
// rather than lines laid out to be cut (tables, timelines).
func (m tuiModel) contentText(w int) (string, bool) {
	if m.mode == modeHelp {
		return m.helpText(), false
	}
	if m.onHeader {
		return m.table(w), false
	}
	switch m.tab {
	case tabQuestions:
		q, ok := m.question()
		if !ok {
			return tuiFaint.Render("When a rule's gate or an ask waits on you, it is here, with what it is about."), true
		}
		return questionText(q), true
	case tabTasks:
		if it, ok := m.item(); ok {
			return tuiTitle.Render(it.name) + "\n\n" + taskText(it.record), true
		}
	case tabData:
		it, ok := m.item()
		if !ok {
			break
		}
		if m.dview == viewTimeline {
			return strings.Join(timeline(m.tasksAbout(func(s string) bool { return tokenUnder(s, it.note) }), it.note, w), "\n"), false
		}
		return m.placeText(it, w), true
	case tabCharters:
		if it, ok := m.item(); ok {
			return charterText(it, w), true
		}
	default:
		if it, ok := m.item(); ok {
			return tuiTitle.Render(it.name) + "  " + tuiFaint.Render(it.state) + "\n\n" + recordText(it.record), true
		}
	}
	return "", true
}

// placeText is a place in the data as it stands: what it was derived from, the questions and
// tasks about it, and its record.
func (m tuiModel) placeText(it tuiItem, w int) string {
	var b strings.Builder
	b.WriteString(tuiTitle.Render(it.note) + "\n\n")
	field := func(k, v string) { b.WriteString(tuiColHead.Render(fmt.Sprintf("%-10s", k)) + v + "\n") }
	if m.recFor != it.note {
		return b.String() + tuiFaint.Render("reading…")
	}
	if m.recErr != "" {
		return b.String() + tuiBad.Render("✗ "+m.recErr)
	}
	// What derived it, as the engine wrote beside it: the rule and the subject. A record's
	// lineage is followed from here a hop at a time, by `:data <subject>`.
	if d := entity.Get(entity.Get(entity.Get(m.rec, "verse"), "meta"), "derivedFrom"); d != nil {
		field("DERIVED", fmt.Sprintf("by rule %s from %s", tuiKey.Render(entity.Str(d, "rule")), entity.Str(d, "subject")))
	}
	switch n := m.rec; {
	case n == nil:
	case n.Type == ir.ObjectType:
		field("HOLDS", fmt.Sprintf("%d fields", len(n.Fields)))
	case n.Type == ir.ArrayType:
		field("HOLDS", fmt.Sprintf("%d items", len(n.Values)))
	}
	if qs := m.waitingUnder(it.note); len(qs) > 0 {
		b.WriteString("\n" + sectionRule(fmt.Sprintf("WAITING ON YOU · %d", len(qs)), w) + "\n")
		for _, q := range qs {
			gate := questionGate(q)
			b.WriteString(tuiWarn.Render("◐ ") + gateStyle(gate).Render(fmt.Sprintf("%-7s", gate)) + " " + short(q.pose, max(w-12, 20)) + "\n")
		}
	}
	if ts := m.tasksAbout(func(s string) bool { return tokenUnder(s, it.note) }); len(ts) > 0 {
		b.WriteString("\n" + sectionRule(fmt.Sprintf("TASKS · %d", len(ts)), w) + "\n")
		from := max(len(ts)-5, 0)
		for _, t := range ts[from:] {
			b.WriteString(fmt.Sprintf("%s %-36s %s\n", glyph(barStatus(t)), short(t.name, 36), tuiFaint.Render(t.start.Local().Format("Jan 2 15:04"))))
		}
		if from > 0 {
			b.WriteString(tuiFaint.Render(fmt.Sprintf("  … %d earlier (the timeline has them all)", from)) + "\n")
		}
	}
	b.WriteString("\n" + sectionRule("RECORD", w) + "\n" + recordText(m.rec))
	return b.String()
}

// charterText is a charter as installed: where it writes, what it reads, and each rule by name
// and what it does, then the package whole.
func charterText(it tuiItem, w int) string {
	var b strings.Builder
	b.WriteString(tuiTitle.Render(it.name) + "  " + tuiFaint.Render(it.state) + "\n\n")
	field := func(k, v string) { b.WriteString(tuiColHead.Render(fmt.Sprintf("%-10s", k)) + v + "\n") }
	field("WRITES", it.note)
	if rs := entity.Strs(it.record, "reads"); len(rs) > 0 {
		field("READS", strings.Join(rs, ", "))
	}
	if e := entity.Str(it.record, "expires"); e != "" {
		field("EXPIRES", e)
	}
	if rules := listOf(entity.Get(it.record, "rules")); len(rules) > 0 {
		b.WriteString("\n" + sectionRule(fmt.Sprintf("RULES · %d", len(rules)), w) + "\n")
		for _, r := range rules {
			does := ruleDoes(entity.Get(r, "action"))
			line := fmt.Sprintf("%s %-28s %-8s", tuiKey.Render("▪"), short(entity.Str(r, "name"), 28), does)
			if st := entity.Str(r, "stratum"); st != "" {
				line += tuiFaint.Render(" level " + st)
			}
			// Gates are written on the rule, or under its gating.
			gates := len(listOf(entity.Get(r, "gates"))) + len(listOf(entity.Get(entity.Get(r, "gating"), "gates")))
			if gates > 0 {
				line += tuiWarn.Render(fmt.Sprintf(" %d gates", gates))
			}
			b.WriteString(line + "\n")
			// What it watches: the subject's path.
			if p := entity.Str(entity.Get(entity.Get(r, "condition"), "subject"), "path"); p != "" {
				b.WriteString(tuiFaint.Render("    on "+p) + "\n")
			}
		}
	}
	if ss := listOf(entity.Get(it.record, "schedules")); len(ss) > 0 {
		b.WriteString("\n" + sectionRule(fmt.Sprintf("SCHEDULES · %d", len(ss)), w) + "\n")
		for _, s := range ss {
			name := entity.Str(s, "name")
			if name == "" {
				name = entity.Str(s, "id")
			}
			line := fmt.Sprintf("%s %-24s every %s", tuiKey.Render("◷"), short(name, 24), entity.Str(s, "every"))
			if next := entity.Str(s, "next"); next != "" {
				line += tuiFaint.Render(" · next " + when(next))
			}
			b.WriteString(line + "\n")
		}
	}
	if ss := listOf(entity.Get(it.record, "sends")); len(ss) > 0 {
		b.WriteString("\n" + sectionRule(fmt.Sprintf("SENDS · %d", len(ss)), w) + "\n")
		for _, s := range ss {
			b.WriteString(fmt.Sprintf("%s %s → %s\n", tuiKey.Render("↗"), entity.Str(s, "from"), entity.Str(s, "to")))
		}
	}
	b.WriteString("\n" + sectionRule("PACKAGE", w) + "\n" + recordText(it.record))
	return b.String()
}

// ruleDoes is what an action does, in a word: it starts a task, derives, patches or deletes —
// the weightiest it does, whatever order its fields come in (onDone sorts before spawn).
func ruleDoes(a *ir.Node) string {
	for _, k := range []string{"spawn", "derive", "patch", "delete"} {
		if entity.Get(a, k) != nil {
			return k
		}
	}
	if a != nil && a.Type == ir.ObjectType && len(a.Fields) > 0 {
		return a.Fields[0].String
	}
	return ""
}

// questionText is a question, what answers it, and what it is about as it stands.
func questionText(q tuiQuestion) string {
	var b strings.Builder
	b.WriteString(tuiTitle.Render(q.pose) + "\n\n")
	if q.about != "" && q.about != q.pose {
		b.WriteString(q.about + "\n\n")
	}
	field := func(k, v string) {
		if v != "" {
			fmt.Fprintf(&b, "%s %s\n", tuiColHead.Render(fmt.Sprintf("%-9s", k)), v)
		}
	}
	p := q.record
	field("ID", q.id)
	field("ABOUT", q.subject)
	// Who asks: the rule whose gate it is, or the task whose step it is, or the person.
	from := entity.Get(p, "from")
	switch {
	case entity.Str(from, "rule") != "":
		field("FROM", "rule "+entity.Str(from, "rule")+tuiFaint.Render(" at "+entity.Str(from, "at")))
	case entity.Str(from, "task") != "":
		field("FROM", "task "+entity.Str(from, "task"))
	case entity.Str(from, "principal") != "":
		field("FROM", entity.Str(from, "principal"))
	}
	// A task's step: what it would do, and with what. This is what a yes lets it do.
	step := entity.Str(p, "step")
	if a := entity.Str(p, "action"); a != "" && step == "" {
		step = a
	}
	// A task's tool says what it called; a rule gate's (verse:trigger:<rule>) says nothing FROM
	// does not.
	if t := entity.Str(p, "tool"); t != "" && !strings.HasPrefix(t, "verse:trigger:") {
		step = strings.TrimSpace(step + tuiFaint.Render(" via "+t))
	}
	field("STEP", step)
	if f := entity.Get(p, "fields"); f != nil {
		b.WriteString(indent(recordText(f), "          ") + "\n")
	}
	field("POSED", when(q.posedAt))
	field("WAITS ON", strings.Join(entity.Strs(p, "waitingOn"), ", "))
	due := entity.Str(entity.Get(p, "deadline"), "at")
	if due == "" {
		due = entity.Str(p, "due")
	}
	if due != "" {
		due = when(due)
		if def := entity.Str(entity.Get(p, "deadline"), "default"); def != "" {
			due += tuiFaint.Render(" · then " + def)
		} else if s := entity.Str(p, "onSilence"); s != "" {
			due += tuiFaint.Render(" · then " + s)
		}
	}
	field("DUE", due)
	if bs := entity.Get(p, "ballots"); bs != nil && bs.Type == ir.ObjectType {
		var votes []string
		for i, who := range bs.Fields {
			votes = append(votes, who.String+" "+entity.Str(bs.Values[i], "choice"))
		}
		field("BALLOTS", strings.Join(votes, ", "))
	}
	var opts []string
	for _, o := range answerKeys(q) {
		label := o.option
		switch o.option {
		case "proceed":
			label = tuiGood.Render(label)
		case "deny":
			label = tuiBad.Render(label)
		}
		if o.says != "" {
			label += tuiFaint.Render(" (" + short(o.says, 40) + ")")
		}
		opts = append(opts, tuiKey.Render(o.key)+" "+label)
	}
	field("ANSWER", strings.Join(opts, "  "))
	if len(q.options) > len(answerKeys(q)) {
		b.WriteString(tuiFaint.Render("          every option: :answer <option>") + "\n")
	}
	if q.subjectRecord != nil {
		b.WriteString("\n" + tuiSection.Render("what it is about, as it stands") + "\n")
		b.WriteString(recordText(q.subjectRecord) + "\n")
	}
	return b.String()
}

// answerKey is an option and the key that chooses it.
type answerKey struct{ key, option, says string }

// answerKeys is a question's options as keys: a for proceed and d for deny where it offers
// them, and 7, 8, 9 for the first three others (1-6 are the sections). The rest are answered by
// name, with :answer.
func answerKeys(q tuiQuestion) []answerKey {
	var out []answerKey
	next := '7'
	for _, o := range q.options {
		k := ""
		switch {
		case o == "proceed":
			k = "a"
		case o == "deny":
			k = "d"
		case next <= '9':
			k = string(next)
			next++
		default:
			continue
		}
		out = append(out, answerKey{key: k, option: o, says: q.says[o]})
	}
	return out
}

// indent puts prefix before every line of s.
func indent(s, prefix string) string {
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	for i, l := range lines {
		lines[i] = prefix + l
	}
	return strings.Join(lines, "\n")
}

// sectionRule is a rule naming a part of the content: `── STEPS ───`.
func sectionRule(title string, w int) string {
	t := "── " + title + " "
	return tuiSection.Render(t) + tuiRule.Render(strings.Repeat("─", max(min(w, 80)-lipgloss.Width(t), 0)))
}

// table is a section as a table: one line a row, its columns, and the cursor's row marked.
func (m tuiModel) table(w int) string {
	var b strings.Builder
	head := func(s string) { b.WriteString(tuiColHead.Render(cut(s, w)) + "\n") }
	switch m.tab {
	case tabQuestions:
		qs := m.questions()
		if len(qs) == 0 {
			return tuiGood.Render("✓ nothing waits on you")
		}
		head(fmt.Sprintf("  %-8s %-20s %s", "GATE", "POSED", "QUESTION"))
		for _, q := range qs {
			gate := questionGate(q)
			b.WriteString(fmt.Sprintf("%s %s %-20s %s\n", tuiWarn.Render("◐"), gateStyle(gate).Render(fmt.Sprintf("%-8s", gate)),
				tuiFaint.Render(short(q.posedAt, 20)), short(q.pose, max(w-32, 10))))
		}
	case tabTasks:
		ts := m.items()
		if len(ts) == 0 {
			return tuiFaint.Render("no tasks")
		}
		nameW := min(40, max(w/2, 16))
		head(fmt.Sprintf("  %-*s %-10s %-10s %s", nameW, "TASK", "STATE", "RUNNER", "STARTED"))
		for _, t := range ts {
			st := firstWord(t.state)
			runner := strings.TrimSpace(strings.TrimPrefix(t.state, st))
			b.WriteString(fmt.Sprintf("%s %-*s %s %-10s %s\n", glyph(st), nameW, short(taskShortName(t.name), nameW),
				statusStyle(st).Render(fmt.Sprintf("%-10s", st)), short(runner, 10), tuiFaint.Render(t.note)))
		}
	case tabData:
		ds := m.items()
		if len(ds) == 0 {
			if e := m.snap.errs[tabData]; e != "" {
				return tuiBad.Render(lipgloss.NewStyle().Width(w).Render("✗ " + e))
			}
			return tuiFaint.Render("nothing under " + addrText(m.dataAt) + " · u goes up")
		}
		nameW := min(40, max(w/3, 12))
		head(fmt.Sprintf("  %-*s %s", nameW, "UNDER "+strings.ToUpper(addrText(m.dataAt)), "WAITING ON YOU"))
		for _, it := range ds {
			mark, _ := m.itemMark(tabData, it)
			waiting := ""
			if n := len(m.waitingUnder(it.note)); n > 0 {
				waiting = tuiWarn.Render(fmt.Sprint(n))
			}
			b.WriteString(fmt.Sprintf("%s %-*s %s\n", mark, nameW, short(it.name, nameW), waiting))
		}
		b.WriteString("\n" + tuiFaint.Render("⏎ opens a place · u goes up · :data <address> goes anywhere"))
	case tabCharters:
		cs := m.items()
		if len(cs) == 0 {
			return tuiFaint.Render("no charters installed")
		}
		nameW := min(48, max(w/2, 16))
		head(fmt.Sprintf("  %-*s %-22s %s", nameW, "CHARTER", "HOLDS", "WRITES"))
		for _, it := range cs {
			b.WriteString(fmt.Sprintf("%s %-*s %-22s %s\n", tuiKey.Render("§"), nameW, short(it.name, nameW), short(it.state, 22), tuiFaint.Render(it.note)))
		}
	case tabRunners:
		rs := m.items()
		if len(rs) == 0 {
			return tuiFaint.Render("no runners")
		}
		head(fmt.Sprintf("  %-14s %s", "RUNNER", "STATE"))
		for _, it := range rs {
			mark, _ := m.itemMark(tabRunners, it)
			b.WriteString(fmt.Sprintf("%s %-14s %s  %s\n", mark, short(it.name, 14), it.state, tuiFaint.Render(it.note)))
		}
	}
	return strings.TrimRight(b.String(), "\n")
}

// helpText is the command palette: the commands, then the keys.
func (m tuiModel) helpText() string {
	var b strings.Builder
	b.WriteString(sectionRule("COMMANDS  (: then the command, tab completes)", 80) + "\n")
	for _, c := range tuiCommands {
		b.WriteString(fmt.Sprintf("%s %s\n", tuiKey.Render(fmt.Sprintf("%-38s", c[0])), tuiFaint.Render(c[1])))
	}
	b.WriteString("\n" + sectionRule("KEYS", 80) + "\n")
	for _, k := range [][2]string{
		{"↑↓ j k", "move in the pane in focus"}, {"←→ h l", "the other pane; ← on a row goes to its section"},
		{"⏎", "open (a section's line folds it; a place opens into what is under it)"}, {"u ⌫", "the data one level up"},
		{"1-5", "a section"}, {"tab v / V", "a place's record or its timeline"},
		{"J K pgup pgdn", "scroll the content"}, {"g G", "top, bottom"}, {"/", "filter the explorer"},
		{"a d 7-9", "answer a question (:answer for any option)"}, {"x", "cancel a task"},
		{"r", "read again"}, {"q", "quit"},
	} {
		b.WriteString(fmt.Sprintf("%s %s\n", tuiKey.Render(fmt.Sprintf("%-38s", k[0])), tuiFaint.Render(k[1])))
	}
	return b.String()
}

// stateText colours a "done · supervised" state: its first word by status, the rest muted.
func stateText(s string) string {
	w := firstWord(s)
	return statusStyle(w).Render(w) + tuiFaint.Render(strings.TrimPrefix(s, w))
}

// footer is the two lines under the panes: the keys that do something here (or what is being
// typed), and the last act's outcome.
func (m tuiModel) footer() string {
	switch m.mode {
	case modeSay:
		verb := tuiGood.Render(m.pending.choice)
		if m.pending.choice == "deny" {
			verb = tuiBad.Render(m.pending.choice)
		}
		return lipgloss.NewStyle().MaxWidth(m.width).Render(verb+" "+tuiFaint.Render(short(m.pending.id, max(m.width-10, 10)))) +
			"\n" + lipgloss.NewStyle().MaxWidth(m.width).Render(m.input.View())
	case modeConfirm:
		return lipgloss.NewStyle().MaxWidth(m.width).Render(tuiWarn.Render("cancel "+short(m.pending.id, max(m.width-50, 10))+"?")+"  "+
			tuiKey.Render("y")+" cancel it · any other key keeps it") + "\n"
	case modeFilter:
		return lipgloss.NewStyle().MaxWidth(m.width).Render(m.finput.View()) + "\n" + tuiFaint.Render("enter keeps the filter · esc clears it")
	}
	keys := [][2]string{{"↑↓", "move"}, {"←→", "pane"}, {"⏎", "open"}, {":", "command"}, {"/", "filter"}, {"?", "help"}, {"q", "quit"}}
	switch {
	case m.tab == tabQuestions && !m.onHeader:
		if q, ok := m.question(); ok {
			var ks [][2]string
			for _, o := range answerKeys(q) {
				ks = append(ks, [2]string{o.key, o.option})
			}
			keys = append(ks, keys...)
		}
	case m.tab == tabTasks && !m.onHeader:
		keys = append([][2]string{{"x", "cancel"}}, keys...)
	case m.tab == tabData && m.focus == paneList && !m.onHeader:
		keys = append([][2]string{{"⏎", "in"}, {"u", "up"}, {"tab", "view"}}, keys...)
	}
	var parts []string
	seen := map[string]bool{}
	for _, k := range keys {
		// A key named twice says the first, the one this row gives it.
		if seen[k[0]] {
			continue
		}
		seen[k[0]] = true
		parts = append(parts, tuiKey.Render(k[0])+" "+tuiFaint.Render(k[1]))
	}
	status := strings.ReplaceAll(screenSafe(m.status), "\n", " ")
	switch {
	case strings.HasPrefix(status, "✗"):
		status = tuiBad.Render(status)
	case strings.HasPrefix(status, "✓"):
		status = tuiGood.Render(status)
	default:
		status = tuiFaint.Render(status)
	}
	return lipgloss.NewStyle().MaxWidth(m.width).Render(strings.Join(parts, tuiFaint.Render(" · "))) + "\n" +
		lipgloss.NewStyle().MaxWidth(m.width).Render(status)
}

// taskShortName is a task's id without its package: `rule@subject`, the subject's last segment.
func taskShortName(id string) string {
	rule, subj, ok := taskParts(id)
	if !ok {
		return id
	}
	subj, _, _ = strings.Cut(subj, entity.HashSep)
	if i := strings.LastIndex(subj, "."); i >= 0 {
		subj = subj[i+1:]
	}
	return rule + "@" + subj
}

// taskParts is a rule's task id taken apart: the rule (after any package and scope, `pkg:rule`,
// `me::pkg:rule`) and the subject as the id carries it, an entity.SegToken. A task run by hand
// (`run-…`) has neither.
func taskParts(id string) (rule, subj string, ok bool) {
	head, subj, ok := strings.Cut(id, "@")
	if !ok {
		return "", "", false
	}
	if i := strings.LastIndex(head, ":"); i >= 0 {
		head = head[i+1:]
	}
	return head, subj, true
}

// taskText is a task as a person reads it: where it stands, then what it said or what went
// wrong, then the rest of its record, and what it was asked to do last.
func taskText(t *ir.Node) string {
	var b strings.Builder
	for _, f := range []string{"status", "runnerName", "started", "ended"} {
		if v := entity.Str(t, f); v != "" {
			if f == "status" {
				v = glyph(v) + " " + statusStyle(v).Render(v)
			}
			fmt.Fprintf(&b, "%s %s\n", tuiColHead.Render(fmt.Sprintf("%-11s", strings.ToUpper(f))), v)
		}
	}
	for _, f := range []string{"err", "result"} {
		if v := entity.Str(t, f); v != "" {
			head := tuiSection.Render(f)
			if f == "err" {
				head = tuiBad.Bold(true).Render(f)
			}
			b.WriteString("\n" + head + "\n" + strings.TrimSpace(v) + "\n")
		}
	}
	var rest []ir.KeyVal
	if t != nil {
		for i, k := range t.Fields {
			switch k.String {
			case "status", "runnerName", "started", "ended", "err", "result", "prompt", "id":
				continue
			}
			rest = append(rest, ir.KeyVal{Key: k.Clone(), Val: t.Values[i].Clone()})
		}
	}
	if len(rest) > 0 {
		b.WriteString("\n" + recordText(ir.FromKeyVals(rest)) + "\n")
	}
	if v := entity.Str(t, "prompt"); v != "" {
		b.WriteString("\n" + tuiSection.Render("prompt") + "\n" + strings.TrimSpace(v) + "\n")
	}
	return b.String()
}

// recordText is a record as YAML for a detail pane, coloured as `verse data` colours it.
func recordText(n *ir.Node) string {
	if n == nil {
		return ""
	}
	var b strings.Builder
	enc := codec.EncodeNode
	if tuiColor {
		enc = codec.EncodeNodeColor
	}
	if err := enc(&b, codec.StripBrackets(n), codec.YAML); err != nil {
		return err.Error()
	}
	return strings.TrimRight(b.String(), "\n")
}

// when is a time as a person reads it: local, to the minute, and how far off it is. Text that
// is not a time is itself.
func when(s string) string {
	t, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		return s
	}
	d := time.Until(t)
	rel := "in " + roundDur(d.Truncate(time.Second))
	if d < 0 {
		rel = roundDur((-d).Truncate(time.Second)) + " ago"
	}
	layout := "15:04"
	if t.Local().YearDay() != time.Now().YearDay() {
		layout = "Jan 2 15:04"
	}
	return t.Local().Format(layout) + tuiFaint.Render(" ("+rel+")")
}
