package source_test

import (
	"strings"
	"testing"

	"github.com/signadot/verse/codec"
	"github.com/signadot/verse/entity"
	"github.com/signadot/verse/entity/entitytest"
	"github.com/signadot/verse/source"
)

// The address, both ways. A name may carry a colon — ParseRef splits three ways and the rest is
// the id — and everything that reads a registration has to agree about that, or a source called
// `a:b` is installable and then unfindable.
func TestRegistrationRefRoundTrips(t *testing.T) {
	for _, name := range []string{"laptop", "build-host", "a:b"} {
		ref := source.Ref("git", name)
		parsed, err := entity.ParseRef(ref.String())
		if err != nil {
			t.Fatalf("ParseRef(%q): %v", ref, err)
		}
		if parsed != ref {
			t.Fatalf("%q round-tripped to %q", ref, parsed)
		}
		if parsed.System != source.System || parsed.Kind != "git" || parsed.ID != name {
			t.Fatalf("the address should be sources:<system>:<name>, got %+v", parsed)
		}
	}
}

// Every refusal Check owes, and each is something that cannot be discovered later.
func TestRegistrationCheck(t *testing.T) {
	ok := source.Entity{Name: "laptop", System: "git", URL: "http://build-host:9801"}
	if err := ok.Check(); err != nil {
		t.Fatalf("a well-formed registration should pass: %v", err)
	}
	if err := (source.Entity{Name: "local", System: "git", Dir: "/repos/verse"}).Check(); err != nil {
		t.Fatalf("`dir` is a way in, not a missing url: %v", err)
	}

	for _, tc := range []struct {
		name string
		e    source.Entity
		want string // a fragment of the message, so the refusal has to SAY something
	}{
		{"no name", source.Entity{System: "git", URL: "http://x:1"}, "name is required"},
		{"no system", source.Entity{Name: "a", URL: "http://x:1"}, "system is required"},
		{"a colon in the system", source.Entity{Name: "a", System: "gi:t", URL: "http://x:1"},
			"may not contain"},
		{"a slash in the name", source.Entity{Name: "a/b", System: "git", URL: "http://x:1"},
			"path segment"},
		{"no way in", source.Entity{Name: "a", System: "git"}, "exactly one way in"},
		{"two ways in", source.Entity{Name: "a", System: "git", URL: "http://x:1", Dir: "/r"},
			"three different ways in"},
		// The typo an operator actually makes: url.Parse takes "build-host:9801" as a scheme
		// with an opaque body, and it would install as a source nothing can dial.
		{"a url with no scheme", source.Entity{Name: "a", System: "git", URL: "build-host:9801"},
			"http:// or https://"},
		{"a url with no host", source.Entity{Name: "a", System: "git", URL: "http://"},
			"no host"},
		{"an unparseable claim", source.Entity{Name: "a", System: "git", URL: "http://x:1",
			Owns: []source.Own{{Ref: "git:ref:a*b"}}}, "TRAILING"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.e.Check()
			if err == nil {
				t.Fatal("should be refused")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("message should mention %q, got: %v", tc.want, err)
			}
		})
	}
}

// dial-in is designed and not built. It is refused rather than accepted, because a registration
// that installed and never dialled would sit in a listing looking installed — the one state an
// operator cannot debug from the outside.
func TestDialInIsRefusedUntilItIsBuilt(t *testing.T) {
	err := (source.Entity{Name: "laptop", System: "git", DialIn: "nonce"}).Check()
	if err == nil {
		t.Fatal("dial-in should be refused while it is unbuilt")
	}
	if !strings.Contains(err.Error(), "not built") {
		t.Fatalf("the refusal should say it is unbuilt rather than reading as a design "+
			"decision, got: %v", err)
	}
}

// A field the decoder would drop, named while somebody is present. It matters more here than
// for a runner: a dropped `owns` installs a source that claims nothing, and a dropped `scope`
// reflects into baseline where everybody sees it.
func TestRegistrationCheckFields(t *testing.T) {
	parse := func(t *testing.T, src string) entity.Payload {
		t.Helper()
		n, err := codec.ParseNode([]byte(src), codec.Tony)
		if err != nil {
			t.Fatalf("parse %q: %v", src, err)
		}
		return n
	}
	for _, src := range []string{
		"{name: a, system: git, ulr: \"http://x:1\"}",
		"{name: a, system: git, url: \"http://x:1\", scoep: s}",
		"{name: a, system: git, url: \"http://x:1\", ownes: [\"git:*:a/*\"]}",
	} {
		err := source.CheckFields(parse(t, src))
		if err == nil {
			t.Fatalf("%s should have named the unknown field", src)
		}
	}
	// Everything verse itself writes is known, so `verse source show` piped back into
	// `verse source add` is not refused for carrying what it was just handed.
	full := "{name: a, system: git, url: \"http://x:1\", owns: [{ref: \"git:*:a/*\"}], scope: s, " +
		"lastReflect: \"2026-01-01T00:00:00Z\", lastError: \"\"}"
	if err := source.CheckFields(parse(t, full)); err != nil {
		t.Fatalf("a registration verse wrote should be re-installable: %v", err)
	}

	// And the shape that USED to work is refused with what to write instead. `owns` was a list
	// of ref globs; a claim is now a document, because it may name a path inside a payload and
	// there is no terse spelling of one. The decoder's own complaint is "expected object, got
	// String" — true, and no help to the person reading it, who is upgrading.
	err := source.CheckFields(parse(t, "{name: a, system: git, owns: [\"git:*:verse/*\"]}"))
	if err == nil {
		t.Fatal("a list of ref globs should be refused")
	}
	for _, want := range []string{"{ref:", "git:*:verse/*", "path inside a payload"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("the refusal does not say %q: %v", want, err)
		}
	}
}

// Find and List read what was written, in one order, and accept the ref in both spellings.
func TestRegistrationFindAndList(t *testing.T) {
	store := entitytest.Store(t)
	write := func(e source.Entity) {
		t.Helper()
		node, err := codec.ToNode(e)
		if err != nil {
			t.Fatal(err)
		}
		entitytest.Set(t, store, source.Ref(e.System, e.Name), node)
	}
	write(source.Entity{Name: "laptop", System: "git", URL: "http://a:1",
		Owns: []source.Own{{Ref: "git:*:verse/*"}}})
	write(source.Entity{Name: "build", System: "git", Dir: "/repos"})
	write(source.Entity{Name: "eng", System: "linear", URL: "http://b:1", Scope: "s"})

	got := source.List(store)
	if len(got) != 3 {
		t.Fatalf("List = %d registrations, want 3", len(got))
	}
	// Ordered by address, so two readers see one order.
	if got[0].Name != "build" || got[1].Name != "laptop" || got[2].System != "linear" {
		t.Fatalf("List order = %+v", got)
	}

	for _, spelling := range []string{"git:laptop", "sources:git:laptop"} {
		e, ok := source.Find(store, spelling)
		if !ok {
			t.Fatalf("Find(%q) found nothing", spelling)
		}
		// Every field survives the round trip — the `owns` one especially, since a claim lost
		// on the way is a registration that can be proved disjoint from nothing.
		if e.URL != "http://a:1" || len(e.Owns) != 1 || e.Owns[0].Ref != "git:*:verse/*" {
			t.Fatalf("Find(%q) = %+v", spelling, e)
		}
	}
	if _, ok := source.Find(store, "git:nope"); ok {
		t.Fatal("Find should answer false for a registration that is not there")
	}
	if _, ok := source.Find(store, "laptop"); ok {
		t.Fatal("a ref with no system half is not a ref")
	}
}
