package viffd

import (
	"errors"
	"fmt"
	"os"
	"time"

	"go.viff.dev/viff/pkg/entity"
	"go.viff.dev/viff/pkg/mirror"
)

// followServed keeps the onward asks of the ask at ref in step with it as it stands — p, or nil
// where it is gone (9ce3bva0, stage 4). Run from the watch on the asks, so a restart, whose watch
// replays what stands, catches up with what changed while it was down.
//
//   - Settled, by anybody or its clock, withdrawn, or removed: its waiting onward asks are
//     withdrawn, saying so. There is nothing left for them to carry back.
//   - Asked again: those raised for an earlier edit are withdrawn. They asked about words no
//     longer asked, and the resolver serves the ask afresh.
//   - Its clock sooner: an onward ask due later is moved to it, and asks nothing again. Its clock
//     later: they stand as they are.
func (s *Server) followServed(ref entity.Ref, p entity.Payload) {
	sys := entity.As(s.Store, entity.SystemDocket)
	ents, err := pendingQuestionsErr(sys)
	if err != nil {
		return
	}
	id := ref.Name()
	_, _, clock := mirror.DeadlineOf(p)
	for _, e := range ents {
		link := entity.Get(e.Payload, servesField)
		if entity.Str(link, "id") != id {
			continue
		}
		onward := e.Ref.Name()
		switch status := entity.Str(p, "status"); {
		case p == nil:
			s.withdrawOnward(onward, fmt.Sprintf("the ask it serves, %s, is gone", id))
		case status != mirror.QuestionPending:
			s.withdrawOnward(onward, fmt.Sprintf("the ask it serves, %s, has settled (%s)", id, status))
		case mirror.EditOf(link) != mirror.EditOf(p):
			s.withdrawOnward(onward, fmt.Sprintf("the ask it serves, %s, was asked again, as edit %d", id, mirror.EditOf(p)))
		case clock != "":
			limit, err := time.Parse(time.RFC3339Nano, clock)
			if err != nil {
				continue
			}
			due, err := time.Parse(time.RFC3339, entity.Str(e.Payload, "due"))
			if err == nil && !due.After(limit) {
				continue
			}
			if err := s.moveAskDue(onward, limit.UTC().Format(time.RFC3339)); err != nil {
				fmt.Fprintf(os.Stderr, "viff: onward ask %s: its clock was not moved inside %s's: %v\n", onward, id, err)
			}
		}
	}
}

// withdrawOnward withdraws an onward ask, saying why on its record: its wait is stopped with the
// reason, which the registry takes off the docket (Registry.OnDropCause), and one nothing here
// waits on is marked the same way directly.
func (s *Server) withdrawOnward(id, why string) {
	s.asks.mu.Lock()
	wait, waiting := s.asks.waits[id]
	s.asks.mu.Unlock()
	if waiting {
		// Not waited on: this runs on the watch, and the wait ending writes the docket.
		wait.cancel(errors.New(why))
		return
	}
	mirror.DropQuestion(entity.As(s.Store, entity.SystemDocket), id, why)
}

// withdrawRaisedBy withdraws every waiting onward ask principal raised, saying why: a resolver
// removed leaves nobody to carry them back.
func (s *Server) withdrawRaisedBy(principal, why string) {
	ents, err := pendingQuestionsErr(entity.As(s.Store, entity.SystemDocket))
	if err != nil {
		return
	}
	for _, e := range ents {
		if entity.Get(e.Payload, servesField) != nil && mirror.PrincipalFrom(e.Payload) == principal {
			s.withdrawOnward(e.Ref.Name(), why)
		}
	}
}
