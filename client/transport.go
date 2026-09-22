package client

import (
	"fmt"
	"net/http"

	"github.com/signadot/verse/api"
)

// Transport stamps every request with the contract version this program speaks and the principal
// it acts as, and compares the version the server answers with.
//
// It is a TRANSPORT rather than a check at each call site because a program's call sites are a
// listing, a write, a stream and a poll, and a stamp that covers three of the four is one that is
// missing when it matters. Every request passes through one place, so the stamp goes there.
//
// The INCOMPATIBLE case fails the request here, in the client, even though versed refuses it too.
// The two are not redundant: the server's refusal is what stops a skewed write from happening at
// all, and this one still reports the truth when the server is old enough not to look at the
// header — which, on the day a check like this starts to matter, is the server you are talking to.
type Transport struct {
	// Base carries the request once it is stamped; nil is http.DefaultTransport.
	Base http.RoundTripper
	// Version is what this program speaks, stated rather than read on each request, which is also
	// what makes a skew arrangeable in a test: every binary in a test tree reports "(devel)".
	Version string
	// Principal is who is asking, asked on every request so a program whose identity can change
	// between requests (the CLI's `--as`) presents the one in force. "" presents nobody, and an
	// error fails the request rather than sending it as nobody.
	Principal func() (string, error)
	// Warn is told when the server's version DRIFTS from Version: the request is served, and
	// what to do with the news is the program's (the CLI says it once, on stderr). Nil says nothing.
	Warn func(msg string)
}

// Static is a Principal that is always p.
func Static(p string) func() (string, error) {
	return func() (string, error) { return p, nil }
}

// RoundTrip stamps, sends, and compares.
func (t Transport) RoundTrip(r *http.Request) (*http.Response, error) {
	// A RoundTripper must not modify the request it is given.
	r = r.Clone(r.Context())
	if t.Version != "" {
		r.Header.Set(api.VersionHeader, t.Version)
	}
	if t.Principal != nil {
		p, err := t.Principal()
		if err != nil {
			return nil, err
		}
		if p != "" {
			r.Header.Set(api.PrincipalHeader, p)
		}
	}

	base := t.Base
	if base == nil {
		base = http.DefaultTransport
	}
	resp, err := base.RoundTrip(r)
	if err != nil {
		return resp, err
	}
	// A server that says nothing is not a server that disagrees. It may be a verse older than the
	// check, a broker face, or a proxy, and none of those is a version to compare against. The
	// check is between two things that both speak this contract.
	theirs := resp.Header.Get(api.VersionHeader)
	if theirs == "" || t.Version == "" {
		return resp, nil
	}
	switch skew, msg := api.VersionSkew(t.Version, theirs); skew {
	case api.VersionIncompatible:
		resp.Body.Close()
		return nil, fmt.Errorf("%s", msg)
	case api.VersionDrift:
		if t.Warn != nil {
			t.Warn(msg)
		}
	}
	return resp, nil
}
