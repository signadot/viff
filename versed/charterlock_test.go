package versed

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// ONE AT A TIME. Two installs at one address used to take the same engine and interleave their
// undo lists, so a refusal in one put back what the other had installed — and both printed
// "nothing was installed", which cannot be true of both.
//
// Asserted on the LOCK rather than on a raced install, because a test that wins the race by
// timing proves nothing on a loaded machine: the second caller must not get in while the first
// holds it, and must get in the moment it does not.
func TestOneCharterOperationAtATime(t *testing.T) {
	srv := testServer(t)
	first, err := srv.lockCharter(context.Background())
	if err != nil {
		t.Fatalf("the first operation could not take the lock: %v", err)
	}

	got := make(chan error, 1)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		release, err := srv.lockCharter(ctx)
		release()
		got <- err
	}()
	select {
	case err := <-got:
		t.Fatalf("a second charter operation got in while the first held the lock: %v", err)
	case <-time.After(150 * time.Millisecond):
	}

	first()
	select {
	case err := <-got:
		if err != nil {
			t.Fatalf("the second operation did not get in once the first let go: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("the lock was released and nothing took it")
	}
}

// A CALLER THAT CANNOT GET IN IS TOLD, not hung. Waiting forever behind an install is how a CLI
// stops being able to say whether it is waiting or wedged.
func TestACharterOperationThatCannotGetInIsAnswered(t *testing.T) {
	srv := testServer(t)
	release, err := srv.lockCharter(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer release()

	ctx, cancel := context.WithTimeout(context.Background(), 80*time.Millisecond)
	defer cancel()
	start := time.Now()
	if _, err := srv.lockCharter(ctx); err == nil {
		t.Fatal("a held lock let a second operation through")
	} else if !strings.Contains(err.Error(), "in progress") {
		t.Errorf("the refusal does not say what is happening: %v", err)
	}
	if took := time.Since(start); took > time.Second {
		t.Errorf("waited %s on an 80ms context — the wait is not bounded by the caller", took)
	}
}

// …and over HTTP that is a 503 with a Retry-After: the server is busy with something else,
// which is what 503 means. A 409 would send an operator looking for a conflict in their file.
func TestABlockedInstallAnswers503(t *testing.T) {
	srv := testServer(t)
	srv.Start(t.Context())
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	srv.charterWait = 100 * time.Millisecond // the refusal is what is under test, not the wait
	release, err := srv.lockCharter(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer release()

	// Longer than the server's wait, so the answer arrives rather than the client giving up.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, "POST", ts.URL+"/charter?at=notes",
		strings.NewReader(`{name: r, condition: {subject: {path: [notes, *, *]}}, action: {patch: {seen: true}}}`))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "text/tony")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("no answer from a blocked install: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("a blocked install answered %s, want 503", resp.Status)
	}
	if resp.Header.Get("Retry-After") == "" {
		t.Error("503 with no Retry-After: it is a queue, and the client is not told to come back")
	}
}

// THE OUTER LOCK. scopeMu is taken by packageEngine, and nothing may reach for the charter lock
// while holding it — the two would deadlock the first time an install ran while a restore held
// the registry. The canary: hold scopeMu and take the charter lock the way a violation would,
// and see that this ordering is the one that works while the reverse is what wedges.
func TestTheCharterLockIsTheOuterLock(t *testing.T) {
	srv := testServer(t)
	done := make(chan struct{})
	go func() {
		defer close(done)
		// The correct order: charter lock, then anything that takes scopeMu.
		release, err := srv.lockCharter(context.Background())
		if err != nil {
			t.Errorf("lockCharter: %v", err)
			return
		}
		defer release()
		srv.scopeMu.Lock()
		srv.scopeMu.Unlock()
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("charter lock then scopeMu deadlocked — the ordering rule is inverted somewhere")
	}
}

// A release is safe to call twice, since every handler defers it and one path releases early.
func TestReleasingTwiceIsHarmless(t *testing.T) {
	srv := testServer(t)
	release, err := srv.lockCharter(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	release()
	release()
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		r2, err := srv.lockCharter(ctx)
		if err != nil {
			t.Errorf("a double release left the lock held: %v", err)
			return
		}
		r2()
	}()
	wg.Wait()
}
