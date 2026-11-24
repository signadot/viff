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
	"go.viff.dev/viff/pkg/api"
	"go.viff.dev/viff/pkg/charterfile"
	"go.viff.dev/viff/pkg/codec"
	"go.viff.dev/viff/pkg/entity"
	"go.viff.dev/viff/pkg/mirror"
	"go.viff.dev/viff/pkg/systems"
	"go.viff.dev/viff/pkg/task"
)

// THE VIFF SYSTEM'S VERBS ARE REQUESTS TO VIFF, carried by the runner for the task: the acts a
// person makes with `viff ask`, made as the task's owner. The runner presents its own token and
// names the task (api.TaskHeader), and viff admits it through the task's record, so the task holds
// no credential and names nobody. What viff refuses, the task reads in viff's words.
const (
	// OpViffAskCreate raises an ask (POST /asks), as `viff ask <name> -q` does.
	OpViffAskCreate = "viff.ask-create"
	// OpViffAskGet reads one of the owner's asks (GET /asks), as `viff ask <name|id>` does.
	OpViffAskGet = "viff.ask-get"
	// OpViffAskList lists the owner's asks (GET /asks), as `viff ask` does.
	OpViffAskList = "viff.ask-list"
	// OpViffDataGet reads the store at an address (GET /state), as `viff data` does.
	OpViffDataGet = "viff.data-get"
	// OpViffDataPut writes a value at an address (POST /entity), as `viff data --put` does.
	OpViffDataPut = "viff.data-put"
	// OpViffDataRm removes what stands at an address (DELETE /entity), as `viff data --rm` does.
	OpViffDataRm = "viff.data-rm"
	// OpViffCharterGet reads the charter at a path (GET /charter), as `viff charter` does.
	OpViffCharterGet = "viff.charter-get"
	// OpViffCharterCheck asks whether charters would install (POST /charter/check), as `viff
	// charter --check` does.
	OpViffCharterCheck = "viff.charter-check"
	// OpViffCharterInstall installs charters (POST /charter), as `viff charter -a` does: what
	// the task installs is held within it, by viff.
	OpViffCharterInstall = "viff.charter-install"
	// OpViffCharterRemove removes a package, or a rule, level or schedule of one (DELETE
	// /charter), as `viff charter -r` does.
	OpViffCharterRemove = "viff.charter-remove"
)

// Viff is how the runner makes a request to its viff instance for a task it hosts: method on path, with
// the runner's own token and the task's id, body a tony document where there is one. It answers
// the viff instance's status and what it said; an error is a request that got no answer, or one viff instance
// refused the runner itself on (its token, or the task it carried the request for).
type Viff func(ctx context.Context, taskID, method, path string, body []byte) (status int, said string, err error)

// noViff is what a verb of the viff system answers on a runner that reaches no viff.
const noViff = "this runner reaches no viff with a token of its own (its `viff:` block and " +
	"VIFF_TOKEN_FILE), so it cannot carry the viff system's verbs for a task"

// viffAskCreate answers a `viff.ask-create`: decided by the task's autonomy and the runner's
// ceiling like any act, then carried to viff.
func (s *Server) viffAskCreate(ctx context.Context, tk Task, req Request) (asked, Reply) {
	if s.Services.Viff == nil {
		return before(Reply{Error: noViff})
	}
	st, why := StepOf(req)
	if why != "" {
		return before(Reply{Refused: why})
	}
	return asking(st, func() Reply {
		// The question as the task wrote it, which the step is about and viff is given; StepOf
		// read it already.
		var ask api.Ask
		if err := codec.Unmarshal([]byte(req.Field("ask")), &ask, codec.Tony); err != nil {
			return Reply{Refused: "the ask does not read: " + err.Error()}
		}
		code, said, err := s.Services.Viff(ctx, tk.ID, http.MethodPost, "/asks", []byte(req.Field("ask")))
		switch {
		case err != nil:
			return unanswered(http.MethodPost, err)
		case code/100 == 4:
			return Reply{Refused: "the viff instance refused the ask: " + said}
		case code/100 != 2:
			return Reply{Error: fmt.Sprintf("the viff instance answered %d: %s", code, said)}
		}
		var raised api.Asked
		if err := codec.Unmarshal([]byte(said), &raised, codec.Tony); err != nil || raised.ID == "" {
			return Reply{Error: "the viff instance raised the ask and its answer does not say the id: " + said}
		}
		out := api.AskRaised(raised.ID, ask.Question)
		fmt.Fprintf(s.Log, "delivery: %s: %s\n", tk.ID, out)
		return Reply{OK: true, Out: out}
	})
}

// StepOf is the step a request to viff is, or an `mcp.call`, and why it is refused before any
// row decides it, "" where it is not. The runner decides it (Server's viff verbs, mcpCall), and
// so does the task before it sends the request (stock's viff tools, its MCP tools), so a row
// decides one step on both sides: the ask a raise carries, the address a data verb names, the
// address an install acts at, the arguments a call sends.
func StepOf(req Request) (task.Step, string) {
	switch req.Op {
	case OpMCPCall:
		c, why := ReadCall(req.Field("server"), req.Field("tool"), req.Body)
		if why != "" {
			return task.Step{}, why
		}
		return c.Step(), ""
	case OpViffAskCreate:
		if strings.TrimSpace(req.Field("ask")) == "" {
			return task.Step{}, "a raise carries the ask"
		}
		var ask api.Ask
		if err := codec.Unmarshal([]byte(req.Field("ask")), &ask, codec.Tony); err != nil {
			return task.Step{}, "the ask does not read: " + err.Error()
		}
		return askStep(ask), ""
	case OpViffAskGet:
		if strings.TrimSpace(req.Field("ask")) == "" {
			return task.Step{}, "a read names the ask, by its name or its id"
		}
		return task.NewStep(task.StepViffAskGet, nil), ""
	case OpViffAskList:
		return task.NewStep(task.StepViffAskList, nil), ""
	}
	route, ok := viffRoutes[req.Op]
	if !ok {
		return task.Step{}, fmt.Sprintf("%s is no verb of viff's", req.Op)
	}
	q, err := url.ParseQuery(req.Field("query"))
	if err != nil {
		return task.Step{}, "the query does not read: " + err.Error()
	}
	// NO SCOPE: a task's acts on viff are its owner's in baseline, whatever scope the task ran
	// in, and the tools send none. A query that names one is not the tools', and where it goes
	// is not the task's to choose.
	if q.Has("scope") {
		return task.Step{}, "a task's acts on viff are its owner's in baseline: the query names no scope"
	}
	switch {
	case req.Op == OpViffDataPut && strings.TrimSpace(req.Body) == "":
		return task.Step{}, "a write carries what it writes"
	case req.Op == OpViffDataRm && q.Get("at") == "":
		return task.Step{}, "a removal names what it removes"
	case req.Op == OpViffCharterCheck && (q.Get("at") == "" || strings.TrimSpace(req.Body) == ""):
		return task.Step{}, "a check names the address and carries the charters"
	case req.Op == OpViffCharterInstall && strings.TrimSpace(req.Body) == "":
		return task.Step{}, "an install carries the charters"
	case req.Op == OpViffCharterRemove && q.Get("at") == "" && q.Get("path") == "":
		return task.Step{}, "a removal names what it removes"
	}
	fields := map[string]string{}
	// The address a data step names, the root where it names none: what a row's `path` bounds,
	// a write's or a removal's `sub` (an access below the address, `votes[0]`) on the end of it.
	// One that does not read is refused here, so no step reaches a row with an address its
	// pattern cannot see.
	var addr, value *ir.Node
	switch req.Op {
	case OpViffDataGet:
		if addr, err = systems.Address(q.Get("at")); err != nil {
			return task.Step{}, err.Error()
		}
	case OpViffDataRm:
		if addr, err = systems.Address(kpath.Join(q.Get("at"), q.Get("sub"))); err != nil {
			return task.Step{}, err.Error()
		}
	case OpViffDataPut:
		var put api.PutEntity
		if err := codec.Unmarshal([]byte(req.Body), &put, codec.Tony); err != nil {
			return task.Step{}, "the write does not read: " + err.Error()
		}
		if addr, err = systems.Address(kpath.Join(put.At, put.Sub)); err != nil {
			return task.Step{}, err.Error()
		}
		value = put.Payload
	}
	if req.Op == OpViffCharterInstall {
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
	st := task.NewStep(task.StepViffAskCreate, nil)
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

// viffAskGet answers a `viff.ask-get`: the ask req.Field("ask") names, by its id or the name it was asked
// under, among the owner's asks, shown as `viff ask <name|id>` shows it (mirror.AskShown): its
// answer, `pending` until it settles. Decided like any act, since it reads as the owner.
func (s *Server) viffAskGet(ctx context.Context, tk Task, req Request) (asked, Reply) {
	if s.Services.Viff == nil {
		return before(Reply{Error: noViff})
	}
	st, why := StepOf(req)
	if why != "" {
		return before(Reply{Refused: why})
	}
	return asking(st, func() Reply {
		arg := strings.TrimSpace(req.Field("ask"))
		asks, rep := s.ownersAsks(ctx, tk, api.AskQuery(arg))
		if asks == nil {
			return rep
		}
		e, err := mirror.AskNamed(asks, arg)
		if err != nil {
			return Reply{Refused: err.Error()}
		}
		return tonyOut(mirror.AskShown(e).Payload())
	})
}

// viffAskList answers a `viff.ask-list`: the owner's asks, each as `viff ask` lists it
// (mirror.AskRows), or the sentence that says there are none.
func (s *Server) viffAskList(ctx context.Context, tk Task, req Request) (asked, Reply) {
	if s.Services.Viff == nil {
		return before(Reply{Error: noViff})
	}
	st, why := StepOf(req)
	if why != "" {
		return before(Reply{Refused: why})
	}
	return asking(st, func() Reply {
		asks, rep := s.ownersAsks(ctx, tk, nil)
		if asks == nil {
			return rep
		}
		if len(asks) == 0 {
			return Reply{OK: true, Out: "the person this task runs for has no asks standing"}
		}
		return tonyOut(mirror.AskRows(asks))
	})
}

// ownersAsks is the owner's asks, read from viff for the task (GET /asks), narrowed by q where
// it names one (`ask`); nil, with the reply that says why, where they were not read. It is the
// act of a read the task's autonomy and the runner's ceiling have decided.
func (s *Server) ownersAsks(ctx context.Context, tk Task, q url.Values) ([]entity.Entity, Reply) {
	path := "/asks"
	if len(q) > 0 {
		path += "?" + q.Encode()
	}
	code, said, err := s.Services.Viff(ctx, tk.ID, http.MethodGet, path, nil)
	switch {
	case err != nil:
		return nil, unanswered(http.MethodGet, err)
	case code/100 == 4:
		return nil, Reply{Refused: "the viff instance refused the read: " + said}
	case code/100 != 2:
		return nil, Reply{Error: fmt.Sprintf("the viff instance answered %d: %s", code, said)}
	}
	list, err := codec.ParseNode([]byte(said), codec.Tony)
	if err != nil {
		return nil, Reply{Error: "the viff instance's asks do not read: " + err.Error()}
	}
	return append([]entity.Entity{}, mirror.AsksIn(list)...), Reply{}
}

// unanswered is the reply to an act viff gave the runner no answer to. A read was not done. A
// write may have been, since viff may do it after the runner stopped waiting, so the task is
// told to read it back rather than do it again: an ask raised twice is two asks.
func unanswered(method string, err error) Reply {
	if method == http.MethodGet {
		return Reply{Error: "the read was not carried to viff: " + err.Error()}
	}
	return Reply{Error: "viff gave no answer, so whether it was done is not known; read it back " +
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

// viffRoutes are the verbs carried on a route of their own, as they stand: the method and the
// path an op carries to, and the step its autonomy decides. The route is the op's; the task gives
// only the query and the document.
var viffRoutes = map[string]struct {
	method, path, step string
}{
	OpViffDataGet:        {http.MethodGet, "/state", task.StepViffDataGet},
	OpViffDataPut:        {http.MethodPost, "/entity", task.StepViffDataPut},
	OpViffDataRm:         {http.MethodDelete, "/entity", task.StepViffDataRm},
	OpViffCharterGet:     {http.MethodGet, "/charter", task.StepViffCharterGet},
	OpViffCharterCheck:   {http.MethodPost, "/charter/check", task.StepViffCharterCheck},
	OpViffCharterInstall: {http.MethodPost, "/charter", task.StepViffCharterInstall},
	OpViffCharterRemove:  {http.MethodDelete, "/charter", task.StepViffCharterRemove},
}

// viffRouted answers a verb of viffRoutes: decided like any act, then carried to viff on its
// route, with the query and the document the task's tool read as the CLI reads them (api's data
// grammar, charterfile). Out is what viff said, for the tool to show.
func (s *Server) viffRouted(ctx context.Context, tk Task, req Request) (asked, Reply) {
	if s.Services.Viff == nil {
		return before(Reply{Error: noViff})
	}
	route := viffRoutes[req.Op]
	st, why := StepOf(req)
	if why != "" {
		return before(Reply{Refused: why})
	}
	q, _ := url.ParseQuery(req.Field("query")) // StepOf read it
	return asking(st, func() Reply {
		path := route.path
		if len(q) > 0 {
			path += "?" + q.Encode()
		}
		var body []byte
		if route.method == http.MethodPost {
			body = []byte(req.Body)
		}
		code, said, err := s.Services.Viff(ctx, tk.ID, route.method, path, body)
		switch {
		case err != nil:
			return unanswered(route.method, err)
		case code/100 == 4:
			return Reply{Refused: "the viff instance refused it: " + said}
		case code/100 != 2:
			return Reply{Error: fmt.Sprintf("the viff instance answered %d: %s", code, said)}
		}
		return Reply{OK: true, Out: said}
	})
}
