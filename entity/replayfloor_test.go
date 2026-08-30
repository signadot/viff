package entity

import (
	"errors"
	"testing"
	"time"

	logdserver "github.com/signadot/tony-format/go-tony/system/logd/server"
	"github.com/signadot/tony-format/go-tony/system/logd/storage"
)

// A cursor that this store instance would happily serve — at or above its own
// low-watermark — but whose deltas compaction has erased. logd used to answer such a
// range with whatever patches survived and report success, which reached the engine as a
// catch-up that looked complete and was not; the store's watermark was the only guard, and
// it does not cover a cursor issued within this instance's lifetime.
//
// It is covered now, by the substrate: logd keeps a replay floor and fails a below-floor
// FromCommit watch with a terminal "replay_compacted" event, which libctl raises as a
// WatchEndedError and Since already turns into ok=false (w.Err() != nil). This pins that,
// because nothing else in the suite reaches it — ordinary Since calls are shielded by the
// watermark, so a silent regression here would surface only as an engine that quietly
// missed commits under compaction.
//
// The compaction is provoked through go-tony's storage API directly (SwitchDLog +
// Compact with a cutoff older than every commit). That is the only way to create the
// condition; the ASSERTION stays on verse's own observable behaviour — what Since returns.
func TestSinceBelowReplayFloorResyncs(t *testing.T) {
	store, err := storage.Open(t.TempDir(), nil)
	if err != nil {
		t.Fatalf("open storage: %v", err)
	}
	defer store.Close()
	srv := logdserver.New(&logdserver.Spec{Storage: store})
	if err := srv.StartTCP("127.0.0.1:0"); err != nil {
		t.Fatalf("start logd: %v", err)
	}
	defer srv.StopTCP()

	// Through docd, as production does — a below-floor replay is a base-routed watch, so
	// the terminal event has to survive the proxy as well as the client library.
	docd := startDocd(t, srv.TCPAddr())
	s, err := OpenLogdStore(LogdConfig{Addr: docd.ClientTCPAddr(), ClientID: "verse-test"})
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer s.Close()

	// The cursor is taken BEFORE the writes, so what is asked for is a range that really
	// existed and was really compacted away — the ok=false below can only be logd refusing
	// the replay, which since the per-instance watermark went away is the only thing that
	// can produce one.
	from := s.Rev()
	for i := range 5 {
		set(t, s, RefAt("demo", "pr", string(rune('a'+i))), Obj{"stage": "open"}.Payload())
	}
	eventually(t, "history to land", func() bool { return s.Rev() >= 5 })

	if err := store.SwitchDLog(); err != nil {
		t.Fatalf("switch dlog: %v", err)
	}
	cfg := storage.DefaultCompactionConfig()
	cfg.Cutoff = -time.Hour // older than every commit: compact the whole delta history away
	if err := store.Compact(cfg); err != nil {
		t.Fatalf("compact: %v", err)
	}
	if floor := store.ReplayFloor(); floor <= from {
		t.Fatalf("replay floor = %d, want > %d — compaction did not erase the range under test", floor, from)
	}

	commits, err := s.Since(from)
	if err == nil {
		t.Fatalf("Since(%d) over a compacted range succeeded with %d commits; a truncated "+
			"catch-up must report the failure so the consumer resyncs", from, len(commits))
	}
	// AND WHICH FAILURE IT IS, since that is what the consumer branches on: this one, and only
	// this one, means the history is gone and resyncing is the answer.
	if !errors.Is(err, ErrReplayTooOld) {
		t.Fatalf("Since over a compacted range failed with %v; want ErrReplayTooOld, which is "+
			"what tells a consumer to resync rather than ask again", err)
	}
}
