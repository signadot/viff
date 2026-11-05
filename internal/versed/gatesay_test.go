package versed

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/signadot/verse/pkg/api"
	"github.com/signadot/verse/pkg/mirror"
)

// Talk about a question lands ON the question: attributed, durable, and deciding nothing. It is
// its own route because an answer (`/asks/{id}/answer`) that names no option is refused, and a comment that
// settled a gate by accident is the worst failure this model could have.

// saidOn is what was said about a question, by whom.
func saidOn(t *testing.T, srv *Server, id string) map[string]string {
	t.Helper()
	out := map[string]string{}
	q := questionDoc(t, srv, id)
	said, _ := q[mirror.SaidField].(map[string]any)
	for _, v := range said {
		entry, _ := v.(map[string]any)
		text, _ := entry["text"].(string)
		by, _ := entry["by"].(string)
		out[text] = by
	}
	return out
}

func TestSayingOnAQuestionDecidesNothing(t *testing.T) {
	srv := testServer(t)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	const id = "verse.trigger.review.review~1"
	answered := answerable(t, srv, id)

	resp := postTony(t, ts.URL+sayPath(id), api.GateSay{
		By: "user:ana@example.com", Say: "checking with ops first"})
	resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("saying: %s", resp.Status)
	}
	if got := saidOn(t, srv, id); got["checking with ops first"] != "user:ana@example.com" {
		t.Errorf("said %v, want it attributed to who said it", got)
	}
	if q := questionDoc(t, srv, id); q["status"] != mirror.QuestionPending {
		t.Errorf("question %v, want it still pending: a say is not an answer", q)
	}

	// Two people talking at once keep both lines, which is why each stands under its own key.
	r2 := postTony(t, ts.URL+sayPath(id), api.GateSay{
		By: "user:bo@example.com", Say: "ops says go"})
	r2.Body.Close()
	if got := saidOn(t, srv, id); len(got) != 2 {
		t.Errorf("said %v, want both lines kept", got)
	}

	// And answering still works, with the words beside the decision rather than instead of it.
	win := postTony(t, ts.URL+answerPath(id), api.Resolve{Choice: "proceed", By: "user:ana@example.com"})
	win.Body.Close()
	if win.StatusCode != http.StatusNoContent {
		t.Fatalf("answering after a say: %s", win.Status)
	}
	<-answered
	if got := saidOn(t, srv, id); len(got) != 2 {
		t.Errorf("said %v, want the talk kept through the settle", got)
	}
}

// A say with no words is nothing said, a say whose body names another question than its path is
// about neither, and a say about a question that does not stand is a mistake worth naming rather
// than a line written into the void.
func TestASayNamesAQuestionAndCarriesWords(t *testing.T) {
	srv := testServer(t)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	for _, c := range []struct {
		name string
		id   string
		body api.GateSay
		want int
	}{
		{"another id", "x~1", api.GateSay{ID: "y~1", By: "user:ana@example.com", Say: "hm"}, http.StatusBadRequest},
		{"no words", "x~1", api.GateSay{By: "user:ana@example.com"}, http.StatusBadRequest},
		{"no question", "nope~1", api.GateSay{By: "user:ana@example.com", Say: "hm"}, http.StatusNotFound},
	} {
		t.Run(c.name, func(t *testing.T) {
			resp := postTony(t, ts.URL+sayPath(c.id), c.body)
			resp.Body.Close()
			if resp.StatusCode != c.want {
				t.Errorf("status %s, want %d", resp.Status, c.want)
			}
		})
	}
}
