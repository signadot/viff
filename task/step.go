package task

import (
	"fmt"
	"maps"
	"path"
	"slices"
	"strings"

	"github.com/signadot/verse/systems"
)

// Step is what a task is about to do: a call of a system's verb, `<system>.<verb>`
// (`commands.run`), and its fields, its block's attributes and its verb's fields as the
// document names them (`command: make test`). An autonomy's rows
// match on steps, and a question about one says which.
//
// A step is not every action. Reading a file or listing a directory isn't one, and nothing asks
// before them.
type Step struct {
	Name   string
	Fields map[string]string
}

// The steps a gate can name: each a verb of a system (package systems), `<system>.<verb>`, and
// naming one here claims it as a step the runner stops, with the fields the code fills on it,
// which systems.Check holds to what the document gives the step. Every verb a document
// describes is a step, so a row on it decides it as written. A builder fills the fields a call has: an update
// that keeps its base has no `base`, a read of a channel no `thread`, and a row on an absent
// field does not match.
var (
	StepGitPush           = systems.Step("git", "push", "repo", "from", "ref", "delete", "force", "exists", "own")
	StepGitFetch          = systems.Step("git", "fetch", "repo", "from")
	StepGitHubCreatePR    = systems.Step("github", "create-pr", "repo", "base", "head", "own")
	StepGitHubUpdatePR    = systems.Step("github", "update-pr", "repo", "head", "base", "own")
	StepGitHubListPRs     = systems.Step("github", "list-prs", "repo")
	StepGitHubReadPR      = systems.Step("github", "read-pr", "repo", "pr")
	StepFilesWrite        = systems.Step("files", "write", "path")
	StepCommandsRun       = systems.Step("commands", "run", "command")
	StepAgentCallSubAgent = systems.Step("agent", "call-sub-agent", "agent")
	// The trackers' steps. A repository's own tracker (git-issue) is written in the checkout, as
	// commits are, and leaves when pushed; GitHub's issues and a Linear team are remote. Each verb
	// of each is a step.
	StepGitIssueCreate     = systems.Step("git-issue", "create", "repo")
	StepGitIssueComment    = systems.Step("git-issue", "comment", "repo", "issue")
	StepGitIssueClose      = systems.Step("git-issue", "close", "repo", "issue")
	StepGitIssuePull       = systems.Step("git-issue", "pull", "repo")
	StepGitIssuePush       = systems.Step("git-issue", "push", "repo")
	StepGitHubIssueCreate  = systems.Step("github-issue", "create", "repo")
	StepGitHubIssueComment = systems.Step("github-issue", "comment", "repo", "issue")
	StepGitHubIssueClose   = systems.Step("github-issue", "close", "repo", "issue")
	StepLinearIssueCreate  = systems.Step("linear-issue", "create", "team")
	StepLinearIssueComment = systems.Step("linear-issue", "comment", "issue")
	StepLinearIssueClose   = systems.Step("linear-issue", "close", "issue")
	// Slack's: each a call a runner makes with its bot token, a read among them, since what a
	// channel holds is not the task's until it is read.
	StepSlackPost  = systems.Step("slack", "post", "channel")
	StepSlackReply = systems.Step("slack", "reply", "channel", "thread")
	StepSlackRead  = systems.Step("slack", "read", "channel", "thread")
	StepSlackReact = systems.Step("slack", "react", "channel", "emoji")
	// A question the doer puts to a person, the `question-ask` tool's or codex's own.
	StepQuestionAsk = systems.Step("question", "ask")
	// An ask the doer raises for the person the task runs for, `verse ask`'s, carried to verse by
	// the runner: it does not wait.
	StepVerseAskCreate = systems.Step("verse", "ask-create")
	// One of that person's asks read back, `verse ask <name|id>`'s: whether it was answered.
	StepVerseAskGet = systems.Step("verse", "ask-get")
	// All of that person's asks, `verse ask`'s listing.
	StepVerseAskList = systems.Step("verse", "ask-list")
	// A connection a task opens through the runner's proxy, which only a network of allow has:
	// the proxy decides it, by the host and the port it sees.
	StepNetworkTCPClient = systems.Step("network", "tcp-client", "mode", "host", "port")
)

// String is the step in one line: `files.write path=db/x.sql`.
func (s Step) String() string {
	var b strings.Builder
	b.WriteString(s.Name)
	for _, k := range slices.Sorted(maps.Keys(s.Fields)) {
		fmt.Fprintf(&b, " %s=%s", k, describeTrunc(oneLine(s.Fields[k])))
	}
	return b.String()
}

// StepOf is the step an action is, for the tools every verse driver proposes: `write_file` writes
// a file, `run_bash` runs a command, and a delegation delegates. Anything else is no step.
func StepOf(tu ToolUse) (Step, bool) {
	if tu.CallAgent != nil {
		return Step{Name: StepAgentCallSubAgent, Fields: map[string]string{"agent": tu.CallAgent.Agent}}, true
	}
	switch tu.Name {
	case "write_file":
		return Step{Name: StepFilesWrite, Fields: map[string]string{"path": rootRelative(str(tu.Input, "path"))}}, true
	case "run_bash":
		return Step{Name: StepCommandsRun, Fields: map[string]string{"command": str(tu.Input, "command")}}, true
	}
	return Step{}, false
}

// rootRelative is a path as the executor writes it: joined onto the task's root, so `/db/x.sql`,
// `./db/x.sql` and `db/../db/x.sql` are all `db/x.sql`, and a gate on `db/**` sees each of them.
// A symlink inside the root can still lead elsewhere in it; the gate sees the path as given.
func rootRelative(p string) string {
	return strings.TrimPrefix(path.Clean("/"+p), "/")
}
