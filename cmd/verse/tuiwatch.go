package main

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"sync/atomic"
	"time"

	"github.com/signadot/verse/pkg/client"
	"github.com/signadot/verse/pkg/entity"
	"github.com/signadot/verse/pkg/mirror"
)

// WHAT CAN BE WATCHED IS WATCHED, AND READ WHEN IT CHANGES. Every task's row comes from the
// mirror at verse.task.instance, and every question from the asks at verse.ask, so a watch at
// each says when the section is out of date; the section is then read again from its route
// (/tasks, /asks?waiting=me), so what it shows is still what `verse task` and `verse question`
// print. A frame is a signal, never a row.
//
// Each watch is at `<place>.*`, a frame a record: at the place itself, the opening state would be
// one frame holding every record. It opens from now; what changed before it opened, or while
// it was down, is caught by the read that follows each opening.
//
// A section whose watch is not open (refused, or the verse not answering) is read every round,
// as before; one whose watch is open is read when a frame says it changed, and every
// tuiResync besides, in case a frame was lost.
//
// The charter tree has no such watch: its rules live under verse.trigger, which a caller without
// a system grant may not watch, and what it says of held and pending rules is the engine's state,
// not a record. It is read every round while the Charters section is the one open, and every
// tuiCharterEvery otherwise. Runners, the Data section's listing and the record under the cursor
// are read every round.

const (
	tuiResync       = 30 * time.Second
	tuiCharterEvery = 30 * time.Second
)

// tuiWatch is what the watches have said, shared by every copy of the model.
type tuiWatch struct {
	tasks, questions watchState
}

// watchState is one watch: whether it is open, and whether it has said something changed since
// the section was last read.
type watchState struct {
	live, dirty atomic.Bool
}

// due says whether the section is to be read this round, and takes the change it is read for.
// A frame arriving while the read is on its way marks it again, for the next round.
func (s *watchState) due() bool {
	dirty := s.dirty.Swap(false)
	return dirty || !s.live.Load()
}

// startTUIWatch opens the watches, for as long as ctx lasts.
func startTUIWatch(ctx context.Context, c *client.Client) *tuiWatch {
	w := &tuiWatch{}
	go follow(ctx, c, entity.VerseRef(mirror.System, mirror.InstanceKind).KPath(), &w.tasks)
	go follow(ctx, c, entity.VerseRef(mirror.AskSystem).KPath(), &w.questions)
	return w
}

// follow keeps a watch at place open, marking s on each frame. A watch the verse refuses is not
// asked again: the section is read every round instead.
func follow(ctx context.Context, c *client.Client, place string, s *watchState) {
	backoff := time.Second
	for ctx.Err() == nil {
		q := url.Values{"at": {place + ".*"}, "wait-if-absent": {"true"}}
		began, err := followFrames(ctx, c, q,
			func() { s.live.Store(true); s.dirty.Store(true) },
			func(askFrame) { s.dirty.Store(true) })
		s.live.Store(false)
		var refused *watchRefused
		if !began && errors.As(err, &refused) && refused.status < http.StatusInternalServerError {
			return
		}
		if began {
			backoff = time.Second
		}
		if !waitOut(ctx, &backoff) {
			return
		}
	}
}
