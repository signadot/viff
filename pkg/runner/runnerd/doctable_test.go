package runnerd

import (
	"net/http"
	"net/http/httptest"
	"os"
	"regexp"
	"strings"
	"testing"

	"github.com/signadot/verse/pkg/runner"
)

// THE RUNNER API'S DOC IS WHAT BOTH RUNTIMES SERVE. pkg/runner/doc.go's table is the contract a runner
// written anywhere reads, so each of its routes is served by an InProcessHandler and by a
// SubprocessHandler under exactly that method and path, and the two runtimes register the same
// routes: a method the table does not name is refused 405 by both rather than taken as another.
// And every answer is marked as the runner's (runner.AnswerHeader), a refusal as much as a route's.
func TestBothRuntimesServeTheRunnerDocTable(t *testing.T) {
	src, err := os.ReadFile("../doc.go")
	if err != nil {
		t.Fatal(err)
	}
	row := regexp.MustCompile(`^//\t(GET|POST|PUT|PATCH|DELETE)\s+(\S+)`)
	var routes []string
	for _, l := range strings.Split(string(src), "\n") {
		if m := row.FindStringSubmatch(l); m != nil {
			path, _, _ := strings.Cut(m[2], "?")
			routes = append(routes, m[1]+" "+path)
		}
	}
	if len(routes) == 0 {
		t.Fatal("pkg/runner/doc.go holds no route table")
	}
	inproc, subproc := NewInProcessHandler(nil), NewSubprocessHandler(SubprocessConfig{})
	for name, rt := range map[string]struct {
		mux     *http.ServeMux
		handler http.Handler
	}{
		"in process": {inproc.routes(), inproc.HTTPHandler()},
		"subprocess": {subproc.routes(), subproc.HTTPHandler()},
	} {
		mux := rt.mux
		for _, route := range routes {
			method, path, _ := strings.Cut(route, " ")
			req := httptest.NewRequest(method, strings.ReplaceAll(path, "{id}", "t1"), nil)
			if _, pattern := mux.Handler(req); pattern != route {
				t.Errorf("%s: %s is served as %q, want exactly %q", name, route, pattern, route)
			}
			wrong := http.MethodDelete
			if method == http.MethodDelete {
				wrong = http.MethodPatch
			}
			rec := httptest.NewRecorder()
			rt.handler.ServeHTTP(rec, httptest.NewRequest(wrong, strings.ReplaceAll(path, "{id}", "t1"), nil))
			if rec.Code != http.StatusMethodNotAllowed {
				t.Errorf("%s: %s %s = %d, want 405: the table names no %s there", name, wrong, path, rec.Code, wrong)
			}
			if !runner.Answered(rec.Header()) {
				t.Errorf("%s: the refusal of %s %s is not marked as the runner's", name, wrong, path)
			}
			if method == http.MethodGet { // answered without a body or a task to have
				rec := httptest.NewRecorder()
				rt.handler.ServeHTTP(rec, httptest.NewRequest(method, strings.ReplaceAll(path, "{id}", "t1"), nil))
				if !runner.Answered(rec.Header()) {
					t.Errorf("%s: %s %s answered %d, not marked as the runner's", name, method, path, rec.Code)
				}
			}
		}
	}
}
