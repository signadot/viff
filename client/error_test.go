package client

import (
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
)

func answer(status int, body string) *http.Response {
	return &http.Response{
		StatusCode: status, Status: http.StatusText(status),
		Body: io.NopCloser(strings.NewReader(body)),
	}
}

// An answer that is not what was asked for reads as the server's words, and carries its status
// for the program: three answers, three next steps.
func TestAnErrorCarriesTheStatusAndReadsAsTheWords(t *testing.T) {
	for _, c := range []struct {
		status                          int
		body                            string
		notInView, refused, unavailable bool
	}{
		{http.StatusNotFound, `in your view, no runner "gpu" is installed`, true, false, false},
		{http.StatusForbidden, "refused: user:bob@example.com holds nothing at demo", false, true, false},
		{http.StatusServiceUnavailable, `runner "gpu" could not be read: timeout`, false, false, true},
		{http.StatusBadGateway, "runner unreachable", false, false, true},
		{http.StatusGatewayTimeout, "", false, false, true},
		{http.StatusBadRequest, "a bare word names nothing", false, false, false},
	} {
		err := StatusError(answer(c.status, c.body+"\n"), "runner show")
		want := "runner show: " + c.body
		if c.body == "" {
			want = "runner show: " + http.StatusText(c.status) // the status, where the server said nothing
		}
		if err.Error() != want {
			t.Errorf("%d: reads %q, want %q", c.status, err, want)
		}
		var e *Error
		if !errors.As(err, &e) || e.Status != c.status {
			t.Errorf("%d: the status did not travel: %#v", c.status, err)
		}
		if NotInView(err) != c.notInView || Refused(err) != c.refused || Unavailable(err) != c.unavailable {
			t.Errorf("%d: notInView=%v refused=%v unavailable=%v", c.status,
				NotInView(err), Refused(err), Unavailable(err))
		}

		_, err = OKBody(answer(c.status, c.body))
		if !errors.As(err, &e) || e.Status != c.status {
			t.Errorf("%d: OKBody: the status did not travel: %#v", c.status, err)
		}
	}
	if body, err := OKBody(answer(http.StatusOK, "{}")); err != nil || string(body) != "{}" {
		t.Errorf("an answer is not an error: %q %v", body, err)
	}
	// An error that is no server's answer is none of the three.
	err := errors.New("dial tcp: connection refused")
	if NotInView(err) || Refused(err) || Unavailable(err) {
		t.Error("a connection that was refused was read as a server's answer")
	}
}
