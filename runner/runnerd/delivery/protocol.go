package delivery

import (
	"bufio"
	"fmt"
	"io"
	"net"
	"os"
	"strconv"
	"strings"
	"syscall"
)

// Request / Reply are the whole protocol, and each op is an act the runner performs for the
// task. `push` is for WRITING the world: the task names a repository and a branch, the runner
// decides it by the launch's autonomy, stages the committed work in its mirror and pushes it from
// there. `create-pr` opens a pull request from such a branch, and `update-pr` changes the open
// one. `fetch` is for READING it: the runner brings its mirror up to upstream
// and the checkout's `mirror/*` refs and tags up to the mirror. Nothing crosses but requests and answers: the credential
// stays in the runner, used where it lives. Length-prefixed frames rather than newline-delimited
// ones because tony is a multi-line format: framing a document by newlines would work until the
// first document that had one.
type Request struct {
	// Op is `push`, `create-pr`, `update-pr` or `fetch`, a read of the forge's pull requests
	// (github.go): `github.list-prs` and `github.read-pr`, a tracker's act (issues.go): `git-issue.pull`,
	// `git-issue.push`, and `github-issue.*` and `linear-issue.*` for create, comment and close,
	// Slack's (slack.go): `slack.post`, `.reply`, `.read` and `.react`, or a verb of the verse system
	// (verse.go): `verse.ask-create`, `verse.ask-get`, `verse.ask-list`, `verse.data-get`,
	// `verse.data-put` and `verse.data-rm`.
	Op string `tony:"field=op"`
	// Repo is which repository this is about, because ONE PUSH ACTION IS ONE REPOSITORY. That is
	// what stops a mixed outcome from being a state anybody has to model: each action has its
	// own gate, its own result and its own place in the record.
	Repo string `tony:"field=repo,omitzero"`
	// Branch is where a push goes, or the head a proposal is from, as the task named it. The
	// runner decides it by the launch's autonomy — the task decided it too, and the task is the
	// side that must not be trusted about what it may touch. A `fetch` names none.
	Branch string `tony:"field=branch,omitzero"`
	// Decision is the id of the decision that let this act through in the task's process
	// (ToolUse.Decision, `<task>~d<n>`), so the runner matches the act to the answer it relayed
	// for that decision by id, and only then by the call's fields. Empty where no gate decided it.
	Decision string `tony:"field=decision,omitzero"`
	// Delete asks to remove Branch rather than push to it: a different act, decided apart from a
	// push (git.push, delete=true). Force asks to rewrite what the branch holds (git.push,
	// force=true); without it a push fast-forwards, except to a branch this run created, which
	// is its own to rewrite.
	Delete bool `tony:"field=delete,omitzero"`
	Force  bool `tony:"field=force,omitzero"`
	// A pull request: into Base, with Title and Body. A verse system write's document (a
	// data-put's api.PutEntity, in tony) is Body too.
	Base  string `tony:"field=base,omitzero"`
	Title string `tony:"field=title,omitzero"`
	Body  string `tony:"field=body,omitzero"`
	// PR is the pull request a read names, by its number at the forge.
	PR string `tony:"field=pr,omitzero"`
	// A tracker's act: Issue is the issue a comment or a close is about (a number at the forge,
	// an identifier like `ENG-42` at Linear), Team the Linear team a filing goes to, by key.
	Issue string `tony:"field=issue,omitzero"`
	Team  string `tony:"field=team,omitzero"`
	// Slack's acts: the Channel by name, the Thread a reply or a read is in, the Message a
	// reaction is on and its Emoji, both by ts, and a read's Since (a ts) and Limit. A post's
	// and a reply's text is Body.
	Channel string `tony:"field=channel,omitzero"`
	Thread  string `tony:"field=thread,omitzero"`
	Message string `tony:"field=message,omitzero"`
	Emoji   string `tony:"field=emoji,omitzero"`
	Since   string `tony:"field=since,omitzero"`
	Limit   int    `tony:"field=limit,omitzero"`
	// Ask is a raise's ask, as a tony document in the shape POST /asks reads (api.Ask), or the
	// ask a read names, by its name or its id.
	Ask string `tony:"field=ask,omitzero"`
	// Query is a verse system read's or removal's query, URL-encoded, as its route takes it
	// (api.StateQuery, api.RemoveQuery): the route is the op's, and only the query is the task's.
	Query string `tony:"field=query,omitzero"`
}

type Reply struct {
	OK bool `tony:"field=ok"`
	// Out is what happened, in the words the task reads and its events record: where the
	// work went, which proposal it is, that there was nothing to push.
	Out string `tony:"field=out,omitzero"`
	// Refused is the answer when the act was declined — by the allowance, or by the remote about
	// its own history — as against failing. The task re-plans on it rather than retrying it.
	Refused string `tony:"field=refused,omitzero"`
	// Updated answers a `fetch`: the checkout's `mirror/*` refs and tags that moved, each as
	// `mirror/<branch> <was>..<now>` or `tags/<tag> <was>..<now>`. Empty means upstream had
	// nothing the checkout lacked.
	Updated []string `tony:"field=updated,omitzero"`
	// Exists and Own answer a `state` (OpState): the branch is at the forge, and its tip there
	// is a commit this task made.
	Exists bool   `tony:"field=exists,omitzero"`
	Own    bool   `tony:"field=own,omitzero"`
	Error  string `tony:"field=error,omitzero"`
}

// ── framing ─────────────────────────────────────────────────────────────────────────────────

// maxFrame is the most a frame carries either way: a request, or the answer to one.
const maxFrame = 1 << 20

// frameTooLarge is a frame over maxFrame, read and dropped: THE STREAM STAYS IN STEP, so the
// channel carries the next request and the next answer as if this one had not been sent. Left
// in the stream, its bytes would be read as the next frame's length, and every act after it on
// the channel would read garbage for the rest of the run.
type frameTooLarge struct{ n int }

func (e frameTooLarge) Error() string {
	return fmt.Sprintf("%d bytes, more than the %d a frame of the task's channel carries", e.n, maxFrame)
}

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
	if n < 0 {
		return nil, fmt.Errorf("frame of %d bytes", n)
	}
	if n > maxFrame {
		if _, err := io.CopyN(io.Discard, br, int64(n)); err != nil {
			return nil, err
		}
		return nil, frameTooLarge{n}
	}
	body := make([]byte, n)
	if _, err := io.ReadFull(br, body); err != nil {
		return nil, err
	}
	return body, nil
}

// SocketPair returns two ends of one connected stream: the parent keeps the first, the task
// process inherits the second.
//
// A pair rather than a listener, because the direction is the opposite of the control plane's.
// There, the parent dials and the child accepts; here the child asks and the parent answers, and
// what the child needs is something already connected rather than something to find.
func SocketPair() (net.Conn, *os.File, error) {
	// Both ends close-on-exec, under the lock the runtime takes while it forks: a process the
	// runner starts gets the task's end only as the descriptor it is handed (cmd.ExtraFiles),
	// never at the number it was made at, where the task's commands would inherit it too and
	// could ask for a push without passing its gates.
	syscall.ForkLock.RLock()
	fds, err := syscall.Socketpair(syscall.AF_UNIX, syscall.SOCK_STREAM, 0)
	if err == nil {
		syscall.CloseOnExec(fds[0])
		syscall.CloseOnExec(fds[1])
	}
	syscall.ForkLock.RUnlock()
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
