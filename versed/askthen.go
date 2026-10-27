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

	"github.com/signadot/tony-format/go-tony/gomap"
	"github.com/signadot/tony-format/go-tony/ir"
	"github.com/signadot/verse/api"
	"github.com/signadot/verse/codec"
	"github.com/signadot/verse/entity"
	"github.com/signadot/verse/mirror"
)

// A `then` THAT ANSWERS OR ASKS: on a yes, the asker answers another ask, or raises one, on their
// own authority, as `verse question --answer` and `verse ask` would for them. The yes lends them
// nothing: it is the condition, and what follows is theirs.
//
//	then: {answer: {ask: ship-3f2a…, choice: proceed, say: "david says the UI is ok"}}
//	then: {ask: {name: deploy, question: "Deploy it?", to: [ann@…], due: 4h, then: {…}}}
//
// NOTHING IS SPAWNED. Each is one more act of the asker's, made by the route that makes it
// (answerQuestion, raiseAsk), so it is held to what the asker's own act would be when it is made.
//
// THE WORDS CONSENTED TO: an answer to a person's ask names the edit it answers, filled in when the
// asking is raised if it names none, so an ask reworded before the yes is not answered with words
// its asker never saw. Filled for a `then` nested in an ask's `then` too, when the first is raised.

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
// now: a duration counts from the yes that raises it, and a date is taken as written.
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
			return body, fmt.Errorf("due %s: a duration from the yes that raises it, and more than none", body.Due)
		}
		body.Due = now.Add(d).UTC().Format(time.RFC3339)
	}
	return body, nil
}

// checkThen checks what a `then` that answers or asks names against the verse as it stands, as
// asker, where the asker is present to hear what is wrong, and fills in the edit an answer
// consents to. self is the ask the `then` is on, where it has an id yet. It recurses through an
// ask's own `then`.
func (s *Server) checkThen(asker string, then map[string]any, self string, now time.Time, within *taskBound) (int, error) {
	if m, ok := asMap(then["answer"]); ok {
		return s.checkThenAnswer(asker, m, self)
	}
	if v, ok := then["spawn"]; ok {
		return s.checkThenSpawn(asker, v, within)
	}
	m, ok := asMap(then["ask"])
	if !ok {
		return 0, nil
	}
	next, err := thenAskOf(entity.Obj(m).Payload(), now)
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
	for _, field := range []string{"then", "else"} {
		if inner, ok := asMap(m[field]); ok {
			if code, err := s.checkThen(asker, inner, "", now, within); err != nil {
				return code, fmt.Errorf("ask.%s: %w", field, err)
			}
		}
	}
	return 0, nil
}

// checkThenSpawn checks a then's spawn as a run started by hand is checked, where the asker is
// present to hear what is wrong: as written (thenSpec), its autonomy's `$principal` asking the
// asker with answerers who can settle its asks, the credential it names readable by the asker,
// and what the runner it names offers — so a spawn the runner can't run is refused now rather
// than after the yes.
func (s *Server) checkThenSpawn(asker string, v any, within *taskBound) (int, error) {
	n, ok := v.(*ir.Node)
	if !ok {
		var err error
		if n, err = gomap.ToTonyIR(v); err != nil {
			return http.StatusBadRequest, fmt.Errorf("spawn: %w", err)
		}
	}
	spec, err := thenSpec(n)
	if err != nil {
		return http.StatusBadRequest, fmt.Errorf("spawn%w", err)
	}
	// WITHIN THE TASK IT FOLLOWS, where one asked: a task hands what follows its yes no more
	// reach and no looser a dial than it has itself (taskBound).
	if within != nil {
		if err := within.holds(spec); err != nil {
			return http.StatusBadRequest, fmt.Errorf("spawn%w", err)
		}
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
	if spec.VirtualKey, err = credentialAt(store, asker, spec.VirtualKey); err != nil {
		return http.StatusBadRequest, fmt.Errorf("spawn.virtualKey: %w", err)
	}
	l, err := launchOf("then", spec, asker)
	if err == nil {
		err = s.checkOffer(store, spec.Runner, &l)
	}
	if err != nil {
		return http.StatusBadRequest, fmt.Errorf("spawn: %w", err)
	}
	return 0, nil
}

// asMap reads a value of a `then` as the object it is, however it was built.
func asMap(v any) (map[string]any, bool) {
	switch m := v.(type) {
	case entity.Obj:
		return m, m != nil
	case map[string]any:
		return m, m != nil
	}
	return nil, false
}

// checkThenAnswer checks an answer a `then` will make: the ask stands, the asker may read it, it
// is pending, it waits on them, and the option is one of its own; and fills in its edit.
func (s *Server) checkThenAnswer(asker string, m map[string]any, self string) (int, error) {
	a, err := thenAnswerOf(m)
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
		m["edit"] = edit
	}
	return 0, nil
}

// answersBack is the chain of asks from `from` to one whose `then` or `else` answers self, each
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
		for _, field := range []string{"then", "else"} {
			next := entity.Str(entity.Get(entity.Get(ent.Payload, field), "answer"), "ask")
			if next == "" {
				continue
			}
			if next == self {
				return path
			}
			if p := walk(next, path); p != nil {
				return p
			}
		}
		return nil
	}
	return walk(from, nil)
}

// thenAnswers makes the answer an answered ask's `then` names, as its asker, once: marked on the
// ask first, conditional on no mark standing, so a restart makes it where it was not made and not
// where it was; and what became of it is written there, as `then-answered`.
func (s *Server) thenAnswers(ref entity.Ref, field, asker string, m entity.Obj) {
	a, err := thenAnswerOf(m)
	if err != nil || !s.markThen(ref, field+"-answered") {
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
	s.writeThen(ref, field+"-answered", out)
}

// thenAsks raises the ask an answered ask's `then` names, as its asker, once, the way thenAnswers
// answers; what became of it is written as `then-asked`.
func (s *Server) thenAsks(ref entity.Ref, field, asker string, q entity.Payload) {
	if !s.markThen(ref, field+"-asked") {
		return
	}
	now := s.now()
	out := entity.Obj{"at": now.UTC().Format(time.RFC3339)}
	body, err := thenAskOf(q, now)
	if err != nil {
		out["status"], out["refused"] = http.StatusBadRequest, err.Error()
		s.writeThen(ref, field+"-asked", out)
		return
	}
	id, code, err := s.raiseAsk(entity.As(s.Store, asker), asker, body, now)
	out["status"] = code
	if err != nil {
		out["refused"] = err.Error()
	} else {
		out["id"] = id
	}
	s.writeThen(ref, field+"-asked", out)
}

// markThen marks an ask's `then` as made, under field, where no mark stands: whether this is the
// one that makes it.
func (s *Server) markThen(ref entity.Ref, field string) bool {
	unmarked, err := codec.ParseNode([]byte(`!not.has-path `+field), codec.Tony)
	if err != nil {
		return false
	}
	// ON THE RECORD THAT STANDS: a mark never creates a stub where the ask is not, as it would for
	// a question kept in a scope this store does not see.
	marked, _, err := entity.As(s.Store, entity.SystemDocket).Write(entity.Write{
		At:    ref.At(),
		Value: entity.Obj{field: entity.Obj{"at": s.now().UTC().Format(time.RFC3339)}}.Payload(),
		When:  []entity.Require{{At: ref.At(), Pattern: entity.Present()}, {Pattern: unmarked}},
	})
	return err == nil && marked
}

// writeThen writes what became of an ask's `then` on the ask.
func (s *Server) writeThen(ref entity.Ref, field string, out entity.Obj) {
	if _, _, err := entity.As(s.Store, entity.SystemDocket).Write(entity.Write{
		At: ref.At(), Value: entity.Obj{field: out}.Payload(),
	}); err != nil {
		fmt.Fprintf(os.Stderr, "verse: ask %s: what its then came to was not written: %v\n", ref.Name(), err)
	}
}
