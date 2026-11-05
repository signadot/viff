package main

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"sync"

	"github.com/signadot/verse/pkg/api"
	"github.com/signadot/verse/pkg/client"
)

// verse is the http.Client every command talks through. Its transport stamps this binary's
// version and the principal in force on the way out and reads the server's version on the way
// back (client.Transport), which is the only reason it exists — a plain http.Client would work
// perfectly and say nothing.
var verse = &http.Client{Transport: skewCheck{mine: api.Version()}}

// skewWarned makes the warning once per invocation. A command that pages through a listing or
// follows a stream makes many requests to one server, and the answer does not change between
// them; saying it once is telling the operator, saying it forty times is noise they will learn
// to scroll past.
var (
	skewWarned = new(sync.Once)
	// warnTo is where that line goes. It is a variable so a test can read it, and stderr rather
	// than stdout because a `verse data | jq` that grew a warning in its input would be a
	// worse bug than the skew.
	warnTo io.Writer = os.Stderr
)

// skewCheck is client.Transport with what is the CLI's to say: the principal is asked afresh on
// each request, because `--as` on a command changes it after the client exists; the bearer is
// the login's, where the CLI acts as whoever logged in (login.go); and drift is said once per
// invocation, on stderr.
//
// mine is stated rather than read on each request, which is also what makes it testable: every
// binary in a test tree reports "(devel)", so a skew cannot be arranged by building one.
type skewCheck struct {
	base http.RoundTripper
	mine string
}

// bearerMu serializes the login's bearer, which reads the tokens file once and rewrites it when it
// renews an expired token: `verse mcp` sends requests side by side through this transport.
var bearerMu sync.Mutex

func lockedBearer(ctx context.Context) (string, error) {
	bearerMu.Lock()
	defer bearerMu.Unlock()
	return bearer(ctx)
}

func (s skewCheck) RoundTrip(r *http.Request) (*http.Response, error) {
	return client.Transport{
		Base:      s.base,
		Version:   s.mine,
		Principal: func() (string, error) { return principal("") },
		Bearer:    lockedBearer,
		Warn: func(msg string) {
			skewWarned.Do(func() { fmt.Fprintf(warnTo, "verse: %s\n", msg) })
		},
	}.RoundTrip(r)
}
