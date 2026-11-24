package main

import (
	"fmt"
	"sort"
	"strings"
	"unicode/utf8"

	tea "github.com/charmbracelet/bubbletea"
)

// THE KEYS ARE A MAP FROM ACTIONS, not a switch on keys: each thing the screen does in browsing
// is an action with a name (move.down, answer.proceed, task.cancel), its keys are listed beside
// it, and the config's `tui.keys` replaces an action's keys by naming it. What the footer, the
// palette and a question's ANSWER line show is read from the same map, so a key moved is shown
// where it moved to.
//
// Typing (a reason, a command, a filter) keeps its own keys: enter sends, esc leaves, tab
// completes. The mouse is not a key.

// tuiAction is one thing a key does while browsing: its name, what it does, and its keys by
// default.
type tuiAction struct {
	name, does string
	keys       []string
}

// tuiActions is every action, in the order the palette lists them. A key is spelled as the
// terminal library names it (ctrl+d, pgdown, shift+tab, enter), and space is "space".
var tuiActions = []tuiAction{
	{"move.down", "move down in the pane in focus", []string{"down", "j"}},
	{"move.up", "move up in the pane in focus", []string{"up", "k"}},
	{"open", "open: the detail, a place in the data; a section's line folds it", []string{"enter"}},
	{"back", "the explorer; on a row, its section's line", []string{"left", "h"}},
	{"pane.next", "the content pane", []string{"right", "l"}},
	{"data.up", "the data one level up", []string{"backspace", "u"}},
	{"view.next", "the next view (a place's timeline, a task's transcript, a charter's graph); else the other pane", []string{"tab", "v"}},
	{"view.prev", "the previous view; else the other pane", []string{"shift+tab", "V"}},
	{"scroll.down", "scroll the content a page down", []string{"pgdown", "ctrl+d", "J", "space"}},
	{"scroll.up", "scroll the content a page up", []string{"pgup", "ctrl+u", "K"}},
	{"top", "the first row, or the content's first line", []string{"g", "home"}},
	{"bottom", "the last row, or the content's last line", []string{"G", "end"}},
	{"section.1", "the first section shown", []string{"1"}},
	{"section.2", "the second section shown", []string{"2"}},
	{"section.3", "the third section shown", []string{"3"}},
	{"section.4", "the fourth section shown", []string{"4"}},
	{"section.5", "the fifth section shown", []string{"5"}},
	{"answer.proceed", "answer the question proceed, where it offers it", []string{"a", "y"}},
	{"answer.deny", "answer the question deny, saying why", []string{"d", "n"}},
	{"answer.option1", "answer with the question's first other option", []string{"7"}},
	{"answer.option2", "answer with its second other option", []string{"8"}},
	{"answer.option3", "answer with its third other option", []string{"9"}},
	{"task.cancel", "cancel the running task under the cursor", []string{"x"}},
	{"filter", "keep the explorer's rows that hold a text", []string{"/"}},
	{"clear", "clear the filter, else go back to the explorer", []string{"esc"}},
	{"command", "a command by name", []string{":"}},
	{"help", "every command and key", []string{"?"}},
	{"refresh", "read the viff again", []string{"r"}},
	{"quit", "leave", []string{"q", "ctrl+c"}},
}

// keymap is the actions' keys in force: the defaults, with the config's over them.
type keymap struct {
	keys  map[string][]string // by action
	byKey map[string]string   // the action a key does
}

// newKeymap is the default keys with over's in place of the actions it names. An action it
// does not know, or a key two actions would share, is an error naming it: a key that does two
// things does one of them silently, which is how 5 once switched sections instead of answering.
func newKeymap(over map[string][]string) (keymap, error) {
	km := keymap{keys: map[string][]string{}, byKey: map[string]string{}}
	known := map[string]bool{}
	for _, a := range tuiActions {
		known[a.name] = true
		km.keys[a.name] = a.keys
	}
	var unknown []string
	for name, keys := range over {
		if !known[name] {
			unknown = append(unknown, name)
			continue
		}
		km.keys[name] = keys
	}
	if len(unknown) > 0 {
		sort.Strings(unknown)
		return keymap{}, fmt.Errorf("tui.keys: no action %s; the actions are %s (viff tui --commands lists them)",
			strings.Join(unknown, ", "), strings.Join(actionNames(), ", "))
	}
	for _, a := range tuiActions {
		for _, k := range km.keys[a.name] {
			raw := k
			k = keyName(k)
			if k == "" {
				return keymap{}, fmt.Errorf("tui.keys: %s names an empty key", a.name)
			}
			if !isKey(k) {
				hint := ""
				if isKey(keyName(strings.ToLower(raw))) {
					hint = fmt.Sprintf(" (keys are lower case: %q)", strings.ToLower(raw))
				}
				return keymap{}, fmt.Errorf("tui.keys: %s: %q is not a key the terminal reports%s; a key is "+
					"one character (a, G, /), a name (enter, esc, tab, shift+tab, backspace, up, pgdown, home, f1…, space), "+
					"or one of those after ctrl+ or alt+ as the terminal sends it (ctrl+d, alt+x)", a.name, raw, hint)
			}
			if other, ok := km.byKey[k]; ok && other == a.name {
				return keymap{}, fmt.Errorf("tui.keys: %s lists %q twice", a.name, keyLabel(k))
			} else if ok {
				return keymap{}, fmt.Errorf("tui.keys: %q is bound to both %s and %s; a key does one thing", keyLabel(k), other, a.name)
			}
			km.byKey[k] = a.name
		}
	}
	return km, nil
}

// defaultKeymap is the keys with nothing configured.
func defaultKeymap() keymap {
	km, err := newKeymap(nil)
	if err != nil {
		panic(err) // the defaults bind no key twice; tuikeys_test.go holds them to it
	}
	return km
}

// action is what a key does while browsing, or "".
func (km keymap) action(key string) string { return km.byKey[key] }

// first is an action's first key as the screen shows it, or "" for an action left unbound.
func (km keymap) first(action string) string {
	if ks := km.keys[action]; len(ks) > 0 {
		return keyLabel(keyName(ks[0]))
	}
	return ""
}

// all is an action's keys as the screen shows them, one after the other.
func (km keymap) all(action string) string {
	var out []string
	for _, k := range km.keys[action] {
		out = append(out, keyLabel(keyName(k)))
	}
	return strings.Join(out, " ")
}

// keyPair is two keys shown as one hint: run together when each is one glyph (↑↓), else apart
// (ctrl+p/ctrl+n), or the one bound when the other is not.
func keyPair(a, b string) string {
	switch {
	case a == "" || b == "":
		return a + b
	case utf8.RuneCountInString(a) == 1 && utf8.RuneCountInString(b) == 1:
		return a + b
	}
	return a + "/" + b
}

// orNone is a key as a hint names it, or "(unbound)" for an action no key starts.
func orNone(k string) string {
	if k == "" {
		return "(unbound)"
	}
	return k
}

// keyName is a key as the terminal library reports it: "space" is " ", and "alt+space" "alt+ ".
func keyName(k string) string {
	k = strings.TrimSpace(k)
	switch k {
	case "space":
		return " "
	case "alt+space":
		return "alt+ "
	}
	return k
}

// termKeys is every key the terminal library names (enter, ctrl+d, pgdown, f1…).
var termKeys = func() map[string]bool {
	out := map[string]bool{}
	for t := tea.KeyType(-256); t <= 256; t++ {
		if s := t.String(); s != "" && s != "runes" {
			out[s] = true
		}
	}
	return out
}()

// isKey is whether k is a key the terminal library reports: a name it has, or one character,
// either one after alt+.
func isKey(k string) bool {
	k = strings.TrimPrefix(k, "alt+")
	return termKeys[k] || utf8.RuneCountInString(k) == 1
}

// keyLabel is a key as the screen shows it.
func keyLabel(k string) string {
	switch k {
	case " ":
		return "space"
	case "up":
		return "↑"
	case "down":
		return "↓"
	case "left":
		return "←"
	case "right":
		return "→"
	case "enter":
		return "⏎"
	case "backspace":
		return "⌫"
	}
	return k
}

func actionNames() []string {
	out := make([]string, 0, len(tuiActions))
	for _, a := range tuiActions {
		out = append(out, a.name)
	}
	return out
}
