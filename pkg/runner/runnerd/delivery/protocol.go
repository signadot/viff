package delivery

import (
	"bufio"
	"fmt"
	"io"
	"net"
	"os"
	"slices"
	"strconv"
	"strings"
	"syscall"
)

// Request / Reply are the whole protocol, and each op is an act the runner performs for the
// task (ops): a `push` WRITES the world, a `fetch` READS it, and the rest are named for the step
// each is, `slack.post`, `github-issue.create`, `verse.data-put`. Nothing crosses but requests
// and answers: the credential stays in the runner, used where it lives. Length-prefixed frames
// rather than newline-delimited ones because tony is a multi-line format: framing a document by
// newlines would work until the first document that had one.
//
// A REQUEST IS AN OP, WHAT IT IS ASKED WITH, AND A BODY. What it is asked with is by name
// (Fields), and which names an op reads, and how, is the op's to say (op.takes, op.flags,
// op.numbers), so a system the channel
// comes to carry adds its ops to the table and nothing here. A name an op does not read is
// refused rather than dropped, and so is a body the op does not read: what the task sent and the
// runner ignored is something the task believes was done.
type Request struct {
	// ID names the request among those the task has sent and not yet heard about, and the
	// answer to it carries the same (Reply.ID). The task's end gives it (Client.Ask). Requests
	// are answered as each is done, not as they were sent: an op performed apart from the
	// others (op.apart) does not wait for them.
	ID int `tony:"field=id,omitzero"`
	// Op is the op's word in the table (ops).
	Op string `tony:"field=op"`
	// Decision is the id of the decision that let this act through in the task's process
	// (ToolUse.Decision, `<task>~d<n>`), so the runner matches the act to the answer it relayed
	// for that decision by id, and only then by the call's fields. Empty where no gate decided it.
	Decision string `tony:"field=decision,omitzero"`
	// Fields is what the op is asked with, by name, as text: the repository and the branch of a
	// push (ONE PUSH ACTION IS ONE REPOSITORY, which is what stops a mixed outcome from being a
	// state anybody has to model), the channel and the thread of a Slack message, the issue a
	// comment is on. The task names them, and the runner decides the act on what they say and
	// on what it knows itself: the task decided it too, and the task is the side that must not
	// be trusted about what it may touch.
	Fields map[string]string `tony:"field=fields,omitzero"`
	// Body is the act's document, where it has one: a pull request's or an issue's body, a
	// message's text, what a verse verb writes or installs, in tony.
	Body string `tony:"field=body,omitzero"`

	// op is the op performing the request, on the runner's side: what its reading of a name is
	// held to (reading). nil on the task's side, which reads as it likes what it wrote itself.
	op *op
}

// Field is what the request names name with, as text, empty where it does not.
func (r Request) Field(name string) string {
	r.reading(name, "text", func(o *op) []string { return o.takes })
	return r.Fields[name]
}

// Flag is whether the request says name. What is neither `true` nor `false` does not reach an
// op (misread).
func (r Request) Flag(name string) bool {
	r.reading(name, "a flag", func(o *op) []string { return o.flags })
	return r.Fields[name] == "true"
}

// Int is the number the request gives name, zero where it gives none. One that does not read
// does not reach an op (misread).
func (r Request) Int(name string) int {
	r.reading(name, "a number", func(o *op) []string { return o.numbers })
	n, _ := strconv.Atoi(r.Fields[name])
	return n
}

// reading holds an op to its entry in the table: it reads a name as what the entry says it
// reads it as, which is what misread checked the request against. Another reading is the op's
// bug, and stops it (misreading, answered in Server.perform). A name the entry does not list
// at all reads as nothing, which is what it is: several ops share one func, and no request
// that gives the name reaches an op that does not list it.
func (r Request) reading(name, as string, declared func(*op) []string) {
	if r.op != nil && slices.Contains(r.op.reads(), name) && !slices.Contains(declared(r.op), name) {
		panic(misreading{name, as})
	}
}

type Reply struct {
	// ID is the request this answers (Request.ID); none where the runner read no request in
	// what it was sent, a frame too large or one that does not decode.
	ID int  `tony:"field=id,omitzero"`
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
