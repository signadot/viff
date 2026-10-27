package versed

import (
	"errors"
	"fmt"
	"os"

	"github.com/signadot/verse/codec"
	"github.com/signadot/verse/entity"
	"github.com/signadot/verse/launch"
	"github.com/signadot/verse/mirror"
	"github.com/signadot/verse/systems"
	"github.com/signadot/verse/task"
	"github.com/signadot/verse/trigger"
)

// WHAT FOLLOWS A QUESTION RUNS AS WHOEVER ASKED, BEFORE THE ANSWER GOES BACK TO THEM. That is one
// rule for every kind of question, and `from` is what makes it one: a person's ask runs its `then`
// as that person, a rule's question as `via:` the rule, and a task's question as whom the task acts
// for, or as the task itself where it acts for nobody. The answer is given back — the firing
// resumed, the task's turn delivered, the asker's wait released — once the `then` has run, and a
// spawn has run once it is launched: nobody waits for a task that follows a yes to finish.
//
// The record keeps it to once. Each `then` marks the ask before it acts (`then-task`,
// `then-answered`, `then-asked`), conditional on no mark standing, so a delivery that failed and
// is tried again, or a restart that finds the question answered, finds the `then` done rather
// than doing it twice (followAsk).

// askerOf is the principal a question's `then` runs as, from its `from`: the principal that asked,
// `via:` the rule that asked, or the task's. Empty for a record that says none of the three, which
// verse never writes.
func (s *Server) askerOf(p entity.Payload) string {
	from := entity.Get(p, mirror.FromField)
	if who := entity.Str(from, "principal"); who != "" {
		return who
	}
	if rule := entity.Str(from, "rule"); rule != "" {
		return entity.Via(trigger.CharterRefAt(entity.Str(from, "at"), rule))
	}
	if id := entity.Str(from, "task"); id != "" {
		actsFor := ""
		if e, ok, err := s.Store.Get(mirror.Ref(id)); err == nil && ok {
			actsFor = entity.Str(e.Payload, "actsFor")
		}
		return thenPrincipal(actsFor, "", id)
	}
	return ""
}

// thenPrincipal is whom a task's `then` runs as: whom the task acts for, where it acts for
// somebody; else whoever launched it, where the caller knows (a rule's `via:`, at the spawn,
// before the task has a record); else the task itself — `via:` its record, which resolves to
// whoever owns it, as the launching rule's `via:` resolves to the charter's owner. The two spell
// one authority, so what the spawn checks is what the settle runs as.
func thenPrincipal(actsFor, launcher, taskID string) string {
	if actsFor != "" && !entity.IsSystem(actsFor) {
		return actsFor
	}
	if launcher != "" {
		return launcher
	}
	return entity.Via(mirror.Ref(taskID))
}

// consequence claims a question for a resolution and runs what follows it, before the answer is
// delivered to whoever asked: its `then` on a yes, its `else` otherwise. THE CLAIM IS THE ANSWER:
// the resolution is written on the record as `followed`, conditional on no claim standing, and
// the first written settles the question. Two answers landing together both come here, and the
// one whose write fails runs nothing — not the other field, and not the same one twice — and
// delivers nothing (settle); what the record holds is then what a restart delivers, where the
// delivery had not yet taken (finishSettle), and what the asks' marks keep the `then` to once
// across (followAsk). A claim with nothing to run behind it costs a key on the record and
// nothing else.
//
// Answers whether this resolution claimed the question, and where it did not, the resolution
// that holds it. An error is the store's: nothing is known, and nothing ran. store is the
// question's, as the system: a scope's question is recorded in its scope, and the claim with it.
func (s *Server) consequence(store entity.Store, id string, res task.Resolution) (claimed bool, standing task.Resolution, err error) {
	ref := mirror.AskRef(id)
	open, err := codec.ParseNode([]byte(`!not.has-path followed`), codec.Tony)
	if err != nil {
		return false, task.Resolution{}, err
	}
	sys := entity.As(store, entity.SystemDocket)
	claimed, _, err = sys.Write(entity.Write{
		At: ref.At(), Value: entity.Obj{"followed": mirror.ClaimRecord(res, s.now())}.Payload(),
		When: []entity.Require{{At: ref.At(), Pattern: entity.Present()}, {Pattern: open}},
	})
	if err != nil {
		return false, task.Resolution{}, err
	}
	if !claimed {
		ent, ok, err := sys.Get(ref)
		if err != nil {
			return false, task.Resolution{}, err
		}
		if !ok {
			return false, task.Resolution{}, fmt.Errorf("%w: question %s is not posed", task.ErrNoSuchDecision, id)
		}
		standing, _ = mirror.ClaimOf(ent.Payload)
		return false, standing, nil
	}
	s.followAsk(ref, followField(res))
	return true, res, nil
}

// followField is which of a question's consequences a resolution runs: `then` on a yes, by
// what it settled on (Chosen), so a resolution and its claim read one field.
func followField(res task.Resolution) string {
	if res.Chosen() == task.Proceed {
		return "then"
	}
	return "else"
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

// thenCheck is what an install checks a gate's `then` against, as the rule: the shape an ask's
// `then` has, and what it names, checked as a person's is when they raise an ask (checkThen). A
// rule has no reach of its own to hold a spawn within; the runner's offer and the owner's grants
// bound it, as they bound a person's.
func (s *Server) thenCheck() func(asker, field string, follow entity.Payload) error {
	return func(asker, field string, follow entity.Payload) error {
		return s.checkFollow(asker, field, follow, nil)
	}
}

// taskBound is what a task's question's `then` may not exceed: the task's own environment and
// autonomy. A task that follows a yes with a spawn hands it no more than it has itself (holds).
// The environment is nil for a task this verse launched before a restart, which it no longer
// knows the environment of.
type taskBound struct {
	environment *systems.Environment
	autonomy    task.Autonomy
}

// holds is why a then's spawn would reach beyond the task it follows, or nil: its environment
// within the task's (systems.Environment.Within) and its autonomy no looser (task.Autonomy.Within).
func (b *taskBound) holds(spec launch.Spec) error {
	if b.environment == nil {
		return errors.New(".environment: the asking task's environment is not known to this verse, which " +
			"launched it before a restart, so a then that spawns cannot be held within it; the row " +
			"carrying this spawn is refused until the task is run again")
	}
	if err := spec.Environment.Within(*b.environment); err != nil {
		return fmt.Errorf(".environment.%w", err)
	}
	if err := spec.Autonomy.Within(b.autonomy, spec.Environment); err != nil {
		return fmt.Errorf(".autonomy%w", err)
	}
	return nil
}

// checkFollow checks a `then` or an `else` (field) written on a gate or an ask row as one written
// on an ask is checked: one key naming a spawn, an answer or an ask, and what it names reachable
// by asker; and a spawn within the task it follows, where within names one.
func (s *Server) checkFollow(asker, field string, v entity.Payload, within *taskBound) error {
	now := s.now()
	follow, err := followRecord(field, v, asker, now)
	if err != nil {
		return err
	}
	if follow == nil {
		return fmt.Errorf("%s names nothing to run", field)
	}
	if _, err := s.checkThen(asker, follow, "", now, within); err != nil {
		return fmt.Errorf("%s.%w", field, err) // `then.spawn…`, as followRecord's own refusals read
	}
	return nil
}

// checkAutonomyThens checks the `then` and `else` of every ask row of an autonomy as the principal
// the task's questions' would run as (thenPrincipal), and within the task's own reach, where the
// task's author is present to hear what is wrong: at the spawn, and when the autonomy is replaced.
func (s *Server) checkAutonomyThens(asker string, p task.Autonomy, within *taskBound) error {
	for i, r := range p {
		if r.Then.Ask == nil {
			continue
		}
		for field, follow := range map[string]entity.Payload{"then": r.Then.Ask.Then, "else": r.Then.Ask.Else} {
			if follow == nil {
				continue
			}
			if err := s.checkFollow(asker, field, follow, within); err != nil {
				// By the row's name where it has one, which is how an operator knows it.
				return fmt.Errorf("%s.ask.%w", p.NameOf(i), err)
			}
		}
	}
	return nil
}
