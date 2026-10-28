package stock

import (
	"strings"

	"github.com/signadot/verse/runner/runnerd/verse"
	"github.com/signadot/verse/systems"
)

// toolsBrief is the paragraph that says what reaches the world from a task: the tools it was
// left with after its autonomy pruned them, by system, and the two facts that hold for every one
// of them. Built from the offered list and nothing else, so it cannot name a tool the task does
// not have, nor leave one out; a task with no performed tools gets no paragraph.
//
// The two facts are what a model that reaches for a shell client lacks: there is no client for
// any of these systems in its shell and nothing it installs there reaches them, since the
// credentials are the runner's; and a call the autonomy refuses is a decision, not a transient.
func toolsBrief(tools []verse.Tool) string {
	bySystem := map[string][]string{}
	for _, t := range tools {
		system, _, ok := systems.SplitTool(t.Name)
		if !ok {
			continue
		}
		bySystem[system] = append(bySystem[system], "`"+t.Name+"`")
	}
	if len(bySystem) == 0 {
		return ""
	}
	var lines []string
	for _, d := range systems.All() { // the documents' order, which is the one the docs use
		if names, ok := bySystem[d.Name]; ok {
			lines = append(lines, strings.Join(names, ", ")+" ("+d.Name+")")
		}
	}
	return "What reaches the world from here is these tools, and nothing else: " +
		strings.Join(lines, "; ") + ". There is no command-line client for any of these systems " +
		"in your shell, and nothing you install there can reach them: the runner holds the " +
		"credentials and performs each call, and your autonomy decides each one. A call the autonomy " +
		"refuses is a decision, not an error to retry; say so and stop.\n\n"
}
