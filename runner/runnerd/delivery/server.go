package delivery

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"

	"github.com/signadot/verse/codec"
	"github.com/signadot/verse/runner/runnerd/workspace"
	"github.com/signadot/verse/task"
)

// Server is the runner's end of the channel: it holds the mirrors work is staged in and pushed
// from, and performs each task's requests with what that task's Credential provides.
type Server struct {
	Mirrors *workspace.Mirrors
	Log     io.Writer
	// HTTP reaches the forge's API for pull requests; nil is http.DefaultClient.
	HTTP *http.Client
}

// Task is one task as the server knows it: its id, the workspace it works in, the launch whose
// provenance its commits must carry, where its credential comes from, and who to tell what
// staging did.
type Task struct {
	ID        string
	Workspace *workspace.Workspace
	Launch    task.Launch
	// Credential is the secret a push to one of the task's repositories is made with, or why
	// there is none. The runner decides; its own forge token is the usual answer.
	Credential func(repo workspace.Repo) (string, error)
}

// Serve answers one task's requests, for as long as its process lives.
//
// Sequential by construction — one connection, one request at a time. A task that asked twice
// gets two pushes, which is what a task with more than one thing to push is.
func (s *Server) Serve(tk Task, conn net.Conn) {
	defer conn.Close()
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
// namespace — has neither the secret nor, with a restricted egress, any route to the forge.
func (s *Server) answer(tk Task, raw []byte) Reply {
	var req Request
	if err := codec.Unmarshal(raw, &req, codec.Tony); err != nil {
		return Reply{Error: "undecodable request: " + err.Error()}
	}
	switch req.Op {
	case "push":
		return s.push(tk, req)
	case "pull":
		return s.pull(tk, req)
	case "fetch":
		return s.refresh(tk, req)
	}
	return Reply{Error: fmt.Sprintf("unknown operation %q", req.Op)}
}

// refresh answers a `fetch`: the mirror up to upstream, then the checkout up to the mirror.
//
// The fetch is the one every spawn does, under the runner's own context: a task's fetch is not
// bounded by anybody's control deadline. A remote that cannot be reached is an error the task
// reads as an answer, and the checkout is left as it was — `ensure` keeps what the mirror had,
// and nothing here moves a ref to a base nobody has.
func (s *Server) refresh(tk Task, req Request) Reply {
	t := tk.Workspace.Tree(req.Repo)
	if t == nil {
		return Reply{Error: fmt.Sprintf("no repository %q in this workspace", req.Repo)}
	}
	mirror, err := s.Mirrors.Ensure(context.Background(), t.Repo.Repo)
	if err != nil {
		fmt.Fprintf(s.Log, "delivery: %s: fetch of %s: %v\n", tk.ID, t.Repo.Name, err)
		return Reply{Error: err.Error()}
	}
	moved, err := t.Refresh(mirror)
	if err != nil {
		fmt.Fprintf(s.Log, "delivery: %s: refreshing %s from its mirror: %v\n", tk.ID, t.Repo.Name, err)
		return Reply{Error: err.Error()}
	}
	if len(moved) > 0 {
		fmt.Fprintf(s.Log, "delivery: %s: fetched %s: %s\n", tk.ID, t.Repo.Name, strings.Join(moved, ", "))
	}
	return Reply{OK: true, Updated: moved}
}

// push answers a `push`: check the branch against the allowance, check what the task committed,
// stage it, and push it from the mirror. Or, with Delete, remove the branch.
//
// THE CHECK IS HERE, whatever the task checked. The task holds a model and the request is
// whatever it chose to send, so the side that holds the secret decides what it may be used for
// (workspace.Repo.Permit) — a branch outside what the spawn permits is refused, and nothing moves.
//
// What is pushed is what the task COMMITTED, each commit carrying the provenance trailers, and
// nothing it left uncommitted — read from the checkout the task and this process share, refused
// with what is missing so the agent re-commits and stays the author of its SHAs.
//
// STAGING FIRST is what makes a failed push a retry rather than a loss: the work is in the mirror,
// which is this side's own disk, before anything is tried over a network. The push is then the
// staged ref to the target, which is the same act the sweep performs for anything that did not
// land (Reflect), so there is one way work leaves and the retry is it.
func (s *Server) push(tk Task, req Request) Reply {
	t := tk.Workspace.Tree(req.Repo)
	if t == nil {
		return Reply{Error: fmt.Sprintf("no repository %q in this workspace", req.Repo)}
	}
	ref, force, err := t.Repo.Permit(req.Branch, req.Delete)
	if err != nil {
		return Reply{Refused: err.Error()}
	}
	tok, err := tk.Credential(t.Repo)
	if err != nil {
		return Reply{Error: err.Error()}
	}
	auth := workspace.TokenAuth(tok)(t.Repo.Repo)
	if req.Delete {
		// Nothing staged: a retirement removes something rather than sending something, so
		// there is no copy for the mirror to hold and nothing that could be lost by it failing.
		fmt.Fprintf(s.Log, "delivery: %s: deleting %s at %s\n", tk.ID, ref, t.Repo.Repo)
		if err := s.Mirrors.DeleteRef(context.Background(), t.Repo.Repo, ref, auth); err != nil {
			if why := workspace.Refusal(err); why != "" {
				return Reply{Refused: "the remote refused it: " + why}
			}
			return Reply{Error: err.Error()}
		}
		return Reply{OK: true, Out: "deleted " + t.Repo.Name + " " + ref}
	}
	ok, refusal, err := t.AuthoredWork(tk.Launch)
	switch {
	case err != nil:
		return Reply{Error: err.Error()}
	case !ok:
		return Reply{OK: true, Out: "nothing to push: " + t.Repo.Name + " has no commits beyond the base it " +
			"started from; commit what you want delivered, then push"}
	case refusal != "":
		return Reply{Refused: refusal}
	}
	d := tk.Workspace.Stage(context.Background(), tk.ID, t, ref, force)
	if d.Err != nil {
		// Staging is the local hop, so this is the disk rather than the network — and nothing is
		// pushed, because the one copy of the work is a directory this runner is about to be
		// asked to remove.
		fmt.Fprintf(s.Log, "delivery: %s: staging before a push to %s: %v\n", tk.ID, ref, d.Err)
		return Reply{Error: d.Err.Error()}
	}
	staged := d.Staged[0]
	fmt.Fprintf(s.Log, "delivery: %s: staged in the mirror at %s, pushing to %s\n", tk.ID, staged, ref)
	why, err := s.Mirrors.Deliver(context.Background(), t.Repo.Repo, staged, ref, force, auth)
	switch {
	case err != nil:
		// Not landed, and not a decision: the ref stays pending, and the sweep tries again. The
		// task hears that, rather than a success it would plan on.
		return Reply{Error: fmt.Sprintf("push to %s did not land (%v); the runner holds the work and retries", ref, err)}
	case why != "":
		return Reply{Refused: "the remote refused it: " + why}
	}
	out := "pushed " + t.Repo.Name + " -> " + ref
	// Said, because an agent that edited and did not commit may believe it delivered.
	if n := len(t.Changed()); n > 0 {
		out += fmt.Sprintf(" (%d uncommitted change(s) stayed in the workspace, not delivered)", n)
	}
	return Reply{OK: true, Out: out}
}

// pull answers a `pull`: a pull request from a branch the task may push, into a base, at a forge
// that has them, opened or updated with this runner's token.
func (s *Server) pull(tk Task, req Request) Reply {
	t := tk.Workspace.Tree(req.Repo)
	if t == nil {
		return Reply{Error: fmt.Sprintf("no repository %q in this workspace", req.Repo)}
	}
	// A pull request is FROM a branch the task may push: the push is what the gate approved, and
	// the pull request is what that branch means at the forge.
	if _, _, err := t.Repo.Permit(req.Branch, false); err != nil {
		return Reply{Refused: "a pull request is from a branch this task may push, and " + err.Error()}
	}
	api, owner, repo, err := PullAPI(t.Repo.Repo)
	if err != nil {
		return Reply{Refused: err.Error()}
	}
	tok, err := tk.Credential(t.Repo)
	if err != nil {
		return Reply{Error: err.Error()}
	}
	spec := PullSpec{API: api, Owner: owner, Repo: repo, Head: req.Branch, Base: req.Base, Title: req.Title, Body: req.Body}
	hc := s.HTTP
	if hc == nil {
		hc = http.DefaultClient
	}
	var out string
	if req.Update {
		out, err = UpdatePull(context.Background(), hc, tok, spec)
	} else {
		out, err = EnsurePull(context.Background(), hc, tok, spec)
	}
	if err != nil {
		return Reply{Error: err.Error()}
	}
	fmt.Fprintf(s.Log, "delivery: %s: %s\n", tk.ID, out)
	return Reply{OK: true, Out: out}
}
