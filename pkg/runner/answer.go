package runner

import "net/http"

// AnswerHeader marks every response a runner gives on its routes as the runner's own answer.
// Between viff and a runner there may be anything that answers HTTP: an ingress's default
// backend, a route not yet set up, a login or maintenance page, a pod that is not the runner.
// Two of a runner's answers are acted on as facts about a task, and only the runner may give
// them: a 404 on GET /tasks/{id} and on its events is the runner saying it has no such task,
// which ends the task, and an answered GET /tasks/{id}/events is a subscribe the runner
// accepted, which keeps the task's wait (the lease). Without the header neither is taken: the
// answer is as good as none, and the reader asks again.
const AnswerHeader = "Viff-Runner"

// Answered reports whether a response is a runner's own answer (AnswerHeader).
func Answered(h http.Header) bool { return h.Get(AnswerHeader) != "" }
