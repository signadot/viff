package mirror

import (
	"fmt"
	"strings"

	"github.com/signadot/verse/pkg/entity"
)

// HOW AN ASK IS SHOWN to whoever asked it, by `verse ask`, verse_ask_get, verse_ask_list and the verse system's
// verse-ask-get alike: found among the asker's asks (GET /asks) by its id or the name it was asked
// under (AskNamed), and shown as a row (AskShown).

// AsksIn is the asks of a GET /asks answer, each at its address.
func AsksIn(list entity.Payload) []entity.Entity {
	var out []entity.Entity
	if list == nil {
		return out
	}
	for _, rec := range list.Values {
		if id := entity.Str(rec, "id"); id != "" {
			out = append(out, entity.Entity{Ref: AskRef(id), Payload: rec})
		}
	}
	return out
}

// AskNamed is the ask arg names among asks: the one numbered arg, or the one asked under the name
// arg. Several under one name are listed rather than guessed between.
func AskNamed(asks []entity.Entity, arg string) (entity.Entity, error) {
	var named []entity.Entity
	for _, e := range asks {
		if e.Ref.Name() == arg {
			return e, nil
		}
		if entity.Str(e.Payload, "name") == arg {
			named = append(named, e)
		}
	}
	switch len(named) {
	case 0:
		return entity.Entity{}, fmt.Errorf("you have no ask named or numbered %q", arg)
	case 1:
		return named[0], nil
	}
	ids := make([]string, 0, len(named))
	for _, e := range named {
		ids = append(ids, e.Ref.Name())
	}
	return entity.Entity{}, fmt.Errorf("you have %d asks named %q; name one by its id: %s",
		len(named), arg, strings.Join(ids, ", "))
}

// AskRows is the asks as the listing shows them, one AskRow each.
func AskRows(asks []entity.Entity) entity.Payload {
	rows := make([]any, 0, len(asks))
	for _, e := range asks {
		rows = append(rows, AskRow(e))
	}
	return entity.Val(rows)
}

// AskRow is one ask as the listing shows it: its status, and its answer, `pending` until it
// settles. One that settled with no answer (QuestionOrphaned) has none to show, and says why;
// a record GET /asks could not read says that.
func AskRow(e entity.Entity) entity.Obj {
	p := e.Payload
	row := entity.Obj{"id": e.Ref.Name(), "question": entity.Str(p, "question")}
	if to := GranteesTo(p); len(to) > 0 {
		row["to"] = to
	}
	for _, f := range []string{"due", "on", "ran", "subject", "keep", "expires"} {
		if v := entity.Get(p, f); v != nil {
			row[f] = v
		}
	}
	status := entity.Str(p, "status")
	if status != "" {
		row["status"] = status
	}
	switch {
	case entity.Str(p, "answer") != "":
		row["answer"] = entity.Str(p, "answer")
	case entity.Str(p, "unreadable") != "":
		row["unreadable"] = entity.Str(p, "unreadable")
	case status == "" || status == QuestionPending:
		row["answer"] = "pending"
	case entity.Str(p, "why") != "":
		row["why"] = entity.Str(p, "why")
	}
	return row
}

// AskShown is one ask as `verse ask <name|id>` shows it: its row, and whom it waits on while it
// does, since the ask is the question on the docket.
func AskShown(e entity.Entity) entity.Obj {
	row := AskRow(e)
	if entity.Str(e.Payload, "status") == QuestionPending {
		if w := entity.Get(e.Payload, "waitingOn"); w != nil {
			row["waitingOn"] = w
		}
	}
	return row
}
