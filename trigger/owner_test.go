package trigger

import (
	"strings"
	"testing"

	"github.com/signadot/verse/entity"
	"github.com/signadot/verse/launch"
	"github.com/signadot/verse/task"
)

// Every id a firing mints leads with the engine that minted it — the scope, then the charter's
// address — so same-named rules in two engines about one subject never share a task or a
// question, and the parts still come apart where a reader cuts them.
func TestIDsLeadWithTheEngineThatMintedThem(t *testing.T) {
	ref := entity.RefAt("demo", "pr", "1")
	for _, c := range []struct {
		scope string
		at    []string
		want  string
	}{
		{"", nil, "review.onDone@demo.pr.1"},
		{"", []string{"verse-dev", "drift"}, "verse-dev.drift:review.onDone@demo.pr.1"},
		{"me", nil, "me::review.onDone@demo.pr.1"},
	} {
		id := spawnID(ownerOf(c.scope, c.at), "review.onDone", ref)
		if id != c.want {
			t.Errorf("spawn id in scope %q at %v = %q, want %q", c.scope, c.at, id, c.want)
		}
		if chain, rest, ok := chainOf(id); !ok || chain != "review.onDone" || rest != "demo.pr.1" {
			t.Errorf("chainOf(%q) = %q %q %v", id, chain, rest, ok)
		}
	}

	// A scope or an address spelling a separator is rewritten and hashed, so it neither moves a
	// boundary nor collides with the name it was rewritten to.
	seen := map[string]string{}
	for _, owner := range []string{
		ownerOf("", nil), ownerOf("me", nil), ownerOf("", []string{"me"}),
		ownerOf("a:b", nil), ownerOf("a_b", nil), ownerOf("a@b", nil), ownerOf("a~b", nil),
		ownerOf("", []string{"a:b"}), ownerOf("", []string{"a_b"}), ownerOf("me", []string{"x"}),
	} {
		id := spawnID(owner, "review", ref)
		if prev, dup := seen[id]; dup {
			t.Fatalf("owners %q and %q mint one id %q", prev, owner, id)
		}
		seen[id] = owner
		if chain, _, _ := chainOf(id); chain != "review" {
			t.Errorf("chainOf(%q) = %q: the owner %q moved the boundary", id, chain, owner)
		}
		gate := gateID(owner, "review", "ok", []entity.Ref{ref})
		if _, rest, ok := strings.Cut(gate, task.GateSep); !ok || strings.Contains(rest, task.GateSep) {
			t.Errorf("gate id %q does not split once on %q", gate, task.GateSep)
		}
	}
	if gateID(ownerOf("me", nil), "review", "ok", []entity.Ref{ref}) == gateID("", "review", "ok", []entity.Ref{ref}) {
		t.Error("a scope's gate and baseline's share a question id")
	}
	if selectID(ownerOf("me", nil), "review", ref, &launch.Provider{}) == selectID("", "review", ref, &launch.Provider{}) {
		t.Error("a scope's selection and baseline's share a question id")
	}

	// And a rule's name may not spell the address's separator, or `x:y@s` could be two rules.
	if err := validTriggerName("x:y"); err == nil {
		t.Error("a rule named x:y installed; its ids would read as rule y at x")
	}
}
