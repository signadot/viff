package versed

import (
	"errors"
	"fmt"
	"net/http"

	"github.com/signadot/verse/codec"
	"github.com/signadot/verse/entity"
)

// A READ OF ONE NAMED THING HAS THREE ANSWERS, and two of them are the same answer.
//
//   - It is there, and the asker may read it: the thing.
//   - It is not in the asker's view — because it is not there, or because they may not read it.
//     ONE ANSWER, which says neither: telling them apart would say that something they may not
//     read exists, and calling it absent would assert what may be false. "In your view" is true
//     of both.
//   - The read did not happen — the store timed out, the session was reset. That is an error,
//     and it is never the second answer: somebody told "not installed" installs it again.
//
// A LISTING has the same three, and its second is no answer at all: what the asker may not read
// is left out, which is what a view is, and the read that did not happen is the same error
// (unreadable).

// inView is the sentence for what the asker's view does not hold.
func inView(what string) string {
	return fmt.Sprintf("in your view, no %s is installed", what)
}

// notRead is the sentence for a read that did not happen.
func notRead(what string, err error) string {
	return fmt.Sprintf("%s could not be read: %v — the store did not answer, so nothing is "+
		"known to be missing", what, err)
}

// installed reports whether a lookup of one installed thing — a runner, an io — found it, and
// answers the request when it did not: 404 in the asker's view, 503 for a read that did not
// happen. what names the thing as the answer should: `runner "gpu"`.
func installed(w http.ResponseWriter, r *http.Request, what string, ok bool, err error) bool {
	switch {
	case err == nil && ok:
		return true
	case err == nil, errors.Is(err, entity.ErrForbidden):
		codec.HTTPError(w, r, http.StatusNotFound, inView(what))
	default:
		codec.HTTPError(w, r, http.StatusServiceUnavailable, notRead(what, err))
	}
	return false
}
