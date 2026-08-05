package main

import (
	"fmt"
	"io"
	"net/http"
	"os"
	"sync"

	"github.com/signadot/verse/api"
)

// verse is the http.Client every command talks through. Its transport stamps this binary's
// version on the way out and reads the server's on the way back, which is the only reason it
// exists — a plain http.Client would work perfectly and say nothing.
//
// It is a TRANSPORT rather than a check at each call site because the call sites are a listing,
// a write, a stream and a broker poll, and a version check that covers three of the four is a
// version check that is wrong when it matters. There is one place every request passes through,
// so the check goes there.
var verse = &http.Client{Transport: skewCheck{mine: api.Version()}}

// skewWarned makes the warning once per invocation. A command that pages through a listing or
// follows a stream makes many requests to one server, and the answer does not change between
// them; saying it once is telling the operator, saying it forty times is noise they will learn
// to scroll past.
var (
	skewWarned = new(sync.Once)
	// warnTo is where that line goes. It is a variable so a test can read it, and stderr rather
	// than stdout because a `verse entities | jq` that grew a warning in its input would be a
	// worse bug than the skew.
	warnTo io.Writer = os.Stderr
)

// skewCheck stamps and compares.
//
// The INCOMPATIBLE case fails the request here, in the client, even though the server refuses
// it too (see versed's version middleware). The two are not redundant: the server's refusal is
// what stops a skewed write from happening at all, and this one is what still reports the truth
// when the server is old enough not to look at the header — which, on the day a check like this
// starts to matter, is precisely the server you are talking to.
//
// mine is stated rather than read on each request, which is also what makes it testable: every
// binary in a test tree reports "(devel)", so a skew cannot be arranged by building one.
type skewCheck struct {
	base http.RoundTripper
	mine string
}

func (s skewCheck) RoundTrip(r *http.Request) (*http.Response, error) {
	// A RoundTripper must not modify the request it is given.
	r = r.Clone(r.Context())
	r.Header.Set(api.VersionHeader, s.mine)

	base := s.base
	if base == nil {
		base = http.DefaultTransport
	}
	resp, err := base.RoundTrip(r)
	if err != nil {
		return resp, err
	}
	// A server that says nothing is not a server that disagrees. It may be a verse older than
	// this check, or the broker face, or a proxy, or curl's idea of a good time — and none of
	// those is a version to compare against. The check is between two things that both speak
	// this contract; where one does not, there is nothing to report and reporting it anyway
	// would put a warning on every `verse task do` that dials a broker.
	theirs := resp.Header.Get(api.VersionHeader)
	if theirs == "" {
		return resp, nil
	}
	switch skew, msg := api.VersionSkew(s.mine, theirs); skew {
	case api.VersionIncompatible:
		resp.Body.Close()
		return nil, fmt.Errorf("%s", msg)
	case api.VersionDrift:
		skewWarned.Do(func() { fmt.Fprintf(warnTo, "verse: %s\n", msg) })
	}
	return resp, nil
}
