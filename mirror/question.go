package mirror

import (
	"strings"
	"time"

	"github.com/signadot/verse/entity"
	"github.com/signadot/verse/process"
)

// A gate question is published as an entity of its own, `gate:question:<id>`, rather than as a
// field on whatever raised it.
//
// Two things force that. A trigger gate is posed by a FIRING, before anything spawns — there is
// no process:instance to hang it on, and if the gate is denied there never will be. And a
// call_agent child raises its own questions from its own Process, which is never mirrored at
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
	return entity.Ref{System: QuestionSystem, Kind: QuestionKind, ID: id}
}

// Question status, monotonic: a question is posed once and settles once.
const (
	QuestionPending  = "pending"
	QuestionAnswered = "answered"
	// QuestionOrphaned is a question whose asker is gone: the process that raised it died
	// with a previous verse, so nothing is waiting on the answer and nothing will read one.
	// It settles rather than being deleted, for the reason an answered one does — what was
	// asked, and why it was never decided, is the trail.
	QuestionOrphaned = "orphaned"
)

// PostQuestion publishes a posed question. `process` is the run that raised it, empty for a
// firing's gate — the origin is on the question either way, but a process id is what the docket
// and the flow tree index by.
//
// The derived effects are published as three flat lists rather than as the Effect structs.
// A charter matches on values, so `effects: {verbs: [write]}` has to be sayable without
// knowing the shape of a Go struct, and the three lists are what a gate predicate already
// speaks in (`GateMatch.Verbs`, `.Resources`, `.Paths`).
func PostQuestion(store entity.Store, p process.Pending, processID string) {
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
	// key is visible, an empty one is camouflage. A bare `verse process run` is about nothing,
	// and a brokered gate legitimately carries neither origin nor mode.
	for k, v := range map[string]string{
		"subject": q.Subject,
		"spawns":  q.Spawns,
		"reason":  q.Reason,
		"tool":    p.Tool,
		"process": processID,
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
func PostFrameQuestion(store entity.Store, ev process.Frame, processID, subject, owner string) {
	rec := entity.Obj{
		"id":      ev.ID,
		"gate":    ev.Gate,
		"status":  QuestionPending,
		"posedAt": time.Now().UTC().Format(time.RFC3339),
	}
	// The origin is READ OFF the id, and only when the id is one verse minted (`<origin>~d<n>`).
	// A worker that raises a gate under an id of its own choosing has no origin — it decided
	// nothing about who answers — and inventing one by taking the id whole would put a name
	// there that means "the question itself".
	origin, _, minted := strings.Cut(ev.ID, process.GateSep)
	if !minted {
		origin = ""
	}
	for k, v := range map[string]string{
		"origin":   origin,
		"mode":     ev.Mode,
		"reason":   ev.Text,
		"tool":     ev.Tool,
		"process":  processID,
		"subject":  subject,
		"resolver": owner,
	} {
		if v != "" {
			rec[k] = v
		}
	}
	writeAt(store, QuestionRef(ev.ID), rec)
}

// AnswerQuestion settles a question. It records rather than deletes: an answered question is
// the evidence trail — who decided, when, and what they were deciding — and a docket that
// forgets is one nobody can audit.
//
// This is written FROM the decision, after the fact. Nothing reads a verdict here and acts on
// it; the answer travelled out-of-band and this is its record. A reaction wired to this field
// would be the command entity the steering invariant forbids.
func AnswerQuestion(store entity.Store, id string, res process.Resolution) {
	rec := answerRecord(res)
	// nil, not absent: a write MERGES, so an answer that was recorded as in-flight would
	// stay in-flight forever unless this says otherwise. Delivery is the news here.
	rec["delivered"] = nil
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
func QueuedAnswer(store entity.Store, id string, res process.Resolution) {
	rec := answerRecord(res)
	rec["delivered"] = false
	writeAt(store, QuestionRef(id), rec)
}

func answerRecord(res process.Resolution) entity.Obj {
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
	// is an instruction to the process, and the record of a decision that omitted it would
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

// OrphanQuestions settles every question a process left open, for a process that has ended.
// Nobody decided them and nobody now can: the asker is gone, and an answer would reach a run
// that is over.
//
// It is a settle, not a delete, for the reason an answered one is: what was asked, and the
// fact that it was never decided, is the trail. `orphaned` is the same word a restart uses
// for the same fact — the party who could have answered no longer exists.
func OrphanQuestions(store entity.Store, processID string) {
	for _, ent := range store.List(func(ref entity.Ref) bool {
		return ref.System == QuestionSystem && ref.Kind == QuestionKind
	}) {
		p := entity.ToObj(ent.Payload)
		if p["process"] != processID || p["status"] != QuestionPending {
			continue
		}
		writeAt(store, ent.Ref, entity.Obj{
			"status":     QuestionOrphaned,
			"orphanedAt": time.Now().UTC().Format(time.RFC3339),
		})
	}
}

// DropQuestion settles one question whose asker gave up on it — the waiter was cancelled, so
// there is nobody left to receive an answer.
//
// Same status as a process that ended holding open gates, because it is the same fact from a
// smaller scope: the party who could have acted on the answer is gone.
func DropQuestion(store entity.Store, id string) {
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
	writeAt(store, QuestionRef(id), entity.Obj{
		"status":     QuestionOrphaned,
		"orphanedAt": time.Now().UTC().Format(time.RFC3339),
	})
}

// SettledBy reports who answered a question, and whether it is settled at all. It is the
// discriminator between "somebody beat you to it" and "there is no such question" — two facts
// that were one 404 before the record existed to tell them apart.
//
// A question nobody has answered reports false, as does one that was never posed: from the
// answerer's side those are the same, since neither is a decision they lost.
func SettledBy(store entity.Store, id string) (string, bool) {
	e, ok, err := store.Get(QuestionRef(id))
	if err != nil || !ok {
		return "", false
	}
	p := entity.ToObj(e.Payload)
	if p["status"] != QuestionAnswered {
		return "", false
	}
	by, _ := p["by"].(string)
	if by == "" {
		by = "an earlier answer"
	}
	return by, true
}

// StaleAnswer records an answer that arrived after the question was settled. Losing a race is
// an outcome, not an error: the answerer decided something, and a docket that keeps only the
// winner cannot show that a question was contested.
//
// Each loser is written UNDER ITS OWN key rather than appended to a list, so two of them
// racing cannot lose each other — a merge of distinct keys needs no read-modify-write, and
// there is nothing here to serialize. One answerer who answers late twice overwrites their own
// entry, which is right: it is their answer, and the later one is what they think.
func StaleAnswer(store entity.Store, id string, res process.Resolution) {
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
	_, _, _ = store.Write(entity.Write{At: QuestionRef(id).At("stale", by), Value: rec.Payload()})
}

// effectLists flattens derived effects into the three lists a gate predicate speaks in.
// Empty when an action classified to nothing, which is a real state: a question whose tool
// derived no effects still publishes its id, gate and subject, because a tool nothing could
// classify must not vanish from the docket.
func effectLists(effs []process.Effect) entity.Obj {
	var verbs, resources, paths []any
	seen := map[string]bool{}
	for _, e := range effs {
		if v := string(e.Verb); v != "" && !seen["v:"+v] {
			seen["v:"+v] = true
			verbs = append(verbs, v)
		}
		if r := e.Resource.Ref; r != "" {
			resources = append(resources, entity.Obj{"kind": string(e.Resource.Kind), "ref": r})
			if e.Resource.Kind == process.KindFile && !seen["p:"+r] {
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

func writeAt(store entity.Store, ref entity.Ref, o entity.Obj) {
	_, _, _ = store.Write(entity.Write{At: ref.At(), Value: o.Payload()})
}
