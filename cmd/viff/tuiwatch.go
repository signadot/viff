package main

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"sync/atomic"
	"time"

	"go.viff.dev/viff/pkg/client"
	"go.viff.dev/viff/pkg/entity"
	"go.viff.dev/viff/pkg/mirror"
)

// WHAT CAN BE WATCHED IS WATCHED, AND READ WHEN IT CHANGES. Every task's row comes from the
// mirror at mirror.task.instance, and every question from the asks at mirror.ask, so a watch at
// each says when the section is out of date; the section is then read again from its route
// (/tasks, /asks?waiting=me), so what it shows is still what `viff task` and `viff question`
// print. A frame is a signal, never a row.
//
// Each watch is at `<place>.*`, a frame a record: at the place itself, the opening state would be
// one frame holding every record. It opens from now; what changed before it opened, or while
// it was down, is caught by the read that follows each opening.
//
// A section whose watch is not open (refused, or the viff not answering) is read every round,
// as before; one whose watch is open is read when a frame says it changed, and every
// tuiResync besides, in case a frame was lost.
//
// A CHANGE IS READ WHEN A READ OF IT IS DRAWN, not when one is sent. Each watch counts its
// frames (gen); a round carries the count as it was when the round was sent, and a section is
// up to date once a round that read it without error is drawn with the count it carries. A
// round dropped for a newer one, or a read that failed, leaves the section behind the count, so
// the next round reads it again.
//
// The charter tree is not watched: what it says of held and pending rules is the engine's state,
// not a record, so a watch of mirror.trigger would not see all of it change. It is read every round while the Charters section is the one open, and every
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

// watchState is one watch: whether it is open, and how many changes it has said (an opening
// counts as one: what changed while it was down is read after it).
type watchState struct {
	live atomic.Bool
	gen  atomic.Int64
}

// startTUIWatch opens the watches, for as long as ctx lasts.
func startTUIWatch(ctx context.Context, c *client.Client) *tuiWatch {
	w := &tuiWatch{}
	go follow(ctx, c, entity.ViffRef(mirror.System, mirror.InstanceKind).KPath(), &w.tasks)
	go follow(ctx, c, entity.ViffRef(mirror.AskSystem).KPath(), &w.questions)
	return w
}

// follow keeps a watch at place open, counting each frame on s. A watch the viff refuses
// (a grant missing, a login expired) is asked again every askRetryMax; meanwhile the section is
// read every round.
func follow(ctx context.Context, c *client.Client, place string, s *watchState) {
	backoff := time.Second
	for ctx.Err() == nil {
		q := url.Values{"at": {place + ".*"}, "wait-if-absent": {"true"}}
		began, err := followFrames(ctx, c, q,
			func() { s.gen.Add(1); s.live.Store(true) },
			func(askFrame) { s.gen.Add(1) })
		s.live.Store(false)
		var refused *watchRefused
		if !began && errors.As(err, &refused) && refused.status < http.StatusInternalServerError {
			backoff = askRetryMax
		}
		if began {
			backoff = time.Second
		}
		if !waitOut(ctx, &backoff) {
			return
		}
	}
}
