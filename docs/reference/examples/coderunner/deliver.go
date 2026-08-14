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
	"github.com/signadot/verse/task/harness"
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
	// Repo is which repository this is about, because ONE PUSH ACTION IS ONE REPOSITORY.
	// Empty is the finisher's one delivery, which covers every tree that declares `auto` and
	// is the shape `deliver` has always had. See checkDelivering for why that is at most one.
	Repo string `tony:"field=repo,omitzero"`
	// Destination is the NAME of a place the environment declared, never a ref. That is the
	// whole of what a task may say about where work goes: a name this environment does not
	// declare resolves to nothing and nothing is pushed, so a task that could name a ref — and
	// thereby send this runner's credential wherever it liked — does not exist.
	Destination string `tony:"field=destination"`
	// Result is the driver's final message, which becomes the commit body. It travels with the
	// request rather than being read from the task's snapshot because at this moment the task
	// has not finished — that is the whole point of delivering inside the loop.
	Result string `tony:"field=result,omitzero"`
}

type deliverReply struct {
	OK   bool     `tony:"field=ok"`
	Refs []string `tony:"field=refs,omitzero"`
	// Refused is the far side declining, and it is a separate field from Error because it is a
	// separate KIND of answer: a remote that will not fast-forward, a protected branch, a
	// ruleset. The task did its job when it tried and was told no, so this ends the action as a
	// decision to re-plan around rather than as a failure — which matters most because it is
	// the ORDINARY outcome of a merge proposal, main having moved while somebody answered.
	Refused string `tony:"field=refused,omitzero"`
	Error   string `tony:"field=error,omitzero"`
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
		// `auto` BY NAME, which is the whole of step 1: the finisher asks for a destination
		// like anything else asking for one, and `push:` is the spelling that declares it.
		target := t.DestRef(autoDestination, d.ws.Branch)
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
	rep, err := ask(d.conn, deliverRequest{Op: "deliver", Destination: autoDestination, Result: d.result})
	if err != nil {
		return "", fmt.Errorf("asking the runner to deliver: %w", err)
	}
	if rep.Refused != "" {
		// The far side declined, which is an ANSWER and not a failure. Returned as an ordinary
		// output so the task ends `done` carrying it: the loop already treats a decision the
		// task complied with that way, and failing here would report the receiving end's own
		// rules as this runner breaking.
		return "not delivered — the remote refused it: " + rep.Refused, nil
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

// pushToolName is what the agent calls it, and the name is load-bearing rather than cosmetic:
// task.ToolUse.isPush() reads it, so a `onPush: true` gate matches an action named this whatever
// ref it carries. The effect below is what a `resources:` predicate discriminates on.
const pushToolName = "push"

// pushTool is the capability the agent exercises to send work OUT of the verse, and it exists
// because a runner whose only outward act is its own closing delivery admits exactly one flow.
// Merging a proposal into main, moving a promotion ref, retiring a branch that can no longer
// land: each was unaskable, and answering that with a second operation and then a third puts a
// policy in a mechanism and leaves the next flow needing a fourth.
//
// What the agent may say is a NAME the environment declared. It cannot say a ref, which is what
// keeps "where the work goes is the environment's to say" true of a task that can now ask for
// more than one thing — and the names are enumerated in the schema, so the vocabulary this
// runner will accept is the vocabulary the model can write rather than something it discovers by
// being refused.
//
// false when this environment declares no destinations at all: offering a tool whose every use
// is refused teaches an agent that the loop is broken.
func pushTool(ws *Workspace, conn net.Conn) (harness.Tool, bool) {
	names := ws.DestinationNames()
	if len(names) == 0 || conn == nil {
		return harness.Tool{}, false
	}
	return harness.Tool{
		Name: pushToolName,
		Description: "Send this task's committed work out of the sandbox, to one of the places " +
			"this environment declares. You cannot name a ref: name a destination and the runner " +
			"resolves where that is. The runner holds the credential and writes the provenance " +
			"trailers; you never see either. The push may be refused — by a supervisor, or by the " +
			"remote's own rules about its history — and a refusal is an answer, not an error to " +
			"retry. Available destinations: " + strings.Join(names, ", ") + ".",
		Params: map[string]any{
			"destination": map[string]any{"type": "string", "enum": names},
		},
		Required: []string{"destination"},
		Derive: func(tu *task.ToolUse) {
			name, _ := tu.Input["destination"].(string)
			t, ref := ws.Destination(name)
			if t == nil {
				// No effect, deliberately. An action with no push effect matches no capability
				// gate, so nobody is asked to approve a push that cannot happen — which is what
				// "an undeclared destination is refused BEFORE the gate" comes to in a loop
				// where classification runs first and the executor runs last.
				return
			}
			tu.Effects = append(tu.Effects, task.Effect{Verb: task.Push, Resource: task.GitRef(ref)})
		},
		Do: func(_ context.Context, tu task.ToolUse) (string, bool, error) {
			name, _ := tu.Input["destination"].(string)
			t, ref := ws.Destination(name)
			if t == nil {
				return fmt.Sprintf("there is no destination %q in this environment — it declares: %s",
					name, strings.Join(names, ", ")), true, nil
			}
			rep, err := ask(conn, deliverRequest{
				Op: "push", Repo: t.Repo.Name, Destination: name,
			})
			switch {
			case err != nil:
				return "", false, fmt.Errorf("asking the runner to push: %w", err)
			case rep.Refused != "":
				return "the remote refused it: " + rep.Refused, true, nil
			case !rep.OK:
				return "", false, fmt.Errorf("push to %s failed: %s", ref, rep.Error)
			case len(rep.Refs) == 0:
				return "nothing to push: " + t.Repo.Name + " has no work that is not already there", false, nil
			}
			return "pushed " + strings.Join(rep.Refs, "; "), false, nil
		},
	}, true
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
	// Two operations and no more, and the difference between them is the PROPOSER rather than
	// the work: `deliver` is the runner's own closing act over every tree that declares `auto`,
	// and `push` is the agent asking for one repository and one named destination. Step 5 of the
	// plan retires the first, at which point delivery is a push like any other.
	//
	// A channel that grows verbs quietly becomes a second control plane, and this one exists
	// precisely because the first cannot cross the credential boundary.
	var repo string
	switch req.Op {
	case "deliver":
	case "push":
		repo = req.Repo
	default:
		return deliverReply{Error: fmt.Sprintf("unknown operation %q", req.Op)}
	}
	d := c.ws.Deliver(context.Background(), l, req.Result, repo, req.Destination)
	c.recordDelivery(d)
	for _, p := range d.Pushed {
		fmt.Fprintf(r.log, "coderunner: %s: pushed %s: %s\n", c.id, c.ws.Branch, p)
	}
	for _, s := range d.Staged {
		fmt.Fprintf(r.log, "coderunner: %s: staged in the mirror at %s\n", c.id, s)
	}
	if len(d.Refused) > 0 {
		// Logged as an outcome rather than as an error, because it is one. The far side's rules
		// about its own history are not this runner malfunctioning.
		fmt.Fprintf(r.log, "coderunner: %s: refused by the remote: %s\n",
			c.id, strings.Join(d.Refused, "; "))
		return deliverReply{Refs: d.Pushed, Refused: strings.Join(d.Refused, "; ")}
	}
	if d.Err != nil {
		fmt.Fprintf(r.log, "coderunner: %s: %s: %v\n", c.id, req.Op, d.Err)
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
