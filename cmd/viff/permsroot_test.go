package main

import (
	"strings"
	"testing"

	"go.viff.dev/viff/internal/config"
	"go.viff.dev/viff/pkg/entity"
)

// `viff perms --root` is the top itself, whose address is the empty path — which on this
// command line is the listing, so it takes a flag rather than a word. It took no words and then
// read args[0].
func TestPermsRootTakesNoPath(t *testing.T) {
	serve(t)
	out, err := runCLI(t, "perms", "--root")
	if err != nil {
		t.Fatalf("perms --root: %v", err)
	}
	if !strings.Contains(out, "admin@viff") {
		t.Errorf("perms --root did not read the top: %q", out)
	}
	// …and it is the root or a path, never both.
	if _, err := runCLI(t, "perms", "--root", "demo"); err == nil {
		t.Error("perms --root demo was admitted")
	}
}

// `perms.child-grants` IS READ FROM THE FIELD IT IS UNDER. Asked for under a name the object did not
// have, it was never found, and a default never found was read as the empty one: every
// deployment that said what a new node holds out was told nothing is, and one that said
// something the grammar refuses was told nothing at all.
func TestTheConfiguredDefaultIsWhatIsSeeded(t *testing.T) {
	p, err := permsPolicy(config.PermsConfig{ChildGrants: map[string][]string{"read": {"*"}}})
	if err != nil {
		t.Fatal(err)
	}
	if got := p.ChildGrants[entity.Read]; len(got) != 1 || got[0] != entity.Everyone {
		t.Errorf("default = %v, want read to everyone", p.ChildGrants)
	}
	if p, err = permsPolicy(config.PermsConfig{ChildGrants: map[string][]string{}}); err != nil || p.ChildGrants == nil || len(p.ChildGrants) != 0 {
		t.Errorf("an empty default = %v (err %v), want the setting that holds out nothing", p.ChildGrants, err)
	}
	if p, err = permsPolicy(config.PermsConfig{}); err != nil || p.ChildGrants != nil {
		t.Errorf("no default = %v (err %v), want unset", p.ChildGrants, err)
	}
	if _, err = permsPolicy(config.PermsConfig{ChildGrants: map[string][]string{"owner": {"*"}}}); err == nil || !strings.Contains(err.Error(), "perms.child-grants") {
		t.Errorf("a default the grammar refuses: %v, want it refused naming perms.child-grants", err)
	}
}
