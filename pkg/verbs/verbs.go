// Package verbs is the viff verbs an agent is offered twice, defined once: by `viff mcp`, to an
// agent acting as you (viff_data), and by a runner's viff system, to a task acting as the
// person it runs for (viff-data-get). Each verb says its name in both, the command it mirrors,
// what it does, the parameters it takes and what a call must say; both faces read a call through
// Read, and what it sends from there (AskOf, DataGetOf, …), so what one takes or refuses the other
// does too. How each answers stays its own: the format viff mcp was started with, or tony.
//
// What only one face offers (viff_question, viff_wait, question-ask) is that face's.
package verbs

import (
	"fmt"
	"maps"
	"slices"
	"strings"

	"go.viff.dev/viff/pkg/systems"
)

// A Face is who a verb is offered to.
type Face int

const (
	// MCP is `viff mcp`: an agent acting as you, its tools spelled viff_data.
	MCP Face = iota
	// Task is a runner's viff system: a task acting as the person it runs for, its tools
	// spelled viff-data-get.
	Task
)

// Param is one parameter of a verb, and what a call must say of it.
type Param struct {
	Name string
	// Type is its JSON-schema type: string, boolean or integer.
	Type string
	Desc string
	// Need is the refusal of a call that leaves it out, or blank after trimming; a parameter
	// with one is required.
	Need string
	// With is the flag it goes with: given without it, the call is refused.
	With string
	// NonNeg is an integer that is never negative.
	NonNeg bool
	// TaskNote follows Desc where a task is offered it.
	TaskNote string
}

// Verb is one verb as both faces offer it.
type Verb struct {
	// Name is the viff system's verb (ask-create): the step viff.ask-create and the task's
	// tool viff-ask-create.
	Name string
	// Tool is viff mcp's tool (viff_ask_create).
	Tool string
	// Lead is what it does, said before who it acts as: "Read the store at an address".
	Lead string
	// CLI is the command it mirrors, as a person types it: "`viff data`".
	CLI string
	// Does is what it does, said after the command. {<verb>} in it, and in a note or a
	// parameter's description, is that verb's tool as the face spells it.
	Does string
	// MCPNote and TaskNote follow Does where that face offers it.
	MCPNote, TaskNote string
	Params            []Param
	// AnyOf is parameters of which a call names at least one, refused with AnyOfNeed.
	AnyOf     []string
	AnyOfNeed string
	// Reads is a verb that writes nothing; Takes, one that takes something away.
	Reads, Takes bool
}

// TaskTool is the verb's tool where a task is offered it: viff-<name>.
func (v *Verb) TaskTool() string { return systems.ToolName("viff", v.Name) }

// ToolOf is the verb's tool as f spells it.
func (v *Verb) ToolOf(f Face) string {
	if f == Task {
		return v.TaskTool()
	}
	return v.Tool
}

// Description is what f's agent reads about the verb: what it does, as whom, as which command
// (and, to a task, which viff mcp tool) does it too.
func (v *Verb) Description(f Face) string {
	var b strings.Builder
	b.WriteString(v.Lead)
	if f == Task {
		fmt.Fprintf(&b, " as the person this task runs for, as %s and %s do: %s", v.CLI, v.Tool, v.Does)
		if v.TaskNote != "" {
			b.WriteString(" " + v.TaskNote)
		}
	} else {
		fmt.Fprintf(&b, " as you, as %s does: %s", v.CLI, v.Does)
		if v.MCPNote != "" {
			b.WriteString(" " + v.MCPNote)
		}
	}
	return spelled(f, b.String())
}

// Properties are its parameters as JSON-schema properties, each described for f.
func (v *Verb) Properties(f Face) map[string]any {
	props := map[string]any{}
	for _, p := range v.Params {
		desc := p.Desc
		if f == Task && p.TaskNote != "" {
			desc += " " + p.TaskNote
		}
		props[p.Name] = map[string]any{"type": p.Type, "description": spelled(f, desc)}
	}
	return props
}

// Required names the parameters a call must give.
func (v *Verb) Required() []string {
	req := []string{}
	for _, p := range v.Params {
		if p.Need != "" {
			req = append(req, p.Name)
		}
	}
	return req
}

// Schema is its input as a JSON schema of an object, for f: its properties, those required, and
// no other.
func (v *Verb) Schema(f Face) map[string]any {
	return map[string]any{"type": "object", "properties": v.Properties(f), "required": v.Required(),
		"additionalProperties": false}
}

func (v *Verb) param(name string) (Param, bool) {
	for _, p := range v.Params {
		if p.Name == name {
			return p, true
		}
	}
	return Param{}, false
}

// Args is a call's arguments as Read took them: strings trimmed, booleans, integers. One absent,
// or a string blank, reads as the zero value.
type Args struct {
	words map[string]string
	flags map[string]bool
	nums  map[string]int
}

// Word is a string argument, trimmed.
func (a Args) Word(name string) string { return a.words[name] }

// Flag is a boolean argument.
func (a Args) Flag(name string) bool { return a.flags[name] }

// Num is an integer argument, and whether it was given.
func (a Args) Num(name string) (int, bool) {
	n, ok := a.nums[name]
	return n, ok
}

func (a Args) given(name string) bool {
	return a.words[name] != "" || a.flags[name] || hasKey(a.nums, name)
}

func hasKey(m map[string]int, k string) bool { _, ok := m[k]; return ok }

// Read is a call of the verb: each argument as the type its parameter says, absent or null as
// none, then held to what a call must say. One of another type, or one the verb does not take,
// is refused naming it rather than read as nothing, which for ask-create's `to` would put the
// ask to anyone.
func (v *Verb) Read(in map[string]any) (Args, error) {
	a := Args{words: map[string]string{}, flags: map[string]bool{}, nums: map[string]int{}}
	for _, k := range slices.Sorted(maps.Keys(in)) {
		val := in[k]
		if val == nil {
			continue
		}
		p, ok := v.param(k)
		if !ok {
			return a, fmt.Errorf("%s is not a parameter this tool takes", k)
		}
		switch p.Type {
		case "string":
			w, ok := val.(string)
			if !ok {
				return a, fmt.Errorf("%s is a string, not %T", k, val)
			}
			if w = strings.TrimSpace(w); w != "" {
				a.words[k] = w
			}
		case "boolean":
			b, ok := val.(bool)
			if !ok {
				return a, fmt.Errorf("%s is a boolean, not %T", k, val)
			}
			a.flags[k] = b
		case "integer":
			n, ok := numberOf(val)
			if !ok {
				return a, fmt.Errorf("%s is an integer, not %v", k, val)
			}
			a.nums[k] = n
		}
	}
	for _, p := range v.Params {
		switch {
		case p.Need != "" && !a.given(p.Name):
			return a, fmt.Errorf("%s", p.Need)
		case p.With != "" && a.given(p.Name) && !a.flags[p.With]:
			return a, fmt.Errorf("%s goes with %s", p.Name, p.With)
		case p.NonNeg && a.nums[p.Name] < 0:
			return a, fmt.Errorf("%s is never negative", p.Name)
		}
	}
	if len(v.AnyOf) > 0 && !slices.ContainsFunc(v.AnyOf, a.given) {
		return a, fmt.Errorf("%s", v.AnyOfNeed)
	}
	return a, nil
}

// numberOf is an integer however it arrived: an int, the codec's int64, or a float that is one,
// as JSON brings it.
func numberOf(v any) (int, bool) {
	switch n := v.(type) {
	case int:
		return n, true
	case int64:
		return int(n), true
	case float64:
		return int(n), n == float64(int(n))
	}
	return 0, false
}

// spelled is s with each {<verb>} its tool as f spells it.
func spelled(f Face, s string) string {
	if !strings.Contains(s, "{") {
		return s
	}
	var pairs []string
	for _, v := range All {
		pairs = append(pairs, "{"+v.Name+"}", v.ToolOf(f))
	}
	return strings.NewReplacer(pairs...).Replace(s)
}
