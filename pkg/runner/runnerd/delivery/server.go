package delivery

import (
	"bufio"
	"cmp"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"

	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/transport"

	"github.com/signadot/verse/pkg/codec"
	"github.com/signadot/verse/pkg/runner"
	"github.com/signadot/verse/pkg/runner/runnerd/workspace"
	"github.com/signadot/verse/pkg/systems"
	"github.com/signadot/verse/pkg/task"
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
	// table is the ops this server answers; nil is the channel's own (ops), and only a test
	// gives another.
	table map[string]op
}

// Services is what a runner holds for the systems beyond its git token: a Linear credential (the
// Authorization header, verbatim: a personal key bare, an OAuth token as `Bearer …`), a Slack
// bot token (`xoxb-…`), and where the APIs are when something other than the real ones answers
// (a test).
type Services struct {
	LinearAuth Credential
	LinearAPI  string // "" is Linear's own
	// GitHubAPI is the API of the forge the runner's repos are on: github.com's (GitHubAPI),
	// an Enterprise Server's, or a test's.
	GitHubAPI  string
	SlackToken Credential
	SlackAPI   string // "" is Slack's own
	// Verse carries a task's request to the verse the runner serves, a verb of the verse system
	// (verse.go); nil for a runner with no verse to reach, or no token of its own to reach it with.
	Verse Verse
}

// A Credential is what the runner acts with at a service, READ WHEN IT IS ASKED FOR: one the
// runner keeps in its secret store is the value there at that act, so a secret that is rotated
// is the next act's with no restart. nil is no credential. Its error names the secret and never
// any of a value, and is the task's to read.
type Credential func() (string, error)

// Holds is a credential that is one value for as long as the runner runs, as one read from its
// environment when it started is. An empty value is no credential.
func Holds(value string) Credential {
	if value == "" {
		return nil
	}
	return func() (string, error) { return value, nil }
}

// read is the credential's value now, and empty with no error where there is none.
func (c Credential) read() (string, error) {
	if c == nil {
		return "", nil
	}
	return c()
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

// apart is the task as an op performed apart is given it: without its checkout and without the
// branches it made and pushed, which the ops in turn write. So what an op apart may not read it
// cannot: the rule is the value it is handed, not a thing it remembers.
func (tk Task) apart() Task {
	tk.Workspace, tk.created, tk.pushed = nil, nil, nil
	return tk
}

// Serve answers one task's requests, for as long as its process lives.
//
// IN TURN, one request at a time and in the order the task asked, as one connection read in a
// loop once made them: a task that asked twice gets two pushes, and the second finds what the
// first left. An op the table performs apart (op.apart) is the exception, and neither waits
// for those in turn nor holds them. Each answer names its request (Reply.ID), since an answer
// may come ahead of one asked for earlier: one apart's ahead of one in turn's, and of two in
// turn the later's ahead of the earlier's, a turn passing once its request is performed and
// before its answer is sent. What is in order is the performing.
//
// Serve returns once the task's end is gone and what it had begun is done. A request in turn
// that was begun is finished, whatever became of the process that asked: a push half-performed
// is not an answer (Channel.Settle). One still waiting its turn is given up, and so is one
// apart: nobody is there to hear of it, and the runner does not act for a task that is gone.
func (s *Server) Serve(tk Task, conn net.Conn) {
	defer conn.Close()
	tk.created, tk.pushed = map[string]bool{}, map[string]bool{}
	gone, leave := context.WithCancel(context.Background())
	var (
		writing  sync.Mutex // one frame onto the connection at a time
		inflight sync.WaitGroup
		turn     = make(chan struct{}) // closed once the last request in turn is done
		slots    = make(chan struct{}, maxInFlight)
	)
	close(turn)
	defer inflight.Wait()
	defer leave()
	// send writes one answer. room, where it is given, gives back the request's place among
	// those the runner holds (slots), and is called with the frame about to be written: A
	// PLACE IS FREE BEFORE ITS ANSWER CAN BE HEARD, so a task that asks again on hearing an
	// answer finds room. It is called holding the write, so one request at most has given its
	// place back and not yet written its answer.
	send := func(rep Reply, room func()) {
		body, err := codec.Marshal(rep, codec.Tony)
		if err != nil {
			// No answer can be written, so the channel is ended rather than left with a
			// request nobody will hear of: the task reads that its end is gone.
			fmt.Fprintf(s.Log, "delivery: %s: an answer does not encode, and the channel is closed: %v\n", tk.ID, err)
			conn.Close()
			return
		}
		// AN ANSWER THE CHANNEL CANNOT CARRY is said to be one, in a frame it can: what was
		// done is done, and the task reads that its answer did not fit, and why. Not OK, though
		// the act may have been: a tool that reads Out on OK would read an empty answer as an
		// empty verse, and say there is nothing where there is too much.
		if len(body) > maxFrame {
			fmt.Fprintf(s.Log, "delivery: %s: the answer to it is %d bytes, over the channel's %d\n", tk.ID, len(body), maxFrame)
			body, err = codec.Marshal(Reply{ID: rep.ID, Error: "the answer is " + frameTooLarge{len(body)}.Error() +
				"; ask for less of it"}, codec.Tony)
			if err != nil {
				conn.Close()
				return
			}
		}
		writing.Lock()
		defer writing.Unlock()
		if room != nil {
			room()
		}
		_ = writeFrame(conn, body) // a task that is gone is found so at the next read
	}
	br := bufio.NewReader(conn)
	for {
		raw, err := readFrame(br)
		var big frameTooLarge
		switch {
		case errors.As(err, &big):
			send(Reply{Error: "the request is " + big.Error()}, nil) // dropped, and the channel goes on
			continue
		case err != nil:
			return // the task process is gone, which is the ordinary end
		}
		req, o, rep := s.read(raw)
		if o == nil {
			send(rep, nil)
			continue
		}
		// No more than maxInFlight at once, in turn or apart. One past that is refused rather
		// than waited on: the loop goes on reading, so it sees the task's end go, and a request
		// behind the ones in turn is not begun for a task that is gone.
		select {
		case slots <- struct{}{}:
		default:
			send(Reply{ID: req.ID, Error: fmt.Sprintf("the runner holds %d of this task's requests, read and not yet "+
				"answered, and takes no more until one is: nothing was done, ask again then", maxInFlight)}, nil)
			continue
		}
		inflight.Add(1)
		// The place is given back once: as its answer is written, or, where none is, when the
		// request is done with.
		room := sync.OnceFunc(func() { <-slots })
		if o.apart {
			go func() {
				defer inflight.Done()
				defer room()
				send(s.run(gone, tk.apart(), *o, req), room)
			}()
			continue
		}
		// A request with none ahead of it is begun as it is read, and so is finished. One that
		// waits is begun when its turn comes, unless the task's end went first.
		waits := true
		select {
		case <-turn:
			waits = false
		default:
		}
		ahead, done := turn, make(chan struct{})
		turn = done
		go func() {
			defer inflight.Done()
			defer room()
			// THE TURN PASSES BEFORE THE ANSWER IS SENT. A task that hears an answer and asks
			// again then finds nothing ahead of what it asks, so that request is begun as it
			// is read, and is finished though the task's end goes the moment after.
			rep, begun := func() (Reply, bool) {
				defer close(done)
				<-ahead
				if waits && gone.Err() != nil {
					fmt.Fprintf(s.Log, "delivery: %s: %s was not begun: the task's end went while it waited its turn\n", tk.ID, req.Op)
					return Reply{}, false
				}
				return s.run(context.Background(), tk, *o, req), true
			}()
			if begun {
				send(rep, room)
			}
		}()
	}
}

// maxInFlight is how many of one task's requests the runner holds at once, read and not yet
// answered. One more may be writing its answer, its place already given back (Serve's send).
const maxInFlight = 32

// read is a frame as the request it carries and the op that answers it, or, with no op, the
// reply that says why there is none. A frame that does not decode is answered under the id it
// gave, where that much of it read. A key the request does not have is one that does not
// decode: what an op is asked with is under `fields`, and a name beside it would be dropped
// where a name within it is refused.
func (s *Server) read(raw []byte) (Request, *op, Reply) {
	var req Request
	if err := codec.UnmarshalStrict(raw, &req, codec.Tony); err != nil {
		// The id is read for itself, whatever else the frame holds, so the one who asked hears
		// why rather than waits on an answer that names nobody.
		var named struct {
			ID int `tony:"field=id,omitzero"`
		}
		_ = codec.Unmarshal(raw, &named, codec.Tony)
		return req, nil, Reply{ID: named.ID, Error: "undecodable request, and nothing was done: " + err.Error()}
	}
	table := s.table
	if table == nil {
		table = ops
	}
	o, ok := table[req.Op]
	if !ok {
		return req, nil, Reply{ID: req.ID, Error: fmt.Sprintf("unknown operation %q", req.Op)}
	}
	return req, &o, Reply{}
}

// run performs a request with its op, within the op's deadline, and names the request in the
// answer. Each is PERFORMED here, by the side that holds the mirror and the credential: the task
// names a repository and a branch, and what comes back is what happened. The credential never
// leaves this process, so the task's shell — which shares the task's network namespace — has
// neither the secret nor, under a network that does not allow the forge, any route to the forge.
//
// A request's calls to the forge are made holding its mirror's lock: a forge that stops
// answering would otherwise hold every task on that repo with it.
func (s *Server) run(ctx context.Context, tk Task, o op, req Request) Reply {
	ctx, cancel := context.WithTimeout(ctx, o.deadline)
	defer cancel()
	rep := s.perform(ctx, tk, o, req)
	rep.ID = req.ID
	return rep
}

// refresh answers a `fetch`: the mirror up to upstream, then the checkout up to the mirror.
//
// The fetch is the one every launch does, bounded by the request's deadline. A remote that cannot be reached is an error the task
// reads as an answer, and the checkout is left as it was — `ensure` keeps what the mirror had,
// and nothing here moves a ref to a base nobody has.
func (s *Server) refresh(ctx context.Context, tk Task, req Request) (asked, Reply) {
	t := tk.Workspace.Tree(req.Field("repo"))
	if t == nil {
		return before(Reply{Error: fmt.Sprintf("no repository %q in this workspace", req.Field("repo"))})
	}
	return asking(task.NewStep(task.StepGitFetch, map[string]string{"repo": t.Repo.Repo.Canonical(), "from": t.Repo.From}), func() Reply {
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
	})
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
func (s *Server) push(ctx context.Context, tk Task, req Request) (asked, Reply) {
	t := tk.Workspace.Tree(req.Field("repo"))
	if t == nil {
		return before(Reply{Error: fmt.Sprintf("no repository %q in this workspace", req.Field("repo"))})
	}
	ref, err := runner.BranchRef(req.Field("branch"))
	if err != nil {
		return before(Reply{Refused: err.Error()})
	}
	tok, err := tk.Credential(t.Repo)
	if err != nil {
		return before(Reply{Error: err.Error()})
	}
	auth := workspace.TokenAuth(tok)(t.Repo.Remote)
	// The branch's state at the forge comes BEFORE the decision, because two of the step's
	// fields are facts only this side can supply: whether the branch is there, and whether its
	// tip is this task's. A row that keeps a task to branches it makes (`exists: false`) or
	// made (`own: true`) is decided on what the forge says now, never on what the task said,
	// and a delete is decided on the same facts as a write.
	st, tip, err := s.state(ctx, tk, t, req.Field("branch"), auth)
	if err != nil {
		return before(Reply{Error: err.Error()})
	}
	return asking(task.NewStep(task.StepGitPush, pushFields(t, ref, req.Flag("delete"), req.Flag("force"), st)), func() Reply {
		// A branch this run created is its own to rewrite; any other is rewritten only when the
		// push says so, which the autonomy saw. A branch not there yet is created, never forced:
		// there is nothing to rewrite, and a create is the same act whatever the push said.
		force := st.Created || (req.Flag("force") && st.Exists)
		key := t.Repo.Name() + " " + ref
		if req.Flag("delete") {
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
		// NOTHING OF ITS OWN IS NOT NOTHING TO PUSH. A task that moved its branch onto commits already
		// upstream (a fast-forward onto another branch's work, a rebase that was a no-op) wrote
		// nothing for the trailers to vouch for, but the target is still to be moved there, as git
		// moves it. Only a target already at the head is up to date, and a task still at its base has
		// delivered nothing at all.
		switch {
		case err != nil:
			return Reply{Error: err.Error()}
		case !ok && head.String() == t.Head:
			return Reply{OK: true, Out: "nothing to push: " + t.Repo.Name() + " has no commits beyond the base it " +
				"started from; commit what you want delivered, then push"}
		case !ok && st.Exists && head == tip:
			// Up to date is a push that succeeded: the branch is where this run would put it, so a
			// pull request from it is this run's to open.
			tk.pushed[key] = true
			return Reply{OK: true, Out: "nothing to push: " + t.Repo.Name() + " " + ref + " is already at " +
				workspace.ShortSha(head.String())}
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
	})
}

// The pull request ops: one for each tool, as the step each is.
const (
	OpCreatePR = "create-pr"
	OpUpdatePR = "update-pr"
)

// pullRequest answers a `create-pr` or an `update-pr`: a pull request from a branch the task has
// pushed into a base, opened or updated with this runner's token at GitHub, as the launch's
// autonomy decides it.
func (s *Server) pullRequest(ctx context.Context, tk Task, req Request) (asked, Reply) {
	t := tk.Workspace.Tree(req.Field("repo"))
	if t == nil {
		return before(Reply{Error: fmt.Sprintf("no repository %q in this workspace", req.Field("repo"))})
	}
	head, err := runner.BranchRef(req.Field("branch"))
	if err != nil {
		return before(Reply{Refused: err.Error()})
	}
	step := task.StepGitHubCreatePR
	if req.Op == OpUpdatePR {
		step = task.StepGitHubUpdatePR
	}
	// The base as a branch's name, as the tool spells it and a row's `base` is written.
	base := strings.TrimPrefix(strings.TrimSpace(req.Field("base")), "refs/heads/")
	if strings.HasPrefix(base, "refs/") {
		return before(Reply{Refused: fmt.Sprintf("the base %q is not a branch", req.Field("base"))})
	}
	tok, err := tk.Credential(t.Repo)
	if err != nil {
		return before(Reply{Error: err.Error()})
	}
	// Whether the head is this task's is the runner's word, read before the decision as a
	// push's facts are.
	st, _, err := s.state(ctx, tk, t, req.Field("branch"), workspace.TokenAuth(tok)(t.Repo.Remote))
	if err != nil {
		return before(Reply{Error: err.Error()})
	}
	fields := map[string]string{"repo": t.Repo.Repo.Canonical(), "head": strings.TrimPrefix(head, "refs/heads/"),
		"own": strconv.FormatBool(st.Own)}
	if base != "" {
		fields["base"] = base // an update that names none keeps the base
	}
	if req.Op != OpUpdatePR {
		fields["draft"] = strconv.FormatBool(req.Flag("draft"))
	}
	return asking(task.NewStep(step, fields), func() Reply {
		if _, has := s.Ceilings["github"]; !has {
			return Reply{Refused: "this runner performs nothing at GitHub for its tasks: it has no `github` system"}
		}
		owner, repo, err := RepoPath(t.Repo.Remote)
		if err != nil {
			return Reply{Refused: err.Error()}
		}
		api := s.Services.GitHubAPI
		if !st.Exists {
			return Reply{Refused: fmt.Sprintf("%s isn't at the forge: push it first, then open a pull request from it", req.Field("branch"))}
		}
		if !tk.pushed[t.Repo.Name()+" "+head] && !st.Created {
			return Reply{Refused: fmt.Sprintf("a proposal is from a branch this run pushed, and %q of %s isn't one",
				strings.TrimPrefix(head, "refs/heads/"), t.Repo.Repo.Repo)}
		}
		spec := GitHubPR{API: api, Owner: owner, Repo: repo, Head: req.Field("branch"), Base: base, Title: req.Field("title"), Body: req.Body, Draft: req.Flag("draft")}
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
	})
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
// rewrite. The tip is the branch's commit at the forge, zero where it is not there.
func (s *Server) state(ctx context.Context, tk Task, t *workspace.Tree, branch string, auth transport.AuthMethod) (runner.BranchState, plumbing.Hash, error) {
	ref := "refs/heads/" + strings.TrimPrefix(strings.TrimSpace(branch), "refs/heads/")
	tip, exists, err := s.Mirrors.RemoteRef(ctx, t.Repo.Remote, ref, auth)
	if err != nil {
		return runner.BranchState{}, tip, fmt.Errorf("asking %s whether %s is there: %w", t.Repo.Remote, ref, err)
	}
	// Own is read off the forge's tip and the checkout, not off anything this channel
	// remembers, so it holds across a resume: the task that made the branch is the task whose
	// trailers its tip carries.
	own := exists && t.Made(tip, tk.Launch)
	created := tk.created[t.Repo.Name()+" "+ref] && (!exists || own)
	return runner.BranchState{Exists: exists, Own: own, Created: created}, tip, nil
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
func (s *Server) branchState(ctx context.Context, tk Task, req Request) (asked, Reply) {
	t := tk.Workspace.Tree(req.Field("repo"))
	if t == nil {
		return before(Reply{Error: fmt.Sprintf("no repository %q in this workspace", req.Field("repo"))})
	}
	if !tk.environment().Gives(task.StepGitPush) {
		return before(Reply{Refused: "refused: the environment gives no git.push, so there is no push to decide"})
	}
	tok, err := tk.Credential(t.Repo)
	if err != nil {
		return before(Reply{Error: err.Error()})
	}
	st, _, err := s.state(ctx, tk, t, req.Field("branch"), workspace.TokenAuth(tok)(t.Repo.Remote))
	if err != nil {
		return before(Reply{Error: err.Error()})
	}
	return before(Reply{OK: true, Exists: st.Exists, Own: st.Own})
}
