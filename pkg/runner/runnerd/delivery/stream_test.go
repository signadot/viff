package delivery

import (
	"bufio"
	"context"
	"io"
	"net"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/signadot/verse/pkg/codec"
	"github.com/signadot/verse/pkg/runner/runnerd/workspace"
)

// A STREAM FRAME CARRIES DESCRIPTORS TO THE ASKER, ahead of the answer: an act that streams
// sends the task's ends from inside, on the socket pair the channel is, and the client hands
// them to the one who asked, who reads what the act writes, and then hears the answer by the
// same id. A client that asked for no descriptors has them closed. Over a connection that is no
// unix socket (a test's net.Pipe) the act cannot send one, and the answer says so.
func TestAStreamFrameCarriesDescriptorsToTheAsker(t *testing.T) {
	table := map[string]op{
		"a-test.stream": (op{unasked: "a test's", takes: []string{"name"}, deadline: time.Minute,
			ask: func(_ *Server, _ context.Context, tk Task, req Request) (asked, Reply) {
				outR, outW, err := os.Pipe()
				if err != nil {
					t.Fatal(err)
				}
				errR, errW, err := os.Pipe()
				if err != nil {
					t.Fatal(err)
				}
				defer outR.Close()
				defer errR.Close()
				if err := tk.stream(req.ID, outR, errR); err != nil {
					outW.Close()
					errW.Close()
					return before(Reply{Error: "no stream: " + err.Error()})
				}
				go func() {
					defer outW.Close()
					defer errW.Close()
					io.WriteString(outW, "out of "+req.Field("name"))
					io.WriteString(errW, "err of "+req.Field("name"))
				}()
				return before(Reply{OK: true, Out: "streamed"})
			}}).performedApart(),
	}
	s := &Server{Log: io.Discard, table: table}

	// Over the pair the runner makes, as the task's process has it (Attach).
	near, far, err := SocketPair()
	if err != nil {
		t.Fatal(err)
	}
	served := make(chan struct{})
	go func() { s.Serve(Task{ID: "s1", Workspace: &workspace.Workspace{}}, near); close(served) }()
	conn, err := net.FileConn(far)
	far.Close()
	if err != nil {
		t.Fatal(err)
	}
	cl := NewClient(conn)
	var files []*os.File
	rep, err := cl.AskStreaming(Request{Op: "a-test.stream", Fields: map[string]string{"name": "one"}}, func(f []*os.File) { files = f })
	if err != nil || !rep.OK || rep.Out != "streamed" || rep.Streams != 0 {
		t.Fatalf("the answer: %+v, %v", rep, err)
	}
	if len(files) != 2 {
		t.Fatalf("the stream brought %d descriptors, want 2", len(files))
	}
	out, _ := io.ReadAll(files[0])
	errs, _ := io.ReadAll(files[1])
	closeAll(files)
	if string(out) != "out of one" || string(errs) != "err of one" {
		t.Errorf("read %q and %q through the descriptors", out, errs)
	}
	// Asked plainly, the descriptors are closed and the answer comes as any other.
	if rep, err := cl.Ask(Request{Op: "a-test.stream", Fields: map[string]string{"name": "two"}}); err != nil || rep.Out != "streamed" {
		t.Errorf("asked plainly: %+v, %v", rep, err)
	}
	// Two asked at once, each hears its own.
	type got struct {
		name string
		out  string
		err  error
	}
	heard := make(chan got, 2)
	for _, name := range []string{"three", "four"} {
		go func() {
			var out string
			_, err := cl.AskStreaming(Request{Op: "a-test.stream", Fields: map[string]string{"name": name}}, func(f []*os.File) {
				b, _ := io.ReadAll(f[0])
				out = string(b)
				closeAll(f)
			})
			heard <- got{name, out, err}
		}()
	}
	for range 2 {
		g := <-heard
		if g.err != nil || g.out != "out of "+g.name {
			t.Errorf("%s heard %q, %v", g.name, g.out, g.err)
		}
	}
	cl.Close()
	<-served

	// Over a pipe, the act learns it cannot stream.
	pnear, pfar := net.Pipe()
	t.Cleanup(func() { pnear.Close() })
	pnear.SetDeadline(time.Now().Add(20 * time.Second))
	go s.Serve(Task{ID: "s2", Workspace: &workspace.Workspace{}}, pfar)
	pcl := NewClient(pnear)
	if rep, err := pcl.Ask(Request{Op: "a-test.stream"}); err != nil || rep.OK || !strings.Contains(rep.Error, "no stream: the channel cannot carry a descriptor") {
		t.Errorf("over a pipe: %+v, %v", rep, err)
	}
}

// AN OP APART IS HANDED THE TASK'S ROOT AND ITS NETWORK, AND NO WORKSPACE: what a command run
// for the task needs, and nothing an op in turn writes. The channel carries the network the
// sandbox built the task (SetNetwork) to the server, with the workspace's root.
func TestAnOpApartHasTheRootAndTheNetworkAndNoWorkspace(t *testing.T) {
	type seen struct {
		root string
		net  Network
		ws   *workspace.Workspace
	}
	var apart, inTurn seen
	table := map[string]op{
		"a-test.apart": (op{unasked: "a test's", deadline: time.Minute,
			ask: func(_ *Server, _ context.Context, tk Task, _ Request) (asked, Reply) {
				apart = seen{tk.Root, tk.Network, tk.Workspace}
				return before(Reply{OK: true})
			}}).performedApart(),
		"a-test.in-turn": {unasked: "a test's", deadline: time.Minute,
			ask: func(_ *Server, _ context.Context, tk Task, _ Request) (asked, Reply) {
				inTurn = seen{tk.Root, tk.Network, tk.Workspace}
				return before(Reply{OK: true})
			}},
	}
	ws := &workspace.Workspace{Root: "/w/t1"}
	ch, err := Open("t1", ws, nil, nil, Services{}, nil, nil, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	ch.srv.table = table
	ch.SetNetwork(Network{Mode: "allow", Egress: "/ctl/e1.sock"})
	served := make(chan struct{})
	go func() { ch.Serve(); close(served) }()
	conn, err := net.FileConn(ch.Child())
	ch.Child().Close()
	if err != nil {
		t.Fatal(err)
	}
	cl := NewClient(conn)
	for _, op := range []string{"a-test.apart", "a-test.in-turn"} {
		if rep, err := cl.Ask(Request{Op: op}); err != nil || !rep.OK {
			t.Fatalf("%s: %+v, %v", op, rep, err)
		}
	}
	cl.Close()
	<-served
	want := Network{Mode: "allow", Egress: "/ctl/e1.sock"}
	if apart.root != "/w/t1" || apart.net != want || apart.ws != nil {
		t.Errorf("the op apart was handed %+v, want the root, the network and no workspace", apart)
	}
	if inTurn.root != "/w/t1" || inTurn.net != want || inTurn.ws != ws {
		t.Errorf("the op in turn was handed %+v, want the root, the network and the workspace", inTurn)
	}
}

// The op builders say what they say.
func TestAnOpIsBuiltApartAndWithADeadline(t *testing.T) {
	o := named("x.y", nil, "a").performedApart().withDeadline(time.Hour)
	if !o.apart || o.deadline != time.Hour || o.takes[0] != "a" {
		t.Errorf("built %+v", o)
	}
	if named("x.y", nil).apart {
		t.Error("an op is in turn unless said apart")
	}
}

// A FRAME CARRIES WHAT IT SAYS, AND ONE STREAM FRAME IS ALL AN ANSWER HAS. A second stream frame
// for one request is refused where the act sends it, and a client that reads one anyway ends
// the channel, as it does for descriptors that came with a frame that takes none: they are some
// other frame's, or nobody's. Whatever is queued when the channel ends is closed.
func TestAFrameCarriesWhatItSaysAndOneStreamFrameIsAll(t *testing.T) {
	// The act's side: a second stream frame for one request is refused.
	twice := map[string]op{
		"a-test.twice": (op{unasked: "a test's", deadline: time.Minute,
			ask: func(_ *Server, _ context.Context, tk Task, req Request) (asked, Reply) {
				r, w, err := os.Pipe()
				if err != nil {
					t.Fatal(err)
				}
				defer r.Close()
				defer w.Close()
				if err := tk.stream(req.ID, r); err != nil {
					return before(Reply{Error: "first: " + err.Error()})
				}
				if err := tk.stream(req.ID, r); err == nil {
					return before(Reply{Error: "a second stream frame was sent"})
				} else if !strings.Contains(err.Error(), "has its stream frame already") {
					return before(Reply{Error: "second: " + err.Error()})
				}
				return before(Reply{OK: true})
			}}).performedApart(),
	}
	s := &Server{Log: io.Discard, table: twice}
	near, far, err := SocketPair()
	if err != nil {
		t.Fatal(err)
	}
	go s.Serve(Task{ID: "s3", Workspace: &workspace.Workspace{}}, near)
	conn, err := net.FileConn(far)
	far.Close()
	if err != nil {
		t.Fatal(err)
	}
	cl := NewClient(conn)
	if rep, err := cl.Ask(Request{Op: "a-test.twice"}); err != nil || !rep.OK {
		t.Errorf("a second stream frame: %+v, %v", rep, err)
	}
	cl.Close()

	// The task's side, fed frames by hand: a stand-in for a runner with a bug, which still reads
	// each request before it answers, as a runner does (asked). An answer fed before the request
	// has gone ends the channel under the asker's feet, and what it reports is its own write
	// failing, not what came.
	feed := func(t *testing.T) (*net.UnixConn, func(), *Client) {
		t.Helper()
		near, far, err := SocketPair()
		if err != nil {
			t.Fatal(err)
		}
		conn, err := net.FileConn(far)
		far.Close()
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { near.Close(); conn.Close() })
		br := bufio.NewReader(near)
		asked := func() {
			t.Helper()
			if _, err := readFrame(br); err != nil {
				t.Fatal("the request: ", err)
			}
		}
		return near.(*net.UnixConn), asked, NewClient(conn)
	}
	frame := func(rep Reply) []byte {
		t.Helper()
		body, err := codec.Marshal(rep, codec.Tony)
		if err != nil {
			t.Fatal(err)
		}
		return body
	}
	pipe := func() (*os.File, *os.File) {
		t.Helper()
		r, w, err := os.Pipe()
		if err != nil {
			t.Fatal(err)
		}
		return r, w
	}

	// Two stream frames for request 1: the second does not read, and ends the channel.
	runner, asked, cl := feed(t)
	r, w := pipe()
	defer w.Close()
	answer := make(chan error, 1)
	go func() {
		_, err := cl.AskStreaming(Request{Op: "x"}, func(f []*os.File) { closeAll(f) })
		answer <- err
	}()
	asked()
	for range 2 {
		if err := writeFrameWith(runner, frame(Reply{ID: 1, Streams: 1}), []*os.File{r}); err != nil {
			t.Fatal(err)
		}
	}
	r.Close()
	if err := <-answer; err == nil || !strings.Contains(err.Error(), "a second stream frame for request 1") {
		t.Errorf("two stream frames: %v", err)
	}

	// Descriptors on a frame that says it carries none: the frame after it does not read.
	// Nobody took them, and the channel's end closes them: the pipe's writer finds no reader.
	runner, asked, cl = feed(t)
	r, w = pipe()
	go func() {
		_, err := cl.Ask(Request{Op: "x"})
		answer <- err
	}()
	asked()
	if err := writeFrameWith(runner, frame(Reply{ID: 1, OK: true}), []*os.File{r}); err != nil {
		t.Fatal(err)
	}
	r.Close()
	if err := <-answer; err != nil {
		t.Errorf("the answer that carried descriptors it did not say: %v", err)
	}
	go func() {
		_, err := cl.Ask(Request{Op: "x"})
		answer <- err
	}()
	asked()
	if err := writeFrame(runner, frame(Reply{ID: 2, OK: true})); err != nil {
		t.Fatal(err)
	}
	if err := <-answer; err == nil || !strings.Contains(err.Error(), "with a frame that takes none") {
		t.Errorf("the frame after: %v", err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		_, err := w.Write([]byte("x"))
		if err != nil {
			break // the reader is closed: dropped with the channel
		}
		if time.Now().After(deadline) {
			t.Error("the descriptor nobody took is still open")
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	w.Close()

	// A stream frame that says more than came does not read either, and the error says the
	// count that did.
	runner, asked, cl = feed(t)
	r, w = pipe()
	defer w.Close()
	go func() {
		_, err := cl.Ask(Request{Op: "x"})
		answer <- err
	}()
	asked()
	if err := writeFrameWith(runner, frame(Reply{ID: 1, Streams: 2}), []*os.File{r}); err != nil {
		t.Fatal(err)
	}
	r.Close()
	if err := <-answer; err == nil || !strings.Contains(err.Error(), "the channel read 1 with it") {
		t.Errorf("a frame saying more than came: %v", err)
	}

	// One that says fewer than came: the count read is named either way, not called "fewer".
	runner, asked, cl = feed(t)
	r, w = pipe()
	defer w.Close()
	r2, w2 := pipe()
	defer w2.Close()
	go func() {
		_, err := cl.Ask(Request{Op: "x"})
		answer <- err
	}()
	asked()
	if err := writeFrameWith(runner, frame(Reply{ID: 1, Streams: 1}), []*os.File{r, r2}); err != nil {
		t.Fatal(err)
	}
	r.Close()
	r2.Close()
	if err := <-answer; err == nil || !strings.Contains(err.Error(), "the channel read 2 with it") {
		t.Errorf("a frame saying fewer than came: %v", err)
	}

	// And one that says it carries some where none came at all.
	runner, asked, cl = feed(t)
	go func() {
		_, err := cl.Ask(Request{Op: "x"})
		answer <- err
	}()
	asked()
	if err := writeFrame(runner, frame(Reply{ID: 1, Streams: 1})); err != nil {
		t.Fatal(err)
	}
	if err := <-answer; err == nil || !strings.Contains(err.Error(), "the channel read none with it") {
		t.Errorf("a frame saying descriptors where none came: %v", err)
	}

	// The descriptor handed over reads as the sender's does, and its reader can be closed
	// from under a read: it was not put in blocking mode by being sent.
	runner, asked, cl = feed(t)
	r, w = pipe()
	defer w.Close()
	var got []*os.File
	go func() {
		_, err := cl.AskStreaming(Request{Op: "x"}, func(f []*os.File) { got = f })
		answer <- err
	}()
	asked()
	if err := writeFrameWith(runner, frame(Reply{ID: 1, Streams: 1}), []*os.File{r}); err != nil {
		t.Fatal(err)
	}
	r.Close()
	if err := writeFrame(runner, frame(Reply{ID: 1, OK: true})); err != nil {
		t.Fatal(err)
	}
	if err := <-answer; err != nil || len(got) != 1 {
		t.Fatalf("the answer: %v, %d files", err, len(got))
	}
	read := make(chan error, 1)
	go func() {
		_, err := got[0].Read(make([]byte, 1))
		read <- err
	}()
	time.Sleep(50 * time.Millisecond)
	got[0].Close()
	select {
	case err := <-read:
		if err == nil {
			t.Error("a read on a closed descriptor read something")
		}
	case <-time.After(5 * time.Second):
		t.Error("closing the descriptor did not end the read on it: it is in blocking mode")
	}
}

// AN ANSWER LETS ITS REQUEST'S STREAM MARK GO: the marks do not pile up for the life of a task
// that loops a streaming op, and an id the task uses again — the answer to its first use heard —
// streams again.
func TestAnAnsweredIdStreamsAgain(t *testing.T) {
	table := map[string]op{
		"a-test.s": (op{unasked: "a test's", deadline: time.Minute,
			ask: func(_ *Server, _ context.Context, tk Task, req Request) (asked, Reply) {
				r, w, err := os.Pipe()
				if err != nil {
					t.Fatal(err)
				}
				defer r.Close()
				defer w.Close()
				if err := tk.stream(req.ID, r); err != nil {
					return before(Reply{Error: err.Error()})
				}
				return before(Reply{OK: true})
			}}).performedApart(),
	}
	s := &Server{Log: io.Discard, table: table}
	near, far, err := SocketPair()
	if err != nil {
		t.Fatal(err)
	}
	go s.Serve(Task{ID: "s9", Workspace: &workspace.Workspace{}}, near)
	conn, err := net.FileConn(far)
	far.Close()
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	// By hand rather than through a Client, which numbers its own requests: both carry id 7.
	uc := conn.(*net.UnixConn)
	rr := newRights(uc)
	br := bufio.NewReader(rr)
	for range 2 {
		body, err := codec.Marshal(Request{ID: 7, Op: "a-test.s"}, codec.Tony)
		if err != nil {
			t.Fatal(err)
		}
		if err := writeFrame(uc, body); err != nil {
			t.Fatal(err)
		}
		for {
			raw, err := readFrame(br)
			if err != nil {
				t.Fatal(err)
			}
			var rep Reply
			if err := codec.Unmarshal(raw, &rep, codec.Tony); err != nil {
				t.Fatal(err)
			}
			if rep.Streams > 0 { // the stream frame: its descriptors are taken and closed
				files, err := rr.take(rep.Streams)
				if err != nil {
					t.Fatal(err)
				}
				closeAll(files)
				continue
			}
			if !rep.OK || rep.Error != "" {
				t.Fatalf("the answer under the reused id: %+v", rep)
			}
			break
		}
	}
}
