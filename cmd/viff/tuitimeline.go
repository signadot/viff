package main

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"

	"go.viff.dev/viff/pkg/entity"
)

// A timeline, as a project's or a place's timeline view draws it: every task the runtime holds
// about it, one row each in the order they started, a bar from its start to its end (to now while
// it runs) on one time axis, coloured by how it ended, and how long it took. Rounds of the
// steward, work, reviews, merges and verifications read as the project's rhythm: what ran in
// parallel, what waited, and where the time went.

type timelineRow struct {
	name, status string
	start, end   time.Time
	running      bool
}

// tasksAbout is the tasks whose subject about says is theirs, in the order they started.
func (m tuiModel) tasksAbout(about func(subject string) bool) []timelineRow {
	var out []timelineRow
	now := time.Now()
	for _, t := range m.snap.everyTask {
		_, subj, ok := taskParts(t.name)
		if !ok || !about(subj) {
			continue
		}
		if r, ok := timelineRowOf(t, now); ok {
			out = append(out, r)
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].start.Before(out[j].start) })
	return out
}

// timelineRowOf is a task as a timeline's row; ok is false for one that does not say when it
// started.
func timelineRowOf(t tuiItem, now time.Time) (timelineRow, bool) {
	start, err := time.Parse(time.RFC3339, entity.Str(t.record, "started"))
	if err != nil {
		return timelineRow{}, false
	}
	r := timelineRow{name: taskShortName(t.name), status: firstWord(t.state), start: start}
	if end, err := time.Parse(time.RFC3339, entity.Str(t.record, "ended")); err == nil {
		r.end = end
	} else {
		r.end, r.running = now, !isTerminalStatus(r.status)
	}
	return r, true
}

func timeline(rows []timelineRow, of string, w int) []string {
	if len(rows) == 0 {
		if of == "" {
			return []string{tuiFaint.Render("no task is running, and none ended in the last " + roundDur(flightRecent))}
		}
		return []string{tuiFaint.Render("no tasks about " + of + " are in the runtime")}
	}
	t0, t1 := rows[0].start, rows[0].end
	for _, r := range rows {
		if r.end.After(t1) {
			t1 = r.end
		}
	}
	span := max(t1.Sub(t0), time.Second)
	nameW := min(30, max(w/3, 12))
	durW := 8
	barW := max(w-nameW-durW-3, 10)
	col := func(t time.Time) int { return int(float64(barW-1) * float64(t.Sub(t0)) / float64(span)) }

	lines := []string{
		tuiFaint.Render(fmt.Sprintf("%d tasks over %s, from %s", len(rows), roundDur(span), t0.Local().Format("Jan 2 15:04"))),
		tuiFaint.Render(fmt.Sprintf("%-*s %s", nameW, "", axis(barW, span))),
	}
	for _, r := range rows {
		a, b := col(r.start), max(col(r.end), col(r.start))
		bar := strings.Repeat(" ", a) + statusStyle(barStatus(r)).Render(strings.Repeat("█", b-a+1))
		pad := barW - lipgloss.Width(bar)
		dur := roundDur(r.end.Sub(r.start))
		if r.running {
			dur += "…"
		}
		lines = append(lines, fmt.Sprintf("%-*s %s%s %s", nameW, short(r.name, nameW), bar, strings.Repeat(" ", max(pad, 0)),
			tuiFaint.Render(fmt.Sprintf("%*s", durW, dur))))
	}
	return lines
}

// barStatus is the colour a task's bar takes: what failed red, what runs amber, the rest green.
func barStatus(r timelineRow) string {
	if r.running {
		return "running"
	}
	return r.status
}

// axis is the time axis over w columns: its start, its middle and its end.
func axis(w int, span time.Duration) string {
	mid, end := roundDur(span/2), roundDur(span)
	if w < len(mid)+len(end)+6 {
		return strings.Repeat("─", w)
	}
	left := "0"
	gap1 := w/2 - len(left) - len(mid)/2
	gap2 := w - len(left) - gap1 - len(mid) - len(end)
	return left + strings.Repeat("─", max(gap1, 1)) + mid + strings.Repeat("─", max(gap2, 1)) + end
}

func roundDur(d time.Duration) string {
	if d < time.Hour {
		return d.Round(time.Second).String()
	}
	return d.Round(time.Minute).String()
}
