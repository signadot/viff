package main

import (
	"slices"
	"sort"
	"strings"
	"sync/atomic"
	"time"

	"github.com/charmbracelet/bubbles/cursor"
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/signadot/tony-format/go-tony/ir"
)

// The tui's model: the last snapshot, where the explorer's cursor is, what the content pane
// shows of it, and what the person is in the middle of (typing a reason, a command, confirming
// a cancel). Every act goes through the source and is followed by a read, so what the screen
// shows after an answer is what the verse says now.
//
// THE EXPLORER is the sections, each with its rows under it: a section's own line shows
// the section as a table in the content pane, a row's shows that row. Where the cursor is, is a
// section (tab) and either its line (onHeader) or one of its rows (cursor[tab]).

type tuiMode int

const (
	modeBrowse  tuiMode = iota
	modeSay             // typing what to say with an answer
	modeConfirm         // confirming a task's cancel
	modeFilter          // typing the explorer's filter
	modeCommand         // typing a command on the command line
	modeHelp            // the command palette, over the content pane
)

// tuiView is what the content pane draws for a place in the data: its record (the default), or
// the tasks about it on a timeline.
type tuiView int

const (
	viewRecord tuiView = iota
	viewTimeline
)

func (v tuiView) String() string { return [...]string{"record", "timeline"}[v] }

// The panes: the explorer on the left, the content on the right. Keys that move act on the one
// in focus, and a click focuses the one it lands on.
const (
	paneList = iota
	paneDetail
)

type tuiModel struct {
	src      tuiSource
	me       string
	projects string
	every    time.Duration

	snap     tuiSnapshot
	loaded   bool
	tab      tuiTab
	cursor   [tabCount]int
	onHeader bool           // the cursor is on the section's own line, not one of its rows
	folded   [tabCount]bool // a section whose rows the explorer hides
	scroll   int            // the content pane's first line
	// sel is the row under each section's cursor, by what it is (a question's id, a row's name
	// and address) rather than where it is: a read that adds, drops or reorders rows puts the
	// cursor back on the same row, so a key never acts on one the person did not see.
	sel [tabCount]string
	// polls is how many reads of the verse are on their way, shared by every copy of the model:
	// a tick while one is starts none, so a slow verse is not read faster than it answers.
	// applied is the newest read drawn, by when it was asked; an older one arriving later is
	// dropped.
	polls   *atomic.Int32
	applied int64
	width   int
	height  int
	mode    tuiMode
	pending struct{ id, choice string } // the answer waiting on what to say, or the task to cancel
	input   textinput.Model
	status  string  // the last act's outcome, or an error
	focus   int     // paneList or paneDetail
	dview   tuiView // a data row's content
	// dataAt is the address the Data section lists; dataSel the row to put its cursor on once
	// the listing arrives (the place just come up from), and entering says the listing of a
	// place just gone to has not arrived yet.
	dataAt   string
	dataSel  string
	entering bool
	// rec is the data row under the cursor as last read, for recFor; recBig says it was too big
	// to read again on every poll (it is read when the cursor comes to it, and on r).
	rec    *ir.Node
	recFor string
	recErr string
	recBig bool
	filter string // what the explorer keeps: rows whose text holds it, in any case
	finput textinput.Model
	cmd    textinput.Model
	read   time.Time
}

// snapMsg is a round of reads, and when it was asked (askSeq).
type snapMsg struct {
	snap tuiSnapshot
	seq  int64
}

var askSeq atomic.Int64

type recMsg struct {
	at  string
	rec *ir.Node
	err error
}
type tickMsg time.Time
type actedMsg struct {
	said string
	err  error
}

func newTUI(src tuiSource, me string, every time.Duration) tuiModel {
	in := textinput.New()
	in.Placeholder = "what to say with it (enter to send, esc to go back)"
	in.CharLimit = 2000
	f := textinput.New()
	f.Prompt = "/"
	f.Placeholder = "filter (enter to keep, esc to clear)"
	c := textinput.New()
	c.Prompt = ""
	c.Placeholder = "a command (tab completes, ? lists them)"
	// The cursors do not blink: a blink is a timer per keystroke, and the screen redraws for it.
	for _, t := range []*textinput.Model{&in, &f, &c} {
		t.Cursor.SetMode(cursor.CursorStatic)
	}
	return tuiModel{src: src, me: me, every: every, input: in, finput: f, cmd: c,
		width: 100, height: 30, dview: viewRecord, polls: new(atomic.Int32)}
}

func (m tuiModel) Init() tea.Cmd { return m.load() }

func (m tuiModel) load() tea.Cmd {
	src, at, polls, seq := m.src, m.dataAt, m.polls, askSeq.Add(1)
	polls.Add(1)
	snap := func() tea.Msg { return snapMsg{snap: src.snapshot(at), seq: seq} }
	// The record under the cursor is read again with the listing, unless it is too big to.
	if m.tab == tabData && m.recBig {
		return snap
	}
	if t := m.loadRecord(); t != nil {
		return tea.Batch(snap, t)
	}
	return snap
}

// loadRecord reads the place under the Data section's cursor: the content pane draws its
// record, which the listing does not hold.
func (m tuiModel) loadRecord() tea.Cmd {
	if m.tab != tabData || m.onHeader {
		return nil
	}
	it, ok := m.item()
	if !ok {
		return nil
	}
	src, at := m.src, it.note
	return func() tea.Msg {
		n, err := src.record(at)
		return recMsg{at: at, rec: n, err: err}
	}
}

func (m tuiModel) tick() tea.Cmd {
	return tea.Tick(m.every, func(t time.Time) tea.Msg { return tickMsg(t) })
}

func (m tuiModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	next, cmd := m.update(msg)
	n := next.(tuiModel)
	switch msg.(type) {
	case tea.KeyMsg, tea.MouseMsg, tea.WindowSizeMsg:
		// What scrolled past the end comes back to it, so a key up moves at once.
		n.scroll = min(n.scroll, n.maxScroll())
	}
	n.remember()
	return n, cmd
}

func (m tuiModel) update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		return m, nil
	case snapMsg:
		m.polls.Add(-1)
		if msg.seq < m.applied {
			return m, nil // a newer read is already drawn
		}
		m.applied = msg.seq
		first := !m.loaded
		m.snap, m.loaded, m.read = m.merge(msg.snap), true, time.Now()
		m.relocate()
		if m.snap.dataAt == m.dataAt && (m.entering || m.dataSel != "") {
			// The listing of the place just gone to: the cursor on the place come up from, or
			// on its first row.
			if m.dataSel != "" {
				for i, it := range m.itemsOf(tabData) {
					if it.note == m.dataSel {
						m.cursor[tabData] = i
					}
				}
			}
			m.entering, m.dataSel = false, ""
			if m.tab == tabData {
				m.onHeader = m.rows() == 0
			}
			m.clamp()
			return m, m.loadRecord()
		}
		if first {
			// Open on what waits on you, else on the first section with anything in it.
			m.onHeader = true
			for t := tuiTab(0); t < tabCount; t++ {
				m.tab = t
				if m.rows() > 0 {
					m.onHeader = false
					break
				}
			}
			if m.onHeader {
				m.tab = tabQuestions
			}
			m.clamp()
			return m, tea.Batch(m.tick(), m.loadRecord())
		}
		m.clamp()
		if m.tab == tabData && !m.onHeader {
			// A listing just arrived: the row under the cursor is read if it is not what the
			// content holds.
			if it, ok := m.item(); ok && it.note != m.recFor {
				return m, m.loadRecord()
			}
		}
		return m, nil
	case recMsg:
		if it, ok := m.item(); !ok || m.tab != tabData || it.note != msg.at {
			return m, nil // the cursor has moved on
		}
		m.recFor, m.rec, m.recErr = msg.at, msg.rec, ""
		m.recBig = nodeSize(msg.rec, recPollLimit) >= recPollLimit
		if msg.err != nil {
			m.rec, m.recErr = nil, msg.err.Error()
		}
		return m, nil
	case tickMsg:
		if m.polls.Load() > 0 {
			return m, m.tick() // the last read has not come back: wait for it
		}
		return m, tea.Batch(m.load(), m.tick())
	case actedMsg:
		if msg.err != nil {
			m.status = "✗ " + msg.err.Error()
		} else {
			m.status = "✓ " + msg.said
		}
		return m, m.load()
	case tea.KeyMsg:
		switch m.mode {
		case modeSay:
			return m.sayKey(msg)
		case modeConfirm:
			return m.confirmKey(msg)
		case modeFilter:
			return m.filterKey(msg)
		case modeCommand:
			return m.commandKey(msg)
		case modeHelp:
			return m.helpKey(msg)
		}
		return m.browseKey(msg)
	case tea.MouseMsg:
		if m.mode != modeBrowse {
			return m, nil // a click does not move what an answer or a cancel is about
		}
		return m.mouse(msg)
	}
	return m, nil
}

// exRow is one line of the explorer: a section's own line (item -1), or one of its rows.
type exRow struct {
	tab  tuiTab
	item int
}

// explorer is the explorer's lines, in order: each section, then its rows the filter keeps
// unless it is folded.
func (m tuiModel) explorer() []exRow {
	var out []exRow
	for t := tuiTab(0); t < tabCount; t++ {
		out = append(out, exRow{tab: t, item: -1})
		if m.folded[t] {
			continue
		}
		m.tab = t
		for i := range m.rows() {
			out = append(out, exRow{tab: t, item: i})
		}
	}
	return out
}

// exAt is where the cursor is among the explorer's lines.
func (m tuiModel) exAt() int {
	for i, r := range m.explorer() {
		if r.tab != m.tab {
			continue
		}
		if (m.onHeader && r.item == -1) || (!m.onHeader && r.item == m.cursor[m.tab]) {
			return i
		}
	}
	return 0
}

// goTo puts the cursor on the explorer's line r, the content at its top.
func (m *tuiModel) goTo(r exRow) {
	if r.tab != m.tab || (r.item == -1) != m.onHeader || (r.item >= 0 && r.item != m.cursor[r.tab]) {
		m.scroll = 0
	}
	m.tab, m.onHeader = r.tab, r.item == -1
	if r.item >= 0 {
		m.cursor[r.tab] = r.item
	}
}

// section goes to section t: its first row where it has one, else its own line.
func (m tuiModel) section(t tuiTab) (tea.Model, tea.Cmd) {
	if t >= tabCount {
		return m, nil
	}
	m.focus, m.scroll = paneList, 0
	m.tab, m.folded[t] = t, false
	m.onHeader = m.rows() == 0
	if !m.onHeader {
		m.cursor[t] = min(m.cursor[t], m.rows()-1)
	}
	return m, m.loadRecord()
}

// header goes to section t's own line: the section as a table.
func (m tuiModel) header(t tuiTab) (tea.Model, tea.Cmd) {
	m.focus, m.scroll, m.tab, m.onHeader = paneList, 0, t, true
	return m, nil
}

// step moves the pane in focus by d: the explorer's cursor, or the content's first line.
func (m tuiModel) step(d int) (tea.Model, tea.Cmd) {
	if m.focus == paneDetail {
		m.scroll = max(m.scroll+d, 0)
		return m, nil
	}
	ex := m.explorer()
	i := min(max(m.exAt()+d, 0), len(ex)-1)
	m.goTo(ex[i])
	return m, m.loadRecord()
}

func (m tuiModel) filterKey(k tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch k.String() {
	case "esc":
		m.mode, m.filter = modeBrowse, ""
		m.finput.SetValue("")
		m.finput.Blur()
		m.relocate()
		return m, m.loadRecord()
	case "enter":
		m.mode = modeBrowse
		m.finput.Blur()
		return m, m.loadRecord()
	}
	var cmd tea.Cmd
	m.finput, cmd = m.finput.Update(k)
	m.filter = strings.TrimSpace(m.finput.Value())
	m.scroll = 0
	m.relocate()
	m.onHeader = m.rows() == 0
	return m, cmd
}

// mouse: the wheel scrolls the pane under the pointer, a click selects the line it lands on.
func (m tuiModel) mouse(e tea.MouseMsg) (tea.Model, tea.Cmd) {
	exW, _, bodyH := m.layout()
	inList := e.X < exW
	switch {
	case e.Button == tea.MouseButtonWheelDown || e.Button == tea.MouseButtonWheelUp:
		d := 1
		if e.Button == tea.MouseButtonWheelUp {
			d = -1
		}
		if inList {
			m.focus = paneList
			return m.step(d)
		}
		m.focus = paneDetail
		return m.step(3 * d)
	case e.Button == tea.MouseButtonLeft && e.Action == tea.MouseActionPress:
		if !inList {
			m.focus = paneDetail
			return m, nil
		}
		m.focus = paneList
		row := e.Y - 2 // the command line and the rule under it
		if row < 0 || row >= bodyH {
			return m, nil
		}
		ex := m.explorer()
		if i := m.exStart(len(ex), bodyH) + row; i < len(ex) {
			m.goTo(ex[i])
		}
		return m, m.loadRecord()
	}
	return m, nil
}

func (m tuiModel) browseKey(k tea.KeyMsg) (tea.Model, tea.Cmd) {
	h := m.contentRows()
	switch k.String() {
	case "q", "ctrl+c":
		return m, tea.Quit
	case "1", "2", "3", "4", "5":
		return m.section(tuiTab(k.String()[0] - '1'))
	case "backspace", "u":
		if m.tab == tabData && m.focus == paneList {
			return m.dataGo(parentOf(m.dataAt), m.dataAt)
		}
	case ":":
		m.mode = modeCommand
		m.cmd.SetValue("")
		return m, m.cmd.Focus()
	case "?":
		m.mode = modeHelp
		return m, nil
	case "left", "h":
		if m.focus == paneList && !m.onHeader {
			// Back up to the section's own line, as a tree does.
			m.onHeader, m.scroll = true, 0
			return m, nil
		}
		m.focus = paneList
	case "enter", " ":
		if m.focus == paneList && m.onHeader && k.String() == "enter" {
			// A section's line folds its rows, or opens them.
			m.folded[m.tab] = !m.folded[m.tab]
			return m, nil
		}
		if k.String() == "enter" && m.tab == tabData && m.focus == paneList {
			// A place opens into what is under it.
			if it, ok := m.item(); ok {
				return m.dataGo(it.note, "")
			}
		}
		if k.String() == "enter" && m.rows() > 0 {
			m.focus = paneDetail
		}
		if k.String() == " " {
			m.scroll += max(h-2, 1)
		}
	case "right", "l":
		if m.focus == paneList && m.onHeader && m.rows() > 0 && m.folded[m.tab] {
			m.folded[m.tab] = false
			return m, nil
		}
		m.focus = paneDetail
	case "tab", "v":
		if vs := m.views(); len(vs) > 1 {
			m.setView(vs[(slices.Index(vs, m.shownView())+1)%len(vs)])
			return m, m.loadRecord()
		}
		if k.String() == "tab" {
			m.focus = 1 - m.focus
		}
	case "shift+tab", "V":
		if vs := m.views(); len(vs) > 1 {
			m.setView(vs[(slices.Index(vs, m.shownView())+len(vs)-1)%len(vs)])
			return m, m.loadRecord()
		}
		m.focus = 1 - m.focus
	case "esc":
		if m.filter != "" {
			m.filter = ""
			m.finput.SetValue("")
			m.relocate()
			return m, m.loadRecord()
		}
		m.focus = paneList
	case "down", "j":
		return m.step(1)
	case "up", "k":
		return m.step(-1)
	case "g", "home":
		if m.focus == paneDetail {
			m.scroll = 0
			return m, nil
		}
		return m.step(-1 << 20)
	case "G", "end":
		if m.focus == paneDetail {
			m.scroll = 1 << 20
			return m, nil
		}
		return m.step(1 << 20)
	case "pgdown", "ctrl+d", "J":
		m.scroll += max(h-2, 1)
	case "pgup", "ctrl+u", "K":
		m.scroll = max(m.scroll-max(h-2, 1), 0)
	case "/":
		m.mode = modeFilter
		m.finput.SetValue(m.filter)
		return m, m.finput.Focus()
	case "r":
		m.status = "reading…"
		return m, m.load()
	case "a", "y", "d", "n", "7", "8", "9":
		// An answer, by the key its option is shown with: a question that does not offer
		// proceed is not sent one.
		q, ok := m.question()
		if !ok {
			return m, nil
		}
		want := map[string]string{"y": "a", "n": "d"}[k.String()]
		if want == "" {
			want = k.String()
		}
		for _, o := range answerKeys(q) {
			if o.key == want {
				return m.startAnswer(q, o.option)
			}
		}
		m.status = "✗ this question's options are " + strings.Join(q.options, ", ")
	case "x":
		if m.tab == tabTasks {
			if t, ok := m.item(); ok && !isTerminalStatus(firstWord(t.state)) {
				m.mode, m.pending.id = modeConfirm, t.name
			}
		}
	}
	return m, nil
}

// tuiCommands is what the command line takes, as the palette lists them.
var tuiCommands = [][2]string{
	{"questions", "what waits on you, as a table"},
	{"tasks", "the tasks running and just run, as a table"},
	{"data [address]", "the data at an address (the root alone), what is under it"},
	{"up", "the data one level up (u, backspace)"},
	{"charters", "the charters installed, and their rules"},
	{"runners", "where work runs"},
	{"view <record|timeline>", "what the pane shows of a place in the data"},
	{"answer <option>", "answer the question under the cursor with any of its options"},
	{"find <text>", "keep the explorer's rows that hold text (/ does the same)"},
	{"top", "the content pane's first line"},
	{"bottom", "the content pane's last line"},
	{"refresh", "read the verse again (r)"},
	{"help", "this list (?)"},
	{"quit", "leave (q)"},
}

// commandKey reads the command line: enter runs it, tab completes it, esc leaves it.
func (m tuiModel) commandKey(k tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch k.String() {
	case "esc":
		m.mode = modeBrowse
		m.cmd.Blur()
		return m, nil
	case "tab":
		if c := m.complete(m.cmd.Value()); c != "" {
			m.cmd.SetValue(c)
			m.cmd.CursorEnd()
		}
		return m, nil
	case "enter":
		line := strings.TrimSpace(m.cmd.Value())
		m.mode = modeBrowse
		m.cmd.Blur()
		return m.run(line)
	}
	var cmd tea.Cmd
	m.cmd, cmd = m.cmd.Update(k)
	return m, cmd
}

// complete is line completed as far as it is one thing: a command's name, or an address, an
// option or a view after the command that takes it.
func (m tuiModel) complete(line string) string {
	verb, arg, spaced := strings.Cut(line, " ")
	var names []string
	switch {
	case !spaced:
		for _, c := range tuiCommands {
			names = append(names, strings.Fields(c[0])[0])
		}
		if c := common(verb, names); c != "" {
			return c
		}
		return ""
	case verb == "data":
		// Within the place listed: its children's addresses.
		for _, d := range m.itemsOf(tabData) {
			names = append(names, d.note)
		}
	case verb == "answer":
		if q, ok := m.question(); ok {
			names = q.options
		}
	case verb == "view":
		for _, v := range m.views() {
			names = append(names, v.String())
		}
	default:
		return ""
	}
	if c := common(arg, names); c != "" {
		return verb + " " + c
	}
	return ""
}

// common is the longest prefix every name starting with p shares, or "" for none.
func common(p string, names []string) string {
	var hit []string
	for _, n := range names {
		if strings.HasPrefix(n, p) {
			hit = append(hit, n)
		}
	}
	if len(hit) == 0 {
		return ""
	}
	sort.Strings(hit)
	c := hit[0]
	for _, n := range hit[1:] {
		for !strings.HasPrefix(n, c) {
			c = c[:len(c)-1]
		}
	}
	if len(hit) == 1 {
		c += " "
	}
	return c
}

// run does one command line.
func (m tuiModel) run(line string) (tea.Model, tea.Cmd) {
	verb, arg, _ := strings.Cut(line, " ")
	arg = strings.TrimSpace(arg)
	switch verb {
	case "":
		return m, nil
	case "questions", "tasks", "charters", "runners":
		for t := tuiTab(0); t < tabCount; t++ {
			if strings.EqualFold(t.String(), verb) {
				return m.header(t)
			}
		}
	case "data":
		return m.dataGo(strings.Trim(arg, "."), "")
	case "up":
		return m.dataGo(parentOf(m.dataAt), m.dataAt)
	case "view":
		vs := m.views()
		var names []string
		for _, v := range vs {
			// A view is named by as much of it as tells it apart: no two start alike.
			if arg != "" && strings.HasPrefix(v.String(), arg) {
				m.setView(v)
				return m, m.loadRecord()
			}
			names = append(names, v.String())
		}
		if len(vs) == 0 {
			m.status = "✗ views are a place's: open one in the data"
		} else {
			m.status = "✗ a view here is " + strings.Join(names, ", ")
		}
	case "answer":
		q, ok := m.question()
		if !ok {
			m.status = "✗ :answer answers the question under the cursor: open one"
			return m, nil
		}
		for _, o := range q.options {
			if o == arg {
				return m.startAnswer(q, o)
			}
		}
		m.status = "✗ this question's options are " + strings.Join(q.options, ", ")
	case "find":
		m.filter = arg
		m.finput.SetValue(arg)
		m.scroll = 0
		m.relocate()
		m.onHeader = m.rows() == 0
		return m, m.loadRecord()
	case "top":
		m.scroll = 0
	case "bottom":
		m.scroll = 1 << 20
	case "refresh":
		m.status = "reading…"
		return m, m.load()
	case "help":
		m.mode = modeHelp
	case "quit", "q":
		return m, tea.Quit
	default:
		m.status = "✗ no command " + verb + " (? lists them)"
	}
	return m, nil
}

func (m tuiModel) helpKey(k tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch k.String() {
	case "q", "ctrl+c":
		return m, tea.Quit
	case ":":
		m.mode = modeCommand
		m.cmd.SetValue("")
		return m, m.cmd.Focus()
	}
	m.mode = modeBrowse
	return m, nil
}

// dataGo lists the data at at, the cursor on the row whose address is sel once it arrives (or
// the first row).
func (m tuiModel) dataGo(at, sel string) (tea.Model, tea.Cmd) {
	m.tab, m.folded[tabData], m.focus, m.scroll = tabData, false, paneList, 0
	m.filter = ""
	m.finput.SetValue("")
	m.dataAt, m.dataSel, m.cursor[tabData], m.onHeader, m.entering = at, sel, 0, false, true
	m.sel[tabData], m.rec, m.recFor, m.recBig = "", nil, "", false
	m.status = "data at " + addrText(at)
	return m, m.load()
}

// addrText is an address as the screen names it: the root by name.
func addrText(at string) string {
	if at == "" {
		return "the root"
	}
	return at
}

// views is the views the row under the cursor has, in the order tab cycles them: a place in
// the data has two, everything else is drawn one way.
func (m tuiModel) views() []tuiView {
	if m.tab == tabData && !m.onHeader && m.rows() > 0 {
		return []tuiView{viewRecord, viewTimeline}
	}
	return nil
}

// shownView is the view the content pane draws.
func (m tuiModel) shownView() tuiView { return m.dview }

func (m *tuiModel) setView(v tuiView) {
	m.dview, m.scroll = v, 0
}

// startAnswer asks what to say with choice: a yes may go with nothing, a no is asked why.
func (m tuiModel) startAnswer(q tuiQuestion, choice string) (tea.Model, tea.Cmd) {
	m.mode, m.pending.id, m.pending.choice = modeSay, q.id, choice
	m.input.SetValue("")
	m.input.Placeholder = "what to say with it (enter to send, esc to go back)"
	if choice == "deny" {
		m.input.Placeholder = "why not — the steward reads this (enter to send, esc to go back)"
	}
	return m, m.input.Focus()
}

func (m tuiModel) sayKey(k tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch k.String() {
	case "esc":
		m.mode = modeBrowse
		m.input.Blur()
		return m, nil
	case "enter":
		say := strings.TrimSpace(m.input.Value())
		if m.pending.choice == "deny" && say == "" {
			m.status = "a no says why: type the reason, or esc"
			return m, nil
		}
		m.mode = modeBrowse
		m.input.Blur()
		src, id, choice, me := m.src, m.pending.id, m.pending.choice, m.me
		m.status = "answering " + choice + "…"
		return m, func() tea.Msg {
			said, err := src.answer(id, choice, me, say)
			return actedMsg{said: said, err: err}
		}
	}
	var cmd tea.Cmd
	m.input, cmd = m.input.Update(k)
	return m, cmd
}

func (m tuiModel) confirmKey(k tea.KeyMsg) (tea.Model, tea.Cmd) {
	m.mode = modeBrowse
	if k.String() != "y" {
		m.status = "not cancelled"
		return m, nil
	}
	src, id := m.src, m.pending.id
	m.status = "cancelling…"
	return m, func() tea.Msg {
		if err := src.cancel(id); err != nil {
			return actedMsg{err: err}
		}
		return actedMsg{said: "cancelled " + id}
	}
}

// rows is how many rows the current section lists, after the filter.
func (m tuiModel) rows() int {
	if m.tab == tabQuestions {
		return len(m.questions())
	}
	return len(m.items())
}

// keep is whether a row's text holds the filter.
func (m tuiModel) keep(texts ...string) bool {
	if m.filter == "" {
		return true
	}
	f := strings.ToLower(m.filter)
	for _, t := range texts {
		if strings.Contains(strings.ToLower(t), f) {
			return true
		}
	}
	return false
}

// questions is the questions the filter keeps.
func (m tuiModel) questions() []tuiQuestion {
	var out []tuiQuestion
	for _, q := range m.snap.questions {
		if m.keep(q.pose, q.id, q.subject, q.gate) {
			out = append(out, q)
		}
	}
	return out
}

// items is the current section's rows the filter keeps.
func (m tuiModel) items() []tuiItem { return m.itemsOf(m.tab) }

func (m tuiModel) itemsOf(t tuiTab) []tuiItem {
	var xs []tuiItem
	switch t {
	case tabTasks:
		xs = m.snap.tasks
	case tabRunners:
		xs = m.snap.runners
	case tabCharters:
		xs = m.snap.charters
	case tabData:
		// A listing of another place than the one asked for is a read still on its way.
		if m.snap.dataAt == m.dataAt {
			xs = m.snap.data
		}
	}
	var out []tuiItem
	for _, it := range xs {
		if m.keep(it.name, it.state, it.note) {
			out = append(out, it)
		}
	}
	return out
}

// clamp keeps each cursor on a row after a read shortened a listing.
func (m *tuiModel) clamp() {
	for t := tuiTab(0); t < tabCount; t++ {
		saved := m.tab
		m.tab = t
		if n := m.rows(); m.cursor[t] >= n {
			m.cursor[t] = max(n-1, 0)
		}
		m.tab = saved
	}
	if m.rows() == 0 && !(m.tab == tabData && m.entering) {
		m.onHeader = true
	}
}

// rowKey is what row i of section t is: a question by its id, a row by its name and address.
func (m tuiModel) rowKey(t tuiTab, i int) string {
	if t == tabQuestions {
		if qs := m.questions(); i < len(qs) {
			return qs[i].id
		}
		return ""
	}
	if xs := m.itemsOf(t); i < len(xs) {
		return xs[i].name + "\x00" + xs[i].note
	}
	return ""
}

// remember notes the row under each section's cursor (see sel).
func (m *tuiModel) remember() {
	for t := tuiTab(0); t < tabCount; t++ {
		if k := m.rowKey(t, m.cursor[t]); k != "" {
			m.sel[t] = k
		}
	}
}

// relocate puts each section's cursor back on the row it was on, wherever a read or a filter
// has moved it; a row that went leaves the cursor where it was, clamped.
func (m *tuiModel) relocate() {
	for t := tuiTab(0); t < tabCount; t++ {
		if m.sel[t] == "" {
			continue
		}
		n := len(m.itemsOf(t))
		if t == tabQuestions {
			n = len(m.questions())
		}
		for i := range n {
			if m.rowKey(t, i) == m.sel[t] {
				m.cursor[t] = i
				break
			}
		}
	}
	m.clamp()
}

// merge is a round of reads over the last: a section whose read failed keeps the rows it had,
// so one refusal or timeout does not empty it (and move its cursor).
func (m tuiModel) merge(s tuiSnapshot) tuiSnapshot {
	old := m.snap
	for t := range s.errs {
		switch t {
		case tabQuestions:
			s.questions = old.questions
		case tabTasks:
			s.tasks, s.everyTask = old.tasks, old.everyTask
		case tabRunners:
			s.runners = old.runners
		case tabCharters:
			s.charters = old.charters
		case tabData:
			if old.dataAt == s.dataAt {
				s.data = old.data
			}
		}
	}
	return s
}

// maxScroll is the furthest the content pane scrolls: its last line at the bottom.
func (m tuiModel) maxScroll() int {
	w := max(m.width-min(34, max(m.width/4, 22))-2, 16)
	text, wrap := m.contentText(w)
	text = screenSafe(text)
	n := strings.Count(text, "\n") + 1
	if wrap {
		n = strings.Count(lipgloss.NewStyle().Width(w).Render(text), "\n") + 1
	}
	return max(n-m.contentRows(), 0)
}

// recPollLimit is how many values a record may hold and still be read again on every poll.
const recPollLimit = 2000

// nodeSize counts n's values, stopping at limit.
func nodeSize(n *ir.Node, limit int) int {
	if n == nil {
		return 0
	}
	c := 1
	for _, v := range n.Values {
		if c >= limit {
			break
		}
		c += nodeSize(v, limit-c)
	}
	return c
}

func (m tuiModel) question() (tuiQuestion, bool) {
	qs := m.questions()
	if m.tab != tabQuestions || m.onHeader || len(qs) == 0 {
		return tuiQuestion{}, false
	}
	return qs[min(m.cursor[tabQuestions], len(qs)-1)], true
}

func (m tuiModel) item() (tuiItem, bool) {
	xs := m.items()
	if m.tab == tabQuestions || m.onHeader || len(xs) == 0 {
		return tuiItem{}, false
	}
	return xs[min(m.cursor[m.tab], len(xs)-1)], true
}

func firstWord(s string) string {
	if f := strings.Fields(s); len(f) > 0 {
		return f[0]
	}
	return ""
}
