package entity_test

import (
	"testing"

	"github.com/signadot/tony-format/go-tony/ir/kpath"

	"github.com/signadot/verse/pkg/entity"
	"github.com/signadot/verse/pkg/entity/entitytest"
)

func listed(t *testing.T, store entity.Store, path string, pattern entity.Payload) ([]string, bool) {
	t.Helper()
	at, err := kpath.Parse(path)
	if err != nil {
		t.Fatal(err)
	}
	paths, reached, err := store.PathsMatching(at, entity.Unbounded, pattern, nil)
	if err != nil {
		t.Fatalf("listing %s: %v", path, err)
	}
	out := make([]string, len(paths))
	for i, p := range paths {
		out[i] = p.KPath()
	}
	return out, reached
}

// scottsSecret is walter's `demo.pr.1`, readable by everyone, with a place inside it —
// `demo.pr.1.secret` — that holds out nothing: ana reads the row and not the secret.
func scottsSecret(t *testing.T) (system, ana entity.Store) {
	t.Helper()
	system = entity.As(entitytest.OpenStore(t), entity.SystemDefault)
	walter, pr := "user:walter@example.com", entity.RefAt("demo", "pr", "1")
	if _, _, err := entity.As(system, walter).Write(entity.Write{At: pr.At(),
		Value: entity.Obj{"stage": "open", "secret": entity.Obj{"key": "hunter2"}}.Payload()}); err != nil {
		t.Fatal(err)
	}
	secret := entity.RefAt("demo", "pr", "1", "secret")
	if err := entity.Claim(system, secret, walter); err != nil {
		t.Fatal(err)
	}
	if err := entity.SetGrants(system, secret, nil); err != nil {
		t.Fatal(err)
	}
	ana = entity.As(system, "user:ana@example.com")
	if _, _, err := ana.Get(secret); err == nil {
		t.Fatal("the fixture is wrong: ana reads the secret")
	}
	return system, ana
}

// A FILTERED LISTING IS NOT A READ OF WHAT THE READER MAY NOT READ. The store matches the
// predicate against what it holds, so a row that came back for a right guess and not for a wrong
// one told ana what `secret.key` holds, a guess at a time.
func TestAFilteredListingDoesNotSayWhatIsWithheld(t *testing.T) {
	system, ana := scottsSecret(t)
	guess := func(v string) entity.Payload {
		return entity.Obj{"secret": entity.Obj{"key": v}}.Payload()
	}
	right, _ := listed(t, ana, "demo.pr.*", guess("hunter2"))
	wrong, _ := listed(t, ana, "demo.pr.*", guess("wrong"))
	if len(right) != 0 || len(wrong) != 0 {
		t.Errorf("a right guess listed %v and a wrong one %v: the listing answers the guess", right, wrong)
	}
	// What she may read still filters, and the row comes back without what she may not.
	open, reached := listed(t, ana, "demo.pr.*", entity.Obj{"stage": "open"}.Payload())
	if !reached || len(open) == 0 {
		t.Fatalf("a predicate on what ana reads kept nothing: %v (reached %v)", open, reached)
	}
	for _, p := range open {
		if p == `demo.pr."1".secret` || p == `demo.pr."1".secret.key` {
			t.Errorf("the listing shows %s", p)
		}
	}
	// And whoever reads everything is answered by the store as it always was.
	if all, _ := listed(t, system, "demo.pr.*", guess("hunter2")); len(all) == 0 {
		t.Error("the system's filtered listing kept nothing")
	}
}

// NOR THAT IT STANDS. A path that reaches nothing is not-found and one that reached something
// the filter or the view left out is an empty answer, so the two told ana apart a withheld node
// from an absent one: `secret.key` answered empty where `secret.nokey` answered not-found.
func TestAListingDoesNotSayAWithheldNodeStands(t *testing.T) {
	_, ana := scottsSecret(t)
	_, there := listed(t, ana, "demo.pr.*.secret.key", nil)
	_, absent := listed(t, ana, "demo.pr.*.secret.nokey", nil)
	if there != absent {
		t.Errorf("reached = %v for a withheld node and %v for an absent one", there, absent)
	}
	_, there = listed(t, ana, "demo.pr.*.secret", entity.Obj{"key": "wrong"}.Payload())
	_, absent = listed(t, ana, "demo.pr.*.nosuch", entity.Obj{"key": "wrong"}.Payload())
	if there != absent {
		t.Errorf("filtered: reached = %v for a withheld node and %v for an absent one", there, absent)
	}
}

// A KEPT NODE MAY STAND BENEATH ANOTHER, and leaving the inner one is not leaving the outer: with
// one kept node remembered at a time, everything in `pr.1` after `sub` was dropped.
func TestAFilteredListingKeepsWhatFollowsAKeptNodeInsideAnother(t *testing.T) {
	system := entity.As(entitytest.OpenStore(t), entity.SystemDefault)
	if _, _, err := system.Write(entity.Write{At: entity.RefAt("demo", "pr", "1").At(), Value: entity.Obj{
		"stage": "open", "sub": entity.Obj{"stage": "open", "x": 1}, "z": 2,
	}.Payload()}); err != nil {
		t.Fatal(err)
	}
	got, _ := listed(t, system, "demo..", entity.Obj{"stage": "open"}.Payload())
	for _, want := range []string{`demo.pr."1"`, `demo.pr."1".stage`, `demo.pr."1".sub`, `demo.pr."1".sub.x`, `demo.pr."1".z`} {
		found := false
		for _, p := range got {
			found = found || p == want
		}
		if !found {
			t.Errorf("the listing lost %s; got %v", want, got)
		}
	}
}

// ONE PRINCIPAL SEES ONE SET WHICHEVER WAY THEY ASK. A read of a node keeps a place inside a
// withheld one that is held out to the reader, so a listing keeps it too: passing over
// everything beneath a withheld node would answer less than the read of the same address does.
func TestAListingShowsWhatAReadOfTheSameNodeCarries(t *testing.T) {
	system := entity.As(entitytest.OpenStore(t), entity.SystemDefault)
	alice, bob := "user:alice@example.com", "user:bob@example.com"
	team, closed, pub := entity.RefAt("team"), entity.RefAt("team", "c"), entity.RefAt("team", "c", "pub")
	if _, _, err := entity.As(system, alice).Write(entity.Write{At: team.At(), Value: entity.Obj{
		"note": "x", "c": entity.Obj{"secret": "hers", "pub": entity.Obj{"v": 1}},
	}.Payload()}); err != nil {
		t.Fatal(err)
	}
	for ref, grants := range map[entity.Ref][]entity.Grant{
		closed: nil,
		pub:    {{At: pub.Segs(), To: map[entity.Level][]string{entity.Read: {bob}}}},
	} {
		if err := entity.Claim(system, ref, alice); err != nil {
			t.Fatal(err)
		}
		if err := entity.SetGrants(system, ref, grants); err != nil {
			t.Fatal(err)
		}
	}
	asBob := entity.As(system, bob)
	node, _, err := asBob.ReadAt(team.Segs())
	if err != nil {
		t.Fatal(err)
	}
	read := entity.Get(entity.Get(node, "c"), "pub") != nil
	if !read || entity.Get(entity.Get(node, "c"), "secret") != nil {
		t.Fatalf("the fixture is wrong: bob's read of team carries c.pub=%v and c.secret=%v", read, entity.Get(entity.Get(node, "c"), "secret") != nil)
	}
	paths, err := asBob.PathsAt(team.Segs(), entity.Unbounded)
	if err != nil {
		t.Fatal(err)
	}
	listed := map[string]bool{}
	for _, p := range paths {
		listed[p.KPath()] = true
	}
	for path, want := range map[string]bool{"team.note": true, "team.c.pub": true, "team.c.pub.v": true, "team.c": false, "team.c.secret": false} {
		if listed[path] != want {
			t.Errorf("the listing carries %s: %v, want %v (it carries %v)", path, listed[path], want, listed)
		}
	}
}
