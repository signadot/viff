package delivery

import (
	"context"
	"fmt"
	"io"
	"net"
	"os"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/signadot/verse/pkg/runner"
	"github.com/signadot/verse/pkg/runner/runnerd/workspace"
	"github.com/signadot/verse/pkg/systems"
	"github.com/signadot/verse/pkg/task"
)

// Channel is the runner's side of one task's delivery: the connected pair whose far end the
// task process inherits, the server that answers on the near end, and what delivery did while
// the task ran. The runner opens one per task that names repos, hands Child to the process, runs
// Serve beside it, and calls Settle once the process is gone.
type Channel struct {
	id   string
	ws   *workspace.Workspace
	l    *runner.Launch
	srv  *Server
	cred func(workspace.Repo) (string, error)
	log  io.Writer

	near net.Conn
	far  *os.File
	// network is the task's network as the sandbox built it (SetNetwork), set before Serve.
	network Network

	// autonomy is the task's CURRENT autonomy: the launch's until a replacement is set (SetPolicy),
	// and what the server decides each act by and settle reads. The launch's own copy (l) never
	// moves, since it is what a replacement is held to.
	autonomy atomic.Pointer[task.Autonomy]

	// answers are the gate answers the runner relayed to this task (Answer), each about one
	// call of one system, and each taken by the one act it is for (answered). An act a row
	// asks about is performed on one of these and on nothing else: the task's process holds
	// the model and says what it likes, and the runner is the side that saw the answer given.
	answersMu sync.Mutex
	answers   []answer
	// relaying counts the answers on their way to the task's process (Relaying), and landed
	// is closed when the last of them has been taken or refused. The process acts the moment
	// it hears an answer, which is before this side hears that it was taken, so an act that
	// arrives meanwhile is judged once it has (answered) rather than refused for the gap.
	relaying int
	landed   chan struct{}

	// serving is set once Serve has begun, and served closed once it has returned: what Settle
	// waits for, so no request is still being performed when the trees are judged.
	serving atomic.Bool
	served  chan struct{}
}

// answer is one gate answer the runner relayed: the decision it answered, by id, the call the
// question was about where the runner could read it (the task's pending gate), the choice, and
// who gave it.
type answer struct {
	decision string
	step     task.Step
	choice   string
	by       string
}

// Answer records a gate answer the runner relayed to the task's process, for the act it is
// about to perform on it. The decision's id is always known, since it is what the answer names;
// the call is kept where the runner read it, and an act is matched by the id first.
func (c *Channel) Answer(decision string, step task.Step, choice, by string) {
	c.answersMu.Lock()
	defer c.answersMu.Unlock()
	c.answers = append(c.answers, answer{decision: decision, step: step, choice: choice, by: by})
}

// Relaying marks an answer on its way to the task's process, and returns what the relay calls
// once the process has taken it (and Answer kept it) or refused it. Until then an act is not
// judged (answered): the process acts as soon as its own side hears the answer, and the act can
// reach this side ahead of the word that the answer was taken. Keeping the answer before relaying
// it would open the opposite gap, an act performed on an answer the process then refused.
func (c *Channel) Relaying() (landed func()) {
	c.answersMu.Lock()
	defer c.answersMu.Unlock()
	if c.relaying == 0 {
		c.landed = make(chan struct{})
	}
	c.relaying++
	var once sync.Once
	return func() {
		once.Do(func() {
			c.answersMu.Lock()
			defer c.answersMu.Unlock()
			c.relaying--
			if c.relaying == 0 {
				close(c.landed)
			}
		})
	}
}

// Answers is how many relayed answers no act has taken yet.
func (c *Channel) Answers() int {
	c.answersMu.Lock()
	defer c.answersMu.Unlock()
	return len(c.answers)
}

// answered is who answered proceed to the question this act was decided by, and takes that
// answer, so one answer performs one act and a second act on the same call is asked about
// again. The act names its decision (Request.Decision) and that is matched first; an act that
// names none is matched to an answer about the same call, by step and fields. False where the
// runner relayed no such answer, whatever the task's process says.
func (c *Channel) answered(st task.Step, decision string) (string, bool) {
	c.answersMu.Lock()
	defer c.answersMu.Unlock()
	for c.relaying > 0 {
		// An answer is in flight: judged once it has landed, whichever way.
		landed := c.landed
		c.answersMu.Unlock()
		<-landed
		c.answersMu.Lock()
	}
	take := func(i int) (string, bool) {
		by := c.answers[i].by
		c.answers = append(c.answers[:i], c.answers[i+1:]...)
		return by, true
	}
	if decision != "" {
		for i, a := range c.answers {
			if a.decision == decision && a.choice == task.Proceed {
				return take(i)
			}
		}
		return "", false
	}
	for i, a := range c.answers {
		if a.step.Same(st) && a.choice == task.Proceed {
			return take(i)
		}
	}
	return "", false
}

// SetAutonomy is the task's autonomy from the next act on, as PUT /tasks/{id}/autonomy replaced it.
// Without it the runner would keep deciding by the launch's rows: a deny a replacement lifted
// would still refuse here, and a deny it added would hold only in the task's own process.
func (c *Channel) SetAutonomy(p task.Autonomy) { c.autonomy.Store(&p) }

// Autonomy is the task's current autonomy.
func (c *Channel) Autonomy() task.Autonomy {
	if p := c.autonomy.Load(); p != nil {
		return *p
	}
	return nil
}

// Open makes the channel for one task: a CONNECTED pair rather than a path, because nothing but
// that process should be able to ask — a path inside the sandbox is one the agent's shell could
// write to, and a task that can call for a credential itself is a task that delivers without
// passing its gate.
//
// credential is what a push to a repository is made with — the runner's forge token, or why
// there is none. services is what the runner holds for the systems beyond it (Services), and
// ceilings the operator's bounds on each system (Server.Ceilings).
func Open(id string, ws *workspace.Workspace, mirrors *workspace.Mirrors,
	credential func(workspace.Repo) (string, error), services Services, ceilings map[string]systems.Ceiling,
	l *runner.Launch, log io.Writer) (*Channel, error) {
	near, far, err := SocketPair()
	if err != nil {
		return nil, err
	}
	c := &Channel{
		id: id, ws: ws, l: l, srv: &Server{Mirrors: mirrors, Log: log, Services: services, Ceilings: ceilings}, cred: credential, log: log,
		near: near, far: far, served: make(chan struct{}),
	}
	if l != nil {
		c.SetAutonomy(l.Autonomy)
	}
	return c, nil
}

// Child is the end the task's process inherits, as an extra descriptor.
func (c *Channel) Child() *os.File { return c.far }

// SetNetwork is the task's network as the sandbox built it, which a command run for the task
// apart from its process is held to (Task.Network). Set it before Serve, once the egress
// socket is there.
func (c *Channel) SetNetwork(n Network) { c.network = n }

// Serve answers the task's requests for as long as its process lives.
func (c *Channel) Serve() {
	c.serving.Store(true)
	defer close(c.served)
	var root string
	if c.ws != nil {
		root = c.ws.Root
	}
	c.srv.Serve(Task{ID: c.id, Workspace: c.ws, Root: root, Network: c.network, Launch: c.l, Policy: c.Autonomy,
		Answered: c.answered, Credential: c.cred}, c.near)
}

// Close shuts the near end. The far end is the handler's to close once the process has it.
func (c *Channel) Close() {
	if c.near != nil {
		_ = c.near.Close()
	}
}

// Settle is what happens to a task's work once its process is gone: a request still being
// performed finishes, whatever did not leave is salvaged, and the workspace is removed unless it
// is the only copy, or keep says otherwise. It returns whether the workspace is still there.
//
// Delivery is not something this function does. It is the task's own action, decided by its
// autonomy and recorded on its events (a git.push step) — so what happens here is only the
// decision about each tree, from what delivery staged out of it. A tree whose head an approved
// push staged has left; every other tree with work in it has not, whatever happened to its
// siblings.
func (c *Channel) Settle(ctx context.Context, l *runner.Launch, keep bool) bool {
	c.Close()
	// A push the process asked for before it died is finished first — staged, and delivered or
	// not — because what Salvage decides about a tree is read off what delivery did with it,
	// and a push half-performed is not an answer. Closing the near end is what ends Serve, so
	// the wait is bounded by the one request in turn that was begun: those behind it are not
	// begun once the end is closed, and one performed apart is given up (Server.Serve).
	if c.serving.Load() {
		<-c.served
	}
	// Work that did not leave is SALVAGED into the mirror rather than left in a directory
	// somebody has to know to look in — under `held`, because it was never authorized to go
	// anywhere and a retry must not push it (workspace/stage.go).
	//
	// This covers four endings: a gate that refused the push, a task that failed before
	// reaching its delivery step, a process that died, and a task that delivered one repository
	// and left work in another. In each the runner has work and nobody said it could leave.
	// Whether what WAS staged landed is the remote's to say — the pending ref settles itself on
	// the sweep. Judged by the environment, which is the launch's for the whole run.
	d := c.ws.Salvage(ctx, l)
	for _, ref := range d.Staged {
		fmt.Fprintf(c.log, "delivery: %s: the work did not leave; it is held in this "+
			"runner's mirror at %s\n", c.id, ref)
	}
	if d.Err != nil {
		fmt.Fprintf(c.log, "delivery: %s: salvage: %v\n", c.id, d.Err)
	}
	// A workspace is removed only when nothing is LOOSE in it. Staging is what makes the
	// ordinary answer "remove": the work is in the mirror before any push is attempted, so a
	// failed delivery is not a reason to keep a checkout — only a failure to stage is, and
	// that one is the disk rather than the network.
	switch {
	case keep:
		fmt.Fprintf(c.log, "delivery: %s: workspace kept at %s\n", c.id, c.ws.Root)
		return true
	case len(d.Loose) > 0:
		fmt.Fprintf(c.log, "delivery: %s: KEEPING %s — %s could not be staged, so this is the "+
			"only copy of the work\n", c.id, c.ws.Root, strings.Join(d.Loose, ", "))
		return true
	}
	if err := c.ws.Remove(ctx); err != nil {
		fmt.Fprintf(c.log, "delivery: %s: remove workspace: %v\n", c.id, err)
		return true
	}
	return false
}
