package delivery

import (
	"bufio"
	"cmp"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"

	"github.com/go-git/go-git/v5/plumbing/transport"

	"github.com/signadot/verse/codec"
	"github.com/signadot/verse/runner"
	"github.com/signadot/verse/runner/runnerd/workspace"
	"github.com/signadot/verse/systems"
	"github.com/signadot/verse/task"
)

// Server is the runner's end of the channel: it holds the mirrors work is staged in and pushed
// from, and performs each task's requests with what that task's Credential provides.
type Server struct {
	Mirrors *workspace.Mirrors
	Log     io.Writer
	// HTTP reaches the forge's API for pull requests and issues, Linear's and Slack's; nil is
	// http.DefaultClient.
	HTTP *http.Client
	// Services is what the runner holds for the systems beyond its git token: a Linear
	// credential, a Slack bot token, and where their APIs are when a test moves them.
	Services Services
	// Ceilings are the operator's bounds on what any task may do with each system this runner
	// performs, by system: THE RUNNER'S OWN LINE, read at each act after the task's autonomy,
	// whatever the autonomy said and whoever answered its questions. A system they name no
	// ceiling for is closed (systems.Beyond).
	Ceilings map[string]systems.Ceiling
}

// Services is what a runner holds for the systems beyond its git token: a Linear credential (the
// Authorization header, verbatim: a personal key bare, an OAuth token as `Bearer …`), a Slack
// bot token (`xoxb-…`), and where the APIs are when something other than the real ones answers
// (a test).
type Services struct {
	LinearAuth string
	LinearAPI  string // "" is Linear's own
	// GitHubAPI is the API of the forge the runner's repos are on: github.com's (GitHubAPI),
	// an Enterprise Server's, or a test's.
	GitHubAPI  string
	SlackToken string
	SlackAPI   string // "" is Slack's own
}

// Task is one task as the server knows it: its id, the workspace it works in, the launch whose
// provenance its commits must carry, where its credential comes from, and who to tell what
// staging did.
type Task struct {
	ID        string
	Workspace *workspace.Workspace
	Launch    *runner.Launch
	// Autonomy is the task's current autonomy, where something holds it apart from the launch (a
	// Channel, after a replacement); nil reads the launch's.
	Policy func() task.Autonomy
	// Answered is who answered proceed to the question a row asked about this call, by the
	// decision the act names or by the call, taking that answer (Channel.answered): what an act
	// a row asks about is performed on. nil is a side that relayed no answers, where such an
	// act is refused.
	Answered func(task.Step, string) (string, bool)
	// Credential is the secret a push to one of the task's repositories is made with, or why
	// there is none. The runner decides; its own forge token is the usual answer.
	Credential func(repo workspace.Repo) (string, error)

	// created is the branches this task set out to create, by repo and ref: recorded when it
	// stages the push that makes one, so it holds though that push failed and the sweep landed
	// it. With the task's own commit at the forge (state), they are its own: to push
	// again, rewrite, and propose from. pushed is every branch it pushed, created or not: what
	// a proposal may be from.
	created, pushed map[string]bool
}

// Serve answers one task's requests, for as long as its process lives.
//
// Sequential by construction — one connection, one request at a time. A task that asked twice
// gets two pushes, which is what a task with more than one thing to push is.
func (s *Server) Serve(tk Task, conn net.Conn) {
	defer conn.Close()
	tk.created, tk.pushed = map[string]bool{}, map[string]bool{}
	br := bufio.NewReader(conn)
	for {
		raw, err := readFrame(br)
		if err != nil {
			return // the task process is gone, which is the ordinary end
		}
		rep := s.answer(tk, raw)
		body, err := codec.Marshal(rep, codec.Tony)
		if err != nil {
			return
		}
		if err := writeFrame(conn, body); err != nil {
			return
		}
	}
}

// answer dispatches the three ops. Each is PERFORMED here, by the side that holds the mirror and
// the credential: the task names a repository and a branch, and what comes back is what happened.
// The credential never leaves this process, so the task's shell — which shares the task's network
// namespace — has neither the secret nor, under a network that does not allow the forge, any route to the forge.
func (s *Server) answer(tk Task, raw []byte) Reply {
	var req Request
	if err := codec.Unmarshal(raw, &req, codec.Tony); err != nil {
		return Reply{Error: "undecodable request: " + err.Error()}
	}
	// A request's calls to the forge are made holding its mirror's lock: a forge that stops
	// answering would otherwise hold every task on that repo with it.
	ctx, cancel := context.WithTimeout(context.Background(), workspace.ForgeTimeout)
	defer cancel()
	switch req.Op {
	case "push":
		return s.push(ctx, tk, req)
	case OpCreatePR, OpUpdatePR:
		return s.pullRequest(ctx, tk, req)
	case "fetch":
		return s.refresh(ctx, tk, req)
	case OpState:
		return s.branchState(ctx, tk, req)
	case OpGitIssuePull:
		return s.gitIssuePull(ctx, tk, req)
	case OpGitIssuePush:
		return s.gitIssuePush(ctx, tk, req)
	case OpGitHubIssueCreate, OpGitHubIssueComment, OpGitHubIssueClose:
		return s.githubIssue(ctx, tk, req)
	case OpLinearIssueCreate, OpLinearIssueComment, OpLinearIssueClose:
		return s.linearIssue(ctx, tk, req)
	case OpSlackPost, OpSlackReply, OpSlackRead, OpSlackReact:
		return s.slack(ctx, tk, req)
	}
	return Reply{Error: fmt.Sprintf("unknown operation %q", req.Op)}
}

// refresh answers a `fetch`: the mirror up to upstream, then the checkout up to the mirror.
//
// The fetch is the one every launch does, bounded by the request's deadline. A remote that cannot be reached is an error the task
// reads as an answer, and the checkout is left as it was — `ensure` keeps what the mirror had,
// and nothing here moves a ref to a base nobody has.
func (s *Server) refresh(ctx context.Context, tk Task, req Request) Reply {
	t := tk.Workspace.Tree(req.Repo)
	if t == nil {
		return Reply{Error: fmt.Sprintf("no repository %q in this workspace", req.Repo)}
	}
	if why := s.refused(tk, req.Decision, task.Step{Name: task.StepGitFetch, Fields: map[string]string{"repo": t.Repo.Repo.Canonical(), "from": t.Repo.From}}); why != "" {
		return Reply{Refused: why}
	}
	mirror, err := s.Mirrors.Ensure(ctx, t.Repo.Remote)
	if err != nil {
		fmt.Fprintf(s.Log, "delivery: %s: fetch of %s: %v\n", tk.ID, t.Repo.Name(), err)
		return Reply{Error: err.Error()}
	}
	moved, err := t.Refresh(mirror)
	if err != nil {
		fmt.Fprintf(s.Log, "delivery: %s: refreshing %s from its mirror: %v\n", tk.ID, t.Repo.Name(), err)
		return Reply{Error: err.Error()}
	}
	if len(moved) > 0 {
		fmt.Fprintf(s.Log, "delivery: %s: fetched %s: %s\n", tk.ID, t.Repo.Name(), strings.Join(moved, ", "))
	}
	return Reply{OK: true, Updated: moved}
}

// push answers a `push`: decide it by the launch's autonomy, check what the task committed, stage
// it, and push it from the mirror. Or, with Delete, remove the branch.
//
// THE AUTONOMY IS READ HERE TOO, whatever the task decided. The task holds a model and the request
// is whatever it chose to send, so the side that holds the secret refuses what a `deny` row
// refuses, and nothing moves. An `ask` row's question was posed and answered in the task's
// process, through the runner's registry, and that answer is taken as it is.
//
// What is pushed is what the task COMMITTED, each commit carrying the provenance trailers, and
// nothing it left uncommitted — read from the checkout the task and this process share, refused
// with what is missing so the agent re-commits and stays the author of its SHAs.
//
// STAGING FIRST is what makes a failed push a retry rather than a loss: the work is in the mirror,
// which is this side's own disk, before anything is tried over a network. The push is then the
// staged ref to the target, which is the same act the sweep performs for anything that did not
// land (Reflect), so there is one way work leaves and the retry is it.
func (s *Server) push(ctx context.Context, tk Task, req Request) Reply {
	t := tk.Workspace.Tree(req.Repo)
	if t == nil {
		return Reply{Error: fmt.Sprintf("no repository %q in this workspace", req.Repo)}
	}
	ref, err := runner.BranchRef(req.Branch)
	if err != nil {
		return Reply{Refused: err.Error()}
	}
	tok, err := tk.Credential(t.Repo)
	if err != nil {
		return Reply{Error: err.Error()}
	}
	auth := workspace.TokenAuth(tok)(t.Repo.Remote)
	// The branch's state at the forge comes BEFORE the decision, because two of the step's
	// fields are facts only this side can supply: whether the branch is there, and whether its
	// tip is this task's. A row that keeps a task to branches it makes (`exists: "false"`) or
	// made (`own: "true"`) is decided on what the forge says now, never on what the task said,
	// and a delete is decided on the same facts as a write.
	st, err := s.state(ctx, tk, t, req.Branch, auth)
	if err != nil {
		return Reply{Error: err.Error()}
	}
	if why := s.refused(tk, req.Decision, task.Step{Name: task.StepGitPush, Fields: pushFields(t, ref, req.Delete, req.Force, st)}); why != "" {
		return Reply{Refused: why}
	}
	// A branch this run created is its own to rewrite; any other is rewritten only when the
	// push says so, which the autonomy saw. A branch not there yet is created, never forced:
	// there is nothing to rewrite, and a create is the same act whatever the push said.
	force := st.Created || (req.Force && st.Exists)
	key := t.Repo.Name() + " " + ref
	if req.Delete {
		// Nothing staged: a retirement removes something rather than sending something, so
		// there is no copy for the mirror to hold and nothing that could be lost by it failing.
		fmt.Fprintf(s.Log, "delivery: %s: deleting %s at %s\n", tk.ID, ref, t.Repo.Remote)
		if err := s.Mirrors.DeleteRef(ctx, t.Repo.Remote, ref, auth); err != nil {
			if why := workspace.Refusal(err); why != "" {
				return Reply{Refused: "the remote refused it: " + why}
			}
			return Reply{Error: err.Error()}
		}
		delete(tk.created, key)
		delete(tk.pushed, key)
		return Reply{OK: true, Out: "deleted " + t.Repo.Name() + " " + ref}
	}
	ok, refusal, head, err := t.AuthoredWork(tk.Launch)
	switch {
	case err != nil:
		return Reply{Error: err.Error()}
	case !ok:
		return Reply{OK: true, Out: "nothing to push: " + t.Repo.Name() + " has no commits beyond the base it " +
			"started from; commit what you want delivered, then push"}
	case refusal != "":
		return Reply{Refused: refusal}
	}
	d := tk.Workspace.Stage(ctx, tk.ID, t, ref, force, head) // what was checked
	if d.Err != nil {
		// Staging is the local hop, so this is the disk rather than the network — and nothing is
		// pushed, because the one copy of the work is a directory this runner is about to be
		// asked to remove.
		fmt.Fprintf(s.Log, "delivery: %s: staging before a push to %s: %v\n", tk.ID, ref, d.Err)
		return Reply{Error: d.Err.Error()}
	}
	staged := d.Staged[0]
	if !st.Exists {
		// Recorded now, not when it lands: if this push fails and the sweep lands it, the branch
		// is still the one this task made.
		tk.created[key] = true
	}
	fmt.Fprintf(s.Log, "delivery: %s: staged in the mirror at %s, pushing to %s\n", tk.ID, staged, ref)
	why, err := s.Mirrors.Deliver(ctx, t.Repo.Remote, staged, ref, force, auth)
	switch {
	case err != nil:
		// Not landed, and not a decision: the ref stays pending, and the sweep tries again. The
		// task hears that, rather than a success it would plan on.
		return Reply{Error: fmt.Sprintf("push to %s did not land (%v); the runner holds the work and retries", ref, err)}
	case why != "":
		return Reply{Refused: "the remote refused it: " + why}
	}
	tk.pushed[key] = true
	out := "pushed " + t.Repo.Name() + " -> " + ref
	// Said, because an agent that edited and did not commit may believe it delivered.
	if n := len(t.Changed()); n > 0 {
		out += fmt.Sprintf(" (%d uncommitted change(s) stayed in the workspace, not delivered)", n)
	}
	return Reply{OK: true, Out: out}
}

// The pull request ops: one for each tool, as the step each is.
const (
	OpCreatePR = "create-pr"
	OpUpdatePR = "update-pr"
)

// pullRequest answers a `create-pr` or an `update-pr`: a pull request from a branch the task has
// pushed into a base, opened or updated with this runner's token at GitHub, as the launch's
// autonomy decides it.
func (s *Server) pullRequest(ctx context.Context, tk Task, req Request) Reply {
	t := tk.Workspace.Tree(req.Repo)
	if t == nil {
		return Reply{Error: fmt.Sprintf("no repository %q in this workspace", req.Repo)}
	}
	head, err := runner.BranchRef(req.Branch)
	if err != nil {
		return Reply{Refused: err.Error()}
	}
	step := task.StepGitHubCreatePR
	if req.Op == OpUpdatePR {
		step = task.StepGitHubUpdatePR
	}
	// The base as a branch's name, as the tool spells it and a row's `base` is written.
	base := strings.TrimPrefix(strings.TrimSpace(req.Base), "refs/heads/")
	if strings.HasPrefix(base, "refs/") {
		return Reply{Refused: fmt.Sprintf("the base %q is not a branch", req.Base)}
	}
	tok, err := tk.Credential(t.Repo)
	if err != nil {
		return Reply{Error: err.Error()}
	}
	// Whether the head is this task's is the runner's word, read before the decision as a
	// push's facts are.
	st, err := s.state(ctx, tk, t, req.Branch, workspace.TokenAuth(tok)(t.Repo.Remote))
	if err != nil {
		return Reply{Error: err.Error()}
	}
	fields := map[string]string{"repo": t.Repo.Repo.Canonical(), "head": strings.TrimPrefix(head, "refs/heads/"),
		"own": strconv.FormatBool(st.Own)}
	if base != "" {
		fields["base"] = base // an update that names none keeps the base
	}
	if why := s.refused(tk, req.Decision, task.Step{Name: step, Fields: fields}); why != "" {
		return Reply{Refused: why}
	}
	if _, has := s.Ceilings["github"]; !has {
		return Reply{Refused: "this runner performs nothing at GitHub for its tasks: it has no `github` system"}
	}
	owner, repo, err := RepoPath(t.Repo.Remote)
	if err != nil {
		return Reply{Refused: err.Error()}
	}
	api := s.Services.GitHubAPI
	if !st.Exists {
		return Reply{Refused: fmt.Sprintf("%s isn't at the forge: push it first, then open a pull request from it", req.Branch)}
	}
	if !tk.pushed[t.Repo.Name()+" "+head] && !st.Created {
		return Reply{Refused: fmt.Sprintf("a proposal is from a branch this run pushed, and %q of %s isn't one",
			strings.TrimPrefix(head, "refs/heads/"), t.Repo.Repo.Repo)}
	}
	spec := GitHubPR{API: api, Owner: owner, Repo: repo, Head: req.Branch, Base: base, Title: req.Title, Body: req.Body}
	hc := s.HTTP
	if hc == nil {
		hc = http.DefaultClient
	}
	var out string
	if req.Op == OpUpdatePR {
		out, err = UpdateGitHubPR(ctx, hc, tok, spec)
	} else {
		out, err = OpenGitHubPR(ctx, hc, tok, spec)
	}
	if err != nil {
		return Reply{Error: err.Error()}
	}
	fmt.Fprintf(s.Log, "delivery: %s: %s\n", tk.ID, out)
	return Reply{OK: true, Out: out}
}

// refused is why the task's environment, its autonomy or the runner's ceiling refuses a step, or ""
// where none does: a step outside the environment, saying what it gives (on the fields the runner
// filled, `exists` and `own` among them, as the task's own check saw them), the `deny` row that
// matches it, by name, or an `ask` row whose question this runner saw no answer to. An `allow` needs nobody. An `ask` is decided
// in the task's process, which holds the model and says what it likes, so the answer is taken
// from what the runner itself relayed to that process (Task.Answered), once per act, and never
// from the process's word. The ceiling is read after the autonomy, and is the operator's: a task's
// autonomy cannot open what it closes.
func (s *Server) refused(tk Task, decision string, st task.Step) string {
	if why, outside := tk.environment().Refuses(st.Name, st.Fields); outside {
		return "refused: outside the environment: " + why
	}
	if p, ok := tk.policy(); ok {
		p = p.OrDefault()
		row, i := p.Decide(st)
		switch row.Then.Mode {
		case task.ThenDeny:
			return fmt.Sprintf("refused by the autonomy's row %s: %s", p.NameOf(i), st)
		case task.ThenAsk:
			by := ""
			answered := false
			if tk.Answered != nil {
				by, answered = tk.Answered(st, decision)
			}
			if !answered {
				return fmt.Sprintf("refused: the autonomy's row %s asks about %s, and this runner relayed no answer to "+
					"the question (%s)", p.NameOf(i), st, cmp.Or(decision, "the act names no decision"))
			}
			fmt.Fprintf(s.Log, "delivery: %s: %s on the answer of %s (%s)\n", tk.ID, st, by, cmp.Or(decision, "by the call"))
		}
	}
	system, _, _ := systems.Split(st.Name)
	if why, beyond := systems.Beyond(s.Ceilings, st.Name, st.Fields); beyond {
		// The launch's environment lies within the ceiling, and this step within the
		// environment: a check that missed, said loudly.
		fmt.Fprintf(s.Log, "delivery: %s: BUG: %s within the environment is beyond the ceiling: %s\n", tk.ID, st, why)
		return fmt.Sprintf("refused: beyond the runner's ceiling for %s: %s", system, why)
	}
	return ""
}

// environment is the task's environment: its launch's, and none where nothing is known of it, as
// a server built bare in a test, so that nothing is within it.
func (tk Task) environment() systems.Environment {
	if tk.Launch == nil {
		return systems.Environment{}
	}
	return tk.Launch.Environment
}

// autonomy is the task's autonomy as it stands: what holds it apart from the launch, else the
// launch's; false where nothing is known of it, as a server built bare in a test.
func (tk Task) policy() (task.Autonomy, bool) {
	switch {
	case tk.Policy != nil:
		return tk.Policy(), true
	case tk.Launch != nil:
		return tk.Launch.Autonomy, true
	}
	return nil, false
}

// state is what the runner knows of a branch of a task's repo: whether it is at the forge, asked
// of the forge now rather than read from a mirror that may be behind it, and whether this task
// created it. That takes the act and the commit both: the task staged the push that makes it,
// and what is at the forge is the task's own commit (Tree.Made), however it got there — its push,
// or the sweep's. The commit alone isn't enough: a branch the task never made can come to hold
// its commit (main, fast-forwarded to a proposal of the task's), and that isn't the task's to
// rewrite.
func (s *Server) state(ctx context.Context, tk Task, t *workspace.Tree, branch string, auth transport.AuthMethod) (runner.BranchState, error) {
	ref := "refs/heads/" + strings.TrimPrefix(strings.TrimSpace(branch), "refs/heads/")
	tip, exists, err := s.Mirrors.RemoteRef(ctx, t.Repo.Remote, ref, auth)
	if err != nil {
		return runner.BranchState{}, fmt.Errorf("asking %s whether %s is there: %w", t.Repo.Remote, ref, err)
	}
	// Own is read off the forge's tip and the checkout, not off anything this channel
	// remembers, so it holds across a resume: the task that made the branch is the task whose
	// trailers its tip carries.
	own := exists && t.Made(tip, tk.Launch)
	created := tk.created[t.Repo.Name()+" "+ref] && (!exists || own)
	return runner.BranchState{Exists: exists, Own: own, Created: created}, nil
}

// pushFields is a git.push step as the autonomy and the ceiling see it: what the task asked (the
// branch, delete, force), and what the runner knows of the branch (exists, own). The last two
// are filled here and nowhere else, so a row naming them is decided on the forge's word.
func pushFields(t *workspace.Tree, ref string, del, force bool, st runner.BranchState) map[string]string {
	return map[string]string{
		"repo": t.Repo.Repo.Canonical(), "from": t.Repo.From, "ref": ref,
		"delete": strconv.FormatBool(del), "force": strconv.FormatBool(force),
		"exists": strconv.FormatBool(st.Exists), "own": strconv.FormatBool(st.Own),
	}
}

// OpState asks what the runner knows of a branch: whether it is at the forge, and whether its
// tip is this task's. The task's push and pull-request tools ask before their own decision, so
// a row naming `exists` or `own` is decided in the task's process on the same facts the runner
// decides it on at the act. It is answered for a task whose environment gives `git.push` at all,
// and says nothing a fetch would not.
const OpState = "state"

// branchState answers a `state`.
func (s *Server) branchState(ctx context.Context, tk Task, req Request) Reply {
	t := tk.Workspace.Tree(req.Repo)
	if t == nil {
		return Reply{Error: fmt.Sprintf("no repository %q in this workspace", req.Repo)}
	}
	if !tk.environment().Gives(task.StepGitPush) {
		return Reply{Refused: "refused: the environment gives no git.push, so there is no push to decide"}
	}
	tok, err := tk.Credential(t.Repo)
	if err != nil {
		return Reply{Error: err.Error()}
	}
	st, err := s.state(ctx, tk, t, req.Branch, workspace.TokenAuth(tok)(t.Repo.Remote))
	if err != nil {
		return Reply{Error: err.Error()}
	}
	return Reply{OK: true, Exists: st.Exists, Own: st.Own}
}
