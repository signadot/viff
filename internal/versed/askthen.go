package versed

import (
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"slices"
	"strings"
	"time"

	"github.com/signadot/tony-format/go-tony/ir"
	"github.com/signadot/verse/pkg/api"
	"github.com/signadot/verse/pkg/codec"
	"github.com/signadot/verse/pkg/entity"
	"github.com/signadot/verse/pkg/mirror"
	"github.com/signadot/verse/pkg/runner/runnerd"
)

// A `then` THAT ANSWERS OR ASKS: on the answer a row of `on` matches (askon.go), the asker answers
// another ask, or raises one, on their own authority, as `verse question --answer` and `verse ask`
// would for them. The answer lends them nothing: it is the condition, and what follows is theirs.
//
//	then: {answer: {ask: ship-3f2a…, choice: proceed, say: "ivan says the UI is ok"}}
//	then: {ask: {name: deploy, question: "Deploy it?", to: [ann@…], due: 4h, on: […]}}
//
// NOTHING IS SPAWNED. Each is one more act of the asker's, made by the route that makes it
// (answerQuestion, raiseAsk), so it is held to what the asker's own act would be when it is made.
//
// THE WORDS CONSENTED TO: an answer to a person's ask names the edit it answers, filled in when the
// asking is raised if it names none, so an ask reworded before the answer is not answered with
// words its asker never saw. Filled for a `then` nested in an ask's `on` too, when the first is
// raised.

// thenAnswerFields are the fields `then: {answer}` takes.
var thenAnswerFields = []string{"ask", "choice", "say", "force", "edit"}

// thenAnswer is `then: {answer: …}`, read.
type thenAnswer struct {
	Ask, Choice, Say string
	Force            bool
	Edit             int
}

// thenAnswerOf reads and checks the shape of `then: {answer: …}`: the ask it answers and the
// option it chooses, words to go with it, and force.
func thenAnswerOf(m entity.Obj) (thenAnswer, error) {
	var a thenAnswer
	if m == nil {
		return a, errors.New("names the ask it answers and the option it chooses: {ask: <id>, choice: <option>}")
	}
	for k := range m {
		if !slices.Contains(thenAnswerFields, k) {
			return a, fmt.Errorf("%q is not something an answer says (%s)", k, strings.Join(thenAnswerFields, ", "))
		}
	}
	var ok bool
	if a.Ask, ok = m["ask"].(string); !ok || a.Ask == "" {
		return a, errors.New("ask names the ask it answers, by id")
	}
	if a.Choice, ok = m["choice"].(string); !ok || a.Choice == "" {
		return a, errors.New("choice names the option it chooses")
	}
	if v, has := m["say"]; has {
		if a.Say, ok = v.(string); !ok {
			return a, errors.New("say is words")
		}
	}
	if v, has := m["force"]; has {
		if a.Force, ok = v.(bool); !ok {
			return a, errors.New("force is true or false")
		}
	}
	if v, has := m["edit"]; has {
		n, ok := codec.AsInt64(v)
		if !ok || n < 1 {
			return a, errors.New("edit is the edit of the ask it answers, 1 or more")
		}
		a.Edit = int(n)
	}
	return a, nil
}

// thenAskOf reads `then: {ask: …}` as the ask it raises, with its date as it would stand if raised
// now: a duration counts from the answer that raises it, and a date is taken as written.
func thenAskOf(v entity.Payload, now time.Time) (api.Ask, error) {
	var body api.Ask
	if err := codec.FromNodeStrict(v, &body); err != nil {
		return body, err
	}
	if body.Serves != "" || body.For != "" {
		return body, errors.New("an onward ask is a resolver's to raise, not a `then`'s")
	}
	if d, err := time.ParseDuration(body.Due); err == nil {
		if d <= 0 {
			return body, fmt.Errorf("due %s: a duration from the answer that raises it, and more than none", body.Due)
		}
		body.Due = now.Add(d).UTC().Format(time.RFC3339)
	}
	return body, nil
}

// checkThen checks what a `then` that answers or asks names against the verse as it stands, as
// asker, where the asker is present to hear what is wrong, and fills in the edit an answer
// consents to. self is the ask the `then` is on, where it has an id yet. It recurses through the
// rows of an ask's own `on`.
//
// WHAT FOLLOWS A TASK'S ASK ANSWERS NOTHING (within set). An answer is made as the task's owner,
// on any ask that waits on them, and another task's gate is one: a task allowed only to raise asks
// would settle a gate its owner was asked, by an ask that answers it on silence, with no person
// in the loop. A spawn has an environment and an autonomy to hold within the task; an answer has
// nothing to hold, so it is refused, here and in every `then` beneath.
func (s *Server) checkThen(asker string, then entity.Payload, self string, now time.Time, within *taskBound) (int, error) {
	if a := entity.Get(then, "answer"); a != nil {
		if within != nil {
			return http.StatusBadRequest, errors.New("then.answer: what follows a task's ask answers no ask: " +
				"the answer would be the owner's, and nothing holds it within the task")
		}
		return s.checkThenAnswer(asker, a, self)
	}
	if v := entity.Get(then, "spawn"); v != nil {
		return s.checkThenSpawn(asker, v, within)
	}
	q := entity.Get(then, "ask")
	if q == nil {
		return 0, nil
	}
	next, err := thenAskOf(q, now)
	if err != nil {
		return http.StatusBadRequest, fmt.Errorf("then.ask: %w", err)
	}
	_, answerers, err := askRecord(next, asker, now)
	if err != nil {
		return http.StatusBadRequest, fmt.Errorf("then.ask: %w", err)
	}
	if err := askAudience(s.Store, asker, next.About, answerers); err != nil {
		return http.StatusForbidden, fmt.Errorf("then.ask: %w", err)
	}
	var code int
	err = eachThen(q, func(row int, inner entity.Payload) error {
		c, err := s.checkThen(asker, inner, "", now, within)
		if err != nil {
			code = c
			return fmt.Errorf("ask.on[%d].%w", row, err)
		}
		return nil
	})
	return code, err
}

// checkThenSpawn checks a then's spawn as a run started by hand is checked, where the asker is
// present to hear what is wrong: as written (thenSpec), its autonomy's `$principal` asking the
// asker with answerers who can settle its asks, the credential it names readable by the asker,
// and what the runner it names offers — so a spawn the runner can't run is refused now rather
// than after the yes.
func (s *Server) checkThenSpawn(asker string, n entity.Payload, within *taskBound) (int, error) {
	spec, err := thenSpec(n)
	if err != nil {
		return http.StatusBadRequest, fmt.Errorf("spawn%w", err)
	}
	// As whom the then's task would act for: the asker where they are somebody, and nobody
	// where the asker is a rule or a task acting for nobody, so a row naming $principal there
	// is refused where its author can hear it.
	if err := mirror.CheckPrincipal(spec.Autonomy, mirror.ActsFor(asker)); err != nil {
		return http.StatusBadRequest, fmt.Errorf("spawn.autonomy: %w", err)
	}
	if err := s.engineOf("").CheckAutonomyAnswerers(spec.Autonomy); err != nil {
		return http.StatusBadRequest, fmt.Errorf("spawn.autonomy: %w", err)
	}
	store := entity.As(s.Store, asker)
	// WITHIN THE TASK THAT RAISED IT, where a task did: a task hands what follows its ask no more
	// reach, no looser a dial, no other runtime and no other credential than it has itself
	// (taskBound). Before the credential is resolved: the task's is held to by its address.
	var on *string
	if within != nil {
		if err := within.holds(spec); err != nil {
			return http.StatusBadRequest, fmt.Errorf("spawn%w", err)
		}
		on = &within.launched.runner
	}
	if spec.VirtualKey, err = credentialAt(store, asker, spec.VirtualKey); err != nil {
		return http.StatusBadRequest, fmt.Errorf("spawn.virtualKey: %w", err)
	}
	l, err := launchOf("then", spec, asker)
	if err == nil {
		err = s.fits(&l, on, runnerd.CheckOffer)
	}
	if err != nil {
		return http.StatusBadRequest, fmt.Errorf("spawn: %w", err)
	}
	return 0, nil
}

// setField sets an object node's field in place, adding it where the object has none.
func setField(obj *ir.Node, key string, val *ir.Node) {
	obj = ir.Uncomment(obj)
	for i, f := range obj.Fields {
		if f.String == key {
			val.Parent, val.ParentIndex, val.ParentField = obj, i, key
			obj.Values[i] = val
			return
		}
	}
	i := len(obj.Fields)
	obj.Fields = append(obj.Fields, &ir.Node{Type: ir.StringType, String: key, Parent: obj, ParentIndex: i, ParentField: key})
	val.Parent, val.ParentIndex, val.ParentField = obj, i, key
	obj.Values = append(obj.Values, val)
}

// checkThenAnswer checks an answer a `then` will make: the ask stands, the asker may read it, it
// is pending, it waits on them, and the option is one of its own; and fills in its edit.
func (s *Server) checkThenAnswer(asker string, n entity.Payload, self string) (int, error) {
	a, err := thenAnswerOf(entity.ToObj(n))
	if err != nil {
		return http.StatusBadRequest, fmt.Errorf("then.answer: %w", err)
	}
	if a.Ask == self {
		return http.StatusBadRequest, errors.New("then.answer: an ask does not answer itself")
	}
	as := entity.As(s.Store, asker)
	ent, found, err := as.Get(mirror.AskRef(a.Ask))
	switch {
	case errors.Is(err, entity.ErrForbidden):
		return http.StatusForbidden, fmt.Errorf("then.answer: %w", err)
	case err != nil:
		return http.StatusBadGateway, fmt.Errorf("then.answer: %w", err)
	case !found:
		return http.StatusNotFound, fmt.Errorf("then.answer: no ask %s stands", a.Ask)
	}
	if status := entity.Str(ent.Payload, "status"); status != mirror.QuestionPending {
		return http.StatusConflict, fmt.Errorf("then.answer: ask %s has settled (%s)", a.Ask, status)
	}
	// AND NO LOOP: what this answers may answer another when it settles, and so on, and a chain
	// that comes back to this ask would settle it from inside its own settle, since what follows a
	// question runs before its answer is delivered (consequence.go). Refused where the loop would
	// close, which is here: the ask being changed is the only one a standing chain can name. A
	// `then` nested in a `then` that asks (`then: {ask: {then: {answer: …}}}`) is checked with no
	// self, and cannot close one: the inner ask does not stand until the outer has settled, so
	// what it answers back to has settled already, and its answer is refused on the record.
	if self != "" {
		if path := s.answersBack(a.Ask, self); path != nil {
			return http.StatusBadRequest, fmt.Errorf("then.answer: ask %s comes back to this ask (%s answers %s): a loop",
				a.Ask, strings.Join(path, ", which answers "), self)
		}
	}
	if !waitsOn(as, asker, ent.Payload) {
		return http.StatusForbidden, fmt.Errorf("then.answer: ask %s does not wait on %s", a.Ask, asker)
	}
	var options []string
	for _, o := range mirror.OptionsOf(ent.Payload) {
		options = append(options, o.ID)
	}
	if !slices.Contains(options, a.Choice) {
		return http.StatusBadRequest, fmt.Errorf("then.answer: %q is not one of ask %s's options (%s)",
			a.Choice, a.Ask, strings.Join(options, ", "))
	}
	// A person's ask has edits; a rule's question is not asked again under its id, and has none.
	edit := mirror.EditOf(ent.Payload)
	if a.Edit != 0 && a.Edit != edit {
		return http.StatusConflict, fmt.Errorf("then.answer: ask %s stands at edit %d, not %d", a.Ask, edit, a.Edit)
	}
	if edit > 0 {
		setField(n, "edit", ir.FromInt(int64(edit)))
	}
	return 0, nil
}

// answersBack is the chain of asks from `from` to one a row of whose `on` answers self, each
// answering the next, or nil where none does: what a `then` answering `from` would come back
// through. Read as the system, since the chain runs through asks the asker may not read.
func (s *Server) answersBack(from, self string) []string {
	sys := entity.As(s.Store, entity.SystemDocket)
	seen := map[string]bool{}
	var walk func(id string, path []string) []string
	walk = func(id string, path []string) []string {
		if seen[id] {
			return nil
		}
		seen[id] = true
		ent, ok, err := sys.Get(mirror.AskRef(id))
		if err != nil || !ok {
			return nil
		}
		path = append(path, id)
		var found []string
		_ = eachThen(ent.Payload, func(_ int, then entity.Payload) error {
			next := entity.Str(entity.Get(then, "answer"), "ask")
			switch {
			case next == "":
				return nil
			case next == self:
				found = path
			default:
				found = walk(next, path)
			}
			if found != nil {
				return errFound
			}
			return nil
		})
		return found
	}
	return walk(from, nil)
}

// errFound stops a walk over an ask's rows once it has what it was looking for.
var errFound = errors.New("found")

// thenAnswers makes the answer the matching row of an answered ask's `on` names, as its asker,
// once: marked on the ask first, conditional on no mark standing, so a restart makes it where it
// was not made and not where it was; and what became of it is written there, as `ran.answered`.
func (s *Server) thenAnswers(ref entity.Ref, row int, asker string, m entity.Obj) {
	a, err := thenAnswerOf(m)
	if err != nil || !s.markRan(ref, row) {
		return
	}
	body := api.Resolve{ID: a.Ask, Choice: a.Choice, By: asker, Edit: a.Edit, Force: a.Force}
	if a.Say != "" {
		body.Say = []string{a.Say}
	}
	req, err := http.NewRequest(http.MethodPost, "/asks/"+url.PathEscape(a.Ask)+"/answer", nil)
	if err != nil {
		return
	}
	rec := httptest.NewRecorder()
	s.answerQuestion(rec, req, body, "", entity.As(s.Store, asker))
	out := entity.Obj{"ask": a.Ask, "at": s.now().UTC().Format(time.RFC3339), "status": rec.Code}
	if rec.Code >= 300 {
		out["refused"] = strings.TrimSpace(rec.Body.String())
	}
	s.writeRan(ref, "answered", out)
}

// thenAsks raises the ask the matching row of an answered ask's `on` names, as its asker, once,
// the way thenAnswers answers; what became of it is written as `ran.asked`.
func (s *Server) thenAsks(ref entity.Ref, row int, asker string, q entity.Payload) {
	if !s.markRan(ref, row) {
		return
	}
	now := s.now()
	out := entity.Obj{"at": now.UTC().Format(time.RFC3339)}
	body, err := thenAskOf(q, now)
	if err != nil {
		out["status"], out["refused"] = http.StatusBadRequest, err.Error()
		s.writeRan(ref, "asked", out)
		return
	}
	id, code, err := s.raiseAsk(entity.As(s.Store, asker), asker, body, now, "")
	out["status"] = code
	if err != nil {
		out["refused"] = err.Error()
	} else {
		out["id"] = id
	}
	s.writeRan(ref, "asked", out)
}

// markRan marks the row of an ask's `on` that matched as run, as `ran: {row, at}`, where no mark
// stands: whether this is the one that runs it. An ask settles once, so one row ever runs.
func (s *Server) markRan(ref entity.Ref, row int) bool {
	return s.markRanWith(ref, row, nil)
}

// markRanWith is markRan with what the mark carries beside the row and when: the task a spawn
// starts, whose id is minted before it runs.
func (s *Server) markRanWith(ref entity.Ref, row int, with entity.Obj) bool {
	unmarked, err := codec.ParseNode([]byte(`!not.has-path `+ranField), codec.Tony)
	if err != nil {
		return false
	}
	mark := entity.Obj{"row": row, "at": s.now().UTC().Format(time.RFC3339)}
	for k, v := range with {
		mark[k] = v
	}
	// ON THE RECORD THAT STANDS: a mark never creates a stub where the ask is not, as it would for
	// a question kept in a scope this store does not see.
	marked, _, err := entity.As(s.Store, entity.SystemDocket).Write(entity.Write{
		At:    ref.At(),
		Value: entity.Obj{ranField: mark}.Payload(),
		When:  []entity.Require{{At: ref.At(), Pattern: entity.Present()}, {Pattern: unmarked}},
	})
	return err == nil && marked
}

// ranField is where an ask's record says what its `on` ran (api.AskRan).
const ranField = "ran"

// writeRan writes what became of the row an ask's `on` ran, under ran.field.
func (s *Server) writeRan(ref entity.Ref, field string, out entity.Obj) {
	if _, _, err := entity.As(s.Store, entity.SystemDocket).Write(entity.Write{
		At: ref.At(), Value: entity.Obj{ranField: entity.Obj{field: out}}.Payload(),
	}); err != nil {
		fmt.Fprintf(os.Stderr, "verse: ask %s: what its on ran was not written: %v\n", ref.Name(), err)
	}
}
