package task

import (
	"fmt"
	"path"
	"slices"
	"strings"

	"github.com/signadot/tony-format/go-tony/ir"

	"go.viff.dev/viff/pkg/systems"
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
				kvs = append(kvs, ir.KeyVal{Key: ir.FromString(k.String), Val: systems.Plain(f.Values[i])})
			}
		}
	}
	kvs = append(kvs, ir.KeyVal{Key: ir.FromString(name), Val: systems.Plain(v)})
	return Step{Name: s.Name, Fields: ir.FromKeyVals(kvs)}
}

// Same is whether o is the same step: its name, and fields that say the same, however they were
// spelled on the way (a flow object read back is tagged `!bracket`), no fields being `{}`.
func (s Step) Same(o Step) bool {
	return s.Name == o.Name && plainFields(s.Fields).DeepEqual(plainFields(o.Fields))
}

// Differs is what o says otherwise than s, by name and never by value: `name` where the steps
// are of different verbs, else each field one carries and the other does not or says otherwise,
// in order. A field of form `doc` that is an object on both sides is named by the keys under it
// that differ, one level down (`arguments.assignee`): a call's arguments are one field, and
// naming the field alone says nothing of which argument moved. Nil where o is the same step
// (Same). A refusal names these and not the values: it is logged, and a value (a command's
// variables, a call's arguments) is not the runner's to repeat.
func (s Step) Differs(o Step) []string {
	if s.Same(o) {
		return nil
	}
	if s.Name != o.Name {
		return []string{"name"}
	}
	a, b := plainFields(s.Fields), plainFields(o.Fields)
	var differ []string
	for _, n := range keysDiffer(a, b) {
		x, y := ir.Get(a, n), ir.Get(b, n)
		if systems.FormOf(s.Name, n) == systems.FormDoc && x != nil && y != nil && x.Type == ir.ObjectType && y.Type == ir.ObjectType {
			if under := keysDiffer(x, y); under != nil {
				for _, k := range under {
					differ = append(differ, n+"."+k)
				}
				continue
			}
		}
		differ = append(differ, n)
	}
	if differ == nil { // fields that are not an object, saying otherwise
		return []string{"fields"}
	}
	return differ
}

// keysDiffer is each key of two objects that one carries and the other does not, or that they
// give different values, a's keys first and then b's own, in order.
func keysDiffer(a, b *ir.Node) []string {
	var names []string
	for _, k := range a.Fields {
		names = append(names, k.String)
	}
	for _, k := range b.Fields {
		if !slices.Contains(names, k.String) {
			names = append(names, k.String)
		}
	}
	var differ []string
	for _, n := range names {
		x, y := ir.Get(a, n), ir.Get(b, n)
		if x == nil || y == nil || !x.DeepEqual(y) {
			differ = append(differ, n)
		}
	}
	return differ
}

// plainFields is fields without their layout, `{}` for none.
func plainFields(f *ir.Node) *ir.Node {
	if f = systems.Plain(ir.Uncomment(f)); f == nil || f.Type == ir.NullType {
		return ir.FromKeyVals(nil)
	}
	return f
}

// Field is the step's value of a field, nil where it carries none.
func (s Step) Field(name string) *ir.Node {
	if f := ir.Uncomment(s.Fields); f != nil {
		return ir.Get(f, name)
	}
	return nil
}

// The steps a gate can name: each a verb of a system (package systems), `<system>.<verb>`, and
// naming one here claims it as a step the runner stops, with the fields the code fills on it,
// which systems.Check holds to what the document gives the step. Every verb a document
// describes is a step, so a row on it decides it as written. A builder fills the fields a call has: an update
// that keeps its base has no `base`, a read of a channel no `thread`, and a row on an absent
// field does not match.
var (
	StepGitPush        = systems.Step("git", "push", "repo", "from", "ref", "delete", "force", "exists", "own")
	StepGitFetch       = systems.Step("git", "fetch", "repo", "from")
	StepGitHubCreatePR = systems.Step("github", "create-pr", "repo", "base", "head", "own", "draft")
	StepGitHubUpdatePR = systems.Step("github", "update-pr", "repo", "head", "base", "own")
	StepGitHubListPRs  = systems.Step("github", "list-prs", "repo")
	StepGitHubReadPR   = systems.Step("github", "read-pr", "repo", "pr")
	// A comment on a pull request's conversation, and a reply in an inline comment's thread: each
	// carries the pull request, so a row pins a task to one.
	StepGitHubCommentPR    = systems.Step("github", "comment-pr", "repo", "pr")
	StepGitHubReplyComment = systems.Step("github", "reply-comment", "repo", "pr", "comment")
	StepFilesWrite         = systems.Step("files", "write", "path")
	StepCommandsRun        = systems.Step("commands", "run", "command")
	StepAgentCallSubAgent  = systems.Step("agent", "call-sub-agent", "agent")
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
	// An ask the doer raises for the person the task runs for, `viff ask`'s, carried to viff by
	// the runner: it does not wait.
	StepViffAskCreate = systems.Step("viff", "ask-create", "to", "labels", "on")
	// One of that person's asks read back, `viff ask <name|id>`'s: whether it was answered.
	StepViffAskGet = systems.Step("viff", "ask-get")
	// All of that person's asks, `viff ask`'s listing.
	StepViffAskList = systems.Step("viff", "ask-list")
	// The viff at an address, `viff data`'s: read, written, or removed, as that person.
	StepViffDataGet = systems.Step("viff", "data-get", "path")
	StepViffDataPut = systems.Step("viff", "data-put", "path", "value")
	StepViffDataRm  = systems.Step("viff", "data-rm", "path")
	// The charters, `viff charter`'s: read, asked whether some would install, installed, or
	// removed. An install names the address it acts at (charterfile.InstallAt), which everything
	// it installs lies at or under.
	StepViffCharterGet     = systems.Step("viff", "charter-get")
	StepViffCharterCheck   = systems.Step("viff", "charter-check")
	StepViffCharterInstall = systems.Step("viff", "charter-install", "at")
	StepViffCharterRemove  = systems.Step("viff", "charter-remove")
	// A connection a task opens through the runner's proxy, which only a network of allow has:
	// the proxy decides it, by the host and the port it sees.
	StepNetworkTCPClient = systems.Step("network", "tcp-client", "mode", "host", "port")
	// One command the runner runs for a task with one secret of its store, under the block's `as`:
	// the command as one line (systems.Argv), where it runs, the plain variables beside it, and
	// whether its stdin is open to the task.
	StepSecretExec = systems.Step("secret", "exec", "name", "as", "argv", "dir", "stdin", "env")
	// One call of a tool of an MCP server the operator names, which the runner makes with the
	// server's credential: the server's name, the tool's, and the call's arguments whole.
	StepMCPCall = systems.Step("mcp", "call", "server", "tool", "arguments")
	// One package installed by the runner into the task's read-only prefix, from the repositories
	// the operator trusts: the package, the version where the step pins one, and the command's
	// name where it overrides the package's own.
	StepPkgInstall = systems.Step("pkg", "install", "pkg", "version", "as")
)

// String is the step in one line: `files.write path=db/x.sql`, its fields in the order the
// document gives them.
func (s Step) String() string {
	var b strings.Builder
	b.WriteString(s.Name)
	if f := ir.Uncomment(s.Fields); f != nil && f.Type == ir.ObjectType {
		for i, k := range f.Fields {
			fmt.Fprintf(&b, " %s=%s", k.String, describeTrunc(oneLine(systems.ValueText(systems.FormOf(s.Name, k.String), f.Values[i]))))
		}
	}
	return b.String()
}

// StepOf is the step an action is, for the tools every viff driver proposes: `write_file` writes
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
