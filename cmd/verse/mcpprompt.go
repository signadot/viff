package main

import (
	"context"
	"fmt"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// The prompts are analyses the reads make between them: each says which tools to call, with what,
// and what to make of their answers. A prompt reads nothing itself; the agent's calls do, as
// whoever the server acts as.

func (m *mcpServer) addPrompts(s *mcp.Server) {
	s.AddPrompt(&mcp.Prompt{
		Name: "explain-charter",
		Description: "Explain the charter at an address: its rules and levels, what each rule's " +
			"condition selects now, and what the rules have spawned.",
		Arguments: []*mcp.PromptArgument{{
			Name:        "address",
			Description: "the charter's address as a kinded path (verse-dev.drift); empty is baseline's",
		}},
	}, explainCharter)
	s.AddPrompt(&mcp.Prompt{
		Name: "why-not",
		Description: "Say why a rule has not acted on a subject: its condition against the subject's " +
			"record, and the questions, suspensions and trouble standing on it.",
		Arguments: []*mcp.PromptArgument{{
			Name:        "rule",
			Description: "the charter and the rule, as <address>:<rule> (verse-dev.drift:review); :<rule> is a baseline rule",
			Required:    true,
		}, {
			Name:        "subject",
			Description: "the subject's address as a kinded path (demo.pr.1)",
			Required:    true,
		}},
	}, whyNot)
}

func explainCharter(_ context.Context, req *mcp.GetPromptRequest) (*mcp.GetPromptResult, error) {
	addr := req.Params.Arguments["address"]
	where, at := "baseline's charter", `path left out`
	if addr != "" {
		where, at = "the charter at "+addr, fmt.Sprintf("path %q", addr)
	}
	text := fmt.Sprintf(`Explain %[1]s in verse, for someone about to change it or depend on it.

1. Call verse_charter with %[2]s and depth 0: the charter as its file, its reads, strata,
   schedules and rules. Notes after it say which rules are held, suspended for every subject,
   or in trouble at their firings.
2. Call verse_charter with %[2]s and paths: the charters under this one, if any, which this
   explanation leaves out but should name.
3. For each rule, read what its condition's subject selects now: verse_data with at the subject's
   path and if its value, when it has one, with paths. Say how many entities that is and name a
   few. Say what on and when add, and what the gates ask before the action runs.
4. Call verse_flow with charter %[3]s: the live tasks these rules spawned. For a subject whose
   work you want to see as a whole, call verse_flow_show.
5. Call verse_flow with if {kind: suspension}: every suspension. %[4]s

Answer with what the charter is for, then each rule in a sentence or two: what it watches, what
it selects now, what it does, and what stands in its way. Say what you could not read rather than
guess at it.`, where, at, charterSelector(addr), covers)
	return promptText("Explain "+where, text), nil
}

func whyNot(_ context.Context, req *mcp.GetPromptRequest) (*mcp.GetPromptResult, error) {
	rule, subject := req.Params.Arguments["rule"], req.Params.Arguments["subject"]
	// The last colon, as the server reads a charter selection (trigger.ParseCharterFlag): an
	// address may hold one in a quoted segment, and a rule's name does not.
	i := strings.LastIndex(rule, ":")
	if i < 0 || i == len(rule)-1 {
		return nil, fmt.Errorf("rule is <address>:<rule>, or :<rule> for a baseline rule: %q names no rule", rule)
	}
	addr, name := rule[:i], rule[i+1:]
	if strings.Contains(addr, "*") || name == "*" {
		return nil, fmt.Errorf("why-not asks about one rule of one charter: %q selects more than one", rule)
	}
	if subject == "" {
		return nil, fmt.Errorf("subject is the address the rule has not acted on")
	}
	read := "path left out"
	if addr != "" {
		read = fmt.Sprintf("path %q", addr)
	}
	text := fmt.Sprintf(`Rule %[1]s has not acted on %[2]s. Find out why, from what verse says.

1. Call verse_charter with %[3]s and depth 0: the charter holding the rule; %[4]s is in its
   rules. Notes after it say whether the rule is held (its stratum is not installed), suspended
   for every subject, or in trouble at its firings, and about which subject.
2. Call verse_data with at %[2]q: the subject's record, or nothing, if nothing stands there.
3. Hold the rule's condition against the record. Does the subject's path select %[2]s? Does its
   value match the payload? What do on and when need that has not happened?
4. Call verse_flow with if {kind: suspension}: every suspension. %[5]s
5. Call verse_flow_show with subject %[2]q: what acted on it, what that spawned, and what holds
   it. Nothing there means no rule has acted on it.
6. Call verse_question with all and if {subject: %[2]q}: a question one of the rule's gates
   asked, still waiting for its answer.

Answer with the reason first, in one sentence, then the evidence for it from those reads. If
several things stand in the way, say each. If the rule has acted, say that and when.`, rule, subject, read, name, covers)
	return promptText(fmt.Sprintf("Why %s has not acted on %s", rule, subject), text), nil
}

// covers says which suspensions of a listing hold a rule: the listing narrowed by a selection
// answers only the suspension made with exactly that selection.
const covers = `A suspension holds a rule when its charter is
   the rule's charter (all of it, or that rule), a charter above it with recursive, or *, and its
   subject is the subject in question, a pattern selecting it, or *. Say which hold these rules,
   and why each was made.`

// charterSelector is a charter's address as verse_flow's charter takes it: every rule of it.
func charterSelector(addr string) string {
	if addr == "" {
		return `":"`
	}
	return fmt.Sprintf("%q", addr)
}

func promptText(desc, text string) *mcp.GetPromptResult {
	return &mcp.GetPromptResult{
		Description: desc,
		Messages:    []*mcp.PromptMessage{{Role: "user", Content: &mcp.TextContent{Text: text}}},
	}
}
