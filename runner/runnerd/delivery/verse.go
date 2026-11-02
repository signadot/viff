package delivery

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"github.com/signadot/tony-format/go-tony/ir"
	"github.com/signadot/tony-format/go-tony/ir/kpath"
	"github.com/signadot/verse/api"
	"github.com/signadot/verse/charterfile"
	"github.com/signadot/verse/codec"
	"github.com/signadot/verse/entity"
	"github.com/signadot/verse/mirror"
	"github.com/signadot/verse/systems"
	"github.com/signadot/verse/task"
)

// THE VERSE SYSTEM'S VERBS ARE REQUESTS TO VERSE, carried by the runner for the task: the acts a
// person makes with `verse ask`, made as the task's owner. The runner presents its own token and
// names the task (api.TaskHeader), and verse admits it through the task's record, so the task holds
// no credential and names nobody. What verse refuses, the task reads in verse's words.
const (
	// OpVerseAskCreate raises an ask (POST /asks), as `verse ask <name> -q` does.
	OpVerseAskCreate = "verse.ask-create"
	// OpVerseAskGet reads one of the owner's asks (GET /asks), as `verse ask <name|id>` does.
	OpVerseAskGet = "verse.ask-get"
	// OpVerseAskList lists the owner's asks (GET /asks), as `verse ask` does.
	OpVerseAskList = "verse.ask-list"
	// OpVerseDataGet reads the verse at an address (GET /state), as `verse data` does.
	OpVerseDataGet = "verse.data-get"
	// OpVerseDataPut writes a value at an address (POST /entity), as `verse data --put` does.
	OpVerseDataPut = "verse.data-put"
	// OpVerseDataRm removes what stands at an address (DELETE /entity), as `verse data --rm` does.
	OpVerseDataRm = "verse.data-rm"
	// OpVerseCharterGet reads the charter at a path (GET /charter), as `verse charter` does.
	OpVerseCharterGet = "verse.charter-get"
	// OpVerseCharterCheck asks whether charters would install (POST /charter/check), as `verse
	// charter --check` does.
	OpVerseCharterCheck = "verse.charter-check"
	// OpVerseCharterInstall installs charters (POST /charter), as `verse charter -a` does: what
	// the task installs is held within it, by verse.
	OpVerseCharterInstall = "verse.charter-install"
	// OpVerseCharterRemove removes a package, or a rule, level or schedule of one (DELETE
	// /charter), as `verse charter -r` does.
	OpVerseCharterRemove = "verse.charter-remove"
)

// Verse is how the runner makes a request to its verse for a task it hosts: method on path, with
// the runner's own token and the task's id, body a tony document where there is one. It answers
// the verse's status and what it said; an error is a request that got no answer, or one verse
// refused the runner itself on (its token, or the task it carried the request for).
type Verse func(ctx context.Context, taskID, method, path string, body []byte) (status int, said string, err error)

// noVerse is what a verb of the verse system answers on a runner that reaches no verse.
const noVerse = "this runner reaches no verse with a token of its own (its `verse:` block and " +
	"VERSE_TOKEN_FILE), so it cannot carry the verse system's verbs for a task"

// verseAskCreate answers a `verse.ask-create`: decided by the task's autonomy and the runner's
// ceiling like any act, then carried to verse.
func (s *Server) verseAskCreate(ctx context.Context, tk Task, req Request) Reply {
	if s.Services.Verse == nil {
		return Reply{Error: noVerse}
	}
	st, why := StepOf(req)
	if why != "" {
		return Reply{Refused: why}
	}
	if why := s.refused(tk, req.Decision, st); why != "" {
		return Reply{Refused: why}
	}
	// The question as the task wrote it, which the step is about and verse is given; StepOf read
	// it already.
	var ask api.Ask
	if err := codec.Unmarshal([]byte(req.Ask), &ask, codec.Tony); err != nil {
		return Reply{Refused: "the ask does not read: " + err.Error()}
	}
	code, said, err := s.Services.Verse(ctx, tk.ID, http.MethodPost, "/asks", []byte(req.Ask))
	switch {
	case err != nil:
		return unanswered(http.MethodPost, err)
	case code/100 == 4:
		return Reply{Refused: "the verse refused the ask: " + said}
	case code/100 != 2:
		return Reply{Error: fmt.Sprintf("the verse answered %d: %s", code, said)}
	}
	var asked api.Asked
	if err := codec.Unmarshal([]byte(said), &asked, codec.Tony); err != nil || asked.ID == "" {
		return Reply{Error: "the verse raised the ask and its answer does not say the id: " + said}
	}
	out := api.AskRaised(asked.ID, ask.Question)
	fmt.Fprintf(s.Log, "delivery: %s: %s\n", tk.ID, out)
	return Reply{OK: true, Out: out}
}

// StepOf is the step a request to verse is, and why it is refused before any row decides it, ""
// where it is not. The runner decides it (Server's verse verbs), and so does the task before it
// sends the request (stock's verse tools), so a row decides one step on both sides: the ask a
// raise carries, the address a data verb names, the address an install acts at.
func StepOf(req Request) (task.Step, string) {
	switch req.Op {
	case OpVerseAskCreate:
		if strings.TrimSpace(req.Ask) == "" {
			return task.Step{}, "a raise carries the ask"
		}
		var ask api.Ask
		if err := codec.Unmarshal([]byte(req.Ask), &ask, codec.Tony); err != nil {
			return task.Step{}, "the ask does not read: " + err.Error()
		}
		return askStep(ask), ""
	case OpVerseAskGet:
		if strings.TrimSpace(req.Ask) == "" {
			return task.Step{}, "a read names the ask, by its name or its id"
		}
		return task.NewStep(task.StepVerseAskGet, nil), ""
	case OpVerseAskList:
		return task.NewStep(task.StepVerseAskList, nil), ""
	}
	route, ok := verseRoutes[req.Op]
	if !ok {
		return task.Step{}, fmt.Sprintf("%s is no verb of verse's", req.Op)
	}
	q, err := url.ParseQuery(req.Query)
	if err != nil {
		return task.Step{}, "the query does not read: " + err.Error()
	}
	// NO SCOPE: a task's acts on verse are its owner's in baseline, whatever scope the task ran
	// in, and the tools send none. A query that names one is not the tools', and where it goes
	// is not the task's to choose.
	if q.Has("scope") {
		return task.Step{}, "a task's acts on verse are its owner's in baseline: the query names no scope"
	}
	switch {
	case req.Op == OpVerseDataPut && strings.TrimSpace(req.Body) == "":
		return task.Step{}, "a write carries what it writes"
	case req.Op == OpVerseDataRm && q.Get("at") == "":
		return task.Step{}, "a removal names what it removes"
	case req.Op == OpVerseCharterCheck && (q.Get("at") == "" || strings.TrimSpace(req.Body) == ""):
		return task.Step{}, "a check names the address and carries the charters"
	case req.Op == OpVerseCharterInstall && strings.TrimSpace(req.Body) == "":
		return task.Step{}, "an install carries the charters"
	case req.Op == OpVerseCharterRemove && q.Get("at") == "" && q.Get("path") == "":
		return task.Step{}, "a removal names what it removes"
	}
	fields := map[string]string{}
	// The address a data step names, the root where it names none: what a row's `path` bounds,
	// a write's or a removal's `sub` (an access below the address, `votes[0]`) on the end of it.
	// One that does not read is refused here, so no step reaches a row with an address its
	// pattern cannot see.
	var addr, value *ir.Node
	switch req.Op {
	case OpVerseDataGet:
		if addr, err = systems.Address(q.Get("at")); err != nil {
			return task.Step{}, err.Error()
		}
	case OpVerseDataRm:
		if addr, err = systems.Address(kpath.Join(q.Get("at"), q.Get("sub"))); err != nil {
			return task.Step{}, err.Error()
		}
	case OpVerseDataPut:
		var put api.PutEntity
		if err := codec.Unmarshal([]byte(req.Body), &put, codec.Tony); err != nil {
			return task.Step{}, "the write does not read: " + err.Error()
		}
		if addr, err = systems.Address(kpath.Join(put.At, put.Sub)); err != nil {
			return task.Step{}, err.Error()
		}
		value = put.Payload
	}
	if req.Op == OpVerseCharterInstall {
		// The address the install acts at, from what it carries, as the tool named it
		// (charterfile.InstallAt): what the task's environment and the ceiling bound.
		docs, err := codec.ParseNodes([]byte(req.Body), codec.Tony)
		if err != nil {
			return task.Step{}, "the charters do not read: " + err.Error()
		}
		at, err := charterfile.InstallAt(q, docs)
		if err != nil {
			return task.Step{}, err.Error()
		}
		fields["at"] = at
	}
	st := task.NewStep(route.step, fields)
	if addr != nil {
		st = st.With("path", addr)
	}
	if value != nil {
		st = st.With("value", value)
	}
	return st, ""
}

// askStep is the raise of ask as a row reads it: whom it is put to, its labels, and the rows of
// its `on` as the ask carries them, each where the ask has it.
func askStep(ask api.Ask) task.Step {
	st := task.NewStep(task.StepVerseAskCreate, nil)
	if len(ask.To) > 0 {
		to := make([]*ir.Node, len(ask.To))
		for i, name := range ask.To {
			to[i] = ir.FromString(name)
		}
		st = st.With("to", ir.FromSlice(to))
	}
	if len(ask.Labels) > 0 {
		labels := make(map[string]*ir.Node, len(ask.Labels))
		for k, v := range ask.Labels {
			labels[k] = ir.FromString(v)
		}
		st = st.With("labels", ir.FromMap(labels))
	}
	if on := ir.Uncomment(ask.On); on != nil && on.Type != ir.NullType {
		st = st.With("on", on)
	}
	return st
}

// verseAskGet answers a `verse.ask-get`: the ask req.Ask names, by its id or the name it was asked
// under, among the owner's asks, shown as `verse ask <name|id>` shows it (mirror.AskShown): its
// answer, `pending` until it settles. Decided like any act, since it reads as the owner.
func (s *Server) verseAskGet(ctx context.Context, tk Task, req Request) Reply {
	if s.Services.Verse == nil {
		return Reply{Error: noVerse}
	}
	if _, why := StepOf(req); why != "" {
		return Reply{Refused: why}
	}
	arg := strings.TrimSpace(req.Ask)
	asks, rep := s.ownersAsks(ctx, tk, req, task.StepVerseAskGet, api.AskQuery(arg))
	if asks == nil {
		return rep
	}
	e, err := mirror.AskNamed(asks, arg)
	if err != nil {
		return Reply{Refused: err.Error()}
	}
	return tonyOut(mirror.AskShown(e).Payload())
}

// verseAskList answers a `verse.ask-list`: the owner's asks, each as `verse ask` lists it
// (mirror.AskRows), or the sentence that says there are none.
func (s *Server) verseAskList(ctx context.Context, tk Task, req Request) Reply {
	if s.Services.Verse == nil {
		return Reply{Error: noVerse}
	}
	asks, rep := s.ownersAsks(ctx, tk, req, task.StepVerseAskList, nil)
	if asks == nil {
		return rep
	}
	if len(asks) == 0 {
		return Reply{OK: true, Out: "the person this task runs for has no asks standing"}
	}
	return tonyOut(mirror.AskRows(asks))
}

// ownersAsks is the owner's asks, read from verse for the task (GET /asks) once its autonomy and
// the runner's ceiling decide step, narrowed by q where it names one (`ask`); nil, with the reply
// that says why, where they were not read.
func (s *Server) ownersAsks(ctx context.Context, tk Task, req Request, step string, q url.Values) ([]entity.Entity, Reply) {
	if why := s.refused(tk, req.Decision, task.NewStep(step, map[string]string{})); why != "" {
		return nil, Reply{Refused: why}
	}
	path := "/asks"
	if len(q) > 0 {
		path += "?" + q.Encode()
	}
	code, said, err := s.Services.Verse(ctx, tk.ID, http.MethodGet, path, nil)
	switch {
	case err != nil:
		return nil, unanswered(http.MethodGet, err)
	case code/100 == 4:
		return nil, Reply{Refused: "the verse refused the read: " + said}
	case code/100 != 2:
		return nil, Reply{Error: fmt.Sprintf("the verse answered %d: %s", code, said)}
	}
	list, err := codec.ParseNode([]byte(said), codec.Tony)
	if err != nil {
		return nil, Reply{Error: "the verse's asks do not read: " + err.Error()}
	}
	return append([]entity.Entity{}, mirror.AsksIn(list)...), Reply{}
}

// unanswered is the reply to an act verse gave the runner no answer to. A read was not done. A
// write may have been, since verse may do it after the runner stopped waiting, so the task is
// told to read it back rather than do it again: an ask raised twice is two asks.
func unanswered(method string, err error) Reply {
	if method == http.MethodGet {
		return Reply{Error: "the read was not carried to verse: " + err.Error()}
	}
	return Reply{Error: "verse gave no answer, so whether it was done is not known; read it back " +
		"before doing it again: " + err.Error()}
}

// tonyOut is a read's reply: the document, in tony.
func tonyOut(doc entity.Payload) Reply {
	var b bytes.Buffer
	if err := codec.EncodeNode(&b, doc, codec.Tony); err != nil {
		return Reply{Error: err.Error()}
	}
	return Reply{OK: true, Out: strings.TrimSpace(b.String())}
}

// verseRoutes are the verbs carried on a route of their own, as they stand: the method and the
// path an op carries to, and the step its autonomy decides. The route is the op's; the task gives
// only the query and the document.
var verseRoutes = map[string]struct {
	method, path, step string
}{
	OpVerseDataGet:        {http.MethodGet, "/state", task.StepVerseDataGet},
	OpVerseDataPut:        {http.MethodPost, "/entity", task.StepVerseDataPut},
	OpVerseDataRm:         {http.MethodDelete, "/entity", task.StepVerseDataRm},
	OpVerseCharterGet:     {http.MethodGet, "/charter", task.StepVerseCharterGet},
	OpVerseCharterCheck:   {http.MethodPost, "/charter/check", task.StepVerseCharterCheck},
	OpVerseCharterInstall: {http.MethodPost, "/charter", task.StepVerseCharterInstall},
	OpVerseCharterRemove:  {http.MethodDelete, "/charter", task.StepVerseCharterRemove},
}

// verseRouted answers a verb of verseRoutes: decided like any act, then carried to verse on its
// route, with the query and the document the task's tool read as the CLI reads them (api's data
// grammar, charterfile). Out is what verse said, for the tool to show.
func (s *Server) verseRouted(ctx context.Context, tk Task, req Request) Reply {
	if s.Services.Verse == nil {
		return Reply{Error: noVerse}
	}
	route := verseRoutes[req.Op]
	st, why := StepOf(req)
	if why != "" {
		return Reply{Refused: why}
	}
	q, _ := url.ParseQuery(req.Query) // StepOf read it
	if why := s.refused(tk, req.Decision, st); why != "" {
		return Reply{Refused: why}
	}
	path := route.path
	if len(q) > 0 {
		path += "?" + q.Encode()
	}
	var body []byte
	if route.method == http.MethodPost {
		body = []byte(req.Body)
	}
	code, said, err := s.Services.Verse(ctx, tk.ID, route.method, path, body)
	switch {
	case err != nil:
		return unanswered(route.method, err)
	case code/100 == 4:
		return Reply{Refused: "the verse refused it: " + said}
	case code/100 != 2:
		return Reply{Error: fmt.Sprintf("the verse answered %d: %s", code, said)}
	}
	return Reply{OK: true, Out: said}
}
