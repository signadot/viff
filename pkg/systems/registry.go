package systems

import (
	"fmt"
	"slices"
	"sort"
	"strings"
	"sync"
)

// THE CODE CLAIMS THE VERBS IT HONOURS BY ASKING FOR THEIR NAMES, AND SAYS WHAT FIELDS IT FILLS.
// Tool and Step are how a runner names a tool it offers and a step it stops, and each records
// that the verb is implemented; a pair no document describes is refused where the name is asked
// for, at the package's init, so code naming an undocumented verb does not build. A Step names
// the verb's fields as the code fills them, which is the one place the code says so. Check is
// the other direction: every documented verb was claimed by something, and the fields a step
// names are the ones its document gives the step (its attributes and the verb's fields),
// neither more nor fewer, so a field added to a document that nothing fills, or a field the code fills that no document names, is named at
// the test that calls Check rather than by a row that never matches.
//
// What Check holds is a property of the source tree, not of a runner: every binary links every
// claim. Which verbs a RUNNER performs is its offer's to say, from its configuration, and viff
// holds a spawn to that.
var (
	claimMu sync.Mutex
	// claimed is each claimed step and the fields its Step claim names; nil where only a Tool
	// has claimed it, which names no fields.
	claimed = map[string][]string{}
)

// Tool is the tool name of a system's verb, `<system>-<verb>`, claimed: the caller implements it.
// A verb no document describes is a build mistake, and panics.
func Tool(system, verb string) string {
	claim(system, verb, nil)
	return ToolName(system, verb)
}

// Step is the step name of a system's verb, `<system>.<verb>`, claimed as Tool is, with the
// fields the code fills on the step: what a row's match names. Check holds them to the
// document's attributes and the verb's fields.
func Step(system, verb string, fields ...string) string {
	claim(system, verb, append([]string{}, fields...)) // a list, empty for a step with no fields
	return StepName(system, verb)
}

func claim(system, verb string, fields []string) {
	d, ok := Get(system)
	if !ok {
		panic(fmt.Sprintf("systems: no document describes a system %q (the verb %s)", system, verb))
	}
	if _, ok := d.Verb(verb); !ok {
		panic(fmt.Sprintf("systems: the %s document has no verb %q", system, verb))
	}
	claimMu.Lock()
	defer claimMu.Unlock()
	name := StepName(system, verb)
	if fields != nil || claimed[name] == nil {
		claimed[name] = fields
	}
}

// Check is whether the code has claimed every verb the documents describe, naming its fields,
// and whether those fields are the step's as the document gives them (its attributes and the
// verb's fields): what is missing or extra is named. A test
// asks it, since what it holds is the tree's; a runner may ask at start as well.
func Check() error {
	claimMu.Lock()
	defer claimMu.Unlock()
	return check(All(), claimed)
}

func check(docs []Document, claimed map[string][]string) error {
	var wrong []string
	for _, d := range docs {
		for _, v := range d.Verbs {
			name := StepName(d.Name, v.Name)
			fields, ok := claimed[name]
			switch {
			case !ok:
				wrong = append(wrong, name+": nothing here implements it")
				continue
			case fields == nil:
				wrong = append(wrong, name+": no step names its fields")
				continue
			}
			stepFields, _ := d.StepFields(v.Name)
			params := make([]string, len(stepFields))
			for i, f := range stepFields {
				params[i] = f.Name
			}
			var missing, extra []string
			for _, p := range params {
				if !slices.Contains(fields, p) {
					missing = append(missing, p)
				}
			}
			for _, f := range fields {
				if !slices.Contains(params, f) {
					extra = append(extra, f)
				}
			}
			if len(missing) > 0 {
				wrong = append(wrong, fmt.Sprintf("%s: the document has %s, which the code never fills", name, strings.Join(missing, ", ")))
			}
			if len(extra) > 0 {
				wrong = append(wrong, fmt.Sprintf("%s: the code fills %s, which the document does not name", name, strings.Join(extra, ", ")))
			}
		}
	}
	if len(wrong) == 0 {
		return nil
	}
	sort.Strings(wrong)
	return fmt.Errorf("the documents and the code disagree: %s", strings.Join(wrong, "; "))
}
