package versed

import (
	"fmt"
	"net/http"
	"sort"
	"strings"

	"github.com/signadot/verse/codec"
	"github.com/signadot/verse/entity"
	"github.com/signadot/verse/mirror"
	"github.com/signadot/verse/task"
	"github.com/signadot/verse/trigger"
)

// A scope owns what it creates (docs/reference/connect.md#what-a-scope-owns).
//
// The world half of a scope is logd's copy-on-write. The program half — rules, tasks, gates,
// flows — is a record AND a live thing: a watch, a handle, an awaiter, a set of handles. A live
// thing runs in exactly one engine, and a scope's engine holds only what the scope created;
// everything else a scope sees is baseline's, by reference, through the overlay. So a control
// write in a scope reaches the scope's own and is REFUSED at anything inherited, naming the
// scope that holds it, rather than passed through to baseline's live thing or copied into a
// record nothing waits on. Baseline is the unnamed scope, and the rule reads the same from it.

// flowEngines is every engine whose live work a request in scope may see and steer: baseline's
// and every package's for the unnamed scope, since a package's charter runs in an engine of its
// own on baseline's behalf (restorePackages); a named scope's alone otherwise, since a scope is
// an engine over its own overlay and holds only what it started. A scope nobody has opened has
// no engine and yields none — a listing or a steer never brings one into being.
//
// One function under both the listing (flowSubjects) and the steer, so that the subjects
// `verse flow` prints are exactly the ones `verse flow pause` can reach.
func (s *Server) flowEngines(scope string) []*trigger.Engine {
	if scope != "" {
		if eng := s.engFor(scope); eng != nil {
			return []*trigger.Engine{eng}
		}
		return nil
	}
	return append([]*trigger.Engine{s.Eng}, s.packageEngines()...)
}

// scopeEngines is every named scope's engine, by name — the federations that are open, as
// against the packages packageEngines lists from the same map.
func (s *Server) scopeEngines() map[string]*trigger.Engine {
	s.scopeMu.Lock()
	defer s.scopeMu.Unlock()
	out := map[string]*trigger.Engine{}
	for key, se := range s.scopeEngs {
		if !strings.HasPrefix(key, "at:") {
			out[key] = se.eng
		}
	}
	return out
}

// flowHolders is the scopes with live work the address selects, other than the one asked:
// "" for baseline (its packages included), else the scope's name. What a refusal names.
func (s *Server) flowHolders(asked string, m trigger.Match) []string {
	var out []string
	if asked != "" {
		for _, eng := range s.flowEngines("") {
			if eng.FlowCount(m) > 0 {
				out = append(out, "")
				break
			}
		}
	}
	for name, eng := range s.scopeEngines() {
		if name != asked && eng.FlowCount(m) > 0 {
			out = append(out, name)
		}
	}
	sort.Strings(out)
	return out
}

// scopeOf says which scope holds a program record — a task's, a question's: "" for baseline,
// else the name of the scope whose overlay has it — and whether any does. Asked from a scope,
// that scope's overlay is looked in after baseline; from anywhere, the open scopes are, so a
// refusal from baseline can say WHICH scope's the thing is rather than "no such".
//
// The record decides because the record is where a scope's creation lands: a task's instance
// and its questions are written into the store that spawned it, and the store they land in is
// what says whose they are (task.GateQuestion.Scope). Baseline first, since a scoped read sees
// baseline's records through the overlay and would claim them.
//
// A read that did not happen is an error, not an absence: the caller refuses the write rather
// than concluding the thing is nobody's.
func (s *Server) scopeOf(ref entity.Ref, asked string) (holder string, found bool, err error) {
	if _, ok, err := s.Store.Get(ref); err != nil {
		return "", false, err
	} else if ok {
		return "", true, nil
	}
	if asked != "" {
		if _, ok, err := s.Store.Scope(asked).Get(ref); err != nil {
			return "", false, err
		} else if ok {
			return asked, true, nil
		}
	}
	for name := range s.scopeEngines() {
		if name == asked {
			continue
		}
		if _, ok, err := s.Store.Scope(name).Get(ref); err != nil {
			return "", false, err
		} else if ok {
			return name, true, nil
		}
	}
	return "", false, nil
}

// taskScope is scopeOf for a task by id, asking the runtime as well: a task hosted here knows
// the scope it was launched in (task.Launch.Scope) whether or not its scope has an engine open
// or its record has landed yet.
func (s *Server) taskScope(id, asked string) (holder string, found bool, err error) {
	if holder, found, err = s.scopeOf(mirror.Ref(id), asked); found || err != nil {
		return holder, found, err
	}
	if sc, ok := s.Taskd.ScopeOf(id); ok {
		return sc, true, nil
	}
	return "", false, nil
}

// questionScope is scopeOf for a question by id. One the registry posted a moment ago may
// have no record yet, and then the registry knows: a firing's gate carries its scope on the
// question, and a task's gate is its task's.
func (s *Server) questionScope(id, asked string) (holder string, found bool, err error) {
	if holder, found, err = s.scopeOf(mirror.QuestionRef(id), asked); found || err != nil {
		return holder, found, err
	}
	for _, p := range s.Reg.Pending() {
		if p.Question.ID == id {
			return p.Question.Scope, true, nil
		}
	}
	if i := strings.Index(id, task.GateSep); i >= 0 {
		if sc, ok := s.Taskd.ScopeOf(id[:i]); ok {
			return sc, true, nil
		}
	}
	return "", false, nil
}

// ownHere is the check every control write makes first: that what it addresses is the
// request's scope's own. Anything else is refused (inherited), having written the response;
// a read that failed is refused as unreadable. True means go on.
func (s *Server) ownHere(w http.ResponseWriter, r *http.Request, what string, holder string, found bool, err error) bool {
	if unreadable(w, r, err) {
		return false
	}
	asked := r.URL.Query().Get("scope")
	if found && holder != asked {
		inherited(w, r, what, asked, []string{holder})
		return false
	}
	return true
}

// inherited refuses a control write addressed, from asked, at what another scope holds. 409:
// the request is well formed and permitted, and conflicts with where the thing is.
//
// The message says what to do instead, since the fix is always to re-address: drop `--scope`
// for baseline's, say the scope's name for a scope's. One sentence for every control verb.
func inherited(w http.ResponseWriter, r *http.Request, what, asked string, holders []string) {
	names := make([]string, 0, len(holders))
	for _, h := range holders {
		if h == "" {
			names = append(names, "baseline's")
		} else {
			names = append(names, "scope "+h+"'s")
		}
	}
	var whose, redo string
	switch {
	case len(names) == 1:
		whose = names[0]
	default:
		whose = strings.Join(names[:len(names)-1], ", ") + " and " + names[len(names)-1]
	}
	switch {
	case asked == "" && len(holders) == 1:
		redo = "say --scope " + holders[0]
	case asked == "":
		redo = "say --scope for the one you mean"
	case len(holders) == 1 && holders[0] == "":
		redo = "act on it without --scope"
	default:
		redo = "act on it from the scope that holds it"
	}
	from := "baseline"
	if asked != "" {
		from = "scope " + asked
	}
	codec.HTTPError(w, r, http.StatusConflict, fmt.Sprintf(
		"%s is %s; %s did not create it — %s", what, whose, from, redo))
}
