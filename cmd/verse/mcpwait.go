package main

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// verse_wait answers what `verse ask --watch` and `verse question --watch` would print: an ask of
// yours settling, a question starting to wait on you.
//
// THE WATCH LIVES IN THE SERVER, NOT IN THE CALL. The watch is right because it keeps a copy of
// every ask, and says what changed from the copy it held: a frame met twice says nothing twice, and
// a watch from now opens with what stands so each copy starts whole (askWatch). None of that is in
// a revision a caller could pass back. So one askWatch is opened by the first call and followed for
// the server's life, what it says is kept in a numbered log, and a call answers the lines after
// the number it was given. Nothing said between two calls is missed, and nothing is said twice.
//
// It is opened by the first call rather than when the server starts, so a session that never waits
// holds no watch on the verse; and never closed after, since a watch opened again from now does not
// say an answer that landed while it was closed.

// waitLogCap is how many lines the log keeps. A var so a test can make it small.
var waitLogCap = 1024

// waitLine is one line the watch said: its number in the log, the revision it was said at ("" for
// what the watch says about its own stream), and the words.
type waitLine struct {
	Seq  uint64 `json:"seq"`
	Rev  string `json:"rev,omitempty"`
	Line string `json:"line"`
}

// waitLog is what the watch said, numbered from 1, the last waitLogCap lines of it.
type waitLog struct {
	id    string // this server's, in every cursor it answers
	mu    sync.Mutex
	lines []waitLine
	last  uint64        // the number of the last line said; 0 before any
	wake  chan struct{} // closed when a line is said, and replaced
}

func newWaitLog() *waitLog {
	return &waitLog{id: strconv.FormatInt(time.Now().UnixNano(), 36), wake: make(chan struct{})}
}

// cursor is the cursor after line seq: this server's, and the number.
func (l *waitLog) cursor(seq uint64) string { return l.id + "." + strconv.FormatUint(seq, 10) }

// seq is the number in a cursor this server answered. One another server answered — this one's
// host started it again — is refused: its number counts another server's lines, and answered
// from here it would hide as many of these.
func (l *waitLog) seq(cursor string) (uint64, error) {
	id, n, _ := strings.Cut(cursor, ".")
	seq, err := strconv.ParseUint(n, 10, 64)
	if id != l.id || err != nil {
		return 0, fmt.Errorf("cursor %q is not one this server answered: it is another server's, from "+
			"before its host started it again; call without since, and verse_question and verse_ask_list to "+
			"catch up", cursor)
	}
	return seq, nil
}

// say is the watch's say: the line is kept, and every call waiting is woken.
func (l *waitLog) say(rev, line string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.last++
	l.lines = append(l.lines, waitLine{Seq: l.last, Rev: rev, Line: line})
	if over := len(l.lines) - waitLogCap; over > 0 {
		l.lines = append([]waitLine(nil), l.lines[over:]...)
	}
	close(l.wake)
	l.wake = make(chan struct{})
}

// after answers the lines after since — every line kept, where all — and the channel that closes
// when the next is said. A cursor older than what is kept is refused: what was said between it and
// the oldest line kept is gone.
func (l *waitLog) after(since uint64, all bool) ([]waitLine, <-chan struct{}, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if !all && len(l.lines) > 0 && since+1 < l.lines[0].Seq {
		return nil, nil, fmt.Errorf("cursor %d is older than the %d lines this server keeps; call "+
			"without since, and verse_question and verse_ask_list to catch up", since, waitLogCap)
	}
	var out []waitLine
	for _, ln := range l.lines {
		if all || ln.Seq > since {
			out = append(out, ln)
		}
	}
	return out, l.wake, nil
}

// lastSeq is the number of the last line said.
func (l *waitLog) lastSeq() uint64 {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.last
}

// waitPart is as many of lines as one answer holds, and at least one. The cursor answered is the
// last one's, so the next call goes on from it.
func waitPart(lines []waitLine) []waitLine {
	size := 0
	for i, ln := range lines {
		size += len("@ \n") + len(ln.Rev) + len(ln.Line)
		if i > 0 && size > mcpAnswerMax-1<<10 {
			return lines[:i]
		}
	}
	return lines
}

const (
	waitDefault = 5 * time.Minute
	waitMax     = time.Hour
)

type waitIn struct {
	Since   string `json:"since,omitempty" jsonschema:"the cursor a previous verse_wait answered: what was said after it is answered at once, so nothing between two calls is missed. Without it, everything said since this server's watch opened, which begins with the questions already waiting on you"`
	Timeout int    `json:"timeout,omitempty" jsonschema:"seconds to wait for something to say before answering with nothing; 300 by default, at most 3600"`
}

type waitOutput struct {
	Lines  []waitLine `json:"lines" jsonschema:"what was said, in order; empty at the timeout"`
	Cursor string     `json:"cursor" jsonschema:"pass as since to the next verse_wait"`
}

func (m *mcpServer) addWaitTool(s *mcp.Server) {
	addTool(m, s, &mcp.Tool{
		Name: "verse_wait",
		Description: "Wait until an ask of yours is asked, answered, asked again or goes, or a question " +
			"starts waiting on you or settles, as `verse ask --watch` and `verse question --watch` say " +
			"it; answers at once when something was said after since, or when the next thing is, or " +
			"with nothing at the timeout, and always with a cursor to pass back as since. Without " +
			"since it answers everything said since this server first waited, which begins with the " +
			"questions already waiting on you.",
		Annotations: readOnly(),
	}, m.wait)
}

func (m *mcpServer) wait(ctx context.Context, _ *mcp.CallToolRequest, in waitIn) (*mcp.CallToolResult, waitOutput, error) {
	if err := m.nobody(); err != nil {
		return nil, waitOutput{}, err
	}
	var since uint64
	all := in.Since == ""
	if !all {
		n, err := m.log.seq(in.Since)
		if err != nil {
			return nil, waitOutput{}, err
		}
		since = n
	}
	timeout := waitDefault
	if in.Timeout > 0 {
		timeout = min(time.Duration(in.Timeout)*time.Second, waitMax)
	}
	m.startWait()
	deadline := time.NewTimer(timeout)
	defer deadline.Stop()
	for {
		lines, wake, err := m.log.after(since, all)
		if err != nil {
			return nil, waitOutput{}, err
		}
		if len(lines) > 0 {
			lines = waitPart(lines)
			return waitAnswer(lines, m.log.cursor(lines[len(lines)-1].Seq), timeout)
		}
		select {
		case <-wake:
		case <-deadline.C:
			return waitAnswer(nil, m.log.cursor(since), timeout)
		case <-ctx.Done():
			return nil, waitOutput{}, ctx.Err()
		}
	}
}

// waitAnswer is the lines as `verse ask --watch` prints them, and the cursor.
func waitAnswer(lines []waitLine, cursor string, timeout time.Duration) (*mcp.CallToolResult, waitOutput, error) {
	var b strings.Builder
	for _, ln := range lines {
		if ln.Rev != "" {
			fmt.Fprintf(&b, "@%s ", ln.Rev)
		}
		fmt.Fprintln(&b, ln.Line)
	}
	if len(lines) == 0 {
		fmt.Fprintf(&b, "nothing in %s\n", timeout)
	}
	fmt.Fprintf(&b, "cursor: %s", cursor)
	return sentence(b.String()), waitOutput{Lines: append([]waitLine{}, lines...), Cursor: cursor}, nil
}

// waitRetryFirst is the first backoff after the watch fails to open. A var so a test can make it
// short.
var waitRetryFirst = time.Second

// startWait opens the watch, once: the caller's asks and the questions waiting on them, followed
// until the server ends. A stream that never opened — a verse down, or refusing — is said, and
// tried again after a backoff, from the last revision met where there is one. The same failure
// again is not said again until the watch has said something else: each line wakes every waiting
// call and takes a place in the log.
func (m *mcpServer) startWait() {
	m.waitOnce.Do(func() {
		w := newAskWatch(m.c, m.me, true, true)
		go func() {
			backoff := waitRetryFirst
			said := ""
			for {
				since := ""
				w.mu.Lock()
				if w.hasLast {
					since = strconv.FormatInt(max(w.last-1, 0), 10)
				}
				w.mu.Unlock()
				before := m.log.lastSeq()
				err := w.follow(m.ctx, since, m.log.say)
				if m.ctx.Err() != nil {
					return
				}
				if m.log.lastSeq() != before {
					said = ""
				}
				if err != nil && err.Error() != said {
					said = err.Error()
					m.log.say("", fmt.Sprintf("the watch did not open (%v); trying again", err))
				}
				if !waitOut(m.ctx, &backoff) {
					return
				}
			}
		}()
	})
}
