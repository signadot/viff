package runnerd

import (
	"net/http"
	"net/http/httptest"
	"os"
	"regexp"
	"strings"
	"testing"
)

// THE RUNNER API'S DOC IS WHAT BOTH RUNTIMES SERVE. runner/doc.go's table is the contract a runner
// written anywhere reads, so each of its routes is served by an InProcessHandler and by a
// SubprocessHandler under exactly that method and path, and the two runtimes register the same
// routes: a method the table does not name is refused 405 by both rather than taken as another.
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
		t.Fatal("runner/doc.go holds no route table")
	}
	for name, h := range map[string]http.Handler{
		"in process": NewInProcessHandler(nil).HTTPHandler(),
		"subprocess": NewSubprocessHandler(SubprocessConfig{}).HTTPHandler(),
	} {
		mux := h.(*http.ServeMux)
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
			mux.ServeHTTP(rec, httptest.NewRequest(wrong, strings.ReplaceAll(path, "{id}", "t1"), nil))
			if rec.Code != http.StatusMethodNotAllowed {
				t.Errorf("%s: %s %s = %d, want 405: the table names no %s there", name, wrong, path, rec.Code, wrong)
			}
		}
	}
}
