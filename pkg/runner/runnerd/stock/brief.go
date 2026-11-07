package stock

import (
	"slices"
	"strings"

	"github.com/signadot/verse/pkg/runner/runnerd/verse"
	"github.com/signadot/verse/pkg/systems"
)

// toolsBrief is the paragraph that says what reaches the world from a task: the tools it was
// left with after its autonomy pruned them, by system, and the two facts that hold for every one
// of them. Built from the offered list, so it cannot name a tool the task does not have, nor
// leave one out; a task with no performed tools gets no paragraph.
//
// The two facts are what a model that reaches for a shell client lacks: there is no client for
// any of these systems in its shell and nothing it installs there reaches them, since the
// credentials are the runner's; and a call the autonomy refuses is a decision, not a transient.
//
// It names the environment's secrets too, which is the one thing a tool's name does not carry:
// every other system's target is in the name the task is offered (`git-push`, `slack-post`), and
// `secret-exec` takes its secret as an argument, so a task that is not told the names guesses
// them, and the variable the secret arrives in is the wrong guess nearest to hand.
func toolsBrief(tools []verse.Tool, env systems.Environment) string {
	bySystem := map[string][]string{}
	for _, t := range tools {
		st, ok := t.StepName()
		if !ok {
			continue
		}
		system, _, _ := systems.Split(st)
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
	brief := "What reaches the world from here is these tools, and nothing else: " +
		strings.Join(lines, "; ") + ". There is no command-line client for any of these systems " +
		"in your shell, and nothing you install there can reach them: the runner holds the " +
		"credentials and performs each call, and your autonomy decides each one. A call the autonomy " +
		"refuses is a decision, not an error to retry; say so and stop."
	if _, ok := bySystem["secret"]; ok {
		brief += secretsBrief(env)
	}
	return brief + "\n\n"
}

// secretsBrief names the secrets the environment gives, each with the variable its command finds
// it in: `secret-exec`'s `name` is one of these and nothing else, and the variable is not one of
// them. Empty where the environment gives no block a name, which leaves the sentence out rather
// than promising secrets a task has none of.
func secretsBrief(env systems.Environment) string {
	var named []string
	for _, b := range env.Blocks("secret") {
		name := b.Attributes["name"]
		if name == "" {
			continue
		}
		if as := b.Attributes["as"]; as != "" {
			named = append(named, "`"+name+"`, in `"+as+"`")
			continue
		}
		named = append(named, "`"+name+"`")
	}
	if len(named) == 0 {
		return ""
	}
	slices.Sort(named)
	if len(named) == 1 {
		return " The one secret `secret-exec` may name is " + named[0] +
			": its `name` is the secret's own name, not the variable's."
	}
	return " The secrets `secret-exec` may name are " + strings.Join(named, "; ") +
		": its `name` is the secret's own name, one of those, and never the variable's."
}
