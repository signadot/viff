package viffd

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"go.viff.dev/viff/pkg/runner/runnerd/runnerdtest"

	"go.viff.dev/viff/pkg/codec"
	"go.viff.dev/viff/pkg/entity"
	"go.viff.dev/viff/pkg/entity/entitytest"
	"go.viff.dev/viff/pkg/mirror"
	"go.viff.dev/viff/pkg/runner"
	"go.viff.dev/viff/pkg/runner/client"
	"go.viff.dev/viff/pkg/runner/runnerd"
	"go.viff.dev/viff/pkg/systems/systemstest"
	"go.viff.dev/viff/pkg/task"
)

// frames reads an SSE attach until the stream is quiet, and returns what it carried.
//
// Quiet rather than closed: a live attach does not end, and what is being compared here is
// the REPLAY — everything the server had to say about the past before it started waiting.
func frames(t *testing.T, url string) []task.Event {
	t.Helper()
	_, lines, stop := sseData(t, http.DefaultClient, url)
	defer stop()

	// The stop condition is the task's own terminal frame, not a stopwatch. Every drive
	// here finishes, so "the replay is complete" is a fact the stream states rather than one
	// the test infers from a quiet moment — which under load is indistinguishable from a
	// burst that has not finished arriving.
	var out []task.Event
	for {
		select {
		case d, ok := <-lines:
			if !ok {
				return out
			}
			var f task.Event
			if err := codec.Unmarshal([]byte(d), &f, codec.Tony); err != nil {
				t.Fatalf("decode frame %q: %v", d, err)
			}
			out = append(out, f)
			if f.Kind == task.EventStatus && task.Status(f.Text).Terminal() {
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
func cursors(fs []task.Event) []string {
	var out []string
	for _, f := range fs {
		out = append(out, f.Kind)
	}
	return out
}

// `?since=` names the same thing on every runner: the frames AFTER that cursor.
//
// It did not. The route read the parameter only for a local task; a remote one
// went through a plain Subscribe and replayed from the beginning however far along the client
// already was. A reader that resumes then sees events it has already acted on, which for a
// resolver — whose whole job is acting on what it reads — is not a cosmetic difference.
func TestSinceMeansTheSameOnEveryRunner(t *testing.T) {
	// A drive of identical shape on each runner: N actions, then done.
	drive := func(l runner.Launch, reg *task.Registry) *task.Task {
		sup := systemstest.Opened(task.NewSupervisor(l.Environment, task.Autonomy{{Then: task.Then{Mode: task.ThenAllow}}}, reg), l.Environment)
		return task.NewTask(l.ID, sup, &countingDriver{n: 6}, noopExecutor{})
	}

	t.Run("local", func(t *testing.T) {
		srv := serverWith(t, drive, entitytest.Store(t))
		ts := httptest.NewServer(srv.Handler())
		defer ts.Close()
		spawn := postTony(t, ts.URL+"/tasks", map[string]any{"id": "p", "spawn": map[string]any{"script": "work", "environment": openEnv}})
		spawn.Body.Close()
		assertSince(t, ts.URL+"/tasks/p/events")
	})

	t.Run("remote", func(t *testing.T) {
		srv := testServer(t)
		noLocalRunner(t, srv) // so the spawn goes to the registered runner
		ts := httptest.NewServer(srv.Handler())
		defer ts.Close()
		d := runnerd.NewInProcessHandler(drive)
		d.Describe(runner.Description{Version: runner.DescriptionVersion, Offers: []runner.Offer{{Script: true, Systems: map[string]runner.System{"network": builds(runner.NetworkOpen)}}}})
		face := provenFace(t, ts.URL, d.HTTPHandler(), func() {})
		if resp := postTony(t, ts.URL+"/runners", face.reg("remote")); resp.StatusCode != http.StatusCreated {
			t.Fatalf("registering the runner = %s: %s", resp.Status, readBody(t, resp))
		}
		spawn := postTony(t, ts.URL+"/tasks", map[string]any{"id": "p", "spawn": map[string]any{"script": "work", "environment": openEnv}})
		spawn.Body.Close()
		assertSince(t, ts.URL+"/tasks/p/events")
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
	var want []task.Event
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

func itoa(n int64) string { return strconv.FormatInt(n, 10) }

// countingDriver takes n no-op actions then completes — a drive of a known shape, so the same
// one can be run on each runner and the streams compared.
type countingDriver struct{ n int }

func (d *countingDriver) Next(context.Context, task.StepResult) (task.ToolUse, bool, error) {
	if d.n <= 0 {
		return task.ToolUse{}, false, nil
	}
	d.n--
	return task.ToolUse{Name: "noop"}, true, nil
}

type noopExecutor struct{}

func (noopExecutor) Execute(_ context.Context, tu task.ToolUse) (task.StepResult, []task.ToolUse, error) {
	return task.StepResult{Use: tu, Output: "ok"}, nil, nil
}

// Below the watermark, the answer is a resync marker and then current state — not silence,
// and not a stream that starts mid-story as though nothing were missing.
//
// This is the half of `?since=` that cannot be papered over by replaying more than was asked.
// A reader resuming from a cursor the server no longer holds has to be TOLD, or it carries on
// believing it has an unbroken view of a task it has actually lost the middle of.
//
// Each task here is spawned on its runtime DIRECTLY rather than through viff, so the only
// subscriber is the one under test. A mirror attached to the same burst would be racing it for
// the same retained window, and what is being measured is the resume, not the race.
func TestBelowTheWatermarkResyncs(t *testing.T) {
	// Past the retained window (task.ringCap is 256), so the early frames are gone.
	drive := func(l runner.Launch, reg *task.Registry) *task.Task {
		sup := systemstest.Opened(task.NewSupervisor(l.Environment, task.Autonomy{{Then: task.Then{Mode: task.ThenAllow}}}, reg), l.Environment)
		return task.NewTask(l.ID, sup, &countingDriver{n: 400}, noopExecutor{})
	}

	assertGap := func(t *testing.T, url string) {
		t.Helper()
		// The precondition is that cursor 1 has FALLEN OUT of the retained window, and until
		// this test waited for it that was a race it usually won by luck: 400 actions outran a
		// reader that attached immediately, so the ring had already rolled past 1 and the gap
		// fired. Under -race the driver is the slow one, cursor 1 was still retained, and the
		// answer was correctly no gap — a test failing on its own setup rather than on what it
		// asserts.
		//
		// Draining to the terminal makes the precondition a fact instead of a wager: a drive
		// this long that has ENDED is past the window on every runner and at every speed. It
		// is the same stop condition frames() already uses — the task saying so — and it
		// costs one pass over a stream this test then re-reads from the far end anyway.
		frames(t, url)
		got := frames(t, url+"?since=1")
		if len(got) == 0 {
			t.Fatal("resuming below the watermark returned nothing at all")
		}
		if got[0].Kind != task.EventGap {
			t.Fatalf("first frame = %s, want %s — the reader is not told it lost the middle: %v",
				got[0].Kind, task.EventGap, cursors(got)[:min(6, len(got))])
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

	// The remote leg is the one this step changed: the cursor is handed to the REMOTE's own
	// `?since=`, so the resync decision is made by the side that knows what it still holds.
	// Asking for everything and discarding locally could never produce this frame.
	t.Run("remote", func(t *testing.T) {
		remote := httptest.NewServer(runnerd.NewInProcessHandler(drive).HTTPHandler())
		defer remote.Close()
		pol := client.HTTP(remote.URL)
		pol.Launch(runner.Launch{ID: "p", Script: "true", Environment: openNet})

		srv := serverWith(t, runnerdtest.Scripted, entitytest.Store(t))
		// The task's record says where it runs, as a launch through viff would have left it.
		entitytest.Set(t, srv.Store, mirror.Ref("p"), entity.Obj{"status": "running", "runner": remote.URL}.Payload())
		ts := httptest.NewServer(srv.Handler())
		defer ts.Close()
		assertGap(t, ts.URL+"/tasks/p/events")
	})
}

// The bug this whole file kept tripping over, end to end: a task that outruns its mirror
// still gets recorded as finished.
//
// The mirror is the consumer that made the loss catastrophic rather than cosmetic. It writes
// a store entry per frame, so it is always the slow one; it returns only on a terminal, so a
// dropped terminal leaked its goroutine and, behind a remote runner, the HTTP connection with
// it; and the engine reacts to the mirrored status, so a sequence waiting on onDone waited
// forever on a task that had finished minutes ago.
func TestAFastTaskIsStillRecordedAsFinished(t *testing.T) {
	for _, n := range []int{50, 300, 5000} {
		drive := func(l runner.Launch, reg *task.Registry) *task.Task {
			sup := systemstest.Opened(task.NewSupervisor(l.Environment, task.Autonomy{{Then: task.Then{Mode: task.ThenAllow}}}, reg), l.Environment)
			return task.NewTask(l.ID, sup, &countingDriver{n: n}, noopExecutor{})
		}
		srv := serverWith(t, drive, entitytest.Store(t))
		ts := httptest.NewServer(srv.Handler())
		defer ts.Close()
		postTony(t, ts.URL+"/tasks", map[string]any{"id": "p", "spawn": map[string]any{"script": "true", "environment": openEnv}}).Body.Close()

		var last string
		for range 6000 { // over the wire, the mirror of a long run takes its time
			e, _, _ := srv.Store.Get(mirror.Ref("p"))
			last = entity.Str(e.Payload, "status")
			if task.Status(last).Terminal() {
				break
			}
			time.Sleep(5 * time.Millisecond)
		}
		if !task.Status(last).Terminal() {
			t.Fatalf("a %d-action task that completed is mirrored as %q", n, last)
		}
	}
}
