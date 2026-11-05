package client

import "net/http"

// Bearer is tr presenting a token on every request, `Authorization: Bearer <token>`: how verse
// proves to a runner that it is the runner's verse, with the workload token its runtime gave it
// (runnerd.Verse). token is asked each time, since a projected token is rotated under the
// process; one that answers "" sends nothing, and an error fails the request before it leaves.
// nil tr is the default transport.
func Bearer(tr http.RoundTripper, token func() (string, error)) http.RoundTripper {
	if tr == nil {
		tr = http.DefaultTransport
	}
	return &bearer{tr: tr, token: token}
}

type bearer struct {
	tr    http.RoundTripper
	token func() (string, error)
}

func (b *bearer) RoundTrip(r *http.Request) (*http.Response, error) {
	tok, err := b.token()
	if err != nil {
		return nil, err
	}
	if tok != "" {
		r = r.Clone(r.Context())
		r.Header.Set("Authorization", "Bearer "+tok)
	}
	return b.tr.RoundTrip(r)
}
