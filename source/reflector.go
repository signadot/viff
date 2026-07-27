package source

import (
	"context"
	"net/http"
	"time"

	"github.com/signadot/verse/entity"
)

// Source is an inbound reflector: it owns and is the single writer of a system slice, keeps
// it converged with external truth via Reconcile, and maps a hint delivery to the ref(s) to
// reconcile via Webhook — which writes nothing. See docs/sources.md.
//
// "Webhook" is the abstraction name for the low-latency hint; the concrete delivery is
// system-specific and need not be an HTTP webhook (git has none — its hint is a local
// reference-transaction hook that POSTs here). The load-bearing law: a hint NEVER writes
// entities; it schedules a Reconcile that reads the source of truth and converges.
type Source interface {
	System() string
	Reconcile(ctx context.Context, store entity.Store, ref entity.Ref) error
	Webhook(r *http.Request) ([]entity.Ref, error)
}

// Reflector drives a Source in the informer shape: an initial full reconcile, a periodic full
// reconcile (the resync net that catches every missed hint), and targeted reconciles kicked
// by hint deliveries to Hooks(). A hint only schedules; the reconcile is the authority.
type Reflector struct {
	src   Source
	store entity.Store
	every time.Duration
	kick  chan entity.Ref
	onErr func(error)
}

// NewReflector reflects src into store, with a full resync every `every` (<=0 disables the
// periodic net, leaving only hint-driven and initial reconciles).
func NewReflector(src Source, store entity.Store, every time.Duration) *Reflector {
	return &Reflector{src: src, store: store, every: every, kick: make(chan entity.Ref, 64)}
}

// OnError registers a sink for reconcile errors (else they are dropped — the periodic net
// retries anyway).
func (rf *Reflector) OnError(f func(error)) *Reflector { rf.onErr = f; return rf }

func (rf *Reflector) reconcile(ctx context.Context, ref entity.Ref) {
	if err := rf.src.Reconcile(ctx, rf.store, ref); err != nil && rf.onErr != nil {
		rf.onErr(err)
	}
}

// Hooks serves the hint endpoint POST /hooks/<system>. It maps the delivery to ref(s) via
// the Source's Webhook and enqueues a targeted reconcile for each — never writing entities
// itself. If the queue is full the hint is dropped; the periodic full reconcile covers it.
func (rf *Reflector) Hooks() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /hooks/"+rf.src.System(), func(w http.ResponseWriter, r *http.Request) {
		refs, err := rf.src.Webhook(r)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if len(refs) == 0 {
			refs = []entity.Ref{{}} // no target → a full resync
		}
		for _, ref := range refs {
			select {
			case rf.kick <- ref:
			default: // backed up; the periodic net will catch it
			}
		}
		w.WriteHeader(http.StatusNoContent)
	})
	return mux
}

// Run does the initial full reconcile, then serves hint-kicked and periodic reconciles until
// ctx is cancelled. The caller mounts Hooks() on an HTTP server; Run owns the reconcile loop.
func (rf *Reflector) Run(ctx context.Context) error {
	rf.reconcile(ctx, entity.Ref{}) // initial full sync
	var tick <-chan time.Time
	if rf.every > 0 {
		t := time.NewTicker(rf.every)
		defer t.Stop()
		tick = t.C
	}
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-tick:
			rf.reconcile(ctx, entity.Ref{}) // full resync net
		case ref := <-rf.kick:
			rf.reconcile(ctx, ref) // targeted, from a hint
		}
	}
}
