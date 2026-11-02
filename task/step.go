package task

import (
	"fmt"
	"path"
	"strings"

	"github.com/signadot/tony-format/go-tony/ir"

	"github.com/signadot/verse/systems"
)

// Step is what a task is about to do: a call of a system's verb, `<system>.<verb>`
// (`commands.run`), and its fields, its block's attributes and its verb's fields as the
// document names them, each in its form's type (`{command: make test}`, `{force: false}`). An
// autonomy's rows match on steps (Doc), and a question about one says which.
//
// The runner works the fields out from the act it is about to carry out, never from what the
// task says of it.
//
// A step is not every action. Reading a file or listing a directory isn't one, and nothing asks
// before them.
type Step struct {
	Name string
	// Fields is an object of the step's fields, nil for a step that carries none.
	Fields *ir.Node
}

// NewStep is a step as a runner works it out, its fields as text, each typed by its form
// (systems.FieldsDoc): `force: "false"` is false, and `port: "443"` 443.
func NewStep(name string, fields map[string]string) Step {
	return Step{Name: name, Fields: systems.FieldsDoc(name, fields)}
}

// With is the step with a field set to v as given, for one whose value is structure (the form
// doc: an ask's `on`). s is not changed.
func (s Step) With(name string, v *ir.Node) Step {
	var kvs []ir.KeyVal
	if f := ir.Uncomment(s.Fields); f != nil && f.Type == ir.ObjectType {
		for i, k := range f.Fields {
			if k.String != name {
				kvs = append(kvs, ir.KeyVal{Key: ir.FromString(k.String), Val: f.Values[i]})
			}
		}
	}
	kvs = append(kvs, ir.KeyVal{Key: ir.FromString(name), Val: systems.Plain(v)})
	return Step{Name: s.Name, Fields: ir.FromKeyVals(kvs)}
}

// Field is the step's value of a field, nil where it carries none.
func (s Step) Field(name string) *ir.Node {
	if f := ir.Uncomment(s.Fields); f != nil {
		return ir.Get(f, name)
	}
	return nil
}

// Doc is the step as a row's match reads it: `system`, `verb`, then its fields, flat:
// `{system: git, verb: push, repo: github.com/acme/app, ref: refs/heads/main, force: false}`.
func (s Step) Doc() *ir.Node {
	system, verb, _ := systems.Split(s.Name)
	kvs := []ir.KeyVal{
		{Key: ir.FromString(matchSystem), Val: ir.FromString(system)},
		{Key: ir.FromString(matchVerb), Val: ir.FromString(verb)},
	}
	if f := ir.Uncomment(s.Fields); f != nil && f.Type == ir.ObjectType {
		for i, k := range f.Fields {
			kvs = append(kvs, ir.KeyVal{Key: ir.FromString(k.String), Val: f.Values[i]})
		}
	}
	return ir.FromKeyVals(kvs)
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
	StepVerseAskCreate = systems.Step("verse", "ask-create", "to", "labels", "on")
	// One of that person's asks read back, `verse ask <name|id>`'s: whether it was answered.
	StepVerseAskGet = systems.Step("verse", "ask-get")
	// All of that person's asks, `verse ask`'s listing.
	StepVerseAskList = systems.Step("verse", "ask-list")
	// The verse at an address, `verse data`'s: read, written, or removed, as that person.
	StepVerseDataGet = systems.Step("verse", "data-get", "path")
	StepVerseDataPut = systems.Step("verse", "data-put", "path", "value")
	StepVerseDataRm  = systems.Step("verse", "data-rm", "path")
	// The charters, `verse charter`'s: read, asked whether some would install, installed, or
	// removed. An install names the address it acts at (charterfile.InstallAt), which everything
	// it installs lies at or under.
	StepVerseCharterGet     = systems.Step("verse", "charter-get")
	StepVerseCharterCheck   = systems.Step("verse", "charter-check")
	StepVerseCharterInstall = systems.Step("verse", "charter-install", "at")
	StepVerseCharterRemove  = systems.Step("verse", "charter-remove")
	// A connection a task opens through the runner's proxy, which only a network of allow has:
	// the proxy decides it, by the host and the port it sees.
	StepNetworkTCPClient = systems.Step("network", "tcp-client", "mode", "host", "port")
)

// String is the step in one line: `files.write path=db/x.sql`, its fields in the order the
// document gives them.
func (s Step) String() string {
	var b strings.Builder
	b.WriteString(s.Name)
	if f := ir.Uncomment(s.Fields); f != nil && f.Type == ir.ObjectType {
		for i, k := range f.Fields {
			fmt.Fprintf(&b, " %s=%s", k.String, describeTrunc(oneLine(systems.ValueText(f.Values[i]))))
		}
	}
	return b.String()
}

// StepOf is the step an action is, for the tools every verse driver proposes: `write_file` writes
// a file, `run_bash` runs a command, and a delegation delegates. Anything else is no step.
func StepOf(tu ToolUse) (Step, bool) {
	if tu.CallAgent != nil {
		return NewStep(StepAgentCallSubAgent, map[string]string{"agent": tu.CallAgent.Agent}), true
	}
	switch tu.Name {
	case "write_file":
		return NewStep(StepFilesWrite, map[string]string{"path": rootRelative(str(tu.Input, "path"))}), true
	case "run_bash":
		return NewStep(StepCommandsRun, map[string]string{"command": str(tu.Input, "command")}), true
	}
	return Step{}, false
}

// rootRelative is a path as the executor writes it: joined onto the task's root, so `/db/x.sql`,
// `./db/x.sql` and `db/../db/x.sql` are all `db/x.sql`, and a gate on `!regexp "^db/"` sees each
// of them.
// A symlink inside the root can still lead elsewhere in it; the gate sees the path as given.
func rootRelative(p string) string {
	return strings.TrimPrefix(path.Clean("/"+p), "/")
}
