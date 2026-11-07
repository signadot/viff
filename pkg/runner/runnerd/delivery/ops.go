package delivery

import (
	"cmp"
	"context"
	"fmt"
	"maps"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/signadot/tony-format/go-tony/ir"
	"github.com/signadot/verse/pkg/runner/runnerd/workspace"
	"github.com/signadot/verse/pkg/systems"
	"github.com/signadot/verse/pkg/task"
)

// AN OP IS ONE ACT THE RUNNER PERFORMS FOR A TASK, and the table below is every one of them. An
// op does not perform anything itself: it reads the request into the steps it asks about and the
// act that follows them (ask), and perform checks every step (refused) before it calls the act.
// So NO ACT RUNS UNCHECKED, and that is the table's doing rather than each op's to remember: an
// op that forgets to ask has no act to hand back, and one that answers OK with none is refused
// here as the bug it is.
type op struct {
	// steps are the steps the op may ask about, by name: the verbs of the systems' documents it
	// performs. What it asks about at a request is among them, or perform refuses it.
	steps []string
	// takes, flags and numbers are the names the op reads of what a request is asked with
	// (Request.Fields), each by how it reads it: as text (Request.Field), as true or false
	// (Request.Flag), as a number (Request.Int). A request that names another is refused, and
	// so is a flag or a number that is not one. An op that reads a name another way than it
	// says here is answered as a bug (misreading). One that reads a name it does not list
	// reads nothing: no request that gives the name reaches it.
	takes, flags, numbers []string
	// body is whether the op reads the request's body. A request that carries one to an op
	// that does not is refused, as a name the op does not read is.
	body bool
	// unasked is why the op asks about no step, for the one that does not: it answers without
	// an act, and says here what bounds it instead.
	unasked string
	// deadline is how long the op has, from when it is begun to its answer. An op in turn is
	// begun once those ahead of it are done.
	deadline time.Duration
	// apart is an op performed apart from the others, where the rest are performed IN TURN, in
	// the order the task asked. One apart does not wait for those in turn, nor they for it, so
	// a call that takes minutes does not hold a push. It reads nothing of what an op in turn
	// writes (the task's branches, its checkout), and cannot: the task it is handed is without
	// them (Task.apart). It is given up when the task's process is gone, where an op in turn
	// is finished (Channel.Settle).
	apart bool
	// ask reads a request: the steps it asks about and the act that performs it once each has
	// passed, or the reply that answers it before any step does, a request that does not read
	// or names what is not there.
	ask func(s *Server, ctx context.Context, tk Task, req Request) (asked, Reply)
}

// asked is what a request asks for: the steps the task's environment, its autonomy and the
// runner's ceiling decide, and the act performed once every one of them has passed. The act
// holds what ask read on the way (the tree, the credential, the branch's state at the forge),
// so what is performed is what was decided.
type asked struct {
	steps []task.Step
	act   func() Reply
}

// asking is a request that asks about one step, and what performs it.
func asking(st task.Step, act func() Reply) (asked, Reply) {
	return asked{steps: []task.Step{st}, act: act}, Reply{}
}

// before is a request answered before it asked about any step.
func before(rep Reply) (asked, Reply) { return asked{}, rep }

// ops is every op of the channel, by the word the request names it with. An op named for its
// step (`slack.post`) is one word on the wire for one act; the first four are older than that.
var ops = map[string]op{
	"push": {steps: []string{task.StepGitPush}, takes: []string{"repo", "branch"}, flags: []string{"delete", "force"},
		deadline: workspace.ForgeTimeout, ask: (*Server).push},
	"fetch": {steps: []string{task.StepGitFetch}, takes: []string{"repo"}, deadline: workspace.ForgeTimeout, ask: (*Server).refresh},
	OpState: {unasked: "it says what a fetch would, to a task whose environment gives git.push",
		takes: []string{"repo", "branch"}, deadline: workspace.ForgeTimeout, ask: (*Server).branchState},
	OpCreatePR: {steps: []string{task.StepGitHubCreatePR}, takes: []string{"repo", "branch", "base", "title"}, flags: []string{"draft"}, body: true,
		deadline: workspace.ForgeTimeout, ask: (*Server).pullRequest},
	// An update leaves a pull request's draft state as it is, so it reads no `draft`.
	OpUpdatePR: {steps: []string{task.StepGitHubUpdatePR}, takes: []string{"repo", "branch", "base", "title"}, body: true,
		deadline: workspace.ForgeTimeout, ask: (*Server).pullRequest},

	OpGitHubListPRs: named(OpGitHubListPRs, (*Server).githubRead, "repo"),
	OpGitHubReadPR:  named(OpGitHubReadPR, (*Server).githubRead, "repo", "pr"),

	OpGitHubCommentPR:    named(OpGitHubCommentPR, (*Server).githubComment, "repo", "pr").withBody(),
	OpGitHubReplyComment: named(OpGitHubReplyComment, (*Server).githubComment, "repo", "pr", "comment").withBody(),

	OpGitIssuePull:       named(OpGitIssuePull, (*Server).gitIssuePull, "repo"),
	OpGitIssuePush:       named(OpGitIssuePush, (*Server).gitIssuePush, "repo"),
	OpGitHubIssueCreate:  named(OpGitHubIssueCreate, (*Server).githubIssue, "repo", "title").withBody(),
	OpGitHubIssueComment: named(OpGitHubIssueComment, (*Server).githubIssue, "repo", "issue").withBody(),
	OpGitHubIssueClose:   named(OpGitHubIssueClose, (*Server).githubIssue, "repo", "issue"),
	OpLinearIssueCreate:  named(OpLinearIssueCreate, (*Server).linearIssue, "team", "title").withBody(),
	OpLinearIssueComment: named(OpLinearIssueComment, (*Server).linearIssue, "issue").withBody(),
	OpLinearIssueClose:   named(OpLinearIssueClose, (*Server).linearIssue, "issue"),

	// A post reads `thread` to refuse it: a message in a thread is a reply.
	OpSlackPost:  named(OpSlackPost, (*Server).slack, "channel", "thread").withBody(),
	OpSlackReply: named(OpSlackReply, (*Server).slack, "channel", "thread").withBody(),
	OpSlackRead:  named(OpSlackRead, (*Server).slack, "channel", "thread", "since").withNumbers("limit"),
	OpSlackReact: named(OpSlackReact, (*Server).slack, "channel", "message", "emoji"),

	OpVerseAskCreate:      named(OpVerseAskCreate, (*Server).verseAskCreate, "ask"),
	OpVerseAskGet:         named(OpVerseAskGet, (*Server).verseAskGet, "ask"),
	OpVerseAskList:        named(OpVerseAskList, (*Server).verseAskList),
	OpVerseDataGet:        named(OpVerseDataGet, (*Server).verseRouted, "query"),
	OpVerseDataPut:        named(OpVerseDataPut, (*Server).verseRouted, "query").withBody(),
	OpVerseDataRm:         named(OpVerseDataRm, (*Server).verseRouted, "query"),
	OpVerseCharterGet:     named(OpVerseCharterGet, (*Server).verseRouted, "query"),
	OpVerseCharterCheck:   named(OpVerseCharterCheck, (*Server).verseRouted, "query").withBody(),
	OpVerseCharterInstall: named(OpVerseCharterInstall, (*Server).verseRouted, "query").withBody(),
	OpVerseCharterRemove:  named(OpVerseCharterRemove, (*Server).verseRouted, "query"),
}

// named is an op whose word on the wire is its step's name, with the deadline of a call to a
// forge or a service, reading takes of what it is asked with.
func named(step string, ask func(*Server, context.Context, Task, Request) (asked, Reply), takes ...string) op {
	return op{steps: []string{step}, takes: takes, deadline: workspace.ForgeTimeout, ask: ask}
}

// withBody is the op, reading the request's body.
func (o op) withBody() op {
	o.body = true
	return o
}

// withNumbers is the op, reading names as numbers.
func (o op) withNumbers(names ...string) op {
	o.numbers = names
	return o
}

// reads is every name the op reads of what a request is asked with, however it reads each.
func (o op) reads() []string { return slices.Concat(o.takes, o.flags, o.numbers) }

// misread is why a request does not read for its op, "" where it does: a name the op does not
// read, a body where it reads none, a flag or a number that is not one. A flag that is neither
// `true` nor `false` is refused rather than read as false: `delete: yes` read as no delete is
// a push where a removal was asked for. A name given nothing (`""`) is let by whatever the op
// reads: it asks for nothing, as a tool's unset parameter does (stock's untaken).
func misread(o op, req Request) string {
	for _, name := range slices.Sorted(maps.Keys(req.Fields)) {
		v := req.Fields[name]
		if v == "" {
			continue
		}
		switch {
		case slices.Contains(o.takes, name):
		case slices.Contains(o.flags, name):
			if v != "true" && v != "false" {
				return fmt.Sprintf("%s reads `%s` as true or false, and it is %q", req.Op, name, v)
			}
		case slices.Contains(o.numbers, name):
			if _, err := strconv.Atoi(v); err != nil {
				return fmt.Sprintf("%s reads `%s` as a number, and it is %q", req.Op, name, v)
			}
		default:
			return fmt.Sprintf("%s reads no `%s` (it reads: %s)", req.Op, name, cmp.Or(strings.Join(o.reads(), ", "), "nothing"))
		}
	}
	if req.Body != "" && !o.body {
		return fmt.Sprintf("%s reads no body", req.Op)
	}
	return ""
}

// misreading is an op reading a name another way than its entry in the table says, as a flag
// where the entry says text. What misread checked is then not what the op read, a `delete: yes`
// let through as text and read as no delete, so the request is answered as a bug and nothing
// more of it is performed. Read so before its steps, nothing was performed; read so in its act,
// what the act did before then is done, and the answer says that is not known.
type misreading struct{ name, as string }

// perform answers one request with its op: what the op asks about is decided, step by step, and
// only then performed. A request the op answered before any step is answered so, a refusal or
// a failure; an OK there is an act nobody decided, and only the op the table says asks about
// nothing (unasked) may give one.
func (s *Server) perform(ctx context.Context, tk Task, o op, req Request) (out Reply) {
	if why := misread(o, req); why != "" {
		return Reply{Error: why + ", and nothing was done"}
	}
	req.op = &o
	acting := false
	defer func() {
		switch r := recover().(type) {
		case nil:
		case misreading:
			what := fmt.Sprintf("%s read `%s` as %s, and its entry in the table says it reads it another way", req.Op, r.name, r.as)
			if !acting {
				out = s.bug(tk, "%s", what)
				return
			}
			// The act was begun, and may have posted or pushed before it read the name: the
			// answer does not say nothing was performed, which a tool would take as leave to
			// do a write again.
			fmt.Fprintf(s.Log, "delivery: %s: BUG: %s, in its act\n", tk.ID, what)
			out = Reply{Error: "the runner stopped while performing it: " + what + "; whether it was performed is " +
				"not known, so read it back before doing it again"}
		default:
			panic(r)
		}
	}()
	a, rep := o.ask(s, ctx, tk, req)
	if a.act == nil {
		if rep.OK && o.unasked == "" {
			return s.bug(tk, "%s answered without asking about a step", req.Op)
		}
		return rep
	}
	if len(a.steps) == 0 {
		return s.bug(tk, "%s has an act and asked about no step", req.Op)
	}
	for _, st := range a.steps {
		if !slices.Contains(o.steps, st.Name) {
			return s.bug(tk, "%s asked about %s, which is no step of its own", req.Op, st.Name)
		}
		if name, stray := undescribed(st); stray {
			return s.bug(tk, "%s asked about %s with a field `%s` its system's document does not describe", req.Op, st.Name, name)
		}
		if why := s.refused(tk, req.Decision, st); why != "" {
			return Reply{Refused: why}
		}
	}
	acting = true
	return a.act()
}

// undescribed is a field of a step that its system's document does not describe, and whether
// there is one: a field no row could name, and no environment's shape could bound.
func undescribed(st task.Step) (string, bool) {
	described, _ := systems.Fields(st.Name)
	f := ir.Uncomment(st.Fields)
	if f == nil || f.Type != ir.ObjectType {
		return "", false
	}
	for _, k := range f.Fields {
		if !slices.Contains(described, k.String) {
			return k.String, true
		}
	}
	return "", false
}

// bug is the answer to a request an op of the table mishandled: nothing was performed, and the
// runner's log says what the op did.
func (s *Server) bug(tk Task, format string, args ...any) Reply {
	what := fmt.Sprintf(format, args...)
	fmt.Fprintf(s.Log, "delivery: %s: BUG: %s\n", tk.ID, what)
	return Reply{Error: "the runner did not perform it: " + what}
}
