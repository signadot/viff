package source

import (
	"context"
	"sync"
	"time"

	"github.com/signadot/verse/entity"
)

// Throttle wraps a Source so a pass repeated inside `every` answers with the last SUCCESSFUL
// reflection instead of asking the external system again. Zero or less returns src unwrapped.
//
// It exists because verse's resync interval is a constant (versed.ResyncEvery) and an API
// budget is not. A git repo on local disk does not care how often it is read; a Linear
// workspace answers a paged GraphQL query against an hourly complexity budget, and a Slack
// workspace against a per-method minute. Where that budget lives is the deciding argument:
// it belongs to the credential, and the credential is in the source — so the source is what
// gets to say how often it will really answer, the same way it is what holds its own webhook
// secret. Making verse's interval per-registration would move the knowledge to the side that
// does not have it.
//
// WHAT IT COSTS is a hint's latency, and only in a burst. A hint that arrives inside the
// window is answered from the cache, so the change it reported lands on the next pass that
// goes live — the periodic resync, at worst. That is the right trade in the direction that
// hurts: a hundred deliveries for one bulk edit would otherwise spend an hour's budget in a
// minute, and a source that has been rate-limited reflects NOTHING until the limit lifts.
//
// WHAT IT MUST NOT DO is cache a failure, and this is the same rule the whole seam is built
// on. A failed reflect is an error and never an empty Reflection; caching one would turn a
// single bad minute into `every` minutes of a source claiming an external system is empty.
// Errors pass straight through and leave the cache alone.
func Throttle(src Source, every time.Duration) Source {
	if every <= 0 {
		return src
	}
	return &throttled{src: src, every: every, last: map[string]answer{}}
}

type throttled struct {
	src   Source
	every time.Duration

	mu   sync.Mutex
	last map[string]answer
}

type answer struct {
	at time.Time
	rn Reflection
}

var _ Source = (*throttled)(nil)

func (t *throttled) System() string { return t.src.System() }

// Owns delegates when the wrapped source will say, and answers nothing when it will not —
// which is what Describe would have read from the source itself. A wrapper that swallowed the
// claim would install a source that owns nothing, and every later install in its system would
// then need --allow-overlap for a reason belonging to this file.
func (t *throttled) Owns() []Own {
	if o, ok := t.src.(Owner); ok {
		return o.Owns()
	}
	return nil
}

// Reflect answers from the last reflection of THIS ref if it is younger than the window.
//
// Per-ref rather than one global gate, because the refs are different questions with different
// costs: a hint about one Slack channel is one call and a full pass is one per channel, so a
// single gate would let either starve the other. Keying by ref also makes the coalescing land
// where a delivery storm actually lands — the same ref, over and over.
func (t *throttled) Reflect(ctx context.Context, ref entity.Ref) (Reflection, error) {
	key := ref.String()
	t.mu.Lock()
	prev, ok := t.last[key]
	t.mu.Unlock()
	if ok && time.Since(prev.at) < t.every {
		return prev.rn, nil
	}
	rn, err := t.src.Reflect(ctx, ref)
	if err != nil {
		return Reflection{}, err
	}
	t.mu.Lock()
	t.last[key] = answer{at: time.Now(), rn: rn}
	t.mu.Unlock()
	return rn, nil
}
