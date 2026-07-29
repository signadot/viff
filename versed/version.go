package versed

import (
	"net/http"

	"github.com/signadot/verse/api"
)

// versioned stamps this verse's version on every response and refuses a request from a client
// that cannot be talking to it.
//
// It wraps the WHOLE surface rather than living in writeOut, because the header has to be on
// every answer — an error, a stream, a 404, the mounted process runtime — and not only on the
// ones that render a document. A version header that appears on the successful replies is a
// version header that is absent from every reply worth diagnosing.
//
// The refusal is the server's half of the check, and it is the half that matters for a WRITE.
// The client compares versions too, but it can only do that once the answer comes back — by
// which time an incompatible `entity put` has already been performed and the error it reports
// is about a write that happened. Refusing here is what makes the check mean "this did not
// occur" rather than "this occurred and then we disagreed about it".
//
// 409 rather than 400: the request is not malformed, and nothing about it will be different if
// it is sent again. It conflicts with the state of the thing it is addressed to, which is what
// 409 says.
func versioned(h http.Handler) http.Handler { return versionedAs(h, api.Version()) }

// versionedAs is versioned with the version stated rather than read from the build info. That
// is what a test needs — every binary in a test tree reports "(devel)", so no arrangement of
// them produces a skew to check.
func versionedAs(h http.Handler, mine string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set(api.VersionHeader, mine)
		// An absent header is a client that predates this check, or something that is not the
		// verse CLI at all — curl, a script, another verse. Neither is grounds for refusing:
		// the versions are unknown, not incompatible, and this endpoint is meant to be usable
		// by hand.
		if theirs := r.Header.Get(api.VersionHeader); theirs != "" {
			if skew, msg := api.VersionSkew(theirs, mine); skew == api.VersionIncompatible {
				http.Error(w, msg, http.StatusConflict)
				return
			}
		}
		h.ServeHTTP(w, r)
	})
}
