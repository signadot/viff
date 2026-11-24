package viffd

import (
	"strings"
	"testing"

	"go.viff.dev/viff/pkg/entity"
	"go.viff.dev/viff/pkg/entity/entitytest"
)

// A SPAWN SPENDS WHAT ITS NAMER MAY READ.
//
// The credential is named by ADDRESS, and the read of that address is the authority: who may
// spend a key is a permission, answered by the permissions model viff already has rather than
// by a group or a list invented for credentials. Checked against the NAMER and not against the
// runtime, because a runner reads as itself — a charter naming an address its own owner may not
// read would otherwise work, and the property would be decoration.
func TestASpawnSpendsWhatItsNamerMayRead(t *testing.T) {
	store := entitytest.OpenStore(t)
	const (
		ana = "user:ana@example.com"
		bo  = "user:bo@example.com"
		at  = "acme.keys.review"
	)
	// The node IS the credential, so rotating it is a write and every charter naming this
	// address follows untouched.
	if _, _, err := entity.As(store, ana).Write(entity.Write{
		At:    entity.RefAt("acme", "keys", "review").At(),
		Value: entity.Obj{"key": "vk-review"}.Payload(),
	}); err != nil {
		t.Fatal(err)
	}
	// …and ana FENCES it, which is what makes the question below a real one: an owner and
	// `grants: []` out loud, since a record with an owner and no grants field reads as holding
	// out the root's default. This is the step an operator does with `viff perms`, and without
	// it a credential under an open root is everybody's.
	if _, _, err := entity.As(store, entity.SystemSeed).Write(entity.Write{
		At: entity.PermsRef(entity.RefAt("acme", "keys", "review")).At(),
		Value: entity.Obj{
			entity.OwnerField:  ana,
			entity.GrantsField: entity.GrantsPayload(nil, nil),
		}.Payload(),
		Principal: entity.SystemSeed,
	}); err != nil {
		t.Fatal(err)
	}

	// Its owner names it and spends it.
	got, err := credentialAt(store, ana, at)
	if err != nil || got != "vk-review" {
		t.Fatalf("as its owner: %q, %v; want the key it holds", got, err)
	}

	// Somebody else names the same address and is refused — with a refusal that says both
	// things it could be, since a node you may not read reads as one that is not there
	// (git-issue 65xaf72n).
	if got, err := credentialAt(store, bo, at); err == nil {
		t.Errorf("a spawn named %s as %s and got %q", at, bo, got)
	} else if !strings.Contains(err.Error(), at) || !strings.Contains(err.Error(), bo) {
		t.Errorf("refusal %q, want it to name the address and who was refused", err)
	}

	// A spawn that names nothing spends the runtime's default, which is every spawn written
	// before this existed.
	if got, err := credentialAt(store, ana, ""); err != nil || got != "" {
		t.Errorf("unnamed: %q, %v; want the runtime's own default", got, err)
	}
}

// A NODE THAT HOLDS NO REFERENCE IS REFUSED, rather than read as "no credential" and run on the
// default. The alternative spends the wrong budget silently, which is the failure this whole
// path exists to make impossible.
func TestACredentialNodeWithoutAReferenceIsRefused(t *testing.T) {
	store := entitytest.OpenStore(t)
	const ana = "user:ana@example.com"
	if _, _, err := entity.As(store, ana).Write(entity.Write{
		At:    entity.RefAt("acme", "keys", "empty").At(),
		Value: entity.Obj{"note": "somebody wrote prose here"}.Payload(),
	}); err != nil {
		t.Fatal(err)
	}
	_, err := credentialAt(store, ana, "acme.keys.empty")
	if err == nil {
		t.Fatal("a node naming no credential resolved to one")
	}
	if !strings.Contains(err.Error(), "key") {
		t.Errorf("refusal %q, want it to name the field it wanted", err)
	}

	// And an address that is not one says so as an address problem, not a permission one.
	if _, err := credentialAt(store, ana, "not an address at all"); err == nil {
		t.Error("a malformed address resolved")
	}
}
