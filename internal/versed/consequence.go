package versed

import (
	"cmp"
	"errors"
	"fmt"
	"os"

	"github.com/signadot/verse/pkg/codec"
	"github.com/signadot/verse/pkg/entity"
	"github.com/signadot/verse/pkg/launch"
	"github.com/signadot/verse/pkg/mirror"
	"github.com/signadot/verse/pkg/task"
)

// WHAT FOLLOWS A QUESTION RUNS AS WHOEVER ASKED, BEFORE THE ANSWER GOES BACK TO THEM. Only an
// asker who does not wait on the answer in the store writes an `on`: a person's ask, whose rows run
// as that person (`from`), and one a task raises for its owner (`verse-ask-create`), whose rows
// run as the owner, held within the task (taskBound). A rule's question carries none, since what
// a rule does about an answer is its action, and a task's carries none, since what a task does
// about an answer is its next step. The asker's wait is released once the row the answer meets
// has run (askon.go), and a spawn has run once it is launched: nobody waits for a task that
// follows an answer to finish.
//
// The record keeps it to once. The row marks the ask before it acts (`ran`), conditional on no
// mark standing, so a delivery that failed and is tried again, or a restart that finds the
// question answered, finds the row run rather than running it twice (followAsk).

// askerOf is the principal a question's `on` runs as, from its `from`: the principal that asked.
// Empty for a record that names none, which a question carrying an `on` never is.
func (s *Server) askerOf(p entity.Payload) string {
	return entity.Str(entity.Get(p, mirror.FromField), "principal")
}

// consequence claims a question for a resolution and runs what follows it, before the answer is
// delivered to whoever asked: the row of its `on` the answer meets. THE CLAIM IS THE ANSWER: the
// resolution is written on the record as `followed`, conditional on no claim standing, and the
// first written settles the question. Two answers landing together both come here, and the one
// whose write fails runs nothing — not another row, and not the same one twice — and delivers
// nothing (settle); what the record holds is then what a restart delivers, where the delivery had
// not yet taken (finishSettle), and what the asks' marks keep the row to once across
// (followAsk). A claim with nothing to run behind it costs a key on the record and
// nothing else.
//
// Answers whether this resolution claimed the question, and where it did not, the claim that
// holds it. An error is the store's: nothing is known, and nothing ran. store is the
// question's, as the system: a scope's question is recorded in its scope, and the claim with it.
func (s *Server) consequence(store entity.Store, id string, res task.Resolution) (claimed bool, standing askClaim, err error) {
	ref := mirror.AskRef(id)
	open, err := codec.ParseNode([]byte(`!not.has-path followed`), codec.Tony)
	if err != nil {
		return false, askClaim{}, err
	}
	sys := entity.As(store, entity.SystemDocket)
	claimed, _, err = sys.Write(entity.Write{
		At: ref.At(), Value: entity.Obj{"followed": mirror.ClaimRecord(res, s.now())}.Payload(),
		When: []entity.Require{{At: ref.At(), Pattern: entity.Present()}, {Pattern: open}},
	})
	if err != nil {
		return false, askClaim{}, err
	}
	if !claimed {
		standing, ok, err := standingClaim(store, id)
		if err != nil {
			return false, askClaim{}, err
		}
		if !ok {
			return false, askClaim{}, fmt.Errorf("%w: question %s is not posed", task.ErrNoSuchDecision, id)
		}
		return false, standing, nil
	}
	s.followAsk(ref)
	return true, askClaim{Resolution: res, pending: true}, nil
}

// askClaim is the resolution a question's claim holds, as the record says of it: whether the
// question still waits on it (pending), and whether its delivery failed (undelivered). A claim
// pending with no note is a settle between its claim and its delivery, or one a crash stopped
// there, which the posing finishes (finishSettle).
type askClaim struct {
	task.Resolution
	pending, undelivered bool
}

// of is whether res is the claim's own answer: the same choice by the same hand, whose retry
// it is.
func (c askClaim) of(res task.Resolution) bool {
	return c.By == res.By && c.Chosen() == res.Chosen()
}

// standingClaim reads the claim on a question's record, where the question is posed. ok is
// false where it is not; a question posed with no claim on it is the zero claim.
func standingClaim(store entity.Store, id string) (c askClaim, ok bool, err error) {
	ent, ok, err := entity.As(store, entity.SystemDocket).Get(mirror.AskRef(id))
	if err != nil || !ok {
		return askClaim{}, false, err
	}
	c.Resolution, _ = mirror.ClaimOf(ent.Payload)
	c.pending = entity.Str(ent.Payload, "status") == mirror.QuestionPending
	c.undelivered = entity.Get(entity.Get(ent.Payload, "followed"), "undelivered") != nil
	return c, true, nil
}

// delivered takes the undelivered note off a question's claim, once a delivery that had failed
// takes: the claim stays, since it is the answer.
func (s *Server) delivered(store entity.Store, id string) {
	ref := mirror.AskRef(id)
	if _, _, err := entity.As(store, entity.SystemDocket).Write(entity.Write{
		At: ref.At(), Value: entity.Obj{"followed": entity.Obj{"undelivered": entity.Removed()}}.Payload(),
		When: []entity.Require{{At: ref.At("followed"), Pattern: entity.Present()}},
	}); err != nil {
		fmt.Fprintf(os.Stderr, "verse: ask %s: that its answer was delivered was not written: %v\n", ref.Name(), err)
	}
}

// undelivered says on the question's claim that its answer has not reached whoever asked, and
// why: the claim stands, since it is the answer, and the delivery is tried again when the
// question is posed again (finishSettle).
func (s *Server) undelivered(store entity.Store, id string, err error) {
	ref := mirror.AskRef(id)
	if _, _, werr := entity.As(store, entity.SystemDocket).Write(entity.Write{
		At: ref.At(), Value: entity.Obj{"followed": entity.Obj{"undelivered": err.Error()}}.Payload(),
		When: []entity.Require{{At: ref.At(), Pattern: entity.Present()}},
	}); werr != nil {
		fmt.Fprintf(os.Stderr, "verse: ask %s: that its answer was not delivered was not written: %v\n", ref.Name(), werr)
	}
}

// taskBound is what an ask a task raised may not exceed in what follows it: the task's own
// launch, and the autonomy it runs under now. A task that follows a yes with a spawn hands it no
// more than it has itself (holds). launched is read back off the task's record at a restart, and
// is nil only where the record carries no `environment`.
//
// An edit of the ask is not held: a runner carries only the raise (relayedActs), so whoever edits
// the ask is its owner, and what follows it is theirs to change.
type taskBound struct {
	launched *launched
	autonomy task.Autonomy
}

// boundOf is the bound on what follows an ask task id raised: the task as this verse launched it,
// with the autonomy it runs under now.
func (s *Server) boundOf(id string) *taskBound {
	ht := s.hostedOf(id)
	return &taskBound{launched: ht.launched, autonomy: ht.autonomy}
}

// holds is why a then's spawn would reach beyond the task that raised the ask, or nil: its
// environment within what the task hands on (systems.Environment.Within, launched.handsOn), so
// what follows a task's ask installs no charter, as what a task's charter spawns does not
// (chartertask.go); its autonomy no looser
// (task.Autonomy.Within), and naming the task's credential where it spends one (spendsWithin).
// That it runs on the task's runner is the routing's to hold (route, with on).
func (b *taskBound) holds(spec launch.Spec) error {
	if b.launched == nil {
		return errors.New(": the raising task's record says no launch, so a then that spawns cannot be " +
			"held within it, and is refused")
	}
	if err := spec.Environment.Within(b.launched.handsOn()); err != nil {
		return fmt.Errorf(".environment.%w — what follows a task's ask reaches within the task, less %s",
			err, task.StepVerseCharterInstall)
	}
	if err := spec.Autonomy.Within(b.autonomy, spec.Environment); err != nil {
		return fmt.Errorf(".autonomy%w", err)
	}
	if !spendsWithin(spec, b.launched.virtualKey) {
		return fmt.Errorf(".virtualKey: %s, and the raising task spends %s: a then's spawn spends the "+
			"task's credential, named as the task's spawn named it", cmp.Or(spec.VirtualKey, "the runtime's default"),
			cmp.Or(b.launched.virtualKey, "the runtime's default"))
	}
	return nil
}
