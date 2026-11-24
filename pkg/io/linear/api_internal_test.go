package linear

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	viffio "go.viff.dev/viff/pkg/io"
)

// WHICH OF LINEAR'S ANSWERS ARE REFUSALS (git-issue na9fce9dh12krdydqsn0): what Linear says is the
// request's own error, and a 4xx; not a rate limit, however it arrives, nor an error Linear does not
// say is the request's, nor a 5xx. A refusal is not asked again until the input changes.
func TestWhichAnswersAreRefusals(t *testing.T) {
	for _, c := range []struct {
		name      string
		status    int
		body      string
		permanent bool
		says      string
	}{
		{"the request's own error", 200,
			`{"errors":[{"message":"Entity not found: Comment","extensions":{"code":"INPUT_ERROR","userError":true}}]}`,
			true, "Entity not found"},
		{"an error not said to be the request's", 200,
			`{"errors":[{"message":"something went wrong"}]}`, false, "something went wrong"},
		{"a rate limit as a 400", 400,
			`{"errors":[{"message":"Rate limit exceeded","extensions":{"code":"RATELIMITED","userError":true}}]}`,
			false, "rate limited"},
		{"a 429", 429, `{}`, false, "rate limited"},
		{"a 4xx", 404, `not found`, true, "404"},
		{"a 5xx", 502, `bad gateway`, false, "502"},
	} {
		t.Run(c.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(c.status)
				_, _ = w.Write([]byte(c.body))
			}))
			defer srv.Close()
			var out struct{}
			err := newAPI("key", srv.URL, srv.Client()).query(context.Background(), "{ viewer { id } }", nil, &out)
			if err == nil || !strings.Contains(err.Error(), c.says) {
				t.Fatalf("err %v, want one saying %q", err, c.says)
			}
			if got := viffio.IsPermanent(err); got != c.permanent {
				t.Errorf("permanent %v, want %v (%v)", got, c.permanent, err)
			}
		})
	}
}
