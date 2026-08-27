package mirror

import (
	"strings"
	"time"

	"github.com/signadot/verse/entity"
	"github.com/signadot/verse/task"
)

// A gate question is published as an entity of its own, `gate:question:<id>`, rather than as a
// field on whatever raised it.
//
// Two things force that. A trigger gate is posed by a FIRING, before anything spawns — there is
// no task:instance to hang it on, and if the gate is denied there never will be. And a
// call_agent child raises its own questions from its own Task, which is never mirrored at
// all. Both are the same shape: the record needs somewhere to live, and "whose it is" does not
// answer that.
//
// The id is the question's own (`<origin>~d<n>`), so the ref carries the origin and a predicate
// can select one run's questions without a second field to match on.
const (
	QuestionSystem = "gate"
	QuestionKind   = "question"
)

// QuestionRef addresses one question.
func QuestionRef(id string) entity.Ref {
	return entity.VerseRef(QuestionSystem, QuestionKind, id)
}

// Question status, monotonic: a question is posed once and settles once.
const (
	QuestionPending  = "pending"
	QuestionAnswered = "answered"
	// QuestionOrphaned is a question that SETTLED WITHOUT A DECISION: nothing is waiting on
	// the answer any more, so nothing will read one. It settles rather than being deleted, for
	// the reason an answered one does — what was asked, and why it was never decided, is the
	// trail.
	//
	// The name says "the asker is gone" and that was once the only way to get here: a task
	// that died with a previous verse. It is no longer, and the difference matters to whoever
	// was going to answer. A task's loop now WITHDRAWS a standing question when an operator
	// intervenes — a say that redirects the run, a pause about to suspend it — and there the
	// asker is alive and somebody chose this. Same settling; not the same news.
	//
	// So `why` carries which it was, and a reader who needs to tell them apart reads that
	// rather than the status. A separate `withdrawn` status would be the alternative and is
	// deliberately not taken: it is a value a charter could match on, so it is a wire change,
	// and nothing yet needs to FILTER on the distinction — only to read it.
	QuestionOrphaned = "orphaned"
)

// PostQuestion publishes a posed question. `task` is the run that raised it, empty for a
// firing's gate — the origin is on the question either way, but a task id is what the docket
// and the flow tree index by.
//
// The derived effects are published as three flat lists rather than as the Effect structs.
// A charter matches on values, so `effects: {verbs: [write]}` has to be sayable without
// knowing the shape of a Go struct, and the three lists are what a gate predicate already
// speaks in (`GateMatch.Verbs`, `.Resources`, `.Paths`).
func PostQuestion(store entity.Store, p task.Pending, taskID string) {
	q := p.Question
	rec := entity.Obj{
		"id":      q.ID,
		"origin":  q.Origin,
		"gate":    q.Gate,
		"mode":    q.Mode.String(),
		"status":  QuestionPending,
		"posedAt": time.Now().UTC().Format(time.RFC3339),
	}
	// Absent rather than empty, for the reason the gate listing already compacts: a missing
	// key is visible, an empty one is camouflage. A bare `verse task run` is about nothing,
	// and a brokered gate legitimately carries neither origin nor mode.
	for k, v := range map[string]string{
		"subject": q.Subject,
		"spawns":  q.Spawns,
		"reason":  q.Reason,
		// The charter the asking rule is in. Absent for baseline and for a task's own gate,
		// where there is no charter to name rather than an unknown one.
		"at":   q.At,
		"tool": p.Tool,
		"task": taskID,
		// resolver is the BINDING that owns this question. Absent means UNBOUND — nobody was
		// declared, so it belongs to whoever reads the docket — which is a different fact
		// from an owner nobody can name, and a later reader depends on the distinction.
		"resolver": q.BoundTo,
	} {
		if v != "" {
			rec[k] = v
		}
	}
	if eff := effectLists(p.Effects); len(eff) > 0 {
		rec["effects"] = eff
	}
	clearPriorAnswer(store, QuestionRef(q.ID), rec)
	writeAt(store, QuestionRef(q.ID), rec)
}

// PostFrameQuestion publishes a question reported as a FRAME — the brokered path, where the
// worker ran the gate on its own machine and sent back what it chose to report.
//
// It writes less than PostQuestion because the frame carries less: derived effects stayed with
// the deriver that produced them, so `effects` is absent here rather than empty. That absence
// is the honest one — nobody classified this action WHERE THE RECORD IS BEING WRITTEN, which
// is different from classifying it to nothing.
//
// A local run posts twice, once from each writer. That is not a conflict: a write merges, so
// the frame's post cannot erase the effects the registry's post already recorded, and every
// field the two share they agree on.
func PostFrameQuestion(store entity.Store, ev task.Frame, taskID, subject, owner string) {
	rec := entity.Obj{
		"id":      ev.ID,
		"gate":    ev.Gate,
		"status":  QuestionPending,
		"posedAt": time.Now().UTC().Format(time.RFC3339),
	}
	// A QUESTION THAT HAS ALREADY SETTLED IS NOT RE-POSED BY A LATE FRAME.
	//
	// A local run posts twice — once from the registry (PostQuestion above) and once from the
	// frame — and the comment above says every field the two share they agree on. They do not
	// agree on `status` once an ANSWER lands between them: this write merges `pending` over
	// `answered` and leaves a record that says both, with a verdict, a `by` and an `answeredAt`
	// sitting under `status: pending`. A person reading the docket then sees a question to
	// decide, already stamped with who decided it.
	//
	// PostQuestion guards this with clearPriorAnswer, which is the RE-ASK case: a genuinely new
	// asking clears the old answer and marks `askedAgain`. This is not that — it is the same
	// asking, reported a second time — so the answer stands and only the fields that would
	// contradict it are dropped.
	//
	// It surfaced as a flaky test (git-issue nkptp9b3h12ksx3kg9n0): under load the answer lands
	// inside the window between the two posts, which on an unloaded machine it never does.
	//
	// SO THE TWO FIELDS THAT CAN CONTRADICT ARE WRITTEN CONDITIONALLY, and the rest is not.
	// `status` and `posedAt` land only while the record does not already say it settled;
	// everything else the frame carries is information no answer contradicts and is written
	// either way. Reading the status here and branching on it is the same race with a smaller
	// window — measured, and it narrowed the flake without closing it — so the store is asked
	// instead.
	//
	// IT DOES NOT CLOSE IT EITHER, and that is worth saying rather than leaving to be
	// rediscovered. The two writers are concurrent and the precondition is evaluated against a
	// view that can lag the answer by a few milliseconds, so a frame post can still be admitted
	// against a record that has just been answered. What is left needs the two paths ordered
	// rather than each checking, which is a question about the runtime and not about this
	// function; it stays on git-issue nkptp9b3h12ksx3kg9n0.
	ref := QuestionRef(ev.ID)
	posed := entity.Obj{"status": rec["status"], "posedAt": rec["posedAt"]}
	delete(rec, "status")
	delete(rec, "posedAt")
	// The origin is READ OFF the id, and only when the id is one verse minted (`<origin>~d<n>`).
	// A worker that raises a gate under an id of its own choosing has no origin — it decided
	// nothing about who answers — and inventing one by taking the id whole would put a name
	// there that means "the question itself".
	origin, _, minted := strings.Cut(ev.ID, task.GateSep)
	if !minted {
		origin = ""
	}
	for k, v := range map[string]string{
		"origin":   origin,
		"mode":     ev.Mode,
		"reason":   ev.Text,
		"tool":     ev.Tool,
		"task":     taskID,
		"subject":  subject,
		"resolver": owner,
	} {
		if v != "" {
			rec[k] = v
		}
	}
	writeAt(store, ref, rec)
	// Still pending, or not there at all — a brokered question whose only writer is this frame.
	// `!not` over the two settled values rather than an equality on `pending`, so a status this
	// does not know about is not silently treated as re-posable.
	_, _, _ = store.Write(entity.Write{
		At:    ref.At(),
		Value: posed.Payload(),
		When:  []entity.Require{notStatus(QuestionAnswered), notStatus(QuestionOrphaned)},
	})

}

// notStatus is the precondition "this record does not say status: s".
//
// Asked of the WHOLE payload rather than of the `status` field, which is what makes it true for
// a record that has no status yet — the brokered case, where this frame is the first writer.
// A field-level `!not` would be a non-match against an absent field and would refuse the very
// write that creates it.
func notStatus(s string) entity.Require {
	return entity.Require{Pattern: entity.Obj{"status": s}.Payload().WithTag("!not")}
}

// AnswerQuestion settles a question. It records rather than deletes: an answered question is
// the evidence trail — who decided, when, and what they were deciding — and a docket that
// forgets is one nobody can audit.
//
// This is written FROM the decision, after the fact. Nothing reads a verdict here and acts on
// it; the answer travelled out-of-band and this is its record. A reaction wired to this field
// would be the command entity the steering invariant forbids.
func AnswerQuestion(store entity.Store, id string, res task.Resolution) {
	rec := answerRecord(res)
	// REMOVED, not absent: a write MERGES, so an answer that was recorded as in-flight would
	// stay in-flight forever unless this says otherwise. Delivery is the news here.
	rec["delivered"] = entity.Removed()
	writeAt(store, QuestionRef(id), rec)
}

// QueuedAnswer records an answer that has been ACCEPTED but not yet delivered — taken by a
// broker for a worker that must dial in and pull it.
//
// The gate is closed either way: nobody else may answer, because two answers to one question
// is a race with one winner however slowly the answer travels. What is not yet true is that
// the party who must act on it knows. Recording that as done is what let an answer evaporate
// with a worker that never came back, while every surface said the question was settled.
//
// `delivered: false` is present only while that is the case; it is cleared when the worker's
// acknowledgement arrives. Absent means delivered, which is what it has always meant.
func QueuedAnswer(store entity.Store, id string, res task.Resolution) {
	rec := answerRecord(res)
	rec["delivered"] = false
	writeAt(store, QuestionRef(id), rec)
}

// Overrode records that an answer was taken against an exclusive binding's claim on the
// question. `by` already names who answered; this names whom they answered over, so the pair
// is on the record and neither half has to be inferred from a binding that may since have
// changed or gone.
func Overrode(store entity.Store, id, owner string) {
	writeAt(store, QuestionRef(id), entity.Obj{"overrode": owner})
}

func answerRecord(res task.Resolution) entity.Obj {
	verdict := "deny"
	if res.Proceed {
		verdict = "proceed"
	}
	rec := entity.Obj{
		"status":     QuestionAnswered,
		"verdict":    verdict,
		"answeredAt": time.Now().UTC().Format(time.RFC3339),
	}
	if res.By != "" {
		rec["by"] = res.By
	}
	if res.Note != "" {
		rec["why"] = res.Note
	}
	// What the answerer said alongside the verdict. It rides Steer rather than Note because
	// that is where an answer's prose goes — `verse gate approve --say "but not on friday"`
	// is an instruction to the task, and the record of a decision that omitted it would
	// keep the yes and lose the condition on it.
	if len(res.Steer) > 0 {
		say := make([]any, len(res.Steer))
		for i, s := range res.Steer {
			say[i] = s
		}
		rec["say"] = say
	}
	return rec
}

// OrphanQuestions settles every question a task left open, for a task that has ended.
// Nobody decided them and nobody now can: the asker is gone, and an answer would reach a run
// that is over.
//
// It is a settle, not a delete, for the reason an answered one is: what was asked, and the
// fact that it was never decided, is the trail. `orphaned` is the same word a restart uses
// for the same fact — the party who could have answered no longer exists.
func OrphanQuestions(store entity.Store, taskID string) {
	for _, ent := range entity.ListUnder(store, func(ref entity.Ref) bool {
		return IsQuestion(ref)
	}, entity.VerseRoot, QuestionSystem, QuestionKind) {
		p := entity.ToObj(ent.Payload)
		if p["task"] != taskID || p["status"] != QuestionPending {
			continue
		}
		writeAt(store, ent.Ref, entity.Obj{
			"status":     QuestionOrphaned,
			"orphanedAt": time.Now().UTC().Format(time.RFC3339),
			"why":        "the task that raised it ended without it being answered",
		})
	}
}

// DropQuestion settles one question whose asker stopped waiting for it, with `why` saying what
// stopping meant — the task ended, or its loop withdrew the question because an operator
// intervened. Either way there is nobody left to receive an answer.
//
// Same status as a task that ended holding open gates, because it is the same fact from a
// smaller scope: nothing will act on an answer to this. `why` is what separates the two for a
// reader, and it is the reason this takes one — a docket that says only `orphaned` reads as
// "the task died" for a run that is alive and was redirected.
func DropQuestion(store entity.Store, id, why string) {
	e, ok, err := store.Get(QuestionRef(id))
	if err != nil || !ok {
		return
	}
	// Only a still-pending one. An answer and a cancellation can cross, and the answer is
	// the fact worth keeping: it says what was decided, where this says only that nobody
	// will decide it now.
	if entity.ToObj(e.Payload)["status"] != QuestionPending {
		return
	}
	rec := entity.Obj{
		"status":     QuestionOrphaned,
		"orphanedAt": time.Now().UTC().Format(time.RFC3339),
	}
	if why != "" {
		// `why` and not `reason`: reason is the gate's own account of why it MATCHED, written
		// when the question was posed, and overwriting it would lose what was being asked in
		// favour of why nobody answered. `why` is where a settling's account already goes —
		// see answerRecord, which puts an answerer's note there.
		rec["why"] = why
	}
	writeAt(store, QuestionRef(id), rec)
}

// SettledBy reports who answered a question, and whether it is settled at all. It is the
// discriminator between "somebody beat you to it" and "there is no such question" — two facts
// that were one 404 before the record existed to tell them apart.
//
// A question nobody has answered reports false, as does one that was never posed: from the
// answerer's side those are the same, since neither is a decision they lost.
// The READ's failure is reported rather than folded into "nobody answered": this decides whether
// an answerer is told they lost a race, and an unreadable store saying "you did not" is the one
// wrong answer that cannot be retried into.
func SettledBy(store entity.Store, id string) (string, bool, error) {
	e, ok, err := store.Get(QuestionRef(id))
	if err != nil {
		return "", false, err
	}
	if !ok {
		return "", false, nil
	}
	p := entity.ToObj(e.Payload)
	if p["status"] != QuestionAnswered {
		return "", false, nil
	}
	by, _ := p["by"].(string)
	if by == "" {
		by = "an earlier answer"
	}
	return by, true, nil
}

// StaleAnswer records an answer that arrived after the question was settled. Losing a race is
// an outcome, not an error: the answerer decided something, and a docket that keeps only the
// winner cannot show that a question was contested.
//
// Each loser is written UNDER ITS OWN key rather than appended to a list, so two of them
// racing cannot lose each other — a merge of distinct keys needs no read-modify-write, and
// there is nothing here to serialize. One answerer who answers late twice overwrites their own
// entry, which is right: it is their answer, and the later one is what they think.
func StaleAnswer(store entity.Store, id string, res task.Resolution) {
	verdict := "deny"
	if res.Proceed {
		verdict = "proceed"
	}
	by := res.By
	if by == "" {
		by = "unattributed"
	}
	rec := entity.Obj{
		"verdict": verdict,
		"at":      time.Now().UTC().Format(time.RFC3339),
	}
	if len(res.Steer) > 0 {
		say := make([]any, len(res.Steer))
		for i, s := range res.Steer {
			say[i] = s
		}
		rec["say"] = say
	}
	// WRITTEN AT `stale`, NOT AT `stale.<by>`, because a write does not create the path it
	// descends through: nothing else writes this field, so the first stale answer to a question
	// would be descending through a key that does not stand yet. One step shallower reaches the
	// same document — the record keyed by who gave it, accumulating across answerers — and its
	// own parent is the question's payload, which stands.
	_, _, _ = store.Write(entity.Write{
		At:    QuestionRef(id).At("stale"),
		Value: entity.Obj{by: rec}.Payload(),
	})
}

// effectLists flattens derived effects into the three lists a gate predicate speaks in.
// Empty when an action classified to nothing, which is a real state: a question whose tool
// derived no effects still publishes its id, gate and subject, because a tool nothing could
// classify must not vanish from the docket.
func effectLists(effs []task.Effect) entity.Obj {
	var verbs, resources, paths []any
	seen := map[string]bool{}
	for _, e := range effs {
		if v := string(e.Verb); v != "" && !seen["v:"+v] {
			seen["v:"+v] = true
			verbs = append(verbs, v)
		}
		if r := e.Resource.Ref; r != "" {
			resources = append(resources, entity.Obj{"kind": string(e.Resource.Kind), "ref": r})
			if e.Resource.Kind == task.KindFile && !seen["p:"+r] {
				seen["p:"+r] = true
				paths = append(paths, r)
			}
		}
	}
	out := entity.Obj{}
	for k, v := range map[string][]any{"verbs": verbs, "resources": resources, "paths": paths} {
		if len(v) > 0 {
			out[k] = v
		}
	}
	return out
}

// clearPriorAnswer prepares a record that is being posed AGAIN under an id that already
// settled — a trigger gate's id is a function of (rule, gate, subjects), so a re-firing about
// the same subject re-poses the same one.
//
// Without it the write merges and the record says two things at once: pending, and answered by
// somebody with a verdict and a time. What a person then reads on the docket is a question to
// decide, already stamped with who decided it and what they said. Nil rather than absent,
// because a write merges and saying "that is gone" is the writer's job.
//
// It does NOT refuse the post. The record is a projection of a registry that is genuinely
// waiting for an answer, so a post that declined to write would leave a live question invisible
// on every surface — worse than the thing it fixed. The earlier answer is not lost either: it
// is at the commit that made it, which is what the store is for.
//
// `askedAgain` marks it, because "you have seen this before" is the fact a reader wants and the
// cleared fields can no longer say. The cause is upstream — a gate whose refusal writes nothing
// its subject reads, which advise names at install.
func clearPriorAnswer(store entity.Store, ref entity.Ref, rec entity.Obj) {
	e, ok, err := store.Get(ref)
	if err != nil || !ok {
		return
	}
	switch entity.Str(e.Payload, "status") {
	case QuestionAnswered, QuestionOrphaned:
	default:
		return
	}
	for _, k := range []string{"verdict", "by", "why", "say", "answeredAt", "delivered",
		"overrode", "reownedFrom", "reownedWhy", "reownedAt", "escalatesAt"} {
		rec[k] = entity.Removed()
	}
	rec["askedAgain"] = true
}

func writeAt(store entity.Store, ref entity.Ref, o entity.Obj) {
	_, _, _ = store.Write(entity.Write{At: ref.At(), Value: o.Payload()})
}

// IsQuestion reports whether an address is a posed question's — see IsTask on why a namespace
// answers this for itself now.
func IsQuestion(ref entity.Ref) bool {
	return ref.Reserved() && ref.System() == QuestionSystem && ref.Kind() == QuestionKind
}
