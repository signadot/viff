package entity_test

import (
	"context"
	"errors"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"github.com/signadot/verse/pkg/entity"
	"github.com/signadot/verse/pkg/entity/entitytest"
)

// WHAT IS SERVED FROM A REVISION IS CHECKED AGAINST PERMS AS OF THAT REVISION, AT LEAST: a record
// committed at R holds for a read served at R, however late the view's own watch delivers it.
// Each test here writes around the view — as another process would, or the system through an
// attribution of its own — and then reads through the view at once, where a view that learned
// of the record only from its watch answered from before it.

const (
	alice    = "user:alice@example.com"
	stranger = "user:stranger@example.com"
)

// twoClients is two stores over one substrate: the one the view reads through, and one that
// writes around it, as another process does. The view's hears of the mirror late (lagging).
func twoClients(t *testing.T) (view entity.Store, writer *entity.LogdStore) {
	t.Helper()
	addr := entitytest.Addr(t)
	return lagging{entitytest.StoreAt(t, addr)}, entitytest.StoreAt(t, addr)
}

// lagging is a store whose watches on the perms mirror deliver each commit a little late, as a
// watch on a busy store does (tens of milliseconds): the window in which a view that answers
// from what its watch has delivered answers from before the record.
type lagging struct{ *entity.LogdStore }

const lag = 20 * time.Millisecond

func (l lagging) WatchSubtreeProgress(depth int, prefix ...string) (<-chan entity.Commit, func(), func(context.Context) (int64, error), error) {
	in, unsub, progress, err := l.LogdStore.WatchSubtreeProgress(depth, prefix...)
	if err != nil || !perms(prefix) {
		return in, unsub, progress, err
	}
	return delayed(in, func() { time.Sleep(lag) }), unsub, progress, nil
}

// delayed is a stream that waits on hold before passing each commit on, in order.
func delayed(in <-chan entity.Commit, hold func()) <-chan entity.Commit {
	out := make(chan entity.Commit, 256)
	go func() {
		defer close(out)
		for c := range in {
			hold()
			out <- c
		}
	}()
	return out
}

// openTracked is a verse whose root holds `read` out to everyone beneath it, with the mirror
// loaded and watched through the system's view; raw is the store underneath, written around it.
func openTracked(t *testing.T, raw entity.Store) entity.Store {
	t.Helper()
	system := entity.As(raw, entity.SystemDefault)
	seedRoot(t, system, entity.SystemDefault, grant(entity.Read, entity.Everyone))
	if err := entity.Track(t.Context(), system); err != nil {
		t.Fatal(err)
	}
	return system
}

// createPrivate writes demo.<id> and its record, which holds nothing out, in ONE commit: a
// write at the root carrying both.
func createPrivate(t *testing.T, writer entity.Store, id string) entity.Ref {
	t.Helper()
	node := entity.RefAt("demo", id)
	rec := entity.Obj{entity.OwnerField: alice, entity.GrantsField: entity.GrantsPayload(node.Segs(), nil)}.Payload()
	mirror := entity.Obj{entity.PermsSystem: entity.Obj{entity.PermsKind: entity.Obj{node.String(): rec}.Payload()}.Payload()}.Payload()
	if _, _, err := writer.Write(entity.Write{At: entity.RefAt().At(), Value: entity.Obj{
		"demo":           entity.Obj{id: entity.Obj{"secret": id}.Payload()}.Payload(),
		entity.VerseRoot: mirror,
	}.Payload()}); err != nil {
		t.Fatalf("writing %s with its record: %v", node, err)
	}
	return node
}

func TestANodeWrittenWithItsRecordIsRefusedAtOnce(t *testing.T) {
	raw, writer := twoClients(t)
	them := entity.As(openTracked(t, raw), stranger)
	const n = 50
	admitted := 0
	for i := range n {
		node := createPrivate(t, writer, fmt.Sprint("n", i))
		e, ok, err := them.Get(node)
		switch {
		case errors.Is(err, entity.ErrForbidden):
		case err != nil:
			t.Fatalf("read %s: %v", node, err)
		default:
			admitted++
			t.Logf("admitted to %s: %v %v", node, ok, e.Payload)
		}
	}
	if admitted > 0 {
		t.Fatalf("a stranger read %d of %d nodes whose record, committed with them, holds nothing out", admitted, n)
	}
}

// drainUntil reads a watch until an event for the sentinel arrives, and answers every ref it
// delivered on the way.
func drainUntil(t *testing.T, ch <-chan entity.Commit, sentinel entity.Ref) []entity.Event {
	t.Helper()
	var got []entity.Event
	deadline := time.After(10 * time.Second)
	for {
		select {
		case c, ok := <-ch:
			if !ok {
				t.Fatal("the watch ended")
			}
			for _, ev := range c.Changes {
				got = append(got, ev)
				if ev.Ref == sentinel {
					return got
				}
			}
		case <-deadline:
			t.Fatalf("the sentinel %s never arrived", sentinel)
		}
	}
}

func TestANodeWrittenWithItsRecordIsNeverDeliveredToAStrangersWatch(t *testing.T) {
	raw, writer := twoClients(t)
	them := entity.As(openTracked(t, raw), stranger)
	ch, unsub, err := them.WatchSubtree(2, "demo")
	if err != nil {
		t.Fatal(err)
	}
	defer unsub()
	private := map[entity.Ref]bool{}
	for i := range 50 {
		private[createPrivate(t, writer, fmt.Sprint("n", i))] = true
	}
	end := entity.RefAt("demo", "end")
	entitytest.Set(t, writer, end, entity.Obj{"v": 1}.Payload())
	for _, ev := range drainUntil(t, ch, end) {
		if private[ev.Ref] {
			t.Errorf("the stranger's watch delivered %s at %d, whose record holds nothing out", ev.Ref, ev.Rev)
		}
	}
}

// A REVOKE AT R holds for what is served after it: a read at once is refused, and a change the
// node makes after it is not delivered to the reader it was withdrawn from.
//
// Not "the perms as of R" — a view past R is one that has applied the revoke, and a grant after
// it admits what it admits — so the watch's half revokes once and grants nothing after.
func TestARevokeHoldsForWhatIsServedAfterIt(t *testing.T) {
	raw, writer := twoClients(t)
	system := openTracked(t, raw)
	bob := "user:bob@example.com"
	reader := entity.As(system, bob)
	// Another process: an attribution of its own over the same store, sharing nothing with the
	// view the reader reads through.
	other := entity.As(writer, entity.SystemDefault)
	node := entity.RefAt("demo", "shared")
	entitytest.Set(t, writer, node, entity.Obj{"n": 0}.Payload())
	if err := entity.Establish(other, node, alice, entity.Ref{}, []entity.Grant{}); err != nil {
		t.Fatal(err)
	}
	readable := []entity.Grant{{At: node.Segs(), To: map[entity.Level][]string{entity.Read: {bob}}}}
	grant := func() {
		t.Helper()
		if err := entity.SetGrants(other, node, readable); err != nil {
			t.Fatal(err)
		}
		deadline := time.Now().Add(3 * time.Second)
		for {
			_, _, err := reader.Get(node)
			if err == nil {
				return
			}
			if time.Now().After(deadline) {
				t.Fatalf("bob does not read %s once granted: %v", node, err)
			}
			time.Sleep(time.Millisecond)
		}
	}
	revoke := func() {
		t.Helper()
		if err := entity.SetGrants(other, node, nil); err != nil {
			t.Fatal(err)
		}
	}
	for i := range 20 {
		grant()
		revoke()
		if _, _, err := reader.Get(node); !errors.Is(err, entity.ErrForbidden) {
			t.Fatalf("round %d: bob read %s right after it was withdrawn: %v", i, node, err)
		}
	}

	grant()
	ch, unsub, err := reader.WatchSubtree(2, "demo")
	if err != nil {
		t.Fatal(err)
	}
	defer unsub()
	revoke()
	withdrawn := map[int64]bool{} // the revisions of changes made after bob's read was withdrawn
	for i := range 20 {
		_, rev, err := writer.Write(entity.Write{At: node.At(), Value: entity.Obj{"n": i + 1}.Payload()})
		if err != nil {
			t.Fatal(err)
		}
		withdrawn[rev] = true
	}
	end := entity.RefAt("demo", "end")
	entitytest.Set(t, writer, end, entity.Obj{"v": 1}.Payload())
	delivered := 0
	for _, ev := range drainUntil(t, ch, end) {
		if ev.Ref == node && withdrawn[ev.Rev] {
			delivered++
		}
	}
	if delivered > 0 {
		t.Fatalf("bob's watch delivered %d of %d changes to %s made after his read was withdrawn", delivered, len(withdrawn), node)
	}
}

// stalling is a store whose perms watches stop answering once it stalls: a progress request is
// refused (as `unsupported` is), or acknowledged and never answered in the stream (as by a watch
// that has stopped delivering). What the watches deliver waits until it no longer stalls.
type stalling struct {
	*entity.LogdStore
	mode atomic.Int32
}

const (
	answering = iota
	refusing
	silent
)

func perms(at []string) bool {
	return len(at) >= 2 && at[0] == entity.VerseRoot && at[1] == entity.PermsSystem
}

func (s *stalling) WatchSubtreeProgress(depth int, prefix ...string) (<-chan entity.Commit, func(), func(context.Context) (int64, error), error) {
	in, unsub, progress, err := s.LogdStore.WatchSubtreeProgress(depth, prefix...)
	if err != nil || !perms(prefix) {
		return in, unsub, progress, err
	}
	asked := func(ctx context.Context) (int64, error) {
		if s.mode.Load() == refusing {
			return 0, errors.New("unsupported: the watch cannot say how far it is current")
		}
		return progress(ctx)
	}
	return delayed(in, func() {
		for s.mode.Load() != answering {
			time.Sleep(time.Millisecond)
		}
	}), unsub, asked, nil
}

// A VIEW THAT CANNOT BE BROUGHT UP TO WHAT IS SERVED REFUSES IT, unchecked, rather than checking it
// against the view it already has: whether the watches refuse to be asked or never answer.
func TestAStalledViewRefusesRatherThanAdmits(t *testing.T) {
	defer entity.SetProgressBudget(200 * time.Millisecond)()
	addr := entitytest.Addr(t)
	st := &stalling{LogdStore: entitytest.StoreAt(t, addr)}
	writer := entitytest.StoreAt(t, addr)
	them := entity.As(openTracked(t, st), stranger)
	for _, mode := range []int32{refusing, silent} {
		st.mode.Store(mode)
		node := createPrivate(t, writer, fmt.Sprint("stalled", mode))
		e, ok, err := them.Get(node)
		if err == nil {
			t.Fatalf("mode %d: a stalled view admitted a stranger to %s: %v %v", mode, node, ok, e.Payload)
		}
		if errors.Is(err, entity.ErrForbidden) || !entity.IsUnread(err) {
			t.Fatalf("mode %d: a stalled view answered other than unchecked: %v", mode, err)
		}
	}
	// And once the watches answer again, the read is checked, and refused on the record.
	st.mode.Store(answering)
	if _, _, err := them.Get(entity.RefAt("demo", fmt.Sprint("stalled", silent))); !errors.Is(err, entity.ErrForbidden) {
		t.Fatalf("once the watches answer, the read is refused on the record: %v", err)
	}
}
