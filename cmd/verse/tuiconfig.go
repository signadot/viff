package main

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/signadot/verse/internal/config"
)

// THE SCREEN IS CONFIGURED WHERE THE CLI IS: the `tui:` section of verse's own config (see
// internal/config: /etc/verse patched by ~/.config/verse, or the one file VERSE_CONFIG names),
// with a flag given to `verse tui` over the field it names. Everything in it is checked when the
// screen starts, and a mistake is said, never ignored: a key bound twice, a section that is not
// one, a bookmark that is not an address.

// tuiSettings is the tui's configuration as it is in force: the config's, under the flags.
type tuiSettings struct {
	every     time.Duration
	at        string
	start     string
	sections  []tuiTab
	recent    int
	bookmarks map[string]string
	keys      keymap
}

// resolveTUI is c with the flags over it (every and at, where given), checked.
func resolveTUI(c config.TUIConfig, everyFlag, atFlag string) (tuiSettings, error) {
	s := tuiSettings{every: 2 * time.Second, at: c.At, start: c.Start, recent: c.Tasks.Recent}
	every := c.Every
	if everyFlag != "" {
		every = everyFlag
	}
	if every != "" {
		d, err := time.ParseDuration(every)
		if err != nil || d < 200*time.Millisecond {
			return s, fmt.Errorf("tui.every (or --every) is a duration of at least 200ms, not %q", every)
		}
		s.every = d
	}
	if atFlag != "" {
		s.at = atFlag
	}
	s.at = strings.Trim(s.at, ".")
	if s.at != "" {
		if _, ok := segsOf(s.at); !ok {
			return s, fmt.Errorf("tui.at (or --at) is an address, and %q is not one", s.at)
		}
	}
	if s.recent < 0 {
		return s, fmt.Errorf("tui.tasks.recent is how many finished tasks are listed, and is not negative")
	}
	sections, err := tuiSections(c.Sections)
	if err != nil {
		return s, err
	}
	s.sections = sections
	if s.start != "" {
		t, ok := tabNamed(s.start)
		if !ok || !slicesContains(sections, t) {
			return s, fmt.Errorf("tui.start is a section shown (%s), not %q", tabNames(sections), s.start)
		}
	}
	s.bookmarks = map[string]string{}
	for name, at := range c.Bookmarks {
		if !bookmarkName(name) {
			return s, fmt.Errorf("tui.bookmarks: %q is not a bookmark's name: a name is letters, digits, - and _", name)
		}
		at = strings.Trim(at, ".")
		if _, ok := segsOf(at); !ok {
			return s, fmt.Errorf("tui.bookmarks.%s is an address, and %q is not one", name, at)
		}
		s.bookmarks[name] = at
	}
	if s.keys, err = newKeymap(c.Keys); err != nil {
		return s, err
	}
	return s, nil
}

// apply puts s on a model just made.
func (s tuiSettings) apply(m *tuiModel) {
	m.keys, m.order, m.bookmarks, m.dataAt = s.keys, s.sections, s.bookmarks, s.at
	if t, ok := tabNamed(s.start); ok {
		m.start, m.hasStart = t, true
	}
}

// tuiSections is the sections named, in order; none named is all of them.
func tuiSections(names []string) ([]tuiTab, error) {
	if len(names) == 0 {
		return []tuiTab{tabQuestions, tabTasks, tabData, tabCharters, tabRunners}, nil
	}
	var out []tuiTab
	for _, n := range names {
		t, ok := tabNamed(n)
		if !ok {
			return nil, fmt.Errorf("tui.sections: %q is not a section; they are questions, tasks, data, charters and runners", n)
		}
		if slicesContains(out, t) {
			return nil, fmt.Errorf("tui.sections names %s twice", n)
		}
		out = append(out, t)
	}
	return out, nil
}

// bookmarkName is whether name may name a bookmark: one word `:go` takes, written in the config
// as it is.
func bookmarkName(name string) bool {
	if name == "" {
		return false
	}
	for _, r := range name {
		if !(r == '-' || r == '_' || r >= '0' && r <= '9' || r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z') {
			return false
		}
	}
	return true
}

func tabNamed(name string) (tuiTab, bool) {
	for t := tuiTab(0); t < tabCount; t++ {
		if strings.EqualFold(t.String(), strings.TrimSpace(name)) {
			return t, true
		}
	}
	return 0, false
}

func tabNames(ts []tuiTab) string {
	var out []string
	for _, t := range ts {
		out = append(out, strings.ToLower(t.String()))
	}
	return strings.Join(out, ", ")
}

func slicesContains(ts []tuiTab, t tuiTab) bool {
	for _, x := range ts {
		if x == t {
			return true
		}
	}
	return false
}

// configText is s as the `tui:` section that says it, ready to paste into ~/.config/verse.tony:
// what --config prints. Every action is listed with its keys, so the one to change is there to
// edit.
func (s tuiSettings) configText() string {
	var b strings.Builder
	q := strconv.Quote
	b.WriteString("tui:\n")
	fmt.Fprintf(&b, "  every: %s\n", s.every)
	if s.start != "" {
		fmt.Fprintf(&b, "  start: %s\n", strings.ToLower(s.start))
	}
	if s.at != "" {
		fmt.Fprintf(&b, "  at: %s\n", q(s.at))
	}
	fmt.Fprintf(&b, "  sections: [%s]\n", tabNames(s.sections))
	recent := s.recent
	if recent == 0 {
		recent = taskRecent
	}
	fmt.Fprintf(&b, "  tasks:\n    recent: %d\n", recent)
	if len(s.bookmarks) > 0 {
		b.WriteString("  bookmarks:\n")
		var names []string
		for n := range s.bookmarks {
			names = append(names, n)
		}
		sort.Strings(names)
		for _, n := range names {
			fmt.Fprintf(&b, "    %s: %s\n", n, q(s.bookmarks[n]))
		}
	}
	b.WriteString("  keys:\n")
	for _, a := range tuiActions {
		var ks []string
		for _, k := range s.keys.keys[a.name] {
			if keyName(k) == " " {
				k = "space"
			}
			ks = append(ks, q(k))
		}
		fmt.Fprintf(&b, "    %s: [%s]\n", a.name, strings.Join(ks, ", "))
	}
	return b.String()
}

// commandsText is every command and every action with its keys: what --commands prints.
func commandsText(km keymap) string {
	var b strings.Builder
	b.WriteString("COMMANDS (: then the command)\n")
	for _, c := range tuiCommands {
		fmt.Fprintf(&b, "  %-38s %s\n", c[0], c[1])
	}
	b.WriteString("\nACTIONS (tui.keys in the config)\n")
	for _, a := range tuiActions {
		keys := km.all(a.name)
		if keys == "" {
			keys = "(unbound)"
		}
		fmt.Fprintf(&b, "  %-16s %-20s %s\n", a.name, keys, a.does)
	}
	return b.String()
}
