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

// Request / Reply are the whole protocol, and it has three ops, each an act the runner performs
// for the task. `push` is for WRITING the world: the task names a repository and a branch, the
// runner checks the branch against what the spawn permits, stages the committed work in its
// mirror and pushes it from there. `pull` opens or updates a pull request from such a branch.
// `fetch` is for READING it: the runner brings its mirror up to upstream and the checkout's
// `mirror/*` refs up to the mirror. Nothing crosses but requests and answers: the credential
// stays in the runner, used where it lives. Length-prefixed frames rather than newline-delimited
// ones because tony is a multi-line format: framing a document by newlines would work until the
// first document that had one.
type Request struct {
	// Op is `push`, `pull` or `fetch`.
	Op string `tony:"field=op"`
	// Repo is which repository this is about, because ONE PUSH ACTION IS ONE REPOSITORY. That is
	// what stops a mixed outcome from being a state anybody has to model: each action has its
	// own gate, its own result and its own place in the record.
	Repo string `tony:"field=repo,omitzero"`
	// Branch is where a push goes, or the head a pull request is from, as the task named it. The
	// runner checks it against the repository's allowance (workspace.Repo.Permit) — the task
	// checked it too, and the task is the side that must not be trusted about what it may touch.
	// A `fetch` names none.
	Branch string `tony:"field=branch,omitzero"`
	// Delete asks to remove Branch rather than push to it: a different act, permitted only for a
	// branch a run of this charter created, and gated apart from a push.
	Delete bool `tony:"field=delete,omitzero"`
	// A pull request: into Base, with Title and Body; Update changes the open one rather than
	// opening one.
	Base   string `tony:"field=base,omitzero"`
	Title  string `tony:"field=title,omitzero"`
	Body   string `tony:"field=body,omitzero"`
	Update bool   `tony:"field=update,omitzero"`
}

type Reply struct {
	OK bool `tony:"field=ok"`
	// Out is what happened, in the words the task reads and the read plane narrates: where the
	// work went, which pull request it is, that there was nothing to push.
	Out string `tony:"field=out,omitzero"`
	// Refused is the answer when the act was declined — by the allowance, or by the remote about
	// its own history — as against failing. The task re-plans on it rather than retrying it.
	Refused string `tony:"field=refused,omitzero"`
	// Updated answers a `fetch`: the checkout's `mirror/*` refs that moved, each as
	// `mirror/<branch> <was>..<now>`. Empty means upstream had nothing the checkout lacked.
	Updated []string `tony:"field=updated,omitzero"`
	Error   string   `tony:"field=error,omitzero"`
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

// SocketPair returns two ends of one connected stream: the parent keeps the first, the task
// process inherits the second.
//
// A pair rather than a listener, because the direction is the opposite of the control plane's.
// There, the parent dials and the child accepts; here the child asks and the parent answers, and
// what the child needs is something already connected rather than something to find.
func SocketPair() (net.Conn, *os.File, error) {
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
