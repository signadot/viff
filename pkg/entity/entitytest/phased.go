package entitytest

import (
	"sync"
	"time"

	"github.com/signadot/tony-format/go-tony/ir/kpath"

	"go.viff.dev/viff/pkg/entity"
)

// Phased is a store whose watches stand in a phase the test sets — refused, in line, opening
// again after a drop — and say so on their handles, withholding their stream until the test
// opens them: the store under load at a restart, where the watch a restored rule asks for is
// not answered yet. Hold says which watches, by prefix; nil holds every one.
type Phased struct {
	entity.Store
	Hold func(prefix []string) bool

	mu      sync.Mutex
	held    bool // whether the phase is the test's; otherwise the store's own
	phase   entity.WatchState
	changed chan struct{}
}

// NewPhased wraps a store; its watches are the store's own until Set.
func NewPhased(s entity.Store) *Phased {
	return &Phased{Store: s, changed: make(chan struct{})}
}

// Set puts every held watch in a phase; Open hands them back to the store.
func (u *Phased) Set(ph entity.WatchPhase, err error) {
	u.mu.Lock()
	defer u.mu.Unlock()
	u.held = ph != entity.WatchOpen
	u.phase = entity.WatchState{Phase: ph, Err: err, Since: time.Now()}
	close(u.changed)
	u.changed = make(chan struct{})
}

func (u *Phased) Watch(prefix []string, at *kpath.KPath, opts entity.WatchOptions) (*entity.Watch, error) {
	real, err := u.Store.Watch(prefix, at, opts)
	if err != nil || (u.Hold != nil && !u.Hold(prefix)) {
		return real, err
	}
	out := make(chan entity.Commit, 256)
	ready := make(chan struct{})
	done := make(chan struct{})
	var once sync.Once
	go func() {
		defer close(out)
		for {
			u.mu.Lock()
			held, changed := u.held, u.changed
			u.mu.Unlock()
			if !held {
				break
			}
			select {
			case <-changed:
			case <-done:
				return
			}
		}
		select {
		case <-real.Ready:
		case <-done:
			return
		}
		close(ready)
		for c := range real.Commits {
			select {
			case out <- c:
			case <-done:
				return
			}
		}
	}()
	return &entity.Watch{
		Commits:  out,
		Ready:    ready,
		Progress: real.Progress,
		State: func() (entity.WatchState, <-chan struct{}) {
			u.mu.Lock()
			held, ph, changed := u.held, u.phase, u.changed
			u.mu.Unlock()
			if held {
				return ph, changed
			}
			return real.State()
		},
		Close: func() {
			once.Do(func() { close(done) })
			real.Close()
		},
	}, nil
}
