package trigger

import (
	"context"
	"testing"
	"time"

	"github.com/example/infra/verse/entity"
	"github.com/example/infra/verse/process"

	logdserver "github.com/signadot/tony-format/go-tony/system/logd/server"
	"github.com/signadot/tony-format/go-tony/system/logd/storage"
)

func startLogdAddr(t *testing.T) string {
	t.Helper()
	store, err := storage.Open(t.TempDir(), nil)
	if err != nil {
		t.Fatalf("open storage: %v", err)
	}
	t.Cleanup(func() { store.Close() })
	srv := logdserver.New(&logdserver.Spec{Storage: store})
	if err := srv.StartTCP("127.0.0.1:0"); err != nil {
		t.Fatalf("start logd: %v", err)
	}
	t.Cleanup(func() { srv.StopTCP() })
	return srv.TCPAddr()
}

// The whole emergent chain runs over a real logd store: an injected pr change flows
// open → reviewed → deployed purely by triggers reacting to state that other
// actions wrote back through logd. This exercises the engine against logd's sparse,
// non-dense commit revisions (the reason the cursor is treated as opaque-monotonic
// and reconciled via Since), plus the watch → event → dispatch path end to end.
func TestEmergentChainOverLogd(t *testing.T) {
	store, err := entity.OpenLogdStore(entity.LogdConfig{Addr: startLogdAddr(t), ClientID: "verse-e2e"})
	if err != nil {
		t.Fatalf("open logd store: %v", err)
	}
	defer store.Close()

	eng := New(store, process.AutoResolver{}) // Ask gates auto-proceed
	eng.Add(Trigger{
		Name:    "review",
		Matches: []EventMatch{{System: "demo", Kind: "pr", Fields: map[string]any{"stage": "open"}}},
		Action:  setStage("reviewed"),
	})
	eng.Add(Trigger{
		Name:    "deploy",
		Matches: []EventMatch{{System: "demo", Kind: "pr", Fields: map[string]any{"stage": "reviewed"}}},
		Gates:   []Gate{{Mode: process.Ask}}, // a human/agent approves the deploy
		Action:  setStage("deployed"),
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	eng.Start(ctx)

	subj := entity.Ref{System: "demo", Kind: "pr", ID: "1"}
	store.Put(subj, entity.Payload{"stage": "open"}) // ignite

	waitStage(t, store, subj, "deployed")

	// The chain converged and stopped (nothing matches "deployed"): exactly the two
	// fires, no runaway, even though logd commits are not dense.
	time.Sleep(100 * time.Millisecond)
	if f := eng.Fired(); f != 2 {
		t.Fatalf("fired = %d, want 2 (review + deploy)", f)
	}
}
