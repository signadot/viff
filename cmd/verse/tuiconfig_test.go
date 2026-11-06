package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/signadot/tony-format/go-tony/ir"

	"github.com/signadot/verse/internal/config"
)

// The tui's configuration: the `tui:` section of verse's config, under the flags.

// configured is a model made with the tui section c, as `verse tui` makes one.
func configured(t *testing.T, src *fakeTUISource, c config.TUIConfig) tuiModel {
	t.Helper()
	set, err := resolveTUI(c, "", "")
	if err != nil {
		t.Fatal(err)
	}
	m := newTUI(src, "user:you@example.com", time.Second)
	set.apply(&m)
	next, _ := m.Update(snapMsg{snap: src.snapshot(m.dataAt, readAll), seq: askSeq.Add(1)})
	m = next.(tuiModel)
	m.width, m.height = 130, 30
	return m
}

// THE DEFAULTS BIND NO KEY TWICE, and every action has a key.
func TestTheDefaultKeys(t *testing.T) {
	km, err := newKeymap(nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, a := range tuiActions {
		if km.first(a.name) == "" {
			t.Errorf("%s has no key by default", a.name)
		}
	}
}

// A KEY MOVED IS MOVED EVERYWHERE: the action takes the new key and drops the old one, and the
// footer and the question's ANSWER line show the new one; [] leaves an action unbound.
func TestTheTUIKeysAreRemapped(t *testing.T) {
	src := twoQuestions()
	m := configured(t, src, config.TUIConfig{Keys: map[string][]string{
		"answer.proceed": {"y"}, "answer.deny": {"n"}, "task.cancel": {}, "move.down": {"ctrl+n"},
	}})
	v := m.View()
	for _, want := range []string{"y proceed", "n deny"} {
		if !strings.Contains(v, want) {
			t.Errorf("the screen does not show %q:\n%s", want, v)
		}
	}
	if strings.Contains(v, "a proceed") {
		t.Errorf("the old key is still shown:\n%s", v)
	}
	m = press(t, m, "a")
	if m.mode != modeBrowse {
		t.Fatal("a still answers")
	}
	m = press(t, m, "y", "enter")
	if len(src.answered) != 1 || !strings.HasPrefix(src.answered[0], "q1 | proceed |") {
		t.Fatalf("y answered %q", src.answered)
	}
	m = press(t, m, "2", "j")
	if m.cursor[tabTasks] != 0 {
		t.Fatal("j still moves down")
	}
	m = press(t, m, "ctrl+n")
	if m.cursor[tabTasks] != 1 {
		t.Fatalf("ctrl+n did not move down: cursor %d", m.cursor[tabTasks])
	}
	m = press(t, m, "k", "x")
	if m.mode == modeConfirm || len(src.canceled) != 0 {
		t.Fatal("x still cancels, though task.cancel is unbound")
	}
	m.height = 80 // the palette whole
	m = press(t, m, "?")
	if v := m.View(); !strings.Contains(v, "(unbound)") || !strings.Contains(v, "task.cancel") {
		t.Errorf("the palette does not say task.cancel is unbound:\n%s", v)
	}
}

// THE SECTIONS ARE THE CONFIG'S, in its order: the number keys follow it, a section left out is
// neither drawn nor gone to, and the screen opens on the one start names.
func TestTheTUIShowsTheSectionsConfigured(t *testing.T) {
	src := twoQuestions()
	src.paths = []string{"repos", "repos.app"}
	src.records = map[string]*ir.Node{"repos": ir.FromMap(nil)}
	m := configured(t, src, config.TUIConfig{Sections: []string{"data", "questions"}, Start: "data"})
	if m.tab != tabData {
		t.Fatalf("start: data opened on %v", m.tab)
	}
	v := m.View()
	if strings.Contains(v, "TASKS") || strings.Contains(v, "RUNNERS") || strings.Index(v, "DATA") > strings.Index(v, "QUESTIONS") {
		t.Errorf("the explorer is not data, questions:\n%s", v)
	}
	m = press(t, m, "2")
	if m.tab != tabQuestions {
		t.Fatalf("2 went to %v, not the second section shown", m.tab)
	}
	m = press(t, m, ":", "t", "a", "s", "k", "s", "enter")
	if m.tab == tabTasks || !strings.Contains(m.status, "not shown") {
		t.Fatalf(":tasks with tasks hidden: tab %v status %q", m.tab, m.status)
	}
	// What no section shown draws is not read.
	src.reads = nil
	run(t, m, m.load())
	if len(src.reads) != 1 || src.reads[0].has(tabCharters) || src.reads[0].has(tabRunners) || !src.reads[0].has(tabTasks) {
		t.Fatalf("with charters and runners hidden, a round read %b", src.reads)
	}
}

// A BOOKMARK IS A NAME FOR A PLACE: :go opens it, and the Data section's table lists them.
func TestTheTUIGoesToABookmark(t *testing.T) {
	src := twoQuestions()
	src.paths = []string{"repos", "repos.app", "repos.app.git", "repos.app.git.pr", "repos.app.git.pr.x"}
	m := configured(t, src, config.TUIConfig{Bookmarks: map[string]string{"prs": "repos.app.git.pr"}})
	m = press(t, m, ":", "g", "o", " ", "p", "tab")
	if got := m.cmd.Value(); got != "go prs " {
		t.Fatalf("tab completed %q", got)
	}
	m = press(t, m, "enter")
	m = run(t, m, m.load())
	if m.tab != tabData || m.dataAt != "repos.app.git.pr" {
		t.Fatalf(":go prs: tab %v at %q", m.tab, m.dataAt)
	}
	m = press(t, m, "h")
	if v := m.View(); !strings.Contains(v, "BOOKMARKS") || !strings.Contains(v, "repos.app.git.pr") {
		t.Errorf("the data table does not list the bookmarks:\n%s", v)
	}
}

// A MISTAKE IN THE CONFIG IS SAID, naming what is wrong, and never ignored.
func TestTheTUIConfigIsChecked(t *testing.T) {
	for want, c := range map[string]config.TUIConfig{
		"no action move.sideways":                           {Keys: map[string][]string{"move.sideways": {"s"}}},
		`"j" is bound to both move.down and quit`:           {Keys: map[string][]string{"quit": {"j"}}},
		`"5" is bound to both section.5 and answer.option1`: {Keys: map[string][]string{"answer.option1": {"5"}}},
		`"logs" is not a section`:                           {Sections: []string{"logs"}},
		"names tasks twice":                                 {Sections: []string{"tasks", "tasks"}},
		`tui.start is a section shown`:                      {Sections: []string{"tasks"}, Start: "data"},
		"at least 200ms":                                    {Every: "10ms"},
		"is not one":                                        {Bookmarks: map[string]string{"x": "a..b"}},
	} {
		if _, err := resolveTUI(c, "", ""); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%+v: got %v, want %q", c, err, want)
		}
	}
}

// THE FLAGS ARE OVER THE CONFIG.
func TestTheTUIFlagsOverTheConfig(t *testing.T) {
	s, err := resolveTUI(config.TUIConfig{Every: "5s", At: "repos.app"}, "1s", "people")
	if err != nil || s.every != time.Second || s.at != "people" {
		t.Fatalf("every %v at %q err %v", s.every, s.at, err)
	}
	s, _ = resolveTUI(config.TUIConfig{}, "", "")
	if s.every != 2*time.Second {
		t.Fatalf("the default every is %v", s.every)
	}
}

// --config PRINTS WHAT IS IN FORCE AS A CONFIG, and that config loads back to the same: printed,
// pasted into ~/.config/verse.tony and read again, nothing moves.
func TestTheTUIConfigPrintsWhatLoadsBack(t *testing.T) {
	want, err := resolveTUI(config.TUIConfig{Every: "3s", Sections: []string{"tasks", "data"},
		Bookmarks: map[string]string{"prs": `repos.app.git.pr."12"`}, Keys: map[string][]string{"scroll.down": {"space", "ctrl+f"}}}, "", "")
	if err != nil {
		t.Fatal(err)
	}
	text := want.configText()
	got := loadTUIConfig(t, text)
	if got.configText() != text {
		t.Fatalf("printed, loaded and printed again:\n%s\nis not\n%s", got.configText(), text)
	}
}

// THE EXAMPLE IN THE DOCS LOADS, and says what it says it does.
func TestTheTUIConfigExample(t *testing.T) {
	b, err := os.ReadFile(filepath.Join("..", "..", "docs", "reference", "examples", "tui", "verse.tony"))
	if err != nil {
		t.Fatal(err)
	}
	s := loadTUIConfig(t, string(b))
	if s.every != 5*time.Second || s.at != "repos.app" || tabNames(s.sections) != "data, questions, tasks, charters" ||
		s.recent != 30 || s.bookmarks["prs"] != "repos.app.git.pr" || s.keys.action("y") != "answer.proceed" ||
		s.keys.action("a") != "" || s.keys.action("ctrl+x") != "task.cancel" {
		t.Fatalf("the example resolved to %+v", s)
	}
}

// loadTUIConfig is text read as verse's config file is, and its tui section resolved.
func loadTUIConfig(t *testing.T, text string) tuiSettings {
	t.Helper()
	p := filepath.Join(t.TempDir(), "verse.tony")
	if err := os.WriteFile(p, []byte(text), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv(config.EnvConfigPath, p)
	c, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	s, err := resolveTUI(c.TUI, "", "")
	if err != nil {
		t.Fatal(err)
	}
	return s
}
