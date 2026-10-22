package main

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/signadot/verse/mirror"
)

// waitCall is one verse_wait: what it said, the cursor it answered, and whether it was an error.
func waitCall(t *testing.T, cs *mcp.ClientSession, since string, timeout int) (string, string, bool) {
	t.Helper()
	args := map[string]any{"timeout": timeout}
	if since != "" {
		args["since"] = since
	}
	out, isErr := callTool(t, cs, "verse_wait", args)
	cursor := ""
	if i := strings.LastIndex(out, "cursor: "); i >= 0 {
		cursor = out[i+len("cursor: "):]
	}
	return out, cursor, isErr
}

// waitUntil calls verse_wait from since until every one of wants has been said, and answers all
// that was said and the cursor after it.
func waitUntil(t *testing.T, cs *mcp.ClientSession, since string, wants ...string) (string, string) {
	t.Helper()
	var all strings.Builder
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		out, cursor, isErr := waitCall(t, cs, since, 5)
		if isErr {
			t.Fatalf("verse_wait since %q: %s", since, out)
		}
		all.WriteString(out + "\n")
		since = cursor
		missing := false
		for _, w := range wants {
			if !strings.Contains(all.String(), w) {
				missing = true
			}
		}
		if !missing {
			return all.String(), since
		}
	}
	t.Fatalf("never said %q; said:\n%s", wants, all.String())
	return "", ""
}

// The wait opens its watch on the first call; a call with a short timeout opens it and answers.
func openWait(t *testing.T, cs *mcp.ClientSession) string {
	t.Helper()
	_, cursor, isErr := waitCall(t, cs, "", 1)
	if isErr {
		t.Fatal("the first wait was an error")
	}
	return cursor
}

// WHAT ALREADY WAITS is the first thing a wait says: the watch opens with what stands.
func TestWaitSaysWhatWaitsAtStart(t *testing.T) {
	store := serveAsking(t)
	id := askAs(t, daniel, "land", "-q", "Review?", "--to", "oscar@example.com")
	questionOn(t, store, mirror.AskRef(id))
	out, cursor := waitUntil(t, mcpSessionAs(t, davido), "", "waiting on you "+id+": Review?")
	if cursor == "" || cursor == "0" {
		t.Fatalf("no cursor after a line:\n%s", out)
	}
}

// WHAT ARRIVES WHILE A CALL WAITS wakes it.
func TestWaitSaysWhatArrives(t *testing.T) {
	store := serveAsking(t)
	dav := mcpSessionAs(t, davido)
	since := openWait(t, dav)
	got := make(chan string, 1)
	go func() {
		out, _, _ := waitCall(t, dav, since, 10)
		got <- out
	}()
	id := askAs(t, daniel, "land", "-q", "Review?", "--to", "oscar@example.com")
	questionOn(t, store, mirror.AskRef(id))
	select {
	case out := <-got:
		if !strings.Contains(out, "waiting on you "+id) {
			t.Fatalf("the waiting call said:\n%s", out)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("the waiting call was never woken")
	}
}

// NOTHING BETWEEN TWO CALLS IS MISSED, AND NOTHING IS SAID TWICE: what happens while no call is in
// flight is answered by the next call from the cursor, once each, in order.
func TestWaitMissesNothingBetweenCalls(t *testing.T) {
	store := serveAsking(t)
	dan := mcpSessionAs(t, daniel)
	since := openWait(t, dan)
	first := askAs(t, daniel, "first", "-q", "First?", "--to", "oscar@example.com")
	second := askAs(t, daniel, "second", "-q", "Second?", "--to", "oscar@example.com")
	_, c1 := waitUntil(t, dan, since, "asked "+first+": First?", "asked "+second+": Second?")

	// No call in flight: davido answers the first, daniel withdraws the second.
	questionOn(t, store, mirror.AskRef(first))
	as(t, davido)
	if out, err := runCLI(t, "question", first, "--answer", "proceed"); err != nil {
		t.Fatalf("davido's answer: %v\n%s", err, out)
	}
	as(t, daniel)
	if out, err := runCLI(t, "ask", second, "--withdraw"); err != nil {
		t.Fatalf("the withdrawal: %v\n%s", err, out)
	}
	answered, withdrawn := "answered "+first+" yes: First?", "withdrawn "+second
	out, c2 := waitUntil(t, dan, c1, answered, withdrawn)
	if strings.Count(out, answered) != 1 || strings.Count(out, withdrawn) != 1 ||
		strings.Index(out, answered) > strings.Index(out, withdrawn) || strings.Contains(out, "asked "+first) {
		t.Fatalf("from %s:\n%s", c1, out)
	}
	if out, c3, _ := waitCall(t, dan, c2, 1); !strings.HasPrefix(out, "nothing in ") || c3 != c2 {
		t.Fatalf("a wait with nothing to say from %s said %q, cursor %s", c2, out, c3)
	}
}

// AN ANSWER IS SAID ONCE: an ask that stood before the watch opened is answered after it, and the
// answer is said once and not again.
func TestWaitSaysAnAnswerOnce(t *testing.T) {
	store := serveAsking(t)
	id := askAs(t, daniel, "land", "-q", "Review?", "--to", "oscar@example.com")
	questionOn(t, store, mirror.AskRef(id))
	dan := mcpSessionAs(t, daniel)
	since := openWait(t, dan)
	as(t, davido)
	if out, err := runCLI(t, "question", id, "--answer", "proceed"); err != nil {
		t.Fatalf("davido's answer: %v\n%s", err, out)
	}
	answered := "answered " + id + " yes: Review?"
	out, cursor := waitUntil(t, dan, since, answered)
	if strings.Count(out, answered) != 1 {
		t.Fatalf("said %d times:\n%s", strings.Count(out, answered), out)
	}
	if again, _, _ := waitCall(t, dan, cursor, 1); strings.Contains(again, answered) {
		t.Fatalf("said again:\n%s", again)
	}
}

// A CURSOR THE SERVER FORGOT is refused, with what to do instead; one it still holds is answered.
func TestWaitRefusesACursorTheServerForgot(t *testing.T) {
	was := waitLogCap
	waitLogCap = 3
	t.Cleanup(func() { waitLogCap = was })
	l := newWaitLog()
	for _, line := range []string{"a", "b", "c", "d"} {
		l.say("1", line)
	}
	if _, _, err := l.after(0, false); err == nil || !strings.Contains(err.Error(), "older than the 3 lines") {
		t.Fatalf("cursor 0 after 4 lines kept 3: %v", err)
	}
	lines, _, err := l.after(1, false)
	if err != nil || len(lines) != 3 || lines[0].Line != "b" {
		t.Fatalf("cursor 1: %v %+v", err, lines)
	}
	if lines, _, err := l.after(0, true); err != nil || len(lines) != 3 {
		t.Fatalf("without since: %v %+v", err, lines)
	}
}

// A STREAM THAT ENDS is followed again, and what was said before it ended is not said again.
func TestWaitFollowsAgainWhenItsStreamEnds(t *testing.T) {
	_, ts := serveAskingAt(t)
	dan := mcpSessionAs(t, daniel)
	since := openWait(t, dan)
	first := askAs(t, daniel, "first", "-q", "First?")
	out1, c1 := waitUntil(t, dan, since, "asked "+first+": First?")
	ts.CloseClientConnections()
	second := askAs(t, daniel, "second", "-q", "Second?")
	out2, _ := waitUntil(t, dan, c1, "asked "+second+": Second?")
	if n := strings.Count(out1+out2, "asked "+first+": First?"); n != 1 {
		t.Fatalf("the ask before the stream ended said %d times:\n%s%s", n, out1, out2)
	}
}

// A CALL THE HOST CANCELS returns, rather than waiting out its timeout.
func TestWaitReturnsWhenTheHostCancels(t *testing.T) {
	serveAsking(t)
	dan := mcpSessionAs(t, daniel)
	since := openWait(t, dan)
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() {
		_, err := dan.CallTool(ctx, &mcp.CallToolParams{Name: "verse_wait",
			Arguments: map[string]any{"since": since, "timeout": 60}})
		done <- err
	}()
	time.Sleep(200 * time.Millisecond)
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("a cancelled wait did not return")
	}
}

// NOBODY CONFIGURED, NOTHING CAN WAIT ON YOU: refused at once.
func TestWaitWithNobodyConfigured(t *testing.T) {
	serveAsking(t)
	out, _, isErr := waitCall(t, mcpSessionAs(t, ""), "", 1)
	if !isErr || !strings.Contains(out, "nobody is configured") {
		t.Fatalf("error=%v %q", isErr, out)
	}
}

// A CURSOR FROM ANOTHER SERVER — this one's host started it again — is refused, rather than hiding
// as many lines as it counts, whether it counts more lines than this server has said or fewer.
func TestWaitRefusesACursorFromAnotherServer(t *testing.T) {
	store := serveAsking(t)
	before := mcpSessionAs(t, davido)
	id := askAs(t, daniel, "land", "-q", "Review?", "--to", "oscar@example.com")
	questionOn(t, store, mirror.AskRef(id))
	_, old := waitUntil(t, before, "", "waiting on you "+id)
	after := mcpSessionAs(t, davido)
	waitUntil(t, after, "", "waiting on you "+id) // the new server has said as many lines, or more
	for _, cursor := range []string{old, "57"} {
		out, _, isErr := waitCall(t, after, cursor, 1)
		if !isErr || !strings.Contains(out, "another server's") {
			t.Fatalf("cursor %q: error=%v %q", cursor, isErr, out)
		}
	}
}

// MORE THAN AN ANSWER HOLDS is answered in parts, each going on from the cursor of the last, and
// the parts are what one answer says.
func TestWaitAnswersInParts(t *testing.T) {
	serveAsking(t)
	dan := mcpSessionAs(t, daniel)
	since := openWait(t, dan)
	var wants []string
	for _, name := range []string{"first", "second", "third", "fourth"} {
		id := askAs(t, daniel, name, "-q", strings.Repeat(name, 10)+"?")
		wants = append(wants, "asked "+id)
	}
	whole, end := waitUntil(t, dan, since, wants...)
	lines := func(s string) []string {
		var out []string
		for _, l := range strings.Split(s, "\n") {
			if l != "" && !strings.HasPrefix(l, "cursor: ") {
				out = append(out, l)
			}
		}
		return out
	}
	was := mcpAnswerMax
	mcpAnswerMax = 1<<10 + 150
	t.Cleanup(func() { mcpAnswerMax = was })
	var parts []string
	calls := 0
	for cursor := since; cursor != end; calls++ {
		out, next, isErr := waitCall(t, dan, cursor, 1)
		if isErr || next == cursor {
			t.Fatalf("from %s: error=%v\n%s", cursor, isErr, out)
		}
		parts = append(parts, lines(out)...)
		cursor = next
	}
	if calls < 2 || strings.Join(parts, "\n") != strings.Join(lines(whole), "\n") {
		t.Fatalf("in %d calls:\n%s\nin one:\n%s", calls, strings.Join(parts, "\n"), whole)
	}
}

// A WATCH REFUSED AGAIN AND AGAIN is said once, not once a try: each line wakes every call and
// takes a place in the log.
func TestWaitSaysARepeatedFailureOnce(t *testing.T) {
	fakeVerse(t, func(w http.ResponseWriter, r *http.Request) { http.Error(w, "no", http.StatusForbidden) })
	was := waitRetryFirst
	waitRetryFirst = 5 * time.Millisecond
	t.Cleanup(func() { waitRetryFirst = was })
	dan := mcpSessionAs(t, daniel)
	openWait(t, dan)
	time.Sleep(300 * time.Millisecond) // five tries, and more
	out, _, _ := waitCall(t, dan, "", 1)
	if n := strings.Count(out, "the watch did not open"); n != 1 {
		t.Fatalf("said %d times:\n%s", n, out)
	}
}
