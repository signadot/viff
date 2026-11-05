package versed

import (
	"fmt"
	"os"
	"sync"
	"time"

	"github.com/signadot/verse/pkg/entity"
	"github.com/signadot/verse/pkg/mirror"
)

// AN ASK GOES WHEN IT HAS SETTLED FOR LONG ENOUGH, whoever raised it: a person, a rule's gate, a
// task's question. Asks accumulate, and one that settled a month ago is clutter on the docket and
// in its asker's list, not history. So a settled ask — answered, withdrawn, orphaned, cancelled:
// anything but waiting — is given an expiry, when it settled plus its `keep`, and at that instant
// the system removes it. A task the row of its `on` started follows the task rules.
//
// Removed from what stands, not from the record of what happened: the store keeps its log as its
// own retention says (cmd/verse/docd.go), so `verse ask --watch --since` replays a removed ask for
// as long as the log does.
//
// The expiry is WRITTEN on the ask, where `verse ask` shows it, and the timer that acts on it is
// this process's, armed from a watch on the asks. The watch opens by replaying what stands, so
// the one subscription arms what a previous process left and what settles from here on.

// askKeepDefault is how long a settled ask stands when neither it nor the server says: a week,
// which is long enough to see how an ask came out, and short enough that a list of them stays a
// list.
const askKeepDefault = 7 * 24 * time.Hour

// askKeep is how long a settled ask stands when it says nothing itself: the server's AskKeep, or
// the default.
func (s *Server) askKeep() time.Duration {
	if s.AskKeep > 0 {
		return s.AskKeep
	}
	return askKeepDefault
}

// askSettled reports whether an ask has settled: any status but waiting, or, on a record from
// before every ask was posed, an answer.
//
// A SUSPENDED ASK HAS NOT SETTLED. A suspension withdraws a question to ask it again when it lifts
// (mirror.HoldQuestion), however long that takes, so it is kept, not counted as gone.
func askSettled(p entity.Payload) bool {
	if entity.Get(p, mirror.SuspendedField) != nil {
		return false
	}
	status := entity.Str(p, "status")
	return (status != "" && status != mirror.QuestionPending) || (status == "" && entity.Str(p, "answer") != "")
}

// askExpiryField is the instant a settled ask is removed at, RFC 3339.
const askExpiryField = "expires"

// expiryTimers is the expiry timers standing for this process, by the id of what each removes:
// a settled ask's, or a charter's (charterttl.go).
type expiryTimers struct {
	mu     sync.Mutex
	timers map[string]Timer
}

// set stands t for id, stopping the one it replaces.
func (e *expiryTimers) set(id string, t Timer) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.timers == nil {
		e.timers = map[string]Timer{}
	}
	if old := e.timers[id]; old != nil {
		old.Stop()
	}
	e.timers[id] = t
}

// drop stops id's timer, where one stands.
func (e *expiryTimers) drop(id string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if t := e.timers[id]; t != nil {
		t.Stop()
		delete(e.timers, id)
	}
}

// armAsks is the startup phase: follow every ask, the ones standing now included, and give each
// settled one its expiry, or its timer.
func (s *Server) armAsks() error {
	if err := s.awaiting(phaseSystem); err != nil {
		return err
	}
	ch, unsub, err := s.Store.WatchSubtree(len(entity.VerseRef(mirror.AskSystem).Segs())+1,
		entity.VerseRoot, mirror.AskSystem)
	if err != nil {
		return entity.Unread(fmt.Errorf("the asks could not be followed, so none was armed: %w", err))
	}
	ctx := s.ctxOrBackground()
	go func() {
		defer unsub()
		for {
			select {
			case <-ctx.Done():
				return
			case c, ok := <-ch:
				if !ok {
					return
				}
				for _, ev := range c.Changes {
					if !mirror.IsAsk(ev.Ref) {
						continue
					}
					if ev.Op == entity.Delete || ev.To == nil {
						s.dropExpiry(ev.Ref.Name())
						s.followServed(ev.Ref, nil)
						continue
					}
					s.expireLater(ev.Ref, ev.To)
					s.followServed(ev.Ref, ev.To)
					s.carryBack(ev.Ref, ev.To)
				}
			}
		}
	}()
	return nil
}

// expireLater is one ask as it stands: nothing while it waits; its expiry written when it has
// settled and has none; its timer armed when it has one.
func (s *Server) expireLater(ref entity.Ref, p entity.Payload) {
	if !askSettled(p) {
		s.dropExpiry(ref.Name()) // waiting, or waiting again: it goes only once it settles
		return
	}
	at := entity.Str(p, askExpiryField)
	if at == "" {
		keep := s.askKeep()
		if k, err := time.ParseDuration(entity.Str(p, "keep")); err == nil && k > 0 {
			keep = k
		}
		// Conditional on the ask standing, so an expiry is never what brings a removed ask back,
		// and on there being none yet: the watch can show the ask settled more than once before
		// this write is seen, and each pass would otherwise write its own instant, the later one
		// moving the expiry past a timer armed on the first. Only the first pass commits. The watch
		// delivers the write, and the timer is armed from it.
		if _, _, err := entity.As(s.Store, entity.SystemDefault).Write(entity.Write{
			At:    entity.Path{Ref: ref, Access: askExpiryField},
			Value: entity.Val(s.now().Add(keep).UTC().Format(time.RFC3339)),
			When:  []entity.Require{{At: ref.At(), Pattern: entity.Present()}, entity.Created()},
		}); err != nil {
			fmt.Fprintf(os.Stderr, "verse: ask %s: its expiry was not written: %v\n", ref.Name(), err)
		}
		return
	}
	when, err := time.Parse(time.RFC3339, at)
	if err != nil {
		fmt.Fprintf(os.Stderr, "verse: ask %s: its expiry %q is not a date, so it stands\n", ref.Name(), at)
		return
	}
	s.expiries.set(ref.Name(), s.at(when, func() { s.expireAsk(ref) }))
}

func (s *Server) dropExpiry(id string) { s.expiries.drop(id) }

// expireAsk removes an ask whose expiry has come. It reads the ask again first, so a timer
// outliving what armed it acts on the ask as it stands: one that is waiting again is not removed.
func (s *Server) expireAsk(ref entity.Ref) {
	sys := entity.As(s.Store, entity.SystemDefault)
	ent, ok, err := sys.Get(ref)
	if err != nil || !ok || !askSettled(ent.Payload) {
		return
	}
	when, err := time.Parse(time.RFC3339, entity.Str(ent.Payload, askExpiryField))
	if err != nil {
		return
	}
	if s.now().Before(when) {
		s.expireLater(ref, ent.Payload)
		return
	}
	if _, _, err := sys.Write(entity.Write{At: ref.At()}); err != nil {
		fmt.Fprintf(os.Stderr, "verse: ask %s: it was not removed: %v\n", ref.Name(), err)
	}
}
