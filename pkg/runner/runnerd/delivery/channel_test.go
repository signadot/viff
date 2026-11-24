package delivery

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	git "github.com/go-git/go-git/v5"

	"go.viff.dev/viff/pkg/codec"
	"go.viff.dev/viff/pkg/runner"
	"go.viff.dev/viff/pkg/runner/runnerd/workspace"
	"go.viff.dev/viff/pkg/systems"
	"go.viff.dev/viff/pkg/systems/systemstest"
	"go.viff.dev/viff/pkg/task"
)

// pushing is a launch's policy that gives the task git and lets every call of it go.
var pushing = task.Autonomy{{Match: &task.Match{System: "git"}, Then: task.Then{Mode: task.ThenAllow}}, {Then: task.Then{Mode: task.ThenAllow}}}

// gitLaunch is a launch of the viff agent that checks out repo, with a fetch and a push to any
// branch of it, under policy.
func gitLaunch(id string, repo runner.Repo, policy task.Autonomy) *runner.Launch {
	attrs := map[string]string{"repo": repo.Repo}
	if repo.From != "" {
		attrs["from"] = repo.From
	}
	return &runner.Launch{ID: id, Prompt: "p", Agent: &runner.Agent{Name: "viff"}, Autonomy: policy,
		Environment: systems.Environment{Systems: systems.Systems{"git": {{Attributes: attrs,
			Verbs: map[string][]systems.Shape{"fetch": {{}}, "push": {{"ref": systems.Regexp("^refs/heads/")}}}}}}}}
}

// within is a launch whose environment gives every verb of each named system: in a block per repo
// for a system a repo names its blocks by, one block otherwise, under an autonomy that allows
// them all. What a call may do is then the ceiling's, or a test's own autonomy's.
func within(repos []string, names ...string) *runner.Launch {
	env := systems.Environment{Systems: systems.Systems{}}
	for _, name := range names {
		d, _ := systems.Get(name)
		verbs := map[string][]systems.Shape{}
		for _, v := range d.Verbs {
			verbs[v.Name] = []systems.Shape{{}}
		}
		if u, ok := d.Unique(); ok {
			for _, r := range repos {
				env.Systems[name] = append(env.Systems[name], systems.Block{Attributes: map[string]string{u.Name: r}, Verbs: verbs})
			}
			continue
		}
		env.Systems[name] = []systems.Block{{Verbs: verbs}}
	}
	return &runner.Launch{ID: "t", Prompt: "p", Environment: env, Autonomy: task.Autonomy{{Then: task.Then{Mode: task.ThenAllow}}}}
}

// A push the task asked for before its process died is finished before anything decides what
// did not leave. Settle closes the channel and then waits for the request in flight; without
// that, salvage would read a tree an approved push was staging from, hold the work under `held`
// as if nobody had authorized it, and the next sweep would send it twice. (Walter's, from #16.)
func TestSettleWaitsForThePushInFlight(t *testing.T) {
	ctx := context.Background()
	forge := t.TempDir()
	origin := initRepo(t, forge, "acme/svc")
	l := gitLaunch("s1", runner.Repo{Repo: "acme/svc"}, pushing)
	repos, err := workspace.FromLaunch(l.Repos(), forge)
	if err != nil {
		t.Fatal(err)
	}
	m := workspace.NewMirrors(t.TempDir(), workspace.TokenAuth(""))
	ws, err := m.Open(ctx, l.ID, repos, t.TempDir(), workspace.Author{})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(ws.Root, "svc", "work.txt"), []byte("x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := ws.Tree("svc").CommitWork(l, "the work"); err != nil {
		t.Fatal(err)
	}

	// The credential is asked for after the allowance and before staging: holding it there is
	// a push in flight at the moment the process dies.
	asked, release := make(chan struct{}), make(chan struct{})
	cred := func(workspace.Repo) (string, error) { close(asked); <-release; return "", nil }
	ch, err := Open(l.ID, ws, m, cred, Services{}, systemstest.Open(l.Environment), l, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	go ch.Serve()
	far, err := net.FileConn(ch.Child())
	if err != nil {
		t.Fatal(err)
	}
	ch.Child().Close()
	req, _ := codec.Marshal(&Request{Op: "push", Fields: map[string]string{"repo": "svc", "branch": "viff/x"}}, codec.Tony)
	if err := writeFrame(far, req); err != nil {
		t.Fatal(err)
	}
	<-asked

	settled := make(chan bool, 1)
	go func() { settled <- ch.Settle(ctx, l, false) }()
	select {
	case <-settled:
		t.Fatal("Settle returned while a push was in flight")
	case <-time.After(200 * time.Millisecond):
	}
	close(release)
	if kept := <-settled; kept {
		t.Error("the workspace was kept, as if work had not left")
	}

	// The push landed, and nothing is held: the work left, and salvage saw that it had.
	if r, err := git.PlainOpen(origin); err != nil {
		t.Fatal(err)
	} else if _, err := r.Reference("refs/heads/viff/x", true); err != nil {
		t.Fatalf("the push in flight did not land: %v", err)
	}
	staged, err := m.Staged(origin)
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range staged {
		if strings.Contains(s.Ref, "/held/") {
			t.Errorf("delivered work was held as if it had not left: %s", s.Ref)
		}
	}
}

// A FRAME TOO LARGE FOR THE CHANNEL LEAVES IT IN STEP: an answer over the cap is said to be one,
// in a frame that fits; a request over it is refused before it is sent; one sent anyway is
// dropped and answered. The next act on the channel is carried as if none of them had been.
func TestAFrameTooLargeLeavesTheChannelInStep(t *testing.T) {
	big := strings.Repeat("x", maxFrame+1)
	viff := func(_ context.Context, taskID, method, path string, body []byte) (int, string, error) {
		if strings.Contains(path, "at=big") {
			return 200, big, nil
		}
		return 200, "{stage: open}", nil
	}
	s := &Server{Log: io.Discard, Services: Services{Viff: viff}, Ceilings: map[string]systems.Ceiling{"viff": systems.Open()}}
	near, far := net.Pipe()
	t.Cleanup(func() { near.Close() })
	// A channel out of step blocks rather than errs, so the test's end has a deadline: out of
	// step is then a failure, not a hang.
	near.SetDeadline(time.Now().Add(20 * time.Second))
	go s.Serve(Task{ID: "t7", Workspace: &workspace.Workspace{}, Launch: within(nil, "viff")}, far)
	cl := NewClient(near)
	inStep := func(after string) {
		t.Helper()
		if rep, err := cl.Ask(Request{Op: OpViffDataGet, Fields: map[string]string{"query": "at=small"}}); err != nil || rep.Out != "{stage: open}" {
			t.Fatalf("after %s, the next read = %+v, %v", after, rep, err)
		}
	}

	if rep, err := cl.Ask(Request{Op: OpViffDataGet, Fields: map[string]string{"query": "at=big"}}); err != nil || rep.OK || !strings.Contains(rep.Error, "the answer is") {
		t.Errorf("an answer over the cap = %+v, %v, want not OK, and an error saying so", rep, err)
	}
	inStep("an answer over the cap")
	if _, err := cl.Ask(Request{Op: OpViffDataPut, Body: big}); err == nil || !strings.Contains(err.Error(), "send less") {
		t.Errorf("a request over the cap = %v, want it refused before it is sent", err)
	}
	inStep("a request refused before it was sent")

	// One sent anyway is not the client's, so it is sent and its answer read on a channel with
	// no client: the answer names no request, since the runner read none.
	raw, err := codec.Marshal(Request{Op: OpViffDataPut, Body: big}, codec.Tony)
	if err != nil {
		t.Fatal(err)
	}
	near2, far2 := net.Pipe()
	t.Cleanup(func() { near2.Close() })
	near2.SetDeadline(time.Now().Add(20 * time.Second))
	go s.Serve(Task{ID: "t8", Workspace: &workspace.Workspace{}, Launch: within(nil, "viff")}, far2)
	br := bufio.NewReader(near2)
	go func() { _ = writeFrame(near2, raw) }()
	got, err := readFrame(br)
	if err != nil || !strings.Contains(string(got), "the request is") {
		t.Errorf("a request over the cap sent anyway = %q, %v, want it answered as one", got, err)
	}
	small, err := codec.Marshal(Request{ID: 7, Op: OpViffDataGet, Fields: map[string]string{"query": "at=small"}}, codec.Tony)
	if err != nil {
		t.Fatal(err)
	}
	go func() { _ = writeFrame(near2, small) }()
	var rep Reply
	if got, err := readFrame(br); err != nil || codec.Unmarshal(got, &rep, codec.Tony) != nil || rep.ID != 7 || rep.Out != "{stage: open}" {
		t.Errorf("after a request over the cap sent anyway, the next read = %q, %v", got, err)
	}
	// And the client's own channel drops an answer that names no request, and goes on. The
	// frame is written whole before the next is asked: two writers on one end would interleave.
	if err := writeFrame(near, raw); err != nil {
		t.Fatal(err)
	}
	inStep("a request over the cap sent anyway")
}

// AN OP PERFORMED APART DOES NOT WAIT FOR THOSE IN TURN, NOR THEY FOR IT. Requests in turn are
// performed in the order the task asked, each begun when the one ahead is done; one apart is
// answered while a request in turn is still being performed, and its answer reaches the one
// that asked.
func TestAnOpApartDoesNotWaitForThoseInTurn(t *testing.T) {
	began, release := make(chan string, 8), make(chan struct{})
	var order []string
	var mu sync.Mutex
	did := func(what string) { mu.Lock(); order = append(order, what); mu.Unlock() }
	table := map[string]op{
		"a-test.in-turn": {unasked: "a test's", takes: []string{"name"}, deadline: time.Minute,
			ask: func(_ *Server, _ context.Context, _ Task, req Request) (asked, Reply) {
				began <- req.Field("name")
				if req.Field("name") == "first" {
					<-release
				}
				did(req.Field("name"))
				return before(Reply{OK: true, Out: req.Field("name")})
			}},
		"a-test.apart": {unasked: "a test's", apart: true, deadline: time.Minute,
			ask: func(_ *Server, _ context.Context, _ Task, req Request) (asked, Reply) {
				did("apart")
				return before(Reply{OK: true, Out: "apart"})
			}},
	}
	s := &Server{Log: io.Discard, table: table}
	near, far := net.Pipe()
	t.Cleanup(func() { near.Close() })
	near.SetDeadline(time.Now().Add(20 * time.Second))
	go s.Serve(Task{ID: "t9", Workspace: &workspace.Workspace{}}, far)
	cl := NewClient(near)
	ask := func(name string) chan string {
		out := make(chan string, 1)
		go func() {
			rep, err := cl.Ask(Request{Op: "a-test.in-turn", Fields: map[string]string{"name": name}})
			if err != nil {
				out <- "error: " + err.Error()
				return
			}
			out <- rep.Out
		}()
		return out
	}
	// The first is being performed, and held, before the second is asked: the order asked.
	first := ask("first")
	if name := <-began; name != "first" {
		t.Fatalf("%q began first", name)
	}
	second := ask("second")
	if rep, err := cl.Ask(Request{Op: "a-test.apart"}); err != nil || rep.Out != "apart" {
		t.Fatalf("an op apart, while one in turn is held: %+v, %v", rep, err)
	}
	select {
	case name := <-began:
		t.Fatalf("%q began while the first was held", name)
	case <-time.After(50 * time.Millisecond):
	}
	close(release)
	if a, b := <-first, <-second; a != "first" || b != "second" {
		t.Errorf("the requests in turn answered %q and %q", a, b)
	}
	mu.Lock()
	defer mu.Unlock()
	if !slices.Equal(order, []string{"apart", "first", "second"}) {
		t.Errorf("performed in the order %v", order)
	}
}

// WHEN THE TASK'S END GOES, WHAT WAS BEGUN IN TURN IS FINISHED AND NOTHING ELSE IS. The request
// being performed is finished, as Settle needs. The ones waiting their turn behind it are not
// begun: the runner does not push or post for a task that is gone. One performed apart is given
// up. Serve returns after all of that, and whoever asks on the client hears that it ended.
func TestWhenTheTasksEndGoesOnlyWhatWasBegunIsFinished(t *testing.T) {
	began, release := make(chan string, 8), make(chan struct{})
	givenUp := make(chan struct{})
	var performed []string
	var mu sync.Mutex
	table := map[string]op{
		"a-test.in-turn": {unasked: "a test's", takes: []string{"name"}, deadline: time.Minute,
			ask: func(_ *Server, _ context.Context, _ Task, req Request) (asked, Reply) {
				began <- req.Field("name")
				if req.Field("name") == "first" {
					<-release
				}
				mu.Lock()
				performed = append(performed, req.Field("name"))
				mu.Unlock()
				return before(Reply{OK: true})
			}},
		"a-test.apart": {unasked: "a test's", apart: true, deadline: time.Minute,
			ask: func(_ *Server, ctx context.Context, _ Task, _ Request) (asked, Reply) {
				began <- "apart"
				<-ctx.Done()
				close(givenUp)
				return before(Reply{Error: ctx.Err().Error()})
			}},
	}
	var log bytes.Buffer
	s := &Server{Log: &log, table: table}
	near, far := net.Pipe()
	near.SetDeadline(time.Now().Add(20 * time.Second))
	served := make(chan struct{})
	go func() { s.Serve(Task{ID: "t9", Workspace: &workspace.Workspace{}}, far); close(served) }()
	cl := NewClient(near)
	heard := make(chan error, 2)
	ask := func(op, name string) {
		go func() {
			_, err := cl.Ask(Request{Op: op, Fields: map[string]string{"name": name}})
			heard <- err
		}()
	}
	ask("a-test.in-turn", "first")
	if name := <-began; name != "first" {
		t.Fatalf("%q began first", name)
	}
	ask("a-test.apart", "")
	if name := <-began; name != "apart" {
		t.Fatalf("%q began, want the op apart", name)
	}
	// Two wait their turn behind the first. They are written as frames, not asked: on a pipe a
	// frame written is a frame the server has read, and it queues one before it reads the next,
	// so once both writes return both are the server's to begin or give up.
	for i, name := range []string{"second", "third"} {
		raw, err := codec.Marshal(Request{ID: 100 + i, Op: "a-test.in-turn", Fields: map[string]string{"name": name}}, codec.Tony)
		if err != nil {
			t.Fatal(err)
		}
		if err := writeFrame(near, raw); err != nil {
			t.Fatal(err)
		}
	}

	cl.Close()
	select {
	case <-givenUp:
	case <-time.After(10 * time.Second):
		t.Fatal("an op apart was not given up when the task's end went")
	}
	select {
	case <-served:
		t.Fatal("Serve returned while a request in turn was being performed")
	case <-time.After(50 * time.Millisecond):
	}
	close(release)
	select {
	case <-served:
	case <-time.After(10 * time.Second):
		t.Fatal("Serve did not return once what it had begun was done")
	}
	mu.Lock()
	if !slices.Equal(performed, []string{"first"}) {
		t.Errorf("performed %v, want the one that was begun and no other", performed)
	}
	mu.Unlock()
	if !strings.Contains(log.String(), "was not begun") {
		t.Errorf("the runner's log does not say what it gave up: %q", log.String())
	}
	for range 2 {
		if err := <-heard; err == nil {
			t.Error("an Ask on a channel that ended heard an answer")
		}
	}
	if _, err := cl.Ask(Request{Op: "a-test.apart"}); err == nil {
		t.Error("an Ask after the channel ended heard an answer")
	}
}

// A REQUEST WITH NONE AHEAD OF IT IS FINISHED THOUGH THE TASK'S END GOES AT ONCE: a task that
// asks for a push and exits has asked for the push.
func TestARequestWithNoneAheadIsFinishedThoughTheEndGoes(t *testing.T) {
	for i := 0; i < 50; i++ {
		done := make(chan struct{})
		table := map[string]op{"a-test.in-turn": {unasked: "a test's", deadline: time.Minute,
			ask: func(*Server, context.Context, Task, Request) (asked, Reply) {
				close(done)
				return before(Reply{OK: true})
			}}}
		s := &Server{Log: io.Discard, table: table}
		near, far := net.Pipe()
		served := make(chan struct{})
		go func() { s.Serve(Task{ID: "t9", Workspace: &workspace.Workspace{}}, far); close(served) }()
		raw, err := codec.Marshal(Request{ID: 1, Op: "a-test.in-turn"}, codec.Tony)
		if err != nil {
			t.Fatal(err)
		}
		if err := writeFrame(near, raw); err != nil {
			t.Fatal(err)
		}
		near.Close()
		<-served
		select {
		case <-done:
		default:
			t.Fatalf("run %d: the request was read and not performed", i)
		}
	}
}

// AN ANSWER THAT DOES NOT READ ENDS THE CHANNEL, and everyone waiting hears that what they asked
// may have been done: it could be the answer to any of them, so none is told it failed, which a
// tool would take as leave to do a write again.
func TestAnAnswerThatDoesNotReadEndsTheChannel(t *testing.T) {
	near, far := net.Pipe()
	t.Cleanup(func() { far.Close() })
	cl := NewClient(near)
	heard := make(chan error, 2)
	for range 2 {
		go func() { _, err := cl.Ask(Request{Op: OpSlackPost}); heard <- err }()
	}
	br := bufio.NewReader(far)
	for range 2 {
		if _, err := readFrame(br); err != nil {
			t.Fatal(err)
		}
	}
	go func() { _ = writeFrame(far, []byte("{id: [")) }()
	for range 2 {
		if err := <-heard; err == nil || !strings.Contains(err.Error(), "whether what was asked was done is not known") {
			t.Errorf("an Ask in flight heard %v", err)
		}
	}
	if _, err := cl.Ask(Request{Op: OpSlackPost}); err == nil || !strings.Contains(err.Error(), "the channel is closed") {
		t.Errorf("an Ask after it heard %v", err)
	}
}

// A REQUEST ASKED ON HEARING AN ANSWER HAS NONE AHEAD OF IT. The turn passes before the answer
// is sent, so a task that hears its fetch answered, asks for a push and exits has asked for the
// push: it is begun as it is read, and finished though the end goes the moment after.
func TestARequestAskedOnHearingAnAnswerIsFinishedThoughTheEndGoes(t *testing.T) {
	for i := 0; i < 200; i++ {
		var mu sync.Mutex
		var performed []string
		table := map[string]op{"a-test.in-turn": {unasked: "a test's", takes: []string{"name"}, deadline: time.Minute,
			ask: func(_ *Server, _ context.Context, _ Task, req Request) (asked, Reply) {
				mu.Lock()
				performed = append(performed, req.Field("name"))
				mu.Unlock()
				return before(Reply{OK: true})
			}}}
		s := &Server{Log: io.Discard, table: table}
		near, far := net.Pipe()
		near.SetDeadline(time.Now().Add(20 * time.Second))
		served := make(chan struct{})
		go func() { s.Serve(Task{ID: "t9", Workspace: &workspace.Workspace{}}, far); close(served) }()
		br := bufio.NewReader(near)
		for id, name := range []string{"first", "second"} {
			if err := writeFrame(near, frame(t, Request{ID: id + 1, Op: "a-test.in-turn", Fields: map[string]string{"name": name}})); err != nil {
				t.Fatal(err)
			}
			if name == "first" {
				if _, err := readFrame(br); err != nil {
					t.Fatal(err)
				}
			}
		}
		near.Close()
		<-served
		mu.Lock()
		if !slices.Equal(performed, []string{"first", "second"}) {
			t.Fatalf("run %d: performed %v, want the request asked on hearing the first's answer too", i, performed)
		}
		mu.Unlock()
	}
}

// THE RUNNER HOLDS SO MANY OF A TASK'S REQUESTS AND REFUSES THE NEXT, rather than leave it
// unread: a loop that waited for room would not see the task's end go, and would begin what
// waits its turn for a task that is gone. The one refused hears that nothing was done, under
// its own id, and those held are answered as they were asked.
func TestARequestPastWhatTheRunnerHoldsIsRefused(t *testing.T) {
	began, release := make(chan struct{}, 1), make(chan struct{})
	table := map[string]op{"a-test.in-turn": {unasked: "a test's", deadline: time.Minute,
		ask: func(*Server, context.Context, Task, Request) (asked, Reply) {
			select {
			case began <- struct{}{}:
			default:
			}
			<-release
			return before(Reply{OK: true})
		}}}
	s := &Server{Log: io.Discard, table: table}
	near, far := net.Pipe()
	t.Cleanup(func() { near.Close() })
	near.SetDeadline(time.Now().Add(20 * time.Second))
	go s.Serve(Task{ID: "t9", Workspace: &workspace.Workspace{}}, far)
	br := bufio.NewReader(near)
	for id := 1; id <= maxInFlight+1; id++ {
		if err := writeFrame(near, frame(t, Request{ID: id, Op: "a-test.in-turn"})); err != nil {
			t.Fatal(err)
		}
	}
	<-began
	hear := func() Reply {
		t.Helper()
		raw, err := readFrame(br)
		if err != nil {
			t.Fatal(err)
		}
		var rep Reply
		if err := codec.Unmarshal(raw, &rep, codec.Tony); err != nil {
			t.Fatal(err)
		}
		return rep
	}
	if rep := hear(); rep.ID != maxInFlight+1 || rep.OK || !strings.Contains(rep.Error, "nothing was done, ask again then") {
		t.Fatalf("the request past what the runner holds: %+v, want it refused under its own id", rep)
	}
	// Each of those held is answered, by its id: they are performed in the order asked, and
	// an answer names its request since it may pass another on the way out.
	close(release)
	answered := map[int]bool{}
	for range maxInFlight {
		rep := hear()
		if !rep.OK || rep.ID < 1 || rep.ID > maxInFlight || answered[rep.ID] {
			t.Fatalf("a held request answered %+v", rep)
		}
		answered[rep.ID] = true
	}
	// And with room again, the next is taken.
	if err := writeFrame(near, frame(t, Request{ID: 99, Op: "a-test.in-turn"})); err != nil {
		t.Fatal(err)
	}
	if rep := hear(); rep.ID != 99 || !rep.OK {
		t.Errorf("a request once there is room: %+v", rep)
	}
}

// AN OP PERFORMED APART IS HANDED A TASK WITHOUT WHAT THE OPS IN TURN WRITE: no checkout, and
// none of the branches the task made or pushed. It runs beside them, so it may not read those,
// and what it is handed is why it cannot.
func TestAnOpApartIsHandedNoCheckout(t *testing.T) {
	saw := func(tk Task) string {
		return fmt.Sprintf("checkout=%t created=%t pushed=%t launch=%t", tk.Workspace != nil, tk.created != nil, tk.pushed != nil, tk.Launch != nil)
	}
	reads := func(_ *Server, _ context.Context, tk Task, _ Request) (asked, Reply) {
		return before(Reply{OK: true, Out: saw(tk)})
	}
	s := &Server{Log: io.Discard, table: map[string]op{
		"a-test.in-turn": {unasked: "a test's", deadline: time.Minute, ask: reads},
		"a-test.apart":   {unasked: "a test's", deadline: time.Minute, apart: true, ask: reads},
	}}
	near, far := net.Pipe()
	t.Cleanup(func() { near.Close() })
	near.SetDeadline(time.Now().Add(20 * time.Second))
	go s.Serve(Task{ID: "t9", Workspace: &workspace.Workspace{}, Launch: within(nil),
		created: map[string]bool{}, pushed: map[string]bool{}}, far)
	cl := NewClient(near)
	for op, want := range map[string]string{
		"a-test.in-turn": "checkout=true created=true pushed=true launch=true",
		"a-test.apart":   "checkout=false created=false pushed=false launch=true",
	} {
		if rep, err := cl.Ask(Request{Op: op}); err != nil || rep.Out != want {
			t.Errorf("%s was handed %q, %v, want %q", op, rep.Out, err, want)
		}
	}
}

// WHEN THE CLIENT'S READING ENDS, ITS END IS CLOSED, whatever ended it: the runner then sees the
// task's side go, rather than write answers to an end nobody reads.
func TestTheClientClosesItsEndWhenReadingEnds(t *testing.T) {
	near, far := net.Pipe()
	t.Cleanup(func() { far.Close() })
	cl := NewClient(near)
	// A read that fails, and not because the runner's end went.
	near.SetReadDeadline(time.Now().Add(-time.Second))
	far.SetReadDeadline(time.Now().Add(10 * time.Second))
	if _, err := far.Read(make([]byte, 1)); !errors.Is(err, io.EOF) {
		t.Errorf("the runner's end read %v, want the task's end closed", err)
	}
	if _, err := cl.Ask(Request{Op: OpSlackPost}); err == nil {
		t.Error("an Ask after the reading ended heard an answer")
	}
}

// A REQUEST ASKED ON HEARING AN ANSWER FINDS ROOM. A place among those the runner holds is given
// back before its answer is written, so a task with as many in flight as the runner holds, each
// asked again as its answer is heard, is never refused for want of room, in turn or apart.
func TestARequestAskedOnHearingAnAnswerFindsRoom(t *testing.T) {
	for _, apart := range []bool{false, true} {
		table := map[string]op{"a-test.op": {unasked: "a test's", deadline: time.Minute, apart: apart,
			ask: func(*Server, context.Context, Task, Request) (asked, Reply) { return before(Reply{OK: true}) }}}
		s := &Server{Log: io.Discard, table: table}
		near, far := net.Pipe()
		near.SetDeadline(time.Now().Add(60 * time.Second))
		go s.Serve(Task{ID: "t9", Workspace: &workspace.Workspace{}}, far)
		cl := NewClient(near)
		failed := make(chan string, maxInFlight)
		var wg sync.WaitGroup
		for range maxInFlight {
			wg.Add(1)
			go func() {
				defer wg.Done()
				for range 500 {
					if rep, err := cl.Ask(Request{Op: "a-test.op"}); err != nil || !rep.OK {
						failed <- fmt.Sprintf("%+v, %v", rep, err)
						return
					}
				}
			}()
		}
		wg.Wait()
		near.Close()
		select {
		case what := <-failed:
			t.Errorf("apart=%t: a request asked on hearing an answer: %s", apart, what)
		default:
		}
	}
}
