package delivery

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"strings"

	"github.com/signadot/verse/api"
	"github.com/signadot/verse/codec"
	"github.com/signadot/verse/mirror"
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
	if strings.TrimSpace(req.Ask) == "" {
		return Reply{Refused: "a raise carries the ask"}
	}
	if why := s.refused(tk, req.Decision, task.Step{Name: task.StepVerseAskCreate, Fields: map[string]string{}}); why != "" {
		return Reply{Refused: why}
	}
	code, said, err := s.Services.Verse(ctx, tk.ID, http.MethodPost, "/asks", []byte(req.Ask))
	switch {
	case err != nil:
		return Reply{Error: "the ask was not carried to verse: " + err.Error()}
	case code/100 == 4:
		return Reply{Refused: "the verse refused the ask: " + said}
	case code/100 != 2:
		return Reply{Error: fmt.Sprintf("the verse answered %d: %s", code, said)}
	}
	var asked api.Asked
	if err := codec.Unmarshal([]byte(said), &asked, codec.Tony); err != nil || asked.ID == "" {
		return Reply{Error: "the verse raised the ask and its answer does not say the id: " + said}
	}
	out := "raised ask " + asked.ID + ": it stands at verse.ask." + asked.ID + " until it is answered; verse-ask-get " + asked.ID + " reads it"
	fmt.Fprintf(s.Log, "delivery: %s: %s\n", tk.ID, out)
	return Reply{OK: true, Out: out}
}

// verseAskGet answers a `verse.ask-get`: the ask req.Ask names, by its id or the name it was asked
// under, among the owner's asks, shown as `verse ask <name|id>` shows it (mirror.AskShown): its
// answer, `pending` until it settles. Decided like any act, since it reads as the owner.
func (s *Server) verseAskGet(ctx context.Context, tk Task, req Request) Reply {
	if s.Services.Verse == nil {
		return Reply{Error: noVerse}
	}
	arg := strings.TrimSpace(req.Ask)
	if arg == "" {
		return Reply{Refused: "a read names the ask, by its name or its id"}
	}
	if why := s.refused(tk, req.Decision, task.Step{Name: task.StepVerseAskGet, Fields: map[string]string{}}); why != "" {
		return Reply{Refused: why}
	}
	code, said, err := s.Services.Verse(ctx, tk.ID, http.MethodGet, "/asks", nil)
	switch {
	case err != nil:
		return Reply{Error: "the read was not carried to verse: " + err.Error()}
	case code/100 == 4:
		return Reply{Refused: "the verse refused the read: " + said}
	case code/100 != 2:
		return Reply{Error: fmt.Sprintf("the verse answered %d: %s", code, said)}
	}
	list, err := codec.ParseNode([]byte(said), codec.Tony)
	if err != nil {
		return Reply{Error: "the verse's asks do not read: " + err.Error()}
	}
	e, err := mirror.AskNamed(mirror.AsksIn(list), arg)
	if err != nil {
		return Reply{Refused: err.Error()}
	}
	var doc bytes.Buffer
	if err := codec.EncodeNode(&doc, mirror.AskShown(e).Payload(), codec.Tony); err != nil {
		return Reply{Error: err.Error()}
	}
	return Reply{OK: true, Out: strings.TrimSpace(doc.String())}
}
