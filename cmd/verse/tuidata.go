package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/signadot/tony-format/go-tony/ir"

	"github.com/signadot/verse/pkg/client"
	"github.com/signadot/verse/pkg/codec"
	"github.com/signadot/verse/pkg/entity"
)

// tuiSource is what the tui reads and does: the verse over HTTP, or a fake in the tests.
type tuiSource interface {
	// snapshot is one round of reads, the Data section's listing of dataAt among them.
	snapshot(dataAt string) tuiSnapshot
	// record is what stands at an address, as the Data section shows the row under its cursor.
	record(at string) (*ir.Node, error)
	answer(id, choice, as, say string) (string, error)
	cancel(id string) error
}

// tuiSnapshot is the verse as one round of reads found it. A read that failed leaves its
// collection as it was and says why in errs, so one refusal does not blank the others.
type tuiSnapshot struct {
	questions []tuiQuestion
	tasks     []tuiItem
	everyTask []tuiItem // every task the runtime holds, for a place's timeline
	runners   []tuiItem
	// data is the children of dataAt, each named by its last segment and carrying its address
	// in note; dataAt is the address they were listed at.
	data     []tuiItem
	dataAt   string
	charters []tuiItem
	errs     map[tuiTab]string
}

// tuiQuestion is one question waiting on the caller.
type tuiQuestion struct {
	id, pose, about, subject, gate, posedAt string
	options                                 []string
	says                                    map[string]string // what an option says it means, by id
	record                                  *ir.Node
	// subjectRecord is what the question is about, as it stands: the step a merge gate names,
	// the spec a spec gate names. Nil where it has no subject or the subject did not read.
	subjectRecord *ir.Node
}

// tuiItem is a row of a listing: its name, its one-line state, and its whole record.
type tuiItem struct {
	name, state, note string
	record            *ir.Node
}

type httpSource struct {
	c        *client.Client
	allTasks bool
	// subjects is what each waiting question is about, as last read and when: read again only
	// once it is subjectFresh old, not on every poll.
	mu       sync.Mutex
	subjects map[string]subjectRead
}

type subjectRead struct {
	n  *ir.Node
	at time.Time
}

const (
	// readTimeout bounds every request: a verse that stops answering shows as an error, not as
	// a screen that stops moving.
	readTimeout  = 15 * time.Second
	subjectFresh = 30 * time.Second
)

// client is the verse client for one request, under readTimeout.
func (h *httpSource) client() (*client.Client, context.CancelFunc) {
	ctx, cancel := context.WithTimeout(context.Background(), readTimeout)
	return h.c.With(ctx), cancel
}

func (h *httpSource) snapshot(dataAt string) tuiSnapshot {
	s := tuiSnapshot{errs: map[tuiTab]string{}, dataAt: dataAt}
	if qs, err := h.questions(); err != nil {
		s.errs[tabQuestions] = err.Error()
	} else {
		s.questions = qs
	}
	if n, err := h.read("/tasks", nil); err != nil {
		s.errs[tabTasks] = err.Error()
	} else {
		s.tasks, s.everyTask = taskRows(n, h.allTasks), taskRows(n, true)
	}
	if rs, err := h.runners(); err != nil {
		s.errs[tabRunners] = err.Error()
	} else {
		s.runners = rs
	}
	if ds, err := h.children(dataAt); err != nil {
		s.errs[tabData] = err.Error()
	} else {
		s.data = ds
	}
	if n, err := h.read("/charter", url.Values{"depth": {"-1"}}); err != nil {
		s.errs[tabCharters] = err.Error()
	} else {
		s.charters = charterRows(n)
	}
	return s
}

func (h *httpSource) record(at string) (*ir.Node, error) {
	return h.read("/state", url.Values{"at": {at}})
}

// children is the addresses one level under at, as the store lists them: a listing names them
// without reading what stands there, so a wide container costs its names, not its records.
func (h *httpSource) children(at string) ([]tuiItem, error) {
	n, err := h.read("/state", url.Values{"at": {at}, "paths": {"1"}, "depth": {"1"}})
	if err != nil {
		return nil, err
	}
	return dataRows(at, entity.Strs(n, "paths")), nil
}

// dataRows is a listing of at as the Data section's rows: each address under it, by its last
// segment, with the address itself in note.
func dataRows(at string, paths []string) []tuiItem {
	var out []tuiItem
	for _, p := range paths {
		if p == at || p == "" {
			continue
		}
		// What follows at: `.field`, or `[0]` where at is an array; a quoted key by what it
		// says (`"123"` is 123).
		name := strings.TrimPrefix(strings.TrimPrefix(p, at), ".")
		if segs, ok := segsOf(p); ok && len(segs) > 0 && !strings.HasPrefix(name, "[") {
			name = segs[len(segs)-1]
		}
		out = append(out, tuiItem{name: name, note: p})
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].name < out[j].name })
	return out
}

// charterRows is the charter tree (GET /charter) as one row a charter: each address a package is
// installed at, stated by its rules and levels, its record the package as the tree answers it.
// A place only holding charters under it is no row.
func charterRows(n *ir.Node) []tuiItem {
	var out []tuiItem
	var walk func(at string, n *ir.Node)
	walk = func(at string, n *ir.Node) {
		if n == nil || n.Type != ir.ObjectType {
			return
		}
		if entity.Get(n, "writes") != nil {
			rules, strata := len(listOf(entity.Get(n, "rules"))), len(listOf(entity.Get(n, "strata")))
			state := fmt.Sprintf("%d rules", rules)
			if strata > 0 {
				state += fmt.Sprintf(" · %d levels", strata)
			}
			if ttl := entity.Str(n, "ttl"); ttl != "" {
				state += " · ttl " + ttl
			}
			name := at
			if name == "" {
				name = "(root)"
			}
			// The package as the tree answers it, without the packages under it: those are rows
			// of their own.
			rec := &ir.Node{Type: ir.ObjectType}
			for i, f := range n.Fields {
				if f.String != "children" {
					rec.Fields, rec.Values = append(rec.Fields, f), append(rec.Values, n.Values[i])
				}
			}
			out = append(out, tuiItem{name: name, state: state, note: entity.Str(n, "writes"), record: rec})
		}
		kids := entity.Get(n, "children")
		if kids == nil || kids.Type != ir.ObjectType {
			return
		}
		for i, k := range kids.Fields {
			child := k.String
			if at != "" {
				child = at + "." + k.String
			}
			walk(child, kids.Values[i])
		}
	}
	walk("", n)
	return out
}

func (h *httpSource) answer(id, choice, as, say string) (string, error) {
	c, cancel := h.client()
	defer cancel()
	return answerVia(c, id, choice, as, say, false, "answered")
}

func (h *httpSource) cancel(id string) error {
	c, cancel := h.client()
	defer cancel()
	resp, err := c.Post("/tasks/"+url.PathEscape(id)+"/cancel", nil, nil)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= http.StatusBadRequest {
		return statusError(resp, "cancel")
	}
	return nil
}

// read is GET path as the node it answers.
func (h *httpSource) read(path string, q url.Values) (*ir.Node, error) {
	c, cancel := h.client()
	defer cancel()
	resp, err := c.Get(path, q)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode >= http.StatusBadRequest {
		msg := strings.TrimSpace(string(body))
		if msg == "" {
			msg = resp.Status
		}
		return nil, errors.New(msg)
	}
	return codec.ParseNode(body, respFmt(resp))
}

func (h *httpSource) questions() ([]tuiQuestion, error) {
	c, cancel := h.client()
	defer cancel()
	asks, err := asksFrom(c, questionQuery(false, false))
	if err != nil {
		return nil, err
	}
	out := make([]tuiQuestion, 0, len(asks))
	keep := map[string]subjectRead{}
	for _, e := range asks {
		q := questionOf(e.Ref.Name(), e.Payload)
		if q.subject != "" {
			h.mu.Lock()
			s, ok := h.subjects[q.id]
			h.mu.Unlock()
			if !ok || time.Since(s.at) > subjectFresh {
				if n, err := h.read("/state", url.Values{"at": {q.subject}}); err == nil {
					s = subjectRead{n: n, at: time.Now()}
				}
			}
			q.subjectRecord = s.n
			keep[q.id] = s
		}
		out = append(out, q)
	}
	// What no question waits on any more is forgotten.
	h.mu.Lock()
	h.subjects = keep
	h.mu.Unlock()
	sort.SliceStable(out, func(i, j int) bool { return out[i].posedAt < out[j].posedAt })
	return out, nil
}

// questionOf reads one ask record as the tui shows it.
func questionOf(id string, p *ir.Node) tuiQuestion {
	q := tuiQuestion{id: id, pose: entity.Str(p, "pose"), about: entity.Str(p, "about"),
		subject: entity.Str(p, "subject"), gate: entity.Str(p, "gate"), posedAt: entity.Str(p, "posedAt"), record: p}
	if q.pose == "" {
		q.pose = entity.Str(p, "question")
	}
	if rule := entity.Str(entity.Get(p, "from"), "rule"); q.pose == "" && rule != "" {
		// A rule's gate poses nothing of its own: what it asks is whether the rule may act,
		// and on what.
		q.pose = fmt.Sprintf("Let rule %s act on %s?", rule, q.subject)
	}
	if q.pose == "" {
		q.pose = entity.Str(p, "reason")
	}
	if s := entity.Str(p, "id"); s != "" {
		q.id = s
	}
	if opts := entity.Get(p, "options"); opts != nil && opts.Type == ir.ArrayType {
		for _, o := range opts.Values {
			if id := entity.Str(o, "id"); id != "" {
				q.options = append(q.options, id)
				if s := entity.Str(o, "says"); s != "" {
					if q.says == nil {
						q.says = map[string]string{}
					}
					q.says[id] = s
				}
			}
		}
	}
	if len(q.options) == 0 {
		q.options = []string{"proceed", "deny"}
	}
	return q
}

// taskRecent is how many finished tasks the listing keeps beside the running ones.
const taskRecent = 15

// taskRows is the task listing: what is running first, then the most recently started, each
// named by its id and stated by its status, its runner and when it started.
func taskRows(n *ir.Node, all bool) []tuiItem {
	var live, rest []tuiItem
	for _, t := range listOf(n) {
		status := entity.Str(t, "status")
		it := tuiItem{name: entity.Str(t, "id"), record: t,
			state: fmt.Sprintf("%-9s %s", status, strings.TrimPrefix(entity.Str(t, "runnerName"), "verse.runners.")),
			note:  entity.Str(t, "started")}
		if isTerminalStatus(status) {
			rest = append(rest, it)
		} else {
			live = append(live, it)
		}
	}
	newest := func(xs []tuiItem) {
		sort.SliceStable(xs, func(i, j int) bool { return xs[i].note > xs[j].note })
	}
	newest(live)
	newest(rest)
	if !all && len(rest) > taskRecent {
		rest = rest[:taskRecent]
	}
	return append(live, rest...)
}

func (h *httpSource) runners() ([]tuiItem, error) {
	n, err := h.read("/runners", nil)
	if err != nil {
		return nil, err
	}
	var out []tuiItem
	for _, r := range listOf(n) {
		offers := entity.Strs(r, "offers")
		state := fmt.Sprintf("running %s · offers %s", nodeText(entity.Get(r, "running")), strings.Join(offers, ","))
		out = append(out, tuiItem{name: entity.Str(r, "name"), state: state, note: entity.Str(r, "url"), record: r})
	}
	return out, nil
}

// listOf is a listing's rows: an array's values, or nothing.
func listOf(n *ir.Node) []*ir.Node {
	if n == nil || n.Type != ir.ArrayType {
		return nil
	}
	return n.Values
}

// nodeText is a scalar as text, or "".
func nodeText(n *ir.Node) string {
	if n == nil {
		return ""
	}
	switch n.Type {
	case ir.StringType:
		return n.String
	case ir.NumberType:
		if n.Int64 != nil {
			return fmt.Sprint(*n.Int64)
		}
		return n.Number
	case ir.BoolType:
		return fmt.Sprint(n.Bool)
	}
	return ""
}
