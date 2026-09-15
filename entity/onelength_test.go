package entity

import (
	"strings"
	"testing"
)

// ONE PATH, ONE LENGTH, ONE ROOT. An address's length is its segments, `verse` included, and the
// root a path renders under is decided from those same segments whoever asks — a Get, a read at
// a path, a replay, a watch. A mounted place is `local.dir-content-*` from the top of the tree, so
// the same segments under `verse` are not mounted, and every entry point agrees on both.
func TestOnePathRendersUnderOneRoot(t *testing.T) {
	s := openStore(t)
	mounted := RefAt("local", "dir-content-a1", "notes.txt")
	for _, tc := range []struct {
		ref   Ref
		mount bool
	}{
		{mounted, true},
		{VerseRef("local", "dir-content-a1", "notes.txt"), false},
		{RefAt("demo", "pr", "1"), false},
		{VerseRef("task", "instance", "run-1"), false},
	} {
		segs := tc.ref.Segs()
		if got := IsMountOwned(segs...); got != tc.mount {
			t.Errorf("IsMountOwned(%s) = %v, want %v", tc.ref, got, tc.mount)
		}
		want := seg(s.root)
		if tc.mount {
			want = seg(MountRoot)
		}
		if got := s.rootFor(tc.ref); got != want {
			t.Errorf("a ref at %s renders under %s, want %s", tc.ref, got, want)
		}
		if got := s.rootForAt(segs); got != want {
			t.Errorf("a path at %s renders under %s, want %s", tc.ref, got, want)
		}
		if !strings.HasPrefix(s.refPath(tc.ref), want+".") {
			t.Errorf("refPath(%s) = %s, want it under %s", tc.ref, s.refPath(tc.ref), want)
		}
	}
	// What has no history says so at every entry point that would look for one.
	if _, err := s.Since(0, mounted.Segs()...); err == nil || !strings.Contains(err.Error(), "no commits") {
		t.Errorf("Since at a mounted place = %v, want a refusal naming why", err)
	}
	if _, _, err := s.WatchSubtree(len(mounted.Segs()), mounted.Segs()[:2]...); err == nil ||
		!strings.Contains(err.Error(), "no commits") {
		t.Errorf("WatchSubtree at a mounted place = %v, want a refusal naming why", err)
	}
}
