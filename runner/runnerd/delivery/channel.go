package delivery

import (
	"context"
	"fmt"
	"io"
	"net"
	"os"
	"strings"
	"sync/atomic"

	"github.com/signadot/verse/runner/runnerd/workspace"
	"github.com/signadot/verse/task"
)

// Channel is the runner's side of one task's delivery, for any runner that prepares a task's
// process: the connected pair whose far end the process inherits, the server that answers on
// the near end, and what delivery did while the task ran. A runner's Preparer opens one per
// task, hands Child to the process, runs Serve beside it, and calls Settle once the process is
// gone.
type Channel struct {
	id   string
	ws   *workspace.Workspace
	l    task.Launch
	srv  *Server
	cred func(workspace.Repo) (string, error)
	log  io.Writer

	near net.Conn
	far  *os.File

	// serving is set once Serve has begun, and served closed once it has returned: what Settle
	// waits for, so no request is still being performed when the trees are judged.
	serving atomic.Bool
	served  chan struct{}
}

// Open makes the channel for one task: a CONNECTED pair rather than a path, because nothing but
// that process should be able to ask — a path inside the sandbox is one the agent's shell could
// write to, and a task that can call for a credential itself is a task that delivers without
// passing its gate.
//
// credential is what a push to a repository is made with — the runner's own forge token, or
// why there is none.
func Open(id string, ws *workspace.Workspace, mirrors *workspace.Mirrors,
	credential func(workspace.Repo) (string, error), l task.Launch, log io.Writer) (*Channel, error) {
	near, far, err := SocketPair()
	if err != nil {
		return nil, err
	}
	return &Channel{
		id: id, ws: ws, l: l, srv: &Server{Mirrors: mirrors, Log: log}, cred: credential, log: log,
		near: near, far: far, served: make(chan struct{}),
	}, nil
}

// Child is the end the task's process inherits, as an extra descriptor (runnerd.Prepared.Extra).
func (c *Channel) Child() *os.File { return c.far }

// Serve answers the task's requests for as long as its process lives: the Serve of a
// runnerd.Prepared.
func (c *Channel) Serve() {
	c.serving.Store(true)
	defer close(c.served)
	c.srv.Serve(Task{ID: c.id, Workspace: c.ws, Launch: c.l, Credential: c.cred}, c.near)
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
// Delivery is not something this function does. It is the task's own action, gated by its
// autonomy and recorded on its read plane (the push tool) — so what happens here is only the
// decision about each tree, from what delivery staged out of it. A tree whose head an approved
// push staged has left; every other tree with work in it has not, whatever happened to its
// siblings.
func (c *Channel) Settle(ctx context.Context, l task.Launch, keep bool) bool {
	c.Close()
	// A push the process asked for before it died is finished first — staged, and delivered or
	// not — because what Salvage decides about a tree is read off what delivery did with it,
	// and a push half-performed is not an answer. Closing the near end is what ends Serve, so
	// the wait is bounded by the one request in flight.
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
	// the sweep.
	d := c.ws.Salvage(ctx, l)
	for _, ref := range d.Staged {
		fmt.Fprintf(c.log, "delivery: %s: the work did not leave; it is in this runner's "+
			"mirror at %s — GET /runner lists it with the command to fetch it\n", c.id, ref)
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
