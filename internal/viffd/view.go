package viffd

import (
	"errors"
	"fmt"
	"net/http"

	"go.viff.dev/viff/pkg/codec"
	"go.viff.dev/viff/pkg/entity"
)

// A READ OF ONE THING HAS FOUR ANSWERS, and none of them says that something the asker may not
// read exists.
//
//   - It is there, and the asker may read it: the thing.
//   - It is not in the asker's view — it is not there, or it is left out of what they read
//     because it is not theirs to read. ONE ANSWER, which says neither: calling it absent would
//     assert what may be false, and "in your view" is true of both (404).
//   - The asker may not read into the address they asked at: REFUSED, in the check's words,
//     which name the nearest owner on record and so whom to ask (403, refused). It is the same
//     refusal whether or not a node stands there.
//   - The read did not happen — the store timed out, the session was reset. That is an error
//     (503), and it is never the second answer: somebody told "not installed" installs again.
//
// A LOOKUP BY NAME — a runner, an io — has no address the asker chose, so its second and third
// are one: not installed, and not theirs to read, are both "in your view, no runner is
// installed" (installed).
//
// A LISTING leaves out what the asker may not read, which is what a view is and is no error,
// and answers the read that did not happen with the same error (unreadable).

// nothingStands is the sentence for an address the asker's view holds nothing at.
func nothingStands(at string) string {
	return fmt.Sprintf("in your view, nothing stands at %s", at)
}

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
