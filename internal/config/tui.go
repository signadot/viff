package config

import (
	"fmt"
	"sort"
	"strings"

	"github.com/signadot/tony-format/go-tony/ir"
)

// TUIConfig is `viff tui`'s section of the config: how often it reads, where it opens, which
// sections it shows, the places it keeps names for, and which keys do what. Every field is
// optional, and a flag given to `viff tui` overrides the field it names. The tui checks it
// when it starts (an unknown section or action, a key bound twice) and says what is wrong
// rather than ignoring it.
//
//tony:schemagen=viff-tui-config,notag
type TUIConfig struct {
	// Every is how often the screen reads the viff instance again, a duration of at least 200ms. Empty
	// is 2s; --every overrides it.
	Every string `tony:"field=every,omitzero"`
	// Start is the section the screen opens on: questions, tasks, data, charters or runners.
	// Empty opens on what waits on you, else the first section with anything in it.
	Start string `tony:"field=start,omitzero"`
	// At is the address the Data section opens at; empty is the root. --at overrides it.
	At string `tony:"field=at,omitzero"`
	// Sections are the sections shown, in the order shown, the number keys going to them in
	// that order. Empty is all five: questions, tasks, data, charters, runners.
	Sections []string `tony:"field=sections,omitzero"`
	// Tasks says how many finished tasks the Tasks section lists beside the running ones.
	Tasks TUITasksConfig `tony:"field=tasks,omitzero"`
	// Bookmarks are names for places in the data: `:go <name>` opens the place, and the Data
	// section's table lists them.
	Bookmarks map[string]string `tony:"field=bookmarks,omitzero"`
	// Keys binds actions to keys: each action named (move.down, answer.proceed, task.cancel…)
	// takes the keys listed, in place of its default ones, and [] leaves it unbound. `viff tui
	// --commands` lists every action and its keys.
	Keys map[string][]string `tony:"field=keys,omitzero"`
}

// TUITasksConfig is how the Tasks section lists tasks.
//
//tony:schemagen=viff-tui-tasks-config,notag
type TUITasksConfig struct {
	// Recent is how many finished tasks are listed, the newest first; 0 is 15. --all lists
	// every one.
	Recent int `tony:"field=recent,omitzero"`
}

// tuiFields says which fields of the file's tui section are none of TUIConfig's: a misspelt
// field would otherwise be read as absent, and the setting it meant ignored without a word.
func tuiFields(merged *ir.Node) error {
	known := map[string]map[string]bool{
		"tui":       {"every": true, "start": true, "at": true, "sections": true, "tasks": true, "bookmarks": true, "keys": true},
		"tui.tasks": {"recent": true},
	}
	var unknown []string
	for at, fields := range known {
		n := merged
		for _, f := range strings.Split(at, ".") {
			n = field(n, f)
		}
		if n == nil || n.Type != ir.ObjectType {
			continue
		}
		for _, f := range n.Fields {
			if !fields[f.String] {
				unknown = append(unknown, at+"."+f.String)
			}
		}
	}
	if len(unknown) == 0 {
		return nil
	}
	sort.Strings(unknown)
	return fmt.Errorf("%s: no such field (viff tui --config prints every one)", strings.Join(unknown, ", "))
}

// field is the value of an object's field name, or nil.
func field(n *ir.Node, name string) *ir.Node {
	if n == nil || n.Type != ir.ObjectType {
		return nil
	}
	for i, f := range n.Fields {
		if f.String == name {
			return n.Values[i]
		}
	}
	return nil
}
