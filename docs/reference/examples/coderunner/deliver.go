package main

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net"
	"os"
	"strconv"
	"strings"
	"syscall"

	"github.com/signadot/verse/codec"
	"github.com/signadot/verse/task"
)

// Delivery, as an action of the task rather than something that happens to it afterwards.
//
// The push used to be the last thing supervise did, after the loop returned, and that cost three
// things at once: the vector never saw it, the read plane never named the branch, and nothing
// downstream could chain off it. task.Finisher is the seam that fixes all three by making it an
// action — see the comment there for why an action beats a completion hook.
//
// The awkward part is that this runner's task runs in ANOTHER PROCESS, and the credential
// deliberately does not. So the action is proposed and gated on one side of the boundary and
// performed on the other:
//
//	child   the finisher proposes `deliver`, carrying a push effect per target
//	        the supervisor gates it — this is where a charter's `verbs: [push]` lands
//	        approved: the executor asks the parent, over an inherited socket
//	parent  commits, pushes with provenance trailers, answers with the refs
//	child   the refs are the action's output: on the read plane, before terminal
//
// That channel is NOT the task-control contract and does not pretend to be. It is a capability
// the parent holds and the task may ask it to exercise — the same shape as the egress proxy, and
// with the same reasoning about what makes it safe: the task can ask, and asking cannot redirect
// anything. Where the work goes is the environment's to say, the trailers are the runner's to
// write, and the token is never on this side.
//
// Unlike the egress socket it is an INHERITED CONNECTION rather than a path, because unlike the
// egress socket nothing needs to reach it except this process. A path in the sandbox would be one
// the agent's shell could also write to, and a task that can call `deliver` directly is a task
// that delivers without passing the gate.

// deliverFD is the connected socket the parent hands the task process, after the control listener
// (3) and the lifeline (4).
const deliverFD = 5

// deliverRequest / deliverReply are the whole protocol. Length-prefixed frames rather than
// newline-delimited ones because tony is a multi-line format: framing a document by newlines
// would work until the first document that had one.
type deliverRequest struct {
	Op string `tony:"field=op"`
	// Result is the driver's final message, which becomes the commit body. It travels with the
	// request rather than being read from the task's snapshot because at this moment the task
	// has not finished — that is the whole point of delivering inside the loop.
	Result string `tony:"field=result,omitzero"`
}

type deliverReply struct {
	OK    bool     `tony:"field=ok"`
	Refs  []string `tony:"field=refs,omitzero"`
	Error string   `tony:"field=error,omitzero"`
}

// ── the child's side ────────────────────────────────────────────────────────────────────────

// deliverFinisher proposes the closing action and asks the parent to perform it.
type deliverFinisher struct {
	ws     *Workspace
	conn   net.Conn
	result string
}

// Finish is the action, or nothing. An environment where no repository declares a push target has
// nowhere to deliver, and proposing a no-op would put a gate in front of nothing — which is worse
// than useless, since a charter that gates pushes would then stop on tasks that never push.
func (d *deliverFinisher) Finish(result string) (task.ToolUse, bool) {
	var targets []string
	var effects []task.Effect
	for _, t := range d.ws.Trees {
		target := t.PushTarget(d.ws.Branch)
		if target == "" {
			continue
		}
		targets = append(targets, t.Repo.Name+" → "+target)
		// One effect per destination, so a gate matching a particular ref matches when that ref
		// is among them rather than only when it is the first. GateMatch.OnPush compares against
		// RepoContext.PushTarget, which is the PRIMARY repository's — so the primary's effect is
		// what makes `onPush: true` fire, and the rest are what make the action legible.
		effects = append(effects, task.Effect{
			Verb: task.Push, Resource: task.Resource{Kind: task.KindGitRef, Ref: target},
		})
	}
	if len(targets) == 0 {
		return task.ToolUse{}, false
	}
	d.result = result
	return task.ToolUse{
		Name: "deliver",
		// Input is what a person reads on the read plane and what a gate question quotes. It
		// says where the work is going, which is the only question worth asking about a push.
		Input:   map[string]any{"branch": d.ws.Branch, "to": strings.Join(targets, ", ")},
		Effects: effects,
	}, true
}

// Do asks the parent to deliver and reports what it did. An error here fails the task, which is
// the reading task.Finisher documents: a run whose work did not leave is not one that succeeded.
func (d *deliverFinisher) Do(_ context.Context, _ task.ToolUse) (string, error) {
	if d.conn == nil {
		return "", fmt.Errorf("this task process was given no way to ask its runner to deliver")
	}
	rep, err := ask(d.conn, deliverRequest{Op: "deliver", Result: d.result})
	if err != nil {
		return "", fmt.Errorf("asking the runner to deliver: %w", err)
	}
	if !rep.OK {
		return "", fmt.Errorf("delivery failed: %s", rep.Error)
	}
	if len(rep.Refs) == 0 {
		// "Nothing to deliver" is a RESULT, and it must not render as a successful push with an
		// empty list. It did: the read plane carried `exec deliver: "pushed "` for a task that
		// changed nothing, which is finding 10 in a new place — nothing-to-do and
		// something-went-wrong reading identically, on the one frame that says where the work
		// went. A drive caught it on the first clean run.
		return "nothing to deliver: no repository with a push target was changed", nil
	}
	return "pushed " + strings.Join(rep.Refs, "; "), nil
}

func ask(conn net.Conn, req deliverRequest) (deliverReply, error) {
	body, err := codec.Marshal(req, codec.Tony)
	if err != nil {
		return deliverReply{}, err
	}
	if err := writeFrame(conn, body); err != nil {
		return deliverReply{}, err
	}
	raw, err := readFrame(bufio.NewReader(conn))
	if err != nil {
		return deliverReply{}, err
	}
	var rep deliverReply
	if err := codec.Unmarshal(raw, &rep, codec.Tony); err != nil {
		return deliverReply{}, err
	}
	return rep, nil
}

// ── the parent's side ───────────────────────────────────────────────────────────────────────

// serveDeliver answers delivery requests for one task, for as long as its process lives.
//
// Sequential by construction — one connection, one request at a time — which is what a task's
// closing act is. A task that asked twice would deliver twice, and that is the right behaviour
// rather than something to guard: the second push updates the same branch to the same content.
func (r *Runner) serveDeliver(c *child, l task.Launch, conn net.Conn) {
	defer conn.Close()
	br := bufio.NewReader(conn)
	for {
		raw, err := readFrame(br)
		if err != nil {
			return // the task process is gone, which is the ordinary end
		}
		rep := r.deliverOnce(c, l, raw)
		body, err := codec.Marshal(rep, codec.Tony)
		if err != nil {
			return
		}
		if err := writeFrame(conn, body); err != nil {
			return
		}
	}
}

func (r *Runner) deliverOnce(c *child, l task.Launch, raw []byte) deliverReply {
	var req deliverRequest
	if err := codec.Unmarshal(raw, &req, codec.Tony); err != nil {
		return deliverReply{Error: "undecodable request: " + err.Error()}
	}
	if req.Op != "deliver" {
		// One operation. A channel that grows verbs quietly becomes a second control plane, and
		// this one exists precisely because the first cannot cross the credential boundary.
		return deliverReply{Error: fmt.Sprintf("unknown operation %q", req.Op)}
	}
	d := c.ws.Deliver(context.Background(), l, req.Result)
	c.recordDelivery(d)
	for _, p := range d.Pushed {
		fmt.Fprintf(r.log, "coderunner: %s: pushed %s: %s\n", c.id, c.ws.Branch, p)
	}
	for _, s := range d.Staged {
		fmt.Fprintf(r.log, "coderunner: %s: staged in the mirror at %s\n", c.id, s)
	}
	if d.Err != nil {
		fmt.Fprintf(r.log, "coderunner: %s: deliver: %v\n", c.id, d.Err)
		return deliverReply{Refs: d.Pushed, Error: d.Err.Error()}
	}
	return deliverReply{OK: true, Refs: d.Pushed}
}

// ── framing ─────────────────────────────────────────────────────────────────────────────────

const maxFrame = 1 << 20

func writeFrame(w io.Writer, body []byte) error {
	if _, err := io.WriteString(w, strconv.Itoa(len(body))+"\n"); err != nil {
		return err
	}
	_, err := w.Write(body)
	return err
}

func readFrame(br *bufio.Reader) ([]byte, error) {
	line, err := br.ReadString('\n')
	if err != nil {
		return nil, err
	}
	n, err := strconv.Atoi(strings.TrimSpace(line))
	if err != nil {
		return nil, fmt.Errorf("bad frame length %q: %w", strings.TrimSpace(line), err)
	}
	if n < 0 || n > maxFrame {
		return nil, fmt.Errorf("frame of %d bytes", n)
	}
	body := make([]byte, n)
	if _, err := io.ReadFull(br, body); err != nil {
		return nil, err
	}
	return body, nil
}

// socketPair returns two ends of one connected stream: the parent keeps the first, the task
// process inherits the second.
//
// A pair rather than a listener, because the direction is the opposite of the control plane's.
// There, the parent dials and the child accepts; here the child asks and the parent answers, and
// what the child needs is something already connected rather than something to find.
func socketPair() (net.Conn, *os.File, error) {
	fds, err := syscall.Socketpair(syscall.AF_UNIX, syscall.SOCK_STREAM, 0)
	if err != nil {
		return nil, nil, fmt.Errorf("delivery channel: %w", err)
	}
	parent := os.NewFile(uintptr(fds[0]), "deliver-parent")
	child := os.NewFile(uintptr(fds[1]), "deliver-child")
	conn, err := net.FileConn(parent)
	parent.Close() // FileConn dups; this end is the conn now
	if err != nil {
		child.Close()
		return nil, nil, fmt.Errorf("delivery channel: %w", err)
	}
	return conn, child, nil
}
