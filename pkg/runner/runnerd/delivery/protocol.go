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
	"sync"
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
// runner ignored is something the task believes was done. The one exception is a name given
// nothing (`""`), which asks for nothing and is let by (misread).
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
	if r.op == nil || slices.Contains(declared(r.op), name) {
		return
	}
	for _, kind := range [][]string{r.op.takes, r.op.flags, r.op.numbers} {
		if slices.Contains(kind, name) {
			panic(misreading{name, as})
		}
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
	// Streams is how many descriptors ride this frame: a frame of an answer ahead of the final
	// one, which carries the task's ends of what an act streams (a command's output, under
	// `secret.exec`). The descriptors are sent with the frame's bytes (SCM_RIGHTS, in one
	// WriteMsgUnix) and read with them (ReadMsgUnix), so a frame that says it carries some has
	// them by the time it decodes. Only a unix socket carries one; the final frame says none.
	Streams int `tony:"field=streams,omitzero"`
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

// writeFrameWith writes one frame with descriptors riding its bytes, in one message: the length
// line and the body together, so the rights are attached to the frame they are for and to no
// other. The files are the sender's still, to close once sent.
func writeFrameWith(uc *net.UnixConn, body []byte, files []*os.File) error {
	fds := make([]int, len(files))
	for i, f := range files {
		// Not f.Fd(), which puts the description, shared with the end sent, into blocking
		// mode: the receiver's reads would then not be interruptible by its Close.
		sc, err := f.SyscallConn()
		if err != nil {
			return err
		}
		if err := sc.Control(func(fd uintptr) { fds[i] = int(fd) }); err != nil {
			return err
		}
	}
	frame := append([]byte(strconv.Itoa(len(body))+"\n"), body...)
	n, _, err := uc.WriteMsgUnix(frame, syscall.UnixRights(fds...), nil)
	if err != nil {
		return err
	}
	// The rights ride the first byte, which went; what a short write left of the frame is
	// plain bytes, written as any frame is (Write loops until all of it has gone).
	if n < len(frame) {
		_, err = uc.Write(frame[n:])
	}
	return err
}

// maxRights is the most descriptors one frame carries.
const maxRights = 8

// rights reads a unix connection message by message, keeping the descriptors that ride the
// bytes: A DESCRIPTOR RIDES THE BYTES IT WAS SENT WITH, and a plain read drops it. The reader
// over the task's end of the channel is one of these, under the bufio.Reader the frames are read
// through. What the kernel promises, on Linux and on macOS: the rights of a message come with
// the read that consumes its first byte, never a later one, and in the order sent (a read may
// take earlier plain bytes before them, on Linux, or later plain bytes after them, on macOS, and
// neither matters). So by the time a frame decodes the descriptors sent with it are queued, and
// the frame takes its own (take); a batch that a frame read whole after its arrival has not
// taken is nobody's (unclaimed).
type rights struct {
	uc  *net.UnixConn
	oob []byte
	// read is the bytes read so far, where each batch of descriptors arrived (batch.at: the
	// bytes read once the read that brought it was done), as hear places a frame against it.
	read int64

	mu      sync.Mutex
	batches []batch
}

// batch is the descriptors one read brought, and where in the bytes read it ended.
type batch struct {
	fds []int
	at  int64
}

func newRights(uc *net.UnixConn) *rights {
	return &rights{uc: uc, oob: make([]byte, syscall.CmsgSpace(4*maxRights))}
}

func (r *rights) Read(p []byte) (int, error) {
	n, oobn, flags, _, err := r.uc.ReadMsgUnix(p, r.oob)
	if n < 0 { // what the system call answered with its error, and no count
		n = 0
	}
	r.read += int64(n)
	if oobn > 0 {
		msgs, perr := syscall.ParseSocketControlMessage(r.oob[:oobn])
		if perr != nil {
			return n, fmt.Errorf("the descriptors on the channel do not read: %w", perr)
		}
		var fds []int
		for _, m := range msgs {
			got, ferr := syscall.ParseUnixRights(&m)
			if ferr != nil {
				return n, fmt.Errorf("the descriptors on the channel do not read: %w", ferr)
			}
			fds = append(fds, got...) // close-on-exec already, as net reads them
		}
		r.mu.Lock()
		r.batches = append(r.batches, batch{fds: fds, at: r.read})
		r.mu.Unlock()
	}
	if flags&syscall.MSG_CTRUNC != 0 {
		return n, fmt.Errorf("a frame of the channel carried more descriptors than the %d it may", maxRights)
	}
	return n, err
}

// consumed is how far into the bytes read the frames decoded so far reach: where the next
// frame starts. Called by the one goroutine that reads br.
func (r *rights) consumed(br *bufio.Reader) int64 { return r.read - int64(br.Buffered()) }

// take is the next n descriptors read, as files: the oldest batch's, which is the frame's own,
// since batches come in the order of the frames they ride. An error is the channel's end
// (hear): there were not that many, or one could not be made a file the task can use.
func (r *rights) take(n int) ([]*os.File, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.batches) == 0 {
		return nil, fmt.Errorf("a frame says it carries %d descriptors, and the channel read none with it", n)
	}
	if got := len(r.batches[0].fds); got != n {
		return nil, fmt.Errorf("a frame says it carries %d descriptors, and the channel read %d with it", n, got)
	}
	batch := r.batches[0]
	r.batches = r.batches[1:]
	files := make([]*os.File, 0, n)
	for i, fd := range batch.fds {
		// Non-blocking, so the file is the runtime's to poll: a read on it is interrupted by
		// its Close, which then closes the descriptor at once. Blocking, Close would wait on
		// the read, and closing an end to let go of a stream would let go of nothing while
		// the far side was quiet. One that cannot be is no use, and the rest go with it.
		if err := syscall.SetNonblock(fd, true); err != nil {
			closeAll(files)
			for _, fd := range batch.fds[i:] {
				_ = syscall.Close(fd)
			}
			return nil, fmt.Errorf("descriptor %d of %d on a frame could not be made non-blocking: %w", i+1, n, err)
		}
		files = append(files, os.NewFile(uintptr(fd), "stream"))
	}
	return files, nil
}

// unclaimed is whether a batch came with bytes read wholly before a frame starting at start,
// and no frame took it: the frame it rode took none, which is nobody's to have.
func (r *rights) unclaimed(start int64) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.batches) > 0 && r.batches[0].at <= start
}

// drop closes every descriptor read and not taken.
func (r *rights) drop() {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, b := range r.batches {
		for _, fd := range b.fds {
			syscall.Close(fd)
		}
	}
	r.batches = nil
}

func closeAll(files []*os.File) {
	for _, f := range files {
		f.Close()
	}
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
