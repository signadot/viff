package versed

import (
	"bytes"
	"cmp"
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/signadot/tony-format/go-tony/ir"
	"github.com/signadot/verse/pkg/api"
	"github.com/signadot/verse/pkg/codec"
	"github.com/signadot/verse/pkg/entity"
	"github.com/signadot/verse/pkg/launch"
	"github.com/signadot/verse/pkg/mirror"
	"github.com/signadot/verse/pkg/runner"
	"github.com/signadot/verse/pkg/task"
)

// thenKinds is what may follow a yes, by the key that names it. One key per kind, so what runs
// is said by which key is there, and a record never carries two things to do. A prompt or a
// spawn is a task written as a charter's spawn is (thenSpec).
var thenKinds = map[string]bool{"spawn": true, "answer": true, "ask": true}

// askGate is the gate a person's ask poses under: the ask is the question, and nothing else
// asked it.
const askGate = "ask"

// asksWaiting is the person's asks this process waits on, by id: what withdrawing one stops.
type asksWaiting struct {
	mu    sync.Mutex
	waits map[string]askWait
}

type askWait struct {
	cancel context.CancelCauseFunc
	done   chan struct{}
}

// postAsk is `verse ask <name> -q …`: an ask written at verse.ask.<id> as its asker's record, and
// posed on the docket by being written. No rule stands between the two: the verb writes the ask,
// and versed waits on it as a task waits on its own question.
//
// CHECKED HERE, where the asker is present: the names in the grammar a grant uses, a date in the
// future, and an `on` of rows, each a match tony can apply and a `then` of one key this verse knows
// how to run. And READ STRICTLY: a key the body
// does not know is refused, since an ask whose `to` was dropped for being misspelled, or spelled as
// it once was (`answerers`), names nobody and is put to everyone.
//
// THE RECORD BEFORE THE ASK. Who reads an ask, and who answers it, is said on its own record, so
// the record is written first: an ask is never readable more widely than its asker said, not even
// for the commit that made it. Written first safely because the id is minted here, so no name
// somebody else could take is claimed by a record whose ask then failed to land.
func (s *Server) postAsk(w http.ResponseWriter, r *http.Request) {
	var body api.Ask
	if err := codec.ReadStrict(r, &body); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	store := s.scopedStore(r)
	asker, ok := actor(w, store, "")
	if !ok {
		return
	}
	id, code, err := s.raiseAsk(store, asker, body, s.now(), relayedTask(r))
	if err != nil {
		http.Error(w, err.Error(), code)
		return
	}
	writeOut(w, r, http.StatusCreated, api.Asked{ID: id})
}

// raiseAsk is an ask checked, written and posed, as asker through store: what POST /asks does, and
// what a `then` of `{ask}` does for its asker on the answer it follows (askthen.go). It answers the ask's id, or
// the status a refusal takes and why.
//
// A TASK'S RAISE is one its runner carried for it, as its owner (relayTask): taskID names the task,
// and the ask's `from` says so beside the owner. What the rows of its `on` spawn is held within
// the task (taskBound), so a task hands what follows its ask no more reach and no looser a dial
// than it has itself.
func (s *Server) raiseAsk(store entity.Store, asker string, body api.Ask, now time.Time, taskID string) (string, int, error) {
	rec, answerers, err := askRecord(body, asker, now)
	if err != nil {
		return "", http.StatusBadRequest, err
	}
	var within *taskBound
	if taskID != "" {
		from := mirror.FromPrincipal(asker)
		from["task"] = taskID
		rec[mirror.FromField] = from
		within = s.boundOf(taskID)
	}
	if body.Serves != "" {
		if code, err := s.onward(rec, body, asker); err != nil {
			return "", code, err
		}
	} else if body.For != "" {
		return "", http.StatusBadRequest, errors.New("`for` names whom an onward ask is raised for, and goes with `serves`")
	}
	if code, err := s.checkOn(asker, rec, "", now, within); err != nil {
		return "", code, err
	}
	if err := askAudience(s.Store, asker, body.About, answerers); err != nil {
		return "", http.StatusForbidden, err
	}
	id, err := spawnID(api.Spawn{Name: body.Name})
	if err != nil {
		return "", http.StatusBadRequest, err
	}
	ref := mirror.AskRef(id)
	// Its asker's record — and a resolver's ask is whoever installed it's, since a resolver acts
	// as them; `from.principal` still says the resolver raised it.
	owner := asker
	if g, err := entity.ParseGrantee(asker); err == nil && g.Kind == entity.ViaKind {
		if owner, err = entity.OwnerOf(s.Store, entity.RefAt(g.Path...)); err != nil || owner == "" {
			return "", http.StatusForbidden, fmt.Errorf("%s acts as nobody: it has no owner on record", asker)
		}
	}
	if err := entity.Establish(s.Store, ref, owner, entity.Ref{}, askGrants(s.Store, ref, asker, body.About, answerers)); err != nil {
		return "", http.StatusBadGateway, err
	}
	storeOn(rec)
	committed, _, err := store.Write(entity.Write{
		At: ref.At(), Value: rec.Payload(), When: []entity.Require{entity.Created()},
	})
	if errors.Is(err, entity.ErrForbidden) {
		return "", http.StatusForbidden, err
	} else if err != nil {
		return "", http.StatusBadGateway, fmt.Errorf("ask: %w", err)
	}
	if !committed {
		return "", http.StatusConflict, fmt.Errorf("ask %s stands already", id)
	}
	s.awaitAsk(askQuestion(id, rec.Payload(), body.About))
	return id, http.StatusCreated, nil
}

// checkOn checks each `then` of the rows of the `on` an ask's record holds against the verse as it
// stands (checkThen), as an ask raised or changed by asker; self is the ask's id, where it has one.
func (s *Server) checkOn(asker string, rec entity.Obj, self string, now time.Time, within *taskBound) (int, error) {
	on, _ := rec["on"].(*ir.Node)
	var code int
	err := eachThen(entity.Obj{"on": on}.Payload(), func(row int, then entity.Payload) error {
		c, err := s.checkThen(asker, then, self, now, within)
		if err != nil {
			code = c
			return fmt.Errorf("on[%d].%w", row, err)
		}
		return nil
	})
	return code, err
}

// followRecord checks what follows an answer, a row's `then`, as field names it, and answers it as
// the record keeps it: one key naming one thing to run.
func followRecord(field string, follow entity.Payload, asker string, now time.Time) (entity.Obj, error) {
	fields := entity.Fields(follow)
	if len(fields) != 1 {
		return nil, fmt.Errorf("%s names one thing to run, as one key (spawn, answer, ask); it has %d", field, len(fields))
	}
	for k, v := range fields {
		if !thenKinds[k] {
			return nil, fmt.Errorf("%s: %q is not something an ask runs (spawn, answer, ask)", field, k)
		}
		switch k {
		case "spawn":
			if _, err := thenSpec(v); err != nil {
				return nil, fmt.Errorf("%s.spawn%w", field, err)
			}
			return entity.Obj{k: v.Clone()}, nil
		case "answer":
			if _, err := thenAnswerOf(entity.ToObj(v)); err != nil {
				return nil, fmt.Errorf("%s.answer: %w", field, err)
			}
			return entity.Obj{k: entity.ToObj(v)}, nil
		case "ask":
			next, err := thenAskOf(v, now)
			if err != nil {
				return nil, fmt.Errorf("%s.ask: %w", field, err)
			}
			if _, _, err := askRecord(next, asker, now); err != nil {
				return nil, fmt.Errorf("%s.ask: %w", field, err)
			}
			// AS WRITTEN, a node: the rows of its own `on` hold match patterns, whose tags a Go map
			// would drop.
			return entity.Obj{k: v.Clone()}, nil
		}
	}
	return nil, nil
}

// thenSpec reads a then's spawn: a spawn as a charter writes one (launch.Spec), read strictly and
// meaning only what it says, written by hand (launch.Spec.CheckByHand), and of the shape a runner
// checks (runner.Launch.Check). Its task is about the ask, so it names no subject of its own. A
// refusal starts with the field it is about (`: …` for the whole, `.environment…` for a key).
func thenSpec(n *ir.Node) (launch.Spec, error) {
	var spec launch.Spec
	var b bytes.Buffer
	err := codec.EncodeNode(&b, n, codec.Tony)
	if err == nil {
		err = codec.UnmarshalStrict(b.Bytes(), &spec, codec.Tony)
	}
	if err != nil {
		return spec, fmt.Errorf(": %w", err)
	}
	if spec.Subject != "" {
		return spec, errors.New(".subject: a then's task is about its ask")
	}
	if err := spec.CheckByHand(); err != nil {
		return spec, fmt.Errorf(".%w", err)
	}
	l, err := launchOf("then", spec, "")
	if err == nil {
		err = l.Check()
	}
	if err == nil {
		err = runner.CheckReach(l.Autonomy, l.Environment)
	}
	if err != nil {
		return spec, fmt.Errorf(": %w", err)
	}
	return spec, nil
}

// askGrants is who reads an ask and who answers it: read by its asker and whom it asks, and
// answered by whom it asks. An ask naming nobody is anyone's to read and answer, a vote.
//
// AN ASK ABOUT SOMETHING is read as that thing is, and where it names nobody answered by whoever
// may write it (9ce3bva0, D3): its asker reads it as its owner, and whom it asks may read the
// subject (askAudience).
//
// AND BY THE RESOLVERS SERVING IT: `answering:` each of its parties (mirror.AnsweringReaders).
func askGrants(store entity.Store, ref entity.Ref, asker, about string, answerers []string) []entity.Grant {
	readers, answer := append([]string{asker}, answerers...), answerers
	if len(answerers) == 0 {
		readers, answer = []string{entity.Everyone}, []string{entity.UserKind + ":*", entity.AgentKind + ":*"}
	}
	if about != "" {
		if at, err := entity.ParseAddr(about); err == nil {
			readers = []string{entity.Audience(at)}
			if len(answerers) == 0 {
				answer = []string{entity.Audience(at)}
			}
		}
	}
	readers = append(readers, mirror.AnsweringReaders(store, about, answerers)...)
	return []entity.Grant{{At: ref.Segs(), To: map[entity.Level][]string{
		entity.Read: readers, entity.ReadWrite: answer,
	}}}
}

// askAudience refuses an ask about something its asker, or anybody it asks, may not read: its
// record says what it is about, and nobody answers what they cannot read (9ce3bva0, D3).
func askAudience(store entity.Store, asker, about string, answerers []string) error {
	if about == "" {
		return nil
	}
	if err := mirror.CheckAudience(store, about, []string{asker}); err != nil {
		return fmt.Errorf("ask about %s: its asker %w", about, err)
	}
	if err := mirror.CheckAudience(store, about, answerers); err != nil {
		return fmt.Errorf("ask about %s: %w", about, err)
	}
	return nil
}

// askRecord is the ask as it is written, checked: the record, and the answerers it names in the
// grammar's spelling.
func askRecord(body api.Ask, asker string, now time.Time) (entity.Obj, []string, error) {
	if strings.TrimSpace(body.Question) == "" {
		return nil, nil, fmt.Errorf("an ask asks something: question is required")
	}
	rec := entity.Obj{"name": body.Name, mirror.FromField: mirror.FromPrincipal(asker), "question": body.Question, "edit": 1}
	var answerers []string
	for _, n := range body.To {
		g, err := entity.ParseGrantee(n)
		if err != nil {
			return nil, nil, fmt.Errorf("to: %w", err)
		}
		answerers = append(answerers, g.String())
	}
	if len(answerers) > 0 {
		rec[mirror.ToField] = entity.Obj{"grantees": answerers}
	}
	if body.Due != "" {
		due, err := time.Parse(time.RFC3339, body.Due)
		if err != nil {
			return nil, nil, fmt.Errorf("due %q: an RFC 3339 date", body.Due)
		}
		if !due.After(now) {
			return nil, nil, fmt.Errorf("due %s is not in the future", body.Due)
		}
		rec["due"] = due.UTC().Format(time.RFC3339)
		// WHAT SILENCE SETTLES ON is the asker's to say, and deny unless they say proceed: nobody
		// answering is not somebody agreeing unless the asker said so.
		rec[onSilenceField] = task.Deny
	}
	switch body.OnSilence {
	case "":
	case task.Proceed, task.Deny:
		if body.Due == "" {
			return nil, nil, fmt.Errorf("onSilence %s: silence settles an ask at its date, and this one has no due", body.OnSilence)
		}
		rec[onSilenceField] = body.OnSilence
	default:
		return nil, nil, fmt.Errorf("onSilence %q: what silence settles on is one of the ask's options, %s or %s",
			body.OnSilence, task.Proceed, task.Deny)
	}
	on, err := onRecord(body.On, asker, now)
	if err != nil {
		return nil, nil, err
	}
	if on != nil {
		rec["on"] = on
	}
	if err := task.CheckLabels(body.Labels); err != nil {
		return nil, nil, err
	}
	if len(body.Labels) > 0 {
		labels := entity.Obj{}
		for k, v := range body.Labels {
			labels[k] = v
		}
		rec[mirror.LabelsField] = labels
	}
	if body.Keep != "" {
		keep, err := time.ParseDuration(body.Keep)
		if err != nil || keep <= 0 {
			return nil, nil, fmt.Errorf("keep %q: how long it stands once settled, as a duration (168h)", body.Keep)
		}
		rec["keep"] = keep.String()
	}
	if body.About != "" {
		if _, err := entity.ParseAddr(body.About); err != nil {
			return nil, nil, fmt.Errorf("about: %w", err)
		}
	}
	return rec, answerers, nil
}

// askQuestion is a person's ask as the docket poses it: the ask is the question, its words the
// pose, its answerers whom it waits on, and its date the clock, silence by then settling on what
// its record says (onSilenceField).
// about is what it is about, where the verb was told; a re-pose reads it off the record.
func askQuestion(id string, p entity.Payload, about string) task.GateQuestion {
	if about == "" {
		about = entity.Str(p, "subject")
	}
	q := task.GateQuestion{
		ID: id, Gate: askGate,
		Pose: entity.Str(p, "question"), Reason: entity.Str(p, "question"),
		Subject: about, Answerers: mirror.GranteesTo(p), Edit: askEdit(p), Labels: mirror.LabelsOf(p),
	}
	if entity.Get(p, "options") != nil {
		q.Options = mirror.OptionsOf(p)
	}
	if due := entity.Str(p, "due"); due != "" {
		if at, err := time.Parse(time.RFC3339, due); err == nil {
			q.Deadline = &task.Deadline{At: at.UTC().Format(time.RFC3339Nano), Default: askSilence(p)}
		}
	}
	return q
}

// onSilenceField is what an ask settles on when its clock passes: deny unless its asker said
// yes (api.Ask.OnSilence), and for an onward ask the served ask's own, so silence carries back
// what silence would have settled there.
const onSilenceField = "onSilence"

// askSilence is what an ask's silence settles on: its record's, or no where the record predates
// saying so.
func askSilence(p entity.Payload) string {
	if entity.Get(p, onSilenceField) != nil {
		return entity.Str(p, onSilenceField)
	}
	return task.Deny
}

// servesField links an onward ask to the ask it serves: `{id, edit, for}`.
const servesField = "serves"

// onward checks an onward ask (api.Ask.Serves) and writes onto rec what follows from the ask it
// serves, answering the status a refusal takes (9ce3bva0, stage 4).
//
// ONLY THE RESOLVER SERVING IT RAISES ONE (Q5), made as that resolver, for a party it serves the
// ask for. Its options are the served ask's, each settled by one ballot — whoever it asks
// answers for the party, once. Its clock fits inside the served ask's: a date past it is
// refused, naming it, and none takes it, with what silence settles on there. An unclocked served
// ask bounds nothing, and silence settles on nothing.
func (s *Server) onward(rec entity.Obj, body api.Ask, asker string) (int, error) {
	if body.On != nil {
		return http.StatusBadRequest, fmt.Errorf("an onward ask takes no `on`: what follows it is the answer it carries back to %s", body.Serves)
	}
	if body.OnSilence != "" {
		return http.StatusBadRequest, fmt.Errorf("an onward ask takes no onSilence: its silence settles on what %s's would", body.Serves)
	}
	served, found, err := entity.As(s.Store, entity.SystemDocket).Get(mirror.AskRef(body.Serves))
	if err != nil {
		return http.StatusBadGateway, err
	}
	if !found {
		return http.StatusNotFound, fmt.Errorf("no ask %s to serve", body.Serves)
	}
	if status := entity.Str(served.Payload, "status"); status != mirror.QuestionPending {
		return http.StatusConflict, fmt.Errorf("ask %s has settled (%s); there is nothing left to serve", body.Serves, status)
	}
	party, err := s.servingParty(asker, served.Payload, body.For)
	if err != nil {
		return http.StatusForbidden, err
	}
	var opts []any
	for _, o := range mirror.OptionsOf(served.Payload) {
		om := entity.Obj{"id": o.ID, "need": 1}
		if o.Says != "" {
			om["says"] = o.Says
		}
		opts = append(opts, om)
	}
	rec["options"] = opts
	_, silence, at := mirror.DeadlineOf(served.Payload)
	// A served ask whose clock has no default settles on no option, and so does this one: said
	// as task.NoOption, since an absent onSilence reads as deny (askSilence) and an empty one
	// would be a third spelling. settleByDeadline settles an empty default on the same option.
	rec[onSilenceField] = cmp.Or(silence, task.NoOption)
	if at != "" {
		limit, err := time.Parse(time.RFC3339Nano, at)
		if err != nil {
			return http.StatusBadGateway, fmt.Errorf("ask %s: its clock %q does not read: %w", body.Serves, at, err)
		}
		if due, ok := rec["due"].(string); ok {
			if d, _ := time.Parse(time.RFC3339, due); d.After(limit) {
				return http.StatusBadRequest, fmt.Errorf("due %s is past ask %s's own clock, %s: an onward ask settles "+
					"inside the ask it serves", due, body.Serves, limit.UTC().Format(time.RFC3339))
			}
		} else {
			rec["due"] = limit.UTC().Format(time.RFC3339)
		}
	}
	rec[servesField] = entity.Obj{"id": body.Serves, "edit": mirror.EditOf(served.Payload), "for": partyName(party)}
	return 0, nil
}

// personsAsk reports whether a record at verse.ask.* is a person's ask, onward or not
// (api.AskRecord's Kind): what a restart waits on again and what its asker may withdraw. Words said
// to a task, a task's question and a firing's gate question are not.
//
// A record that does not read as an AskRecord is told by the two keys Kind reads first, whom it
// is to and whom it is from, so an ask with one bad key is still waited on after a restart and
// still withdrawn by its asker, which is when the asker most wants it gone.
func personsAsk(p entity.Payload) bool {
	var rec api.AskRecord
	if codec.FromNode(p, &rec) != nil {
		return mirror.PrincipalFrom(p) != "" && mirror.TaskTo(p) == ""
	}
	k := rec.Kind()
	return k == api.AskKindPerson || k == api.AskKindOnward
}

// awaitAsk waits on a person's ask, as the task that raised a question would: posted through the
// registry, which publishes it on the docket and arms its clock, and settled by whatever answers
// it. The wait is this process's, so a restart waits again (resumeAsks), and withdrawing the ask
// stops it (deleteAsk).
//
// NOT UNDER THE SERVER'S CONTEXT. A wait that ends takes its question off the docket, which is
// right for a task that stopped and wrong for a process that stopped: the ask is its asker's and
// still waits, and the next process waits on it again. Only withdrawing it ends the wait.
func (s *Server) awaitAsk(q task.GateQuestion) {
	ctx, cancel := context.WithCancelCause(context.Background())
	wait := askWait{cancel: cancel, done: make(chan struct{})}
	s.asks.mu.Lock()
	if s.asks.waits == nil {
		s.asks.waits = map[string]askWait{}
	}
	if _, ok := s.asks.waits[q.ID]; ok {
		s.asks.mu.Unlock()
		cancel(nil)
		return
	}
	s.asks.waits[q.ID] = wait
	s.asks.mu.Unlock()
	go func() {
		defer close(wait.done)
		defer func() {
			s.asks.mu.Lock()
			delete(s.asks.waits, q.ID)
			s.asks.mu.Unlock()
		}()
		res, err := s.Reg.Resolve(ctx, q, task.ToolUse{Name: askGate})
		if err != nil {
			if ctx.Err() == nil {
				// REFUSED, not withdrawn: the registry would not pose it (whom it asks can no
				// longer read what it is about, say). Said on the ask, which otherwise stands
				// pending with nobody waiting on it, and never settles or expires.
				mirror.DropQuestion(entity.As(s.Store, entity.SystemDocket), q.ID,
					"it could not be asked: "+strings.TrimSpace(err.Error()))
			}
			return // withdrawn, or the process is stopping: the docket says which
		}
		s.settleAsk(mirror.AskRef(q.ID), res)
	}()
}

// settleAsk writes what an ask came to on the ask, as its answer. Its `then` ran before the
// answer reached here, as every question's does (settle, consequence.go).
func (s *Server) settleAsk(ref entity.Ref, res task.Resolution) {
	answer := "no"
	if res.Choice == task.Proceed {
		answer = "yes"
	}
	sys := entity.As(s.Store, entity.SystemDocket)
	if _, _, err := sys.Write(entity.Write{
		At:    entity.Path{Ref: ref, Access: "answer"},
		Value: entity.Val(answer),
		When:  []entity.Require{{At: ref.At(), Pattern: entity.Present()}},
	}); err != nil {
		// Logged: the verdict on the record says the same, so what the ask came to
		// is not lost for want of this field.
		fmt.Fprintf(os.Stderr, "verse: ask %s: its answer was not written: %v\n", ref.Name(), err)
	}
}

// carryBack answers the ask an onward ask serves with what the onward ask came to: as the resolver
// that raised it, for the party it was raised for (9ce3bva0, stage 4, Q6). The answer takes the
// route every answer takes (answerQuestion), so it is held to what the resolver's own answer
// would be at this moment — the grant standing, the match admitting, the party able to answer —
// and a clock that settled on nothing carries nothing. What became of it is written on the
// onward ask, as `carried`.
//
// FROM THE WATCH ON THE ASKS, for an answered onward ask not yet carried: the watch replays what
// stands when it opens, so one answered while the process stopped is carried when it starts. A
// carry made twice, where the process stopped between the answer and its record, finds the served
// ask answered with the same ballot, which counts once.
func (s *Server) carryBack(ref entity.Ref, p entity.Payload) {
	link := entity.Get(p, servesField)
	served, asker := entity.Str(link, "id"), mirror.PrincipalFrom(p)
	if served == "" || asker == "" || entity.Str(p, "status") != mirror.QuestionAnswered || entity.Get(p, "carried") != nil {
		return
	}
	sys := entity.As(s.Store, entity.SystemDocket)
	// ONCE, by the record and not by the event: the watch hands this the ask as its event saw
	// it, and an answer delivered again — a replay when the watch reopens, a change to the ask
	// after the carry landed — can carry a payload from before `carried` was written. The
	// served ask counts the same ballot once, but the carry's own record would say it was made
	// twice, at two instants.
	if e, ok, err := sys.Get(ref); err != nil || !ok || entity.Get(e.Payload, "carried") != nil {
		return
	}
	choice := entity.Str(p, "verdict")
	carried := entity.Obj{"at": s.now().UTC().Format(time.RFC3339)}
	if choice == "" || choice == task.NoOption {
		// Nothing was carried, so there is no status: the note says why. `status` is only ever
		// the HTTP code the served ask answered.
		carried["note"] = "nothing: the clock settled on no option"
	} else {
		body := api.Resolve{ID: served, Choice: choice, For: entity.Str(link, "for"), Edit: mirror.EditOf(link)}
		req, _ := http.NewRequest(http.MethodPost, "/asks/"+served+"/answer", nil)
		rec := httptest.NewRecorder()
		s.answerQuestion(rec, req, body, "", entity.As(s.Store, asker))
		carried["status"] = rec.Code
		if rec.Code >= 300 {
			carried["refused"] = strings.TrimSpace(rec.Body.String())
		}
	}
	_, _, _ = sys.Write(entity.Write{At: ref.At(), Value: entity.Obj{"carried": carried}.Payload()})
}

// followAsk runs what follows a settled question's answer as whoever asked, once: the then of the
// first row of its `on` the answer matches (onMatch). The row is marked on the ask first (`ran`),
// conditional on no mark standing, so a restart that finds the ask answered and its row not yet
// run runs it, and one that finds it run does not.
//
// Whoever asked is read from `from` (askerOf): the person who asked. A record naming nobody,
// which a question carrying an `on` never is, has its row refused on the record rather than
// skipped, so a then that did not run is never silent.
func (s *Server) followAsk(ref entity.Ref) {
	sys := entity.As(s.Store, entity.SystemDocket)
	ent, ok, err := sys.Get(ref)
	if err != nil || !ok {
		return
	}
	row, then, matched := onMatch(ent.Payload)
	if !matched || then == nil {
		return
	}
	asker := s.askerOf(ent.Payload)
	if asker == "" {
		if s.markRan(ref, row) {
			s.writeRan(ref, "failed", entity.Obj{"at": s.now().UTC().Format(time.RFC3339),
				"why": "the question says nobody asked it, so there is nobody to run this as"})
		}
		return
	}
	// An answer or an ask is one more act of the asker's, made by the route that makes it: nothing
	// is spawned (askthen.go).
	if a := entity.Get(then, "answer"); a != nil {
		s.thenAnswers(ref, row, asker, entity.ToObj(a))
		return
	}
	if q := entity.Get(then, "ask"); q != nil {
		s.thenAsks(ref, row, asker, q)
		return
	}
	node := entity.Get(then, "spawn")
	if node == nil || entity.Get(ent.Payload, ranField) != nil {
		return
	}
	spec, perr := thenSpec(node)
	id, err := spawnID(api.Spawn{Name: ref.Name()})
	if err != nil {
		return
	}
	if !s.markRanWith(ref, row, entity.Obj{"task": id}) {
		return
	}
	err = perr
	// WHERE THE TASK THAT RAISED IT RUNS, where a task did: what follows its ask runs on the
	// same runner, as it was checked when the ask was raised (checkThenSpawn).
	var on *string
	if raisedBy := entity.Str(entity.Get(ent.Payload, mirror.FromField), "task"); err == nil && raisedBy != "" {
		var runner string
		if runner, err = s.taskRunner(raisedBy); err == nil {
			on = &runner
		}
	}
	if err == nil {
		err = s.runThen(id, ref, asker, spec, on)
	}
	if err != nil {
		// Marked, so it is not tried again at every restart, and said, since the mark names a task
		// that never ran.
		fmt.Fprintf(os.Stderr, "verse: ask %s: what its on[%d] spawns did not start: %v\n", ref.Name(), row, err)
		s.writeRan(ref, "failed", entity.Obj{"at": s.now().UTC().Format(time.RFC3339), "why": err.Error()})
	}
}

// runThen starts what follows a yes, as a task of the asker's: its principal and its record are
// theirs, and its subject is the ask. It is the spawn `verse task run` makes, on the asker's
// authority alone, as the asker wrote it (thenSpec). on, where set, is the runner it is held to:
// the one the task that raised the ask runs on.
func (s *Server) runThen(id string, ref entity.Ref, asker string, spec launch.Spec, on *string) error {
	store := entity.As(s.Store, asker)
	spec = s.withDefaultAgent(spec)
	var err error
	named := spec.VirtualKey // the address the launch is held to, before it is resolved
	if spec.VirtualKey, err = credentialAt(store, asker, spec.VirtualKey); err != nil {
		return err
	}
	l, err := launchOf(id, spec, asker)
	if err != nil {
		return err
	}
	place, err := s.route(&l, on)
	if err != nil {
		return err
	}
	// ITS QUESTIONS ARE THE ASKER'S, where the asker is somebody: a gate or an ask of its own
	// naming nobody waits on them. A `then` run as a rule, or as a task acting for nobody, acts
	// for nobody too, and such a task's questions naming nobody are the docket's.
	actsFor := mirror.ActsFor(asker)
	var answerers []string
	if actsFor != "" {
		answerers = []string{task.PrincipalAnswerer}
	}
	serial := s.host(id, hostedTask{subject: ref.String(), principal: asker, answerers: answerers, autonomy: l.Autonomy, launched: &launched{spec.Environment, place.name, named}})
	l.Provenance.Subject = ref.String()
	h := place.launcher.Launch(l)
	eng := s.engineOf("")
	s.reflect(store, id, serial, h, mirror.Meta{
		Runner: place.launcher.Ref(), RunnerName: place.name, RunnerSubject: place.subject, Subject: ref.String(), Prompt: l.Prompt,
		Agent: recordAgent(l.Agent), Script: l.Script != "", Autonomy: l.Autonomy, ActsFor: actsFor, Answerers: answerers,
		Environment: &spec.Environment, VirtualKey: named,
		HeldBy: func() []string { return eng.HeldBy(id) },
	})
	eng.Adopt(ref.String(), id, h)
	return nil
}

// resumeAsks is the startup phase for the asks: wait again on each of a person's that waits,
// since the wait was the last process's, and run what the row of each answered ask's `on` that
// matched did not finish, since what it ran is the record's to keep across a restart, and the
// marks say what ran.
func (s *Server) resumeAsks() error {
	if err := s.awaiting(phaseSystem); err != nil {
		return err
	}
	ents, err := entity.BelowErr(s.Store, mirror.IsAsk, 1, entity.VerseRoot, mirror.AskSystem)
	if err != nil {
		return entity.Unread(fmt.Errorf("the asks could not be read, so none was waited on: %w", err))
	}
	for _, ent := range ents {
		switch {
		case entity.Str(ent.Payload, "status") == mirror.QuestionPending:
			if personsAsk(ent.Payload) { // a gate's or a task's is waited on by what asked it
				s.awaitAsk(askQuestion(ent.Ref.Name(), ent.Payload, ""))
			}
		case entity.Str(ent.Payload, "status") == mirror.QuestionAnswered:
			s.followAsk(ent.Ref)
		}
	}
	return nil
}

// deleteAsk is `verse ask <id> --withdraw`: the wait on it is stopped, which withdraws it from the
// docket, and the ask is removed. Withdrawing is its owner's, as removing any node is, and is
// asked before anything is stopped.
//
// An id that names no ask is refused as one somebody else's is, not answered 404: whether an ask
// stands is itself what its readers may know.
func (s *Server) deleteAsk(w http.ResponseWriter, r *http.Request) {
	ref := mirror.AskRef(r.PathValue("id"))
	store := s.scopedStore(r)
	if refused(w, entity.Check(store, entity.PrincipalOf(store), entity.OwnerLevel, ref, "withdrawing it")) {
		return
	}
	// A PERSON'S ASK, and only one: a rule's gate or a task's question is waited on by what raised
	// it, and removing its record would leave that waiting on a question nobody can answer. Words
	// said to a task are the task's to take.
	if ent, found, err := entity.As(s.Store, entity.SystemDocket).Get(ref); err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	} else if found && !personsAsk(ent.Payload) {
		http.Error(w, fmt.Sprintf("%s is not a person's ask: what raised it withdraws it", ref.Name()), http.StatusConflict)
		return
	}
	s.asks.mu.Lock()
	wait, waiting := s.asks.waits[ref.Name()]
	s.asks.mu.Unlock()
	if waiting {
		wait.cancel(nil)
		select {
		case <-wait.done:
		case <-time.After(5 * time.Second):
		}
	}
	if _, _, err := store.Write(entity.Write{At: ref.At()}); refused(w, err) {
		return
	} else if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// askedBy reports whether a question is a person's ask raised by who, and who did not name
// themselves among whom it asks: an ask is answered by whom it asks, not by its asker, though the
// asker owns the record.
func askedBy(q entity.Payload, who string) bool {
	return mirror.PrincipalFrom(q) == who && !slices.Contains(mirror.GranteesTo(q), who)
}

// askEdit is which edit of a person's ask its record stands at.
func askEdit(p entity.Payload) int {
	if n := mirror.EditOf(p); n != 0 {
		return n
	}
	return 1
}

// patchAsk is `verse ask <id> --due …` or `--then …`: its asker changing an ask while it waits.
//
// WHAT CHANGES DECIDES WHETHER IT IS ASKED AGAIN. An answer is consent to what was asked, not to
// when, so a new date moves the clock and keeps every ballot. New words, answerers or `then` are
// a new asking: the ask moves to its next edit, the ballots cast for the last one are dropped, and
// an answer still naming it is refused.
//
// ITS ASKER'S, by whom it is from and not by who may write it or who owns it: whom it asks hold
// `read-write` on the record, since answering is writing to it, and an ask a resolver raised
// (onward or not) is owned by whoever installed the resolver, which changes it as itself (`via:`),
// as it asked. Its installer acting as themselves is not its asker.
func (s *Server) patchAsk(w http.ResponseWriter, r *http.Request) {
	var body api.AskChange
	if err := codec.ReadStrict(r, &body); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	asker, ok := actor(w, s.scopedStore(r), "")
	if !ok {
		return
	}
	id := r.PathValue("id")
	ref := mirror.AskRef(id)
	sys := entity.As(s.Store, entity.SystemDocket)
	ent, found, err := sys.Get(ref)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	if !found || mirror.PrincipalFrom(ent.Payload) != asker {
		http.Error(w, fmt.Sprintf("ask %s: only its asker changes it", id), http.StatusForbidden)
		return
	}
	if status := entity.Str(ent.Payload, "status"); status != mirror.QuestionPending {
		http.Error(w, fmt.Sprintf("ask %s has settled (%s); a change is a new ask", id, status), http.StatusConflict)
		return
	}
	// AN ASK BEING SETTLED IS NOT CHANGED. A claim on it is its answer, decided and not yet
	// heard (consequence.go), and a change would make an edit that answer could never be
	// delivered to, with no answer able to replace it.
	if claim, ok := mirror.ClaimOf(ent.Payload); ok {
		http.Error(w, fmt.Sprintf("ask %s is being settled by %s's answer; a change is a new ask", id, claim.By), http.StatusConflict)
		return
	}
	edit := askEdit(ent.Payload)
	if body.Edit != 0 && body.Edit != edit {
		http.Error(w, fmt.Sprintf("ask %s is at edit %d, not %d: read it and change it again", id, edit, body.Edit),
			http.StatusConflict)
		return
	}
	rewords := body.Question != "" || len(body.To) > 0 || body.On != nil
	if body.Due == "" && !rewords {
		http.Error(w, "a change says what changes: due, question, to or on", http.StatusBadRequest)
		return
	}
	// Checked as an ask is when it is made, over what it will say once changed.
	next := askChangeOf(ent.Payload, body)
	rec, answerers, err := askRecord(next, asker, s.now())
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	// A new `on` whose rows answer or ask is checked as one is when an ask is raised.
	if body.On != nil {
		if code, err := s.checkOn(asker, rec, id, s.now(), nil); err != nil {
			http.Error(w, err.Error(), code)
			return
		}
	}
	about := entity.Str(ent.Payload, "subject")
	if err := askAudience(s.Store, asker, about, answerers); err != nil {
		http.Error(w, err.Error(), http.StatusForbidden)
		return
	}
	// AN ONWARD ASK KEEPS THE RULES IT WAS RAISED UNDER: it takes no `on`, and its clock fits
	// inside the served ask's (onward).
	if link := entity.Get(ent.Payload, servesField); link != nil {
		if body.On != nil {
			http.Error(w, fmt.Sprintf("ask %s is onward, and takes no `on`: what follows it is the answer it carries back", id),
				http.StatusBadRequest)
			return
		}
		if due, ok := rec["due"].(string); ok && body.Due != "" {
			if code, err := s.withinServed(entity.Str(link, "id"), due); err != nil {
				http.Error(w, err.Error(), code)
				return
			}
		}
	}
	// The words first, then the date: a change refused whole moves nothing.
	if rewords {
		if err := s.reword(id, ref, edit, rec, mirror.GranteesTo(ent.Payload), answerers, asker, about); err != nil {
			http.Error(w, err.Error(), http.StatusConflict)
			return
		}
		edit++
	}
	if body.Due != "" {
		due, _ := rec["due"].(string)
		if err := s.moveAskDue(id, due); err != nil {
			http.Error(w, err.Error(), http.StatusConflict)
			return
		}
	}
	writeOut(w, r, http.StatusOK, api.Asked{ID: id, Edit: edit})
}

// withinServed refuses a date past the clock of the ask an onward ask serves, as raising one does.
func (s *Server) withinServed(served, due string) (int, error) {
	ent, found, err := entity.As(s.Store, entity.SystemDocket).Get(mirror.AskRef(served))
	if err != nil {
		return http.StatusBadGateway, err
	}
	if !found {
		return 0, nil // the served ask is gone, and the onward ask with it (onwardfollow.go)
	}
	_, _, at := mirror.DeadlineOf(ent.Payload)
	if at == "" {
		return 0, nil
	}
	limit, err := time.Parse(time.RFC3339Nano, at)
	if err != nil {
		return http.StatusBadGateway, fmt.Errorf("ask %s: its clock %q does not read: %w", served, at, err)
	}
	if d, _ := time.Parse(time.RFC3339, due); d.After(limit) {
		return http.StatusBadRequest, fmt.Errorf("due %s is past ask %s's own clock, %s: an onward ask settles "+
			"inside the ask it serves", due, served, limit.UTC().Format(time.RFC3339))
	}
	return 0, nil
}

// askChangeOf is the ask as it will stand once changed: what it said, with what the change says
// in its place.
func askChangeOf(p entity.Payload, change api.AskChange) api.Ask {
	next := api.Ask{
		Name: entity.Str(p, "name"), Question: entity.Str(p, "question"),
		To: mirror.GranteesTo(p), Due: entity.Str(p, "due"), On: entity.Get(p, "on"),
		Keep: entity.Str(p, "keep"),
	}
	if change.Due != "" {
		next.Due = change.Due
	}
	if change.Question != "" {
		next.Question = change.Question
	}
	if len(change.To) > 0 {
		next.To = change.To
	}
	if change.On != nil {
		next.On = change.On
	}
	return next
}

// moveAskDue moves a waiting ask's clock to due, and arms the timer that settles it there on what
// its silence settles on, which moving the date leaves as it was. It asks nothing again.
func (s *Server) moveAskDue(id, due string) error {
	at, err := time.Parse(time.RFC3339, due)
	if err != nil {
		return err
	}
	ref := mirror.AskRef(id)
	ent, found, err := entity.As(s.Store, entity.SystemDocket).Get(ref)
	if err != nil {
		return err
	}
	if !found {
		return fmt.Errorf("no ask %s", id)
	}
	silence := askSilence(ent.Payload)
	moved, _, err := entity.As(s.Store, entity.SystemDocket).Write(entity.Write{
		At: ref.At(),
		Value: entity.Obj{"due": due, onSilenceField: silence, "deadline": entity.Obj{
			"at": at.UTC().Format(time.RFC3339Nano), "default": silence,
		}}.Payload(),
		When: []entity.Require{{Pattern: entity.Obj{"status": mirror.QuestionPending}.Payload()}},
	})
	if err != nil {
		return err
	}
	if !moved {
		return fmt.Errorf("ask %s settled before its date could move", id)
	}
	s.at(at, func() { s.settleByDeadline(s.Store, id) })
	return nil
}

// reword asks a waiting ask again, as its next edit, with what its asker changed.
//
// WHOM IT ASKS CHANGES IN TWO STEPS, narrowed to those asked both before and after, then widened
// to those asked now, with the asking in between: nobody answers words they were not asked, even
// for a moment.
func (s *Server) reword(id string, ref entity.Ref, from int, rec entity.Obj, was, now []string, asker, about string) error {
	sys := entity.As(s.Store, entity.SystemDocket)
	cur, _, err := sys.Get(ref)
	if err != nil {
		return err
	}
	// THE WAITER LEARNS THE NEW EDIT FIRST. An answer is checked against the edit it names where it
	// is delivered (Registry.Deliver), so the registry moves to the next edit before the record
	// does: an answer read at this edit and delivered after is refused, rather than settling the
	// next edit and running a `then` its answerer never saw. An ask answered meanwhile is not
	// asked again; one whose record will not move is put back as it was.
	next := entity.ToObj(cur.Payload)
	next["edit"], next["question"] = from+1, rec["question"]
	for _, f := range []string{mirror.ToField, "on"} {
		if rec[f] == nil {
			delete(next, f)
		} else {
			next[f] = rec[f]
		}
	}
	if !s.Reg.Revise(askQuestion(id, next.Payload(), about)) {
		return fmt.Errorf("ask %s settled while it was changed", id)
	}
	restore := func() { s.Reg.Revise(askQuestion(id, cur.Payload, about)) }
	both := slices.DeleteFunc(slices.Clone(was), func(n string) bool { return !slices.Contains(now, n) })
	narrow := askGrants(s.Store, ref, asker, about, both)
	if len(both) == 0 && len(now) > 0 {
		narrow = askGrants(s.Store, ref, asker, about, []string{asker}) // held by nobody who may answer, meanwhile
	}
	if err := setAskGrants(s.Store, ref, narrow); err != nil {
		restore()
		return err
	}
	fields := entity.Obj{"edit": from + 1, "question": rec["question"], "pose": rec["question"], "reason": rec["question"]}
	for _, f := range []string{mirror.ToField, "on"} {
		switch {
		case rec[f] != nil:
			fields[f] = rec[f]
		case entity.Get(cur.Payload, f) != nil:
			// Removed where it stands: a removal of a field that is not there leaves a null.
			fields[f] = entity.Removed()
		}
	}
	storeOn(fields)
	reworded, err := mirror.Reword(sys, id, from, fields)
	if err != nil {
		restore()
		return err
	}
	if !reworded {
		restore()
		return fmt.Errorf("ask %s settled, or moved past edit %d, while it was changed", id, from)
	}
	if err := setAskGrants(s.Store, ref, askGrants(s.Store, ref, asker, about, now)); err != nil {
		return err
	}
	mirror.WaitOnAgain(sys, id)
	e, _, err := sys.Get(ref)
	if err != nil {
		return err
	}
	s.Reg.Revise(askQuestion(id, e.Payload, ""))
	return nil
}

// setAskGrants sets who reads and who answers an ask, from the grants askGrants says.
func setAskGrants(store entity.Store, ref entity.Ref, grants []entity.Grant) error {
	for level, names := range grants[0].To {
		if err := entity.SetLevel(store, ref, level, names); err != nil {
			return err
		}
	}
	return nil
}
