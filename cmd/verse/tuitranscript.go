package main

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/signadot/verse/pkg/codec"
	"github.com/signadot/verse/pkg/task"
)

// A TASK'S TRANSCRIPT, as `verse task attach` streams it: what the agent says, each action it
// takes and what came of it, and the questions it stops on. It is a task row's second view, read
// from the task's events route from its first retained event, and followed while the task runs.
//
// The stream is open only while the view shows it: moving to another row, or back to the record,
// closes it. The pane follows the last line while it is on it, and stays where it is once
// scrolled up.

// tuiTranscript is the transcript the content pane holds: the task it is of, its events as
// lines, and how the stream stands.
type tuiTranscript struct {
	id    string
	lines []string
	ended string // why nothing more will come: the task's last status, or how the stream stopped
	err   string
	// stop closes the stream; ch is what it hands over.
	stop context.CancelFunc
	ch   <-chan transcriptMsg
}

// transcriptMsg is what a task's stream said since the last one: events, or its end.
type transcriptMsg struct {
	id   string
	evs  []task.Event
	done bool
	err  error
}

// transcriptWant is the task whose transcript the pane shows now, "" when it shows none.
func (m tuiModel) transcriptWant() string {
	if m.tab != tabTasks || m.onHeader || m.dview != viewTranscript || m.mode == modeHelp {
		return ""
	}
	if it, ok := m.item(); ok {
		return it.name
	}
	return ""
}

// syncTranscript opens the stream of the task the pane shows, and closes the one it has left.
func (m *tuiModel) syncTranscript() tea.Cmd {
	want := m.transcriptWant()
	if want == m.tr.id {
		return nil
	}
	if m.tr.stop != nil {
		m.tr.stop()
	}
	m.tr = tuiTranscript{id: want}
	if want == "" {
		return nil
	}
	ctx, stop := context.WithCancel(context.Background())
	ch := make(chan transcriptMsg, 64)
	m.tr.stop, m.tr.ch = stop, ch
	src := m.src
	go func() {
		defer close(ch)
		err := src.transcript(ctx, want, func(ev task.Event) {
			select {
			case ch <- transcriptMsg{id: want, evs: []task.Event{ev}}:
			case <-ctx.Done():
			}
		})
		if ctx.Err() == nil {
			ch <- transcriptMsg{id: want, done: true, err: err}
		}
	}()
	return waitTranscript(ch)
}

// waitTranscript is the stream's next word: every event already waiting, as one message.
func waitTranscript(ch <-chan transcriptMsg) tea.Cmd {
	return func() tea.Msg {
		msg, ok := <-ch
		if !ok {
			return nil
		}
		for !msg.done {
			select {
			case more, ok := <-ch:
				if !ok {
					return msg
				}
				msg.evs = append(msg.evs, more.evs...)
				msg.done, msg.err = more.done, more.err
				continue
			default:
			}
			break
		}
		return msg
	}
}

// transcribed takes what the stream said: its lines under the ones held, the pane kept on the
// last line if it was on it.
func (m tuiModel) transcribed(msg transcriptMsg) (tea.Model, tea.Cmd) {
	if msg.id != m.tr.id {
		return m, nil // the pane has moved on
	}
	follow := m.scroll >= m.maxScroll()
	for _, ev := range msg.evs {
		m.tr.lines = append(m.tr.lines, eventLines(ev)...)
		if ev.Kind == task.EventStatus && isTerminalStatus(ev.Text) {
			m.tr.ended = ev.Text
		}
	}
	var next tea.Cmd
	switch {
	case msg.err != nil:
		m.tr.err = msg.err.Error()
	case msg.done && m.tr.ended == "":
		m.tr.ended = "the stream ended"
	case !msg.done:
		next = waitTranscript(m.tr.ch)
	}
	if follow {
		m.scroll = m.maxScroll()
	}
	return m, next
}

// transcriptText is the transcript as the pane draws it.
func (m tuiModel) transcriptText(it tuiItem) string {
	var b strings.Builder
	b.WriteString(tuiTitle.Render(it.name) + "  " + tuiFaint.Render(it.state) + "\n\n")
	if m.tr.id != it.name {
		return b.String() + tuiFaint.Render("attaching…")
	}
	b.WriteString(strings.Join(m.tr.lines, "\n"))
	if len(m.tr.lines) > 0 {
		b.WriteString("\n")
	}
	switch {
	case m.tr.err != "":
		b.WriteString("\n" + tuiBad.Render("✗ "+m.tr.err))
	case m.tr.ended != "":
		if len(m.tr.lines) == 0 {
			b.WriteString(tuiFaint.Render("Nothing of this task's transcript is kept: it holds the last events of a task the runner still has."))
		}
	case len(m.tr.lines) == 0:
		b.WriteString(tuiFaint.Render("attached · nothing said yet"))
	default:
		b.WriteString("\n" + tuiFaint.Render("⁝ following"))
	}
	return b.String()
}

// eventLines is one event of a task as the transcript reads it. A boundary says nothing a
// reader follows, so it draws nothing.
func eventLines(ev task.Event) []string {
	indent := func(text string, style func(...string) string) []string {
		var out []string
		for _, l := range strings.Split(strings.TrimRight(text, "\n"), "\n") {
			out = append(out, "    "+style(l))
		}
		return out
	}
	what := ev.Action
	if what == "" {
		what = strings.TrimSpace(ev.Tool + " " + ev.Step)
	}
	var out []string
	switch ev.Kind {
	case task.EventBoundary:
		return nil
	case task.EventSay:
		if ev.By != "" {
			out = append(out, tuiKey.Render(ev.By+":")+" "+ev.Text)
		} else {
			out = append(out, ev.Text)
		}
	case task.EventDecision:
		by := ev.By
		if ev.Gate != "" {
			by = strings.TrimSpace(by + " · " + ev.Gate)
		}
		if ev.Proceed {
			out = append(out, tuiBrand.Render("▸ ")+what+"  "+tuiFaint.Render(by))
		} else {
			out = append(out, tuiBad.Render("✗ ")+what+"  "+tuiFaint.Render(by))
			if ev.Text != "" {
				out = append(out, indent(ev.Text, tuiBad.Render)...)
			}
		}
	case task.EventExec:
		if ev.Text != "" {
			out = append(out, indent(ev.Text, tuiFaint.Render)...)
		}
	case task.EventPending:
		ask := ev.Text
		if ask == "" {
			ask = what
		}
		out = append(out, tuiWarn.Render("◐ waiting on an answer: ")+ask)
	case task.EventWithdrawn:
		out = append(out, tuiFaint.Render("the question was withdrawn: "+ev.Text))
	case task.EventStatus:
		out = append(out, glyph(ev.Text)+" "+statusStyle(ev.Text).Render(ev.Text))
	case task.EventChild:
		out = append(out, tuiFaint.Render("child: "+ev.Text))
	case task.EventGap:
		out = append(out, tuiFaint.Render(fmt.Sprintf("… %d events were missed", ev.Dropped)))
	case task.EventAutonomy:
		out = append(out, tuiFaint.Render("autonomy replaced by "+ev.By+": "+ev.Text))
	default:
		out = append(out, tuiFaint.Render(strings.TrimSpace(ev.Kind+" "+ev.Text)))
	}
	if ev.Err != "" {
		out = append(out, indent(ev.Err, tuiBad.Render)...)
	}
	return out
}

// transcript streams a task's events to fn, from the first one retained, until the task ends,
// the stream does, or ctx does.
func (s *httpSource) transcript(ctx context.Context, id string, fn func(task.Event)) error {
	resp, err := s.c.With(ctx).Get("/tasks/"+id+"/events", url.Values{"since": {"0"}})
	if err != nil {
		if ctx.Err() != nil {
			return nil
		}
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= http.StatusBadRequest {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<10))
		msg := strings.TrimSpace(string(body))
		if msg == "" {
			msg = resp.Status
		}
		if resp.StatusCode == http.StatusNotFound {
			return fmt.Errorf("this task has no transcript to attach to: it never started, or its runner no longer holds it")
		}
		return fmt.Errorf("%s", msg)
	}
	in := streamFmt(resp)
	frames := codec.NewFrames(resp.Body, frameMax)
	for frames.Next() {
		var ev task.Event
		if err := codec.Unmarshal([]byte(frames.Text()), &ev, in); err != nil {
			continue
		}
		fn(ev)
		if ev.Kind == task.EventStatus && isTerminalStatus(ev.Text) {
			return nil
		}
	}
	if ctx.Err() != nil {
		return nil
	}
	return frames.Err()
}
