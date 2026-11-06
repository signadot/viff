package main

import (
	"fmt"
	"os"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/fatih/color"
	"github.com/muesli/termenv"
	"github.com/scott-cotton/cli"
)

// `verse tui` is the verse at a glance, in one terminal: the questions waiting on you, the tasks
// running and just run, the data, the charters installed over it, and the runners. It answers
// what it shows: a question proceeds or is denied with a reason, a task is cancelled.
//
// IT IS A CLIENT, as every subcommand but `up` is: it reads the routes the other commands read
// (GET /asks?waiting=me, /tasks, /state, /charter, /runners) and acts through the ones they act
// through (POST /asks/{id}/answer, /tasks/{id}/cancel), as the configured user. Nothing it shows is
// computed anywhere else.
//
// IT READS WHAT CHANGED. Every --every (2s) it reads a round: the runners, the place the Data
// section lists and the row under the cursor always; the tasks and the questions when their
// watch says they changed (tuiwatch.go); the charters while their section is open. A read is
// what makes the screen agree with what `verse question` or `verse task` print at the same
// moment, so a watch's frame only says which sections to read.
//
// A tick starts no round while one is on its way, so a slow verse is not read faster than it
// answers. A key that needs a read now starts one anyway: r, an answer, a cancel, going to a
// place in the data. Rounds may then overlap, and the one asked last is the one drawn (seq). Each
// request is bounded (readTimeout), and a section whose read fails keeps its rows and says why.
//
// THE DATA is the verse's own tree, browsed an address at a time: an address's children, and
// the one under the cursor as it stands — its record, what it was derived from (the engine's
// verse.meta.derivedFrom), the questions and tasks about it. Nothing in it knows any charter:
// what a charter keeps is data like any other, read where it stands.
//
// THE CONFIG'S `tui:` SECTION says how often it reads, where it opens, which sections it shows
// and in what order, names for places in the data, and which keys do what (tuiconfig.go,
// tuikeys.go); --config prints it as in force, --commands every command and key.

type tuiConfig struct {
	*cli.Command
	At       string `cli:"name=at desc='the address the Data section opens at (tui.at in the config; the root when neither says); VERSE_TUI_AT when left off'"`
	Every    string `cli:"name=every desc='how often to read the verse again, as a duration (tui.every in the config; 2s when neither says)'"`
	All      bool   `cli:"name=all desc='list every task the runtime holds, not only those running and the most recent'"`
	Config   bool   `cli:"name=config desc='print the tui section of the config as in force, every key included, ready to paste into ~/.config/verse.tony'"`
	Commands bool   `cli:"name=commands desc='print every command and every key, and what each does'"`
}

func tuiCmd() *cli.Command {
	cfg := &tuiConfig{}
	opts, err := cli.StructOpts(cfg)
	if err != nil {
		panic(err)
	}
	return cli.NewCommandAt(&cfg.Command, "tui").
		WithSynopsis("tui — the verse at a glance: questions waiting on you, tasks, the data, charters and runners, answered and cancelled in place [--at <address>] [--every 2s] [--config] [--commands]").
		WithOpts(opts...).
		WithRun(cfg.run)
}

func (cfg *tuiConfig) run(cc *cli.Context, args []string) error {
	args, err := cfg.Parse(cc, args)
	if err != nil {
		return err
	}
	if len(args) > 0 {
		return fmt.Errorf("%w: tui takes no positional, and %q is one", cli.ErrUsage, args[0])
	}
	at := cfg.At
	if at == "" {
		at = os.Getenv("VERSE_TUI_AT")
	}
	set, err := resolveTUI(appCfg.TUI, cfg.Every, at)
	if err != nil {
		return fmt.Errorf("%w: %v", cli.ErrUsage, err)
	}
	if cfg.Config {
		_, err := fmt.Fprint(cc.Out, set.configText())
		return err
	}
	if cfg.Commands {
		_, err := fmt.Fprint(cc.Out, commandsText(set.keys))
		return err
	}
	// Colour as every command decides it (--color, NO_COLOR, a terminal), for both palettes the
	// screen draws with: lipgloss's, and fatih/color's, which the records' YAML is coloured by.
	tuiColor = colorize(os.Stdout)
	color.NoColor = !tuiColor
	switch {
	case !tuiColor:
		lipgloss.SetColorProfile(termenv.Ascii)
	case lipgloss.ColorProfile() == termenv.Ascii:
		lipgloss.SetColorProfile(termenv.ANSI256)
	}
	// The background is asked of the terminal NOW, before the program reads the keyboard: asked
	// later, by the first adaptive colour drawn, the terminal's reply arrives among the keys.
	if tuiColor {
		lipgloss.SetHasDarkBackground(lipgloss.HasDarkBackground())
	}
	me, err := principal("")
	if err != nil {
		return err
	}
	src := &httpSource{c: apiClient(), allTasks: cfg.All, recent: set.recent}
	m := newTUI(src, me, set.every)
	set.apply(&m)
	m.watch = startTUIWatch(cc.Go, src.c)
	_, err = tea.NewProgram(m, tea.WithAltScreen(), tea.WithMouseCellMotion(), tea.WithContext(cc.Go)).Run()
	return err
}

// tuiTab is one of the screens.
type tuiTab int

const (
	tabQuestions tuiTab = iota
	tabTasks
	tabData
	tabCharters
	tabRunners
	tabCount
)

func (t tuiTab) String() string {
	return [...]string{"Questions", "Tasks", "Data", "Charters", "Runners"}[t]
}

// short is s on one line, cut to n runes.
func short(s string, n int) string {
	s = strings.Join(strings.Fields(s), " ")
	if r := []rune(s); len(r) > n {
		if n <= 1 {
			return string(r[:n])
		}
		return string(r[:n-1]) + "…"
	}
	return s
}
