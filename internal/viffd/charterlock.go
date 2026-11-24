package viffd

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"
)

// ONE CHARTER OPERATION AT A TIME.
//
// Nothing serialized charter mutation. scopeMu guards the MAP of engines, not what is done to
// one, so two `charter add`s at an address both took the engine and mutated it — and each
// installs into a charterTx, which is an undo list rather than a store transaction (see
// charter.go). Two undo lists over one engine interleave: a refusal in one puts back what the
// other installed, and both print "nothing was installed", and one of them is lying. Uninstall
// is in the same position, and it now sweeps the store as well as the engine.
//
// So: one lock, over every charter mutation — install and uninstall, every address, every
// scope, and the restore that reinstalls the durable charter at startup. NOT per address,
// because a recursive removal spans addresses (and a recursive install will), so a per-address
// lock would need a canonical acquisition order to stay deadlock-free, and would buy
// concurrency between operations that people take seconds apart.
//
// A CHANNEL RATHER THAN A sync.Mutex, because a mutex cannot be waited for with a deadline and
// waiting forever is the failure this is next to. A charter install is a sequence of store
// writes, each of which may already be queued behind other writes, so an operation can hold
// this for a while — and a second operator behind it should be told that, not hung. That is
// where "viff charter times out" came from: a client with nothing to report and no way to tell
// waiting from wedged.
//
// TWO RULES, both silent when broken:
//
//   - THIS IS THE OUTER LOCK. packageEngine takes scopeMu; this is taken before it and never
//     from inside it. restorePackages already releases scopeMu before loading charters, for its
//     own reason (holding it across per-rule watch establishment stalls every lookup behind
//     it), so the ordering here is the tree's existing habit rather than a new constraint.
//   - READS DO NOT TAKE IT. `GET /charter` sees whatever is installed when it asks,
//     half-applied included — which is what it would see of any concurrent write. It cannot be
//     otherwise anyway: a rule begins observing when it installs, so a half-applied charter is
//     already ACTING, and a reader held behind this lock would get a tidy before-and-after view
//     of a viff that had already fired half a program. Locking reads buys a neater display and
//     costs a listing that wedges behind an install, which is the readiness probe that made
//     health a function of load, one surface over.
//
// What this promises, exactly: all-or-none is about what is INSTALLED when a request answers.
// Not about what was visible while it ran, and not about what the rules did while they stood —
// which is the same sentence `uninstall` prints about what a package wrote.

// charterLockWait is how long a charter operation waits for the one in front of it.
//
// The store's write budget (entity.writeBudget), for the same reason it is what it is: an
// install is a sequence of writes and each may wait behind other writes, so the wait has to
// exceed a whole operation rather than a single write.
const charterLockWait = 30 * time.Second

// lockCharter takes the charter lock, or answers why it could not.
//
// The returned release is safe to defer unconditionally: it is nil-free on the error path
// because the error path never took anything.
func (s *Server) lockCharter(ctx context.Context) (func(), error) {
	wait := s.charterWait
	if wait <= 0 {
		wait = charterLockWait
	}
	ctx, cancel := context.WithTimeout(ctx, wait)
	defer cancel()
	select {
	case s.charterMu <- struct{}{}:
		var once bool
		return func() {
			if !once {
				once = true
				<-s.charterMu
			}
		}, nil
	case <-ctx.Done():
		// The CALLER's context ending is a client that went away, and the deadline is this
		// server being busy. Different news, and only the second is worth a Retry-After.
		if ctx.Err() == context.DeadlineExceeded {
			return func() {}, fmt.Errorf("%w and did not "+
				"finish within %s — a charter is installed and removed one at a time, so this "+
				"is a queue rather than a refusal: try again", errCharterBusy, wait)
		}
		return func() {}, ctx.Err()
	}
}

// errCharterBusy is why a charter operation that waited its turn for the charter lock, and did
// not get it, is refused: answered 503, to be asked again.
var errCharterBusy = errors.New("another charter operation is in progress")

// ctxOrBackground is the server's lifetime context, or Background before Start has set one.
// Callers that are not handlers use it, so a charter operation on the startup path is bounded
// by the same wait as one on a request.
func (s *Server) ctxOrBackground() context.Context {
	if s.ctx != nil {
		return s.ctx
	}
	return context.Background()
}

// lockCharterHTTP is lockCharter for a handler: it writes the 503 itself and reports whether
// the caller may proceed.
//
// 503 RATHER THAN 409, because nothing about what was asked is in conflict — the server is
// busy with something else, which is what 503 says and what Retry-After is for. A 409 would
// send an operator looking for the conflict in their own charter file.
func (s *Server) lockCharterHTTP(w http.ResponseWriter, r *http.Request) (func(), bool) {
	release, err := s.lockCharter(r.Context())
	if err != nil {
		charterError(w, err.Error(), http.StatusServiceUnavailable)
		return release, false
	}
	return release, true
}

// charterError answers a charter request refused with code. A refusal that asking again mends
// says when to ask (Retry-After): a package being uninstalled (429), and a server busy with
// another charter operation or a store that did not answer (503).
func charterError(w http.ResponseWriter, msg string, code int) {
	if code == http.StatusTooManyRequests || code == http.StatusServiceUnavailable {
		w.Header().Set("Retry-After", "5")
	}
	http.Error(w, msg, code)
}

// charterRefused is charterError for an error, with the code it is answered with (charterCode).
func charterRefused(w http.ResponseWriter, err error) {
	charterError(w, err.Error(), charterCode(err))
}
