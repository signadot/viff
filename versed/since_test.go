package versed

import (
	"bufio"
	"context"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/signadot/verse/broker"
	"github.com/signadot/verse/codec"
	"github.com/signadot/verse/entity/entitytest"
	"github.com/signadot/verse/process"
	"github.com/signadot/verse/process/processd"
	"github.com/signadot/verse/runner"
)

// frames reads an SSE attach until the stream is quiet, and returns what it carried.
//
// Quiet rather than closed: a live attach does not end, and what is being compared here is
// the REPLAY — everything the server had to say about the past before it started waiting.
func frames(t *testing.T, url string) []process.Frame {
	t.Helper()
	resp, err := http.Get(url)
	if err != nil {
		t.Fatalf("attach %s: %v", url, err)
	}
	defer resp.Body.Close()

	lines := make(chan string, 1024)
	go func() {
		sc := bufio.NewScanner(resp.Body)
		for sc.Scan() {
			if d, ok := strings.CutPrefix(sc.Text(), "data: "); ok {
				lines <- d
			}
		}
		close(lines)
	}()

	// The stop condition is the process's own terminal frame, not a stopwatch. Every drive
	// here finishes, so "the replay is complete" is a fact the stream states rather than one
	// the test infers from a quiet moment — which under load is indistinguishable from a
	// burst that has not finished arriving.
	var out []process.Frame
	for {
		select {
		case d, ok := <-lines:
			if !ok {
				return out
			}
			var f process.Frame
			if err := codec.Unmarshal([]byte(d), &f, codec.Tony); err != nil {
				t.Fatalf("decode frame %q: %v", d, err)
			}
			out = append(out, f)
			if f.Kind == process.EvStatus && process.Status(f.Text).Terminal() {
				return out
			}
		case <-time.After(5 * time.Second):
			t.Fatalf("the stream stalled after %d frames without reaching a terminal: %v",
				len(out), cursors(out))
			return out
		}
	}
}

// cursors is what a comparison across runners can actually be made on: the frames' kinds and
// order. Ids and timings differ by runner; the sequence is the contract.
func cursors(fs []process.Frame) []string {
	var out []string
	for _, f := range fs {
		out = append(out, f.Kind)
	}
	return out
}

// `?since=` names the same thing on every runner: the frames AFTER that cursor.
//
// It did not. The route read the parameter only for a local process; a brokered or remote one
// went through a plain Subscribe and replayed from the beginning however far along the client
// already was. A reader that resumes then sees events it has already acted on, which for a
// resolver — whose whole job is acting on what it reads — is not a cosmetic difference.
func TestSinceMeansTheSameOnEveryRunner(t *testing.T) {
	// A drive of identical shape on each runner: N actions, then done.
	drive := func(l process.Launch, reg *process.Registry, _ process.ContentReader) *process.Process {
		sup := process.NewSupervisor(&process.Vector{Default: process.Auto}, reg)
		return process.NewProcess(l.ID, sup, &countingDriver{n: 6}, noopExecutor{})
	}

	t.Run("local", func(t *testing.T) {
		srv := NewWithStore(drive, entitytest.Store(t))
		ts := httptest.NewServer(srv.Handler())
		defer ts.Close()
		spawn := postTony(t, ts.URL+"/processes", map[string]any{"id": "p", "task": "work"})
		spawn.Body.Close()
		assertSince(t, ts.URL+"/processes/p/events")
	})

	t.Run("remote", func(t *testing.T) {
		remote := httptest.NewServer(processd.New(drive).Handler())
		defer remote.Close()
		srv := NewWithStore(ScriptedBuilder, entitytest.Store(t))
		srv.SetRunner(runner.HTTP(remote.URL))
		ts := httptest.NewServer(srv.Handler())
		defer ts.Close()
		spawn := postTony(t, ts.URL+"/processes", map[string]any{"id": "p", "task": "work"})
		spawn.Body.Close()
		assertSince(t, ts.URL+"/processes/p/events")
	})

	t.Run("broker", func(t *testing.T) {
		b := broker.New()
		srv := NewWithStore(ScriptedBuilder, entitytest.Store(t))
		srv.SetRunner(b)
		nonce := srv.HostBroker("", b.WorkerHandler())
		ts := httptest.NewServer(srv.Handler())
		defer ts.Close()
		work := httptest.NewServer(srv.BrokerHandler())
		defer work.Close()

		b.Spawn(process.Launch{ID: "p", Task: "work", Driver: "human"}, nil)
		claim := postTony(t, work.URL+"/broker/"+nonce+"/processes/p/claim", map[string]any{"identity": "human:w"})
		var cl struct {
			Epoch int64 `tony:"field=epoch"`
		}
		decodeResp(t, claim, &cl)
		claim.Body.Close()
		// The worker reports the same shape of drive the other two ran.
		var evs []process.Frame
		for range 6 {
			evs = append(evs, process.Frame{Kind: process.EvExec, Tool: "noop"})
		}
		evs = append(evs, process.Frame{Kind: process.EvStatus, Text: string(process.Done)})
		rep := postTony(t, work.URL+"/broker/"+nonce+"/processes/p/report",
			map[string]any{"epoch": cl.Epoch, "events": evs})
		rep.Body.Close()
		assertSince(t, ts.URL+"/processes/p/events")
	})
}

// assertSince is the contract itself, asked of one attach URL: what `?since=N` returns is
// exactly what a full replay returns with the first N dropped, and the cursors say so.
func assertSince(t *testing.T, url string) {
	t.Helper()
	all := frames(t, url+"?since=0")
	if len(all) < 4 {
		t.Fatalf("the drive produced %d frames, too few to resume into: %v", len(all), cursors(all))
	}
	cut := all[1].Cursor // resume just after the second frame

	got := frames(t, url+"?since="+itoa(cut))
	var want []process.Frame
	for _, f := range all {
		if f.Cursor > cut {
			want = append(want, f)
		}
	}
	if len(got) != len(want) {
		t.Fatalf("since=%d gave %d frames, want %d\n got: %v\nwant: %v",
			cut, len(got), len(want), cursors(got), cursors(want))
	}
	for i := range want {
		if got[i].Cursor != want[i].Cursor || got[i].Kind != want[i].Kind {
			t.Fatalf("since=%d frame %d = {%s @%d}, want {%s @%d} — the resume is not the tail of the replay",
				cut, i, got[i].Kind, got[i].Cursor, want[i].Kind, want[i].Cursor)
		}
	}
	// And the frames it dropped are the ones the client said it had. Asserting only the tail
	// would pass for a stream that replayed everything and happened to be counted loosely.
	for _, f := range got {
		if f.Cursor != 0 && f.Cursor <= cut {
			t.Fatalf("since=%d replayed %s at cursor %d — a resumed reader is being shown what it already saw",
				cut, f.Kind, f.Cursor)
		}
	}
}

// firstFrames reads the first n frames of an attach and stops, for a stream whose tail is not
// what is under test.
//
// It exists because a process that emits faster than its subscriber drains loses frames — and
// can lose the terminal one, which nothing follows and no gap marker can announce. A
// completed 400-action process therefore streams ~260 frames and then nothing, forever. That
// is a defect in the bus, not in `?since=`, and it is recorded in the commit rather than
// worked around silently (git-issue 56s91yabh12kskzcdsn0): this helper reads the head so the
// resume contract can be tested while the tail is broken.
func firstFrames(t *testing.T, url string, n int) []process.Frame {
	t.Helper()
	resp, err := http.Get(url)
	if err != nil {
		t.Fatalf("attach %s: %v", url, err)
	}
	defer resp.Body.Close()

	lines := make(chan string, 1024)
	go func() {
		sc := bufio.NewScanner(resp.Body)
		for sc.Scan() {
			if d, ok := strings.CutPrefix(sc.Text(), "data: "); ok {
				lines <- d
			}
		}
		close(lines)
	}()

	var out []process.Frame
	for len(out) < n {
		select {
		case d, ok := <-lines:
			if !ok {
				return out
			}
			var f process.Frame
			if err := codec.Unmarshal([]byte(d), &f, codec.Tony); err != nil {
				t.Fatalf("decode frame %q: %v", d, err)
			}
			out = append(out, f)
		case <-time.After(5 * time.Second):
			t.Fatalf("the stream delivered %d of %d frames: %v", len(out), n, cursors(out))
		}
	}
	return out
}

func itoa(n int64) string { return strconv.FormatInt(n, 10) }

// countingDriver takes n no-op actions then completes — a drive of a known shape, so the same
// one can be run on each runner and the streams compared.
type countingDriver struct{ n int }

func (d *countingDriver) Next(context.Context, process.StepResult) (process.ToolUse, bool, error) {
	if d.n <= 0 {
		return process.ToolUse{}, false, nil
	}
	d.n--
	return process.ToolUse{Name: "noop"}, true, nil
}

type noopExecutor struct{}

func (noopExecutor) Execute(_ context.Context, tu process.ToolUse) (process.StepResult, []process.ToolUse, error) {
	return process.StepResult{Use: tu, Output: "ok"}, nil, nil
}

// Below the watermark, the answer is a resync marker and then current state — not silence,
// and not a stream that starts mid-story as though nothing were missing.
//
// This is the half of `?since=` that cannot be papered over by replaying more than was asked.
// A reader resuming from a cursor the server no longer holds has to be TOLD, or it carries on
// believing it has an unbroken view of a process it has actually lost the middle of.
//
// Each process here is spawned on its runtime DIRECTLY rather than through verse, so the only
// subscriber is the one under test. A mirror attached to the same burst would be racing it for
// the same retained window, and what is being measured is the resume, not the race.
func TestBelowTheWatermarkResyncs(t *testing.T) {
	// Past the retained window (process.ringCap is 256), so the early frames are gone.
	drive := func(l process.Launch, reg *process.Registry, _ process.ContentReader) *process.Process {
		sup := process.NewSupervisor(&process.Vector{Default: process.Auto}, reg)
		return process.NewProcess(l.ID, sup, &countingDriver{n: 400}, noopExecutor{})
	}

	assertGap := func(t *testing.T, url string) {
		t.Helper()
		// A bounded read, not a read-to-terminal: a burst this size outruns its subscriber
		// and the terminal frame is among what gets dropped, so waiting for one would be
		// waiting on a known-separate defect (see the note on the drop below). What is being
		// asserted is the head of the stream, and the head is all it takes.
		got := firstFrames(t, url+"?since=1", 4)
		if len(got) == 0 {
			t.Fatal("resuming below the watermark returned nothing at all")
		}
		if got[0].Kind != process.EvGap {
			t.Fatalf("first frame = %s, want %s — the reader is not told it lost the middle: %v",
				got[0].Kind, process.EvGap, cursors(got)[:min(6, len(got))])
		}
		// And what follows is current, not the start of the story: everything after the
		// marker is above the cursor the client asked to resume from.
		if len(got) < 2 {
			t.Fatal("the resync marker arrived with no state after it")
		}
		if got[1].Cursor <= 1 {
			t.Fatalf("after the resync the stream restarted at cursor %d", got[1].Cursor)
		}
	}

	t.Run("local", func(t *testing.T) {
		srv := NewWithStore(drive, entitytest.Store(t))
		ts := httptest.NewServer(srv.Handler())
		defer ts.Close()
		srv.Procd.Spawn(process.Launch{ID: "p", Task: "work"}, nil)
		assertGap(t, ts.URL+"/processes/p/events")
	})

	// The remote leg is the one this step changed: the cursor is handed to the REMOTE's own
	// `?since=`, so the resync decision is made by the side that knows what it still holds.
	// Asking for everything and discarding locally could never produce this frame.
	t.Run("remote", func(t *testing.T) {
		remote := httptest.NewServer(processd.New(drive).Handler())
		defer remote.Close()
		pol := runner.HTTP(remote.URL)
		pol.Spawn(process.Launch{ID: "p", Task: "work", Driver: "scripted"}, nil)

		srv := NewWithStore(ScriptedBuilder, entitytest.Store(t))
		srv.SetRunner(pol)
		ts := httptest.NewServer(srv.Handler())
		defer ts.Close()
		assertGap(t, ts.URL+"/processes/p/events")
	})
}
