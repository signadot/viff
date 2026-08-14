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

// A push is a CAPABILITY the parent holds and the task may ask it to exercise, and the whole of
// what it hands over is a credential.
//
// The push used to be the last thing supervise did, after the loop returned, and that cost three
// things at once: the vector never saw it, the read plane never named the branch, and nothing
// downstream could chain off it. task.Finisher is the seam that fixed all three by making it an
// action — see the comment there for why an action beats a completion hook.
//
// What it was NOT was a capability. The channel carried one op with no target, which is a button:
// a task could ask to deliver its own branch to its own ref and could ask for nothing else, so
// merging a proposal, moving a promotion ref or retiring a stale branch had no way to be asked
// for at all. The obvious repair is a second op and then a third, and that repair is the actual
// defect — each op is a new parser, a new authorization decision and a new thing to audit, and a
// protocol that grows one op per flow is a complexity multiplier, which at that growth rate is a
// bigger hazard than the thing the button was protecting against.
//
// So: the environment declares destinations, the task names one and never a ref, deriving stamps
// the ref it resolves to, the vector gates THAT, and approval releases the credential that
// destination carries. The task runs the git. A new flow is a line of config and costs the
// protocol nothing.
//
//	child   the finisher, or the agent's own `push` tool, proposes an action naming a destination
//	        the supervisor gates it — this is where a charter's `verbs: [push]` lands
//	        approved: the executor commits, asks for the credential, and pushes
//	parent  stages the work in its mirror and answers with the ref and the secret
//	child   what happened is the action's output: on the read plane, before terminal
//
// The channel is NOT the task-control contract and does not pretend to be. The task can ask, and
// asking cannot redirect anything: where the work goes is the environment's to say, and the
// trailers are the runner's to write.
//
// Unlike the egress socket it is an INHERITED CONNECTION rather than a path, because unlike the
// egress socket nothing needs to reach it except this process. A path in the sandbox would be one
// the agent's shell could also write to, and a task that can ask for a credential directly is a
// task that pushes without passing the gate.

// pushFD is the connected socket the parent hands the task process, after the control listener
// (3) and the lifeline (4).
const pushFD = 5

// pushRequest / pushReply are the whole protocol, and ONE op is the whole of it. Length-prefixed
// frames rather than newline-delimited ones because tony is a multi-line format: framing a
// document by newlines would work until the first document that had one.
type pushRequest struct {
	// Op is `credential` and nothing else. It is still a field because a protocol with one verb
	// and no name for it is one that grows a second verb by accident.
	Op string `tony:"field=op"`
	// Repo is which repository this is about, because ONE PUSH ACTION IS ONE REPOSITORY. That is
	// what stops a mixed outcome from being a state anybody has to model: each action has its
	// own gate, its own result and its own place in the record.
	Repo string `tony:"field=repo,omitzero"`
	// Ref is the one thing a task may say about the WORLD rather than about the environment,
	// and it is meaningful only for a destination that retires. The parent re-checks it against
	// the glob that destination declares — the child checked it too, and the child is the side
	// that must not be trusted about which ref it is allowed to remove.
	Ref string `tony:"field=ref,omitzero"`
	// Destination is the NAME of a place the environment declared, never a ref. That is the
	// whole of what a task may say about where work goes: a name this environment does not
	// declare resolves to nothing, so a task that could name a ref — and thereby send this
	// runner's credential wherever it liked — does not exist.
	Destination string `tony:"field=destination"`
}

type pushReply struct {
	OK bool `tony:"field=ok"`
	// Ref and Token are the answer: where the approved push goes, and the secret that lets it.
	// THE GATE SAYING YES IS WHAT PUTS THE SECRET IN THE TASK PROCESS — there is no other path
	// to it — which is what makes the vector load-bearing rather than advisory.
	//
	// It never reaches `buildEnv`'s allowlist, so it never reaches the model's shell. That
	// boundary, and not the sandbox wall, is what keeps a secret away from the thing whose
	// output is published: `coderunner -task` already holds the model key on exactly these terms.
	Ref   string `tony:"field=ref,omitzero"`
	Token string `tony:"field=token,omitzero"`
	Error string `tony:"field=error,omitzero"`
}

// ── the child's side ────────────────────────────────────────────────────────────────────────

// pushFinisher is the runner's closing act: a push to `auto`, proposed like anything else.
//
// `deliver` is gone, and what it turns out to have been is one destination all along. Nothing
// observable moved when it went — the same ref, from the same task, at the same point in the
// loop — because the argument for delivery being an action inside the loop was always about the
// ACTION and never about the transport underneath it.
type pushFinisher struct {
	p      *taskProc
	conn   net.Conn
	result string
}

// Finish is the action, or nothing. An environment where no repository declares a destination has
// nowhere to deliver, and proposing a no-op would put a gate in front of nothing — which is worse
// than useless, since a charter that gates pushes would then stop on tasks that never push.
//
// ONE action, for ONE repository, and checkDelivering is what makes that complete rather than
// merely true today: an environment where a second repository could deliver is refused when the
// config is read, because `Finish` returns one action and the second delivery would be silently
// held. If a second one ever arrives the shape is this returning a SEQUENCE proposed once.
func (d *pushFinisher) Finish(result string) (task.ToolUse, bool) {
	t, target := d.p.ws.Destination(autoDestination)
	if t == nil {
		return task.ToolUse{}, false
	}
	d.result = result
	return task.ToolUse{
		// The same name the agent's own tool has, and deliberately: `ToolUse.isPush` reads it, so
		// this is what finally makes `onPush: true` match the one action it was written for. A
		// reader of the plane sees a push, which is what it is.
		Name: pushToolName,
		// Input is what a person reads on the read plane and what a gate question quotes. It says
		// where the work is going, which is the only question worth asking about a push.
		Input: map[string]any{
			"destination": autoDestination, "repo": t.Repo.Name,
			"branch": d.p.ws.Branch, "to": target,
		},
		Effects: []task.Effect{{Verb: task.Push, Resource: task.GitRef(target)}},
	}, true
}

// Do commits, asks for the credential, and pushes — the same three steps the agent's own tool
// takes, through the same function. An error here fails the task, which is the reading
// task.Finisher documents: a run whose work did not leave is not one that succeeded.
func (d *pushFinisher) Do(ctx context.Context, _ task.ToolUse) (string, error) {
	if d.conn == nil {
		return "", fmt.Errorf("this task process was given no way to ask its runner to deliver")
	}
	t, target := d.p.ws.Destination(autoDestination)
	if t == nil {
		return "", fmt.Errorf("no destination %q in this environment", autoDestination)
	}
	// A refusal comes back as an ordinary output rather than as an error, so the task ends `done`
	// carrying it: being told no by the receiving end is a decision the task complied with, and
	// failing here would report somebody else's rules about their own repository as this runner
	// breaking.
	out, _, err := push(ctx, d.conn, t, d.p.ws.Branch, autoDestination, target, d.p.launch(), d.result)
	return out, err
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
			"this environment declares, or retire a ref at one that permits it. You cannot name " +
			"where work GOES: name a destination and the runner resolves that. The runner holds " +
			"the credential and writes the provenance trailers; you never see either. It may be " +
			"refused — by a supervisor, or by the remote's own rules about its history — and a " +
			"refusal is an answer, not an error to retry. Available destinations: " +
			strings.Join(names, ", ") + ". `ref` applies ONLY to a destination that retires: give " +
			"the ref to delete, and the environment decides which refs that destination may " +
			"touch at all.",
		Params: map[string]any{
			"destination": map[string]any{"type": "string", "enum": names},
			"ref":         map[string]any{"type": "string"},
		},
		Required: []string{"destination"},
		Derive: func(tu *task.ToolUse) {
			name, _ := tu.Input["destination"].(string)
			if t, ref := ws.Retirable(name, argRef(tu.Input)); t != nil {
				// DELETE rather than PUSH, so a charter gates retiring on its own. A push to an
				// auto branch is recoverable and this is the one act here that destroys
				// something, and `verbs: [delete]` is how somebody says that in a vector.
				tu.Effects = append(tu.Effects, task.Effect{Verb: task.Delete, Resource: task.GitRef(ref)})
				return
			}
			t, ref := ws.Destination(name)
			if t == nil {
				// No effect, deliberately. An action with no effect matches no capability gate,
				// so nobody is asked to approve something that cannot happen — which is what
				// "an undeclared destination is refused BEFORE the gate" comes to in a loop
				// where classification runs first and the executor runs last. A ref outside what
				// a retiring destination permits lands here too, and for the same reason.
				return
			}
			tu.Effects = append(tu.Effects, task.Effect{Verb: task.Push, Resource: task.GitRef(ref)})
		},
		Do: func(ctx context.Context, tu task.ToolUse) (string, bool, error) {
			name, _ := tu.Input["destination"].(string)
			asked := argRef(tu.Input)
			if t, ref := ws.Retirable(name, asked); t != nil {
				return retire(ctx, conn, t, name, ref)
			}
			t, ref := ws.Destination(name)
			switch {
			case t == nil:
				return fmt.Sprintf("there is no destination %q in this environment — it declares: %s",
					name, strings.Join(names, ", ")), true, nil
			case asked != "":
				// Said rather than ignored: a model that supplied a ref believes it decided
				// where this went, and letting it push somewhere else while quietly dropping
				// the argument is the one outcome worse than refusing.
				return fmt.Sprintf("destination %q does not retire refs, so `ref` means nothing "+
					"to it — it sends this task's work to the one place the environment named. "+
					"Drop `ref`, or name a destination that retires.", name), true, nil
			}
			return push(ctx, conn, t, ws.Branch, name, ref, p.launch(), "")
		},
	}, true
}

// argRef is the `ref` a retirement was asked to remove, or "" — the one argument this tool takes
// that names something in the world rather than something the environment declared.
func argRef(in map[string]any) string {
	s, _ := in["ref"].(string)
	return strings.TrimSpace(s)
}

// retire deletes a ref at a destination that permits it.
//
// There is nothing to commit and nothing to stage: a retirement removes something rather than
// sending something, so the mirror has no copy to keep and the workspace holds nothing that
// would be lost. That is the whole difference from push below, and it is why this asks for the
// credential and does the git without the two hops.
func retire(ctx context.Context, conn net.Conn, t *Tree, dest, ref string) (string, bool, error) {
	rep, err := ask(conn, pushRequest{Op: "credential", Repo: t.Repo.Name, Destination: dest, Ref: ref})
	switch {
	case err != nil:
		return "", false, fmt.Errorf("asking the runner for the credential for %q: %w", dest, err)
	case rep.Error != "":
		return "", false, fmt.Errorf("the runner would not release the credential for %q: %s", dest, rep.Error)
	case rep.Ref == "":
		return "", false, fmt.Errorf("the runner resolved no ref for destination %q", dest)
	}
	if err := t.deleteRef(ctx, rep.Ref, TokenAuth(rep.Token)(t.Repo.Repo)); err != nil {
		if why := refusal(err); why != "" {
			return "not retired — the remote refused it: " + why, true, nil
		}
		return "", false, err
	}
	return "retired " + rep.Ref, false, nil
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
	rep, err := ask(conn, pushRequest{Op: "credential", Repo: t.Repo.Name, Destination: dest})
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

func ask(conn net.Conn, req pushRequest) (pushReply, error) {
	body, err := codec.Marshal(req, codec.Tony)
	if err != nil {
		return pushReply{}, err
	}
	if err := writeFrame(conn, body); err != nil {
		return pushReply{}, err
	}
	raw, err := readFrame(bufio.NewReader(conn))
	if err != nil {
		return pushReply{}, err
	}
	var rep pushReply
	if err := codec.Unmarshal(raw, &rep, codec.Tony); err != nil {
		return pushReply{}, err
	}
	return rep, nil
}

// ── the parent's side ───────────────────────────────────────────────────────────────────────

// servePush answers one task's credential requests, for as long as its process lives.
//
// Sequential by construction — one connection, one request at a time. A task that asked twice
// gets two credentials for two approved destinations, which is what a task with more than one
// thing to push is; the closing act is still at most once per quiescence, because that is the
// loop's rule rather than this channel's.
func (r *Runner) servePush(c *child, conn net.Conn) {
	defer conn.Close()
	br := bufio.NewReader(conn)
	for {
		raw, err := readFrame(br)
		if err != nil {
			return // the task process is gone, which is the ordinary end
		}
		rep := r.answer(c, raw)
		body, err := codec.Marshal(rep, codec.Tony)
		if err != nil {
			return
		}
		if err := writeFrame(conn, body); err != nil {
			return
		}
	}
}

// answer is ONE operation, and it is not an operation at all: the task names a destination and
// what comes back is a SECRET rather than a performance.
//
// That is what stops the protocol growing. A merge, a promotion and a retirement are all this
// one request, because the side holding the credential does not need to know which of them is
// happening. An op per flow would keep the op count at one only by growing the OPERATION count
// behind it, which is the same multiplier wearing a different hat — and it is why `deliver` is
// gone rather than kept beside this: it was one destination all along.
func (r *Runner) answer(c *child, raw []byte) pushReply {
	var req pushRequest
	if err := codec.Unmarshal(raw, &req, codec.Tony); err != nil {
		return pushReply{Error: "undecodable request: " + err.Error()}
	}
	if req.Op != "credential" {
		return pushReply{Error: fmt.Sprintf("unknown operation %q", req.Op)}
	}
	return r.release(c, req)
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
func (r *Runner) release(c *child, req pushRequest) pushReply {
	// A RETIREMENT first, and re-checked here rather than taken from the request. The child
	// validated the ref against the destination's glob too, and that check is for the agent's
	// benefit — this one is the one that matters, because the child is the side holding a model
	// and the request is whatever it chose to send. Nothing else in this protocol takes an
	// argument that names something in the world, and this is why.
	if t, ref := c.ws.Retirable(req.Destination, req.Ref); t != nil {
		if req.Repo != "" && t.Repo.Name != req.Repo {
			return pushReply{Error: fmt.Sprintf("destination %q is not on repository %q",
				req.Destination, req.Repo)}
		}
		tok, err := r.credentialFor(c.ws.Env, t.Repo, req.Destination)
		if err != nil {
			return pushReply{Error: err.Error()}
		}
		// Nothing staged: a retirement removes something rather than sending something, so
		// there is no copy for the mirror to hold and nothing that could be lost by it failing.
		fmt.Fprintf(r.log, "coderunner: %s: releasing a credential to retire %s\n", c.id, ref)
		return pushReply{OK: true, Ref: ref, Token: tok}
	}
	if req.Ref != "" {
		// Asked to delete something no retiring destination permits. Refused loudly: this is the
		// one request that can destroy, and a silent fallthrough to the delivery path below
		// would push instead, which is not what was asked and not obviously worse to a reader.
		return pushReply{Error: fmt.Sprintf("destination %q does not permit retiring %q — a "+
			"destination that deletes declares which refs it may touch, and this is not one of "+
			"them", req.Destination, req.Ref)}
	}
	t, ref := c.ws.Destination(req.Destination)
	if t == nil || (req.Repo != "" && t.Repo.Name != req.Repo) {
		// The two ends disagree about what this environment declares. It cannot happen through
		// the tool, whose schema enumerates the names, so it means a task speaking the protocol
		// directly — which is exactly the thing that must resolve to nothing.
		return pushReply{Error: fmt.Sprintf("no destination %q in environment %q",
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
		return pushReply{Error: d.Err.Error()}
	}
	tok, err := r.credentialFor(c.ws.Env, t.Repo, req.Destination)
	if err != nil {
		return pushReply{Error: err.Error()}
	}
	return pushReply{OK: true, Ref: ref, Token: tok}
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
