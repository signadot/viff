package trigger

import (
	"strconv"
	"strings"

	"go.viff.dev/viff/pkg/entity"
	"go.viff.dev/viff/pkg/mirror"
)

// AN ASK SETTLES ONCE. The id a firing derives for its question (gateID, selectID) names what is
// being decided, and it is where that question is asked — until an answer settles it. A firing
// that comes back afterwards, the subject selected again and the gate asked again, is a new
// question with an id of its own: the answer given stands on the ask it answered, with its
// verdict and its ballots, until that ask's keep removes it (viffd askttl.go). A firing is not
// an edit of the ask before it.
//
// An asking that is still open is JOINED, under the id it stands at: a firing posing while one
// stands pending is the same asking reported twice (task.Registry.Resolve), and one standing
// orphaned lost its waiter without anybody deciding it — a restart, or a suspension that
// withdrew it — so the firing that comes back to it takes up the ballots already cast and the
// clock already running.
//
// The asks of one derived id are the id itself and then `<id>-2`, `<id>-3`, … in the order they
// were asked. The suffix follows the whole id, after task.GateSep, so the id still cuts once to
// its owner there. A new asking takes the number after the highest standing, not the first
// free: asks go after their keep oldest first, so the earliest numbers are the ones free, and
// reusing one would name a new question after an old one while its successors still stand.
// Once none stands the next asking is the derived id again.

// askingID is the id the next asking of the question derived as base goes under: the open one
// among its asks where one stands, and a new one where none does. The error is the docket not
// answering, and then nothing is asked: a guess would either pose over a settled answer or split
// an open question in two.
func (e *Engine) askingID(base string) (string, error) {
	ents, err := entity.BelowErr(e.store, func(ref entity.Ref) bool {
		_, ok := askingOrdinal(base, ref.Name())
		return ok
	}, 1, entity.ViffRoot, mirror.AskSystem)
	if err != nil {
		return "", err
	}
	top, open := 0, 0
	for _, ent := range ents {
		n, _ := askingOrdinal(base, ent.Ref.Name())
		top = max(top, n)
		if !answered(ent.Payload) {
			open = max(open, n)
		}
	}
	switch {
	case open > 0:
		return askingName(base, open), nil
	case top == 0:
		return base, nil
	default:
		return askingName(base, top+1), nil
	}
}

// answered reports whether an ask has been decided: answered, or carrying a verdict.
func answered(p entity.Payload) bool {
	return entity.Str(p, "status") == mirror.QuestionAnswered || entity.Str(p, "verdict") != ""
}

// askingName is the id of the nth asking of base, the first being base itself.
func askingName(base string, n int) string {
	if n <= 1 {
		return base
	}
	return base + "-" + strconv.Itoa(n)
}

// askingOrdinal is which asking of base the id name is, and whether it is one at all.
func askingOrdinal(base, name string) (int, bool) {
	if name == base {
		return 1, true
	}
	rest, ok := strings.CutPrefix(name, base+"-")
	if !ok {
		return 0, false
	}
	n, err := strconv.Atoi(rest)
	if err != nil || n < 2 || askingName(base, n) != name {
		return 0, false
	}
	return n, true
}
