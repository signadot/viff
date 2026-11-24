package runnerd

import (
	"net/http"

	"go.viff.dev/viff/pkg/runner"
)

// asRunner marks every response h gives as the runner's own (runner.AnswerHeader): its routes'
// answers, and its refusal of a route or method it does not serve.
func asRunner(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set(runner.AnswerHeader, "1")
		h.ServeHTTP(w, r)
	})
}
