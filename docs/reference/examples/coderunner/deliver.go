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
	// Ref and Token are what a `credential` request is answered with: where the approved push
	// goes, and the secret that lets it. THE GATE SAYING YES IS WHAT PUTS THE SECRET IN THE TASK
	// PROCESS — there is no other path to it — which is what makes the vector load-bearing
	// rather than advisory.
	//
	// It never reaches `buildEnv`'s allowlist, so it never reaches the model's shell. That
	// boundary, and not the sandbox wall, is what keeps a secret away from the thing whose
	// output is published: `coderunner -task` already holds the model key on exactly these terms.
	Ref   string `tony:"field=ref,omitzero"`
	Token string `tony:"field=token,omitzero"`
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
// The GIT is done here, in the task process, and that is the point of the arrangement rather
// than an implementation detail. What crosses the boundary is a CREDENTIAL and never an
// operation: the parent answers "you may push <repo> to <ref>, here is the secret", and what a
// push, a merge or a retirement actually is stays on this side. The alternative — one op that
// the parent performs — keeps the op count at one while the OPERATION count grows inside the
// parent, which is the same multiplier wearing a different hat.
//
// false when this environment declares no destinations at all: offering a tool whose every use
// is refused teaches an agent that the loop is broken.
func pushTool(p *taskProc, conn net.Conn) (harness.Tool, bool) {
	ws := p.ws
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
		Do: func(ctx context.Context, tu task.ToolUse) (string, bool, error) {
			name, _ := tu.Input["destination"].(string)
			t, ref := ws.Destination(name)
			if t == nil {
				return fmt.Sprintf("there is no destination %q in this environment — it declares: %s",
					name, strings.Join(names, ", ")), true, nil
			}
			return push(ctx, conn, t, ws.Branch, name, ref, p.launch(), "")
		},
	}, true
}

// push is the whole of a push from this side: commit what is there, ask the runner for the
// credential that destination names, and do the git.
//
// The ORDER is the design. Committing first means the parent stages a commit rather than a
// promise, so the two hops the mirror exists for are unchanged — work is in this runner's own
// object store before anything is attempted over a network. Asking second means the credential
// arrives only after the vector has approved this exact destination, which is what makes the
// gate load-bearing rather than advisory. Pushing third means the parent never learns what git
// operation this was, which is the point: a merge, a promotion and a retirement are all "a
// credential for a destination" to the side that holds the secret.
//
// result is the commit body for a closing delivery and empty for an agent's own push, where
// there is no final message yet — commitMessage falls back to naming the task.
func push(ctx context.Context, conn net.Conn, t *Tree, branch, dest, ref string, l task.Launch, result string) (string, bool, error) {
	work, err := t.commitWork(l, result)
	if err != nil {
		return "", false, fmt.Errorf("committing the work in %s: %w", t.Repo.Name, err)
	}
	if !work {
		// "Nothing to push" is a RESULT and must not read as a successful push of nothing. It
		// also asks for no credential, which is the right shape: no work, no secret.
		return "nothing to push: " + t.Repo.Name + " has no work that is not already there", false, nil
	}
	rep, err := ask(conn, deliverRequest{Op: "credential", Repo: t.Repo.Name, Destination: dest})
	switch {
	case err != nil:
		return "", false, fmt.Errorf("asking the runner for the credential for %q: %w", dest, err)
	case rep.Error != "":
		return "", false, fmt.Errorf("the runner would not release the credential for %q: %s", dest, rep.Error)
	case rep.Ref == "":
		// The two ends disagree about what this environment declares, which is worth failing
		// loudly rather than pushing to a ref this side computed alone.
		return "", false, fmt.Errorf("the runner resolved no ref for destination %q", dest)
	}
	if err := t.pushAs(ctx, branch, rep.Ref, TokenAuth(rep.Token)(t.Repo.Repo)); err != nil {
		if why := refusal(err); why != "" {
			return "not pushed — the remote refused it: " + why, true, nil
		}
		return "", false, err
	}
	return "pushed " + t.Repo.Name + " -> " + rep.Ref, false, nil
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
	// Two operations, and only one of them is an operation.
	//
	// `credential` is the whole design: the task names a destination, and what comes back is a
	// SECRET rather than a performance. That is what stops the protocol growing — a merge, a
	// promotion, a retirement are all this one request, because the side holding the credential
	// does not need to know which of them is happening. An op per flow would keep the op count
	// at one only by growing the OPERATION count inside the parent, which is the same multiplier
	// wearing a different hat.
	//
	// `deliver` is the runner's own closing act, still performed here. Step 5 of the plan retires
	// it, at which point delivery is a push like any other and this switch has one arm.
	switch req.Op {
	case "credential":
		return r.release(c, req)
	case "deliver":
	default:
		return deliverReply{Error: fmt.Sprintf("unknown operation %q", req.Op)}
	}
	d := c.ws.Deliver(context.Background(), l, req.Result, "", req.Destination)
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

// release answers a `credential` request: stage the work, then hand over the secret.
//
// STAGING FIRST is what keeps the mirror's promise across the move. The task pushes now, so this
// side no longer sees the push — but the reason work goes into the mirror on its way out was
// never that the parent was pushing it, it was that a checkout is a directory somebody deletes
// and a failed push must not be the difference between a result and nothing. Putting it there at
// the moment a push is authorized keeps exactly that, and it costs a thin pack against a
// repository that already has every base object.
//
// Whether it LANDED is not this side's to know, and pretending otherwise would be the mistake
// this design makes elsewhere on purpose (a cancelled push is `unknown`, not `not done`). The
// pending ref settles itself: Reflect pushes it again on the next sweep, which is a no-op if the
// world already has it, a retry if it did not land, and a move to `held` if the remote refuses.
func (r *Runner) release(c *child, req deliverRequest) deliverReply {
	t, ref := c.ws.Destination(req.Destination)
	if t == nil || (req.Repo != "" && t.Repo.Name != req.Repo) {
		// The two ends disagree about what this environment declares. It cannot happen through
		// the tool, whose schema enumerates the names, so it means a task speaking the protocol
		// directly — which is exactly the thing that must resolve to nothing.
		return deliverReply{Error: fmt.Sprintf("no destination %q in environment %q",
			req.Destination, c.ws.Env.Name)}
	}
	d := c.ws.Stage(context.Background(), c.id, t, ref)
	c.recordDelivery(d)
	for _, s := range d.Staged {
		fmt.Fprintf(r.log, "coderunner: %s: staged in the mirror at %s, on its way to %s\n",
			c.id, s, ref)
	}
	if d.Err != nil {
		// Staging is the local hop, so this is the disk rather than the network — and the
		// credential is NOT released, because the one copy of the work is a directory this
		// runner is about to be asked to remove.
		fmt.Fprintf(r.log, "coderunner: %s: staging before a push to %s: %v\n", c.id, ref, d.Err)
		return deliverReply{Error: d.Err.Error()}
	}
	tok, err := r.credentialFor(c.ws.Env, t.Repo, req.Destination)
	if err != nil {
		return deliverReply{Error: err.Error()}
	}
	return deliverReply{OK: true, Ref: ref, Token: tok}
}

// credentialFor is the mapping the whole design turns on: a granted capability → the secret it
// carries. Nothing here reads a ref, and nothing here knows what the secret is for — which is
// what keeps this from being a git feature.
//
// A destination naming no credential falls back to the runner's own token, which is what every
// environment relied on while the parent did the pushing, and what a local path (the tests) wants.
func (r *Runner) credentialFor(env Environment, repo Repo, dest string) (string, error) {
	d, ok := repo.Dest(dest)
	if !ok {
		return "", fmt.Errorf("no destination %q on repository %q", dest, repo.Name)
	}
	name := env.CredentialEnv(d)
	if name == "" {
		return r.token, nil
	}
	tok := os.Getenv(name)
	if tok == "" {
		// Refused rather than released empty, because an empty credential reaches the far side
		// as somebody else's "authentication required" and the task then reports the forge's
		// opinion of this runner's configuration.
		return "", fmt.Errorf("destination %q wants credential %q, and $%s is not set in this "+
			"runner's environment", dest, d.Credential, name)
	}
	return tok, nil
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
