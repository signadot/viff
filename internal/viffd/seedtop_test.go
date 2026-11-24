package viffd

import (
	"testing"

	"go.viff.dev/viff/pkg/entity"
	"go.viff.dev/viff/pkg/entity/entitytest"
)

// The seed writes the tree's own top: who may take a place, and where viff's half begins. The
// open viff and the administered one differ by one line of config, and the check reads that
// line as a record rather than as a rule only the code knows.

func seeded(t *testing.T, p Perms) entity.Store {
	t.Helper()
	store := entity.As(entitytest.Store(t), entity.SystemDefault)
	if err := seedPerms(store, p); err != nil {
		t.Fatalf("seeding: %v", err)
	}
	return store
}

// mayCreate is whether who may bring at into being — what a `child-owner` line holds out, which
// holding `owner` at a node that stands does not say.
func mayCreate(t *testing.T, store entity.Store, who string, at entity.Ref) bool {
	t.Helper()
	ok, err := entity.MayCreate(store, who, at)
	if err != nil {
		t.Fatalf("checking whether %s may create %s: %v", who, at, err)
	}
	return ok
}

func holds(t *testing.T, store entity.Store, who string, level entity.Level, at entity.Ref) bool {
	t.Helper()
	ok, err := entity.Holds(store, who, level, at)
	if err != nil {
		t.Fatalf("checking %s at %s: %v", who, at, err)
	}
	return ok
}

// With no admins named, the seed says the open viff in a record: anybody takes a place in the
// world's half, and what they create is theirs.
func TestAnOpenViffSaysItInTheRootRecord(t *testing.T) {
	store := seeded(t, Perms{})
	bob := "user:bob@example.com"
	if !mayCreate(t, store, bob, entity.RefAt("demo", "pr", "1")) {
		t.Error("bob takes no place in an open viff")
	}
	// The root itself is readable — `viff data` with no path, and where every wildcard read
	// starts — and nobody's to own: the policy every other record hangs from is the system's.
	if !holds(t, store, bob, entity.Read, entity.RefAt()) {
		t.Error("bob cannot read the root")
	}
	if mayCreate(t, store, bob, entity.RefAt()) {
		t.Error("bob owns the root")
	}
}

// Named admins narrow one line: they take top-level places, everybody else reads the corridors
// and takes nothing. Ownership is what is held back, not sight — an unreadable top would hide
// every source's slice from everyone.
func TestAdminsAreWhoMayTakeAPlace(t *testing.T) {
	admin, bob := "user:admin@example.com", "user:bob@example.com"
	store := seeded(t, Perms{Admins: []string{admin}})
	top := entity.RefAt("spaces")
	if !mayCreate(t, store, admin, top) {
		t.Error("an admin cannot take a top-level place")
	}
	if mayCreate(t, store, bob, top) {
		t.Error("a non-admin took a top-level place in an administered viff")
	}
	if !holds(t, store, bob, entity.Read, top) {
		t.Error("a non-admin cannot read the corridor")
	}
	// The group is the system's, so nobody joins it by writing the entity.
	owner, err := entity.Holds(store, bob, entity.ReadWrite, entity.GroupRef(AdminGroup))
	if err != nil {
		t.Fatal(err)
	}
	if owner {
		t.Error("a non-admin may write the admin group")
	}
}

// The `viff` record is what keeps the root's grant out of viff's half: a grant does not reach
// through a node carrying an owner record. Without it the open viff's `owner` beneath the root
// would hand everybody the perms mirror.
func TestTheRootsGrantStopsAtViffsHalf(t *testing.T) {
	for _, p := range []Perms{{}, {Admins: []string{"user:admin@example.com"}}} {
		store := seeded(t, p)
		for _, who := range []string{"user:bob@example.com", "user:admin@example.com"} {
			mirror := entity.ViffRef(entity.PermsSystem, entity.PermsKind, `demo.pr."1"`)
			if holds(t, store, who, entity.Read, mirror) {
				t.Errorf("admins=%v: %s reads the mirror", p.Admins, who)
			}
			// …while the rows the seed writes beneath `viff` still decide viff's half: a
			// task is anybody's to create, which is what runs the flows.
			if !mayCreate(t, store, who, entity.ViffRef("task", "instance", "run-1")) {
				t.Errorf("admins=%v: %s cannot create a task", p.Admins, who)
			}
		}
	}
}

// THE ROOT'S RECORD IS NOT IN THE MIRROR, it stands beside it. Keyed by the address it is about
// — the empty path — its fields would land on the mirror's own node, where every other record
// stands under them, and a listing would answer `owner` and `grants` as records of nodes by
// those names. That listing is the load path's own call, so the phantoms would reach the cache.
func TestTheMirrorListsRecordsAndNothingElse(t *testing.T) {
	store := seeded(t, Perms{})
	if _, _, err := entity.As(store, "user:alice@example.com").Write(entity.Write{
		At: entity.RefAt("demo", "pr", "1").At(), Value: entity.Obj{"stage": "open"}.Payload(),
		Principal: "user:alice@example.com",
	}); err != nil {
		t.Fatal(err)
	}
	mirror := []string{entity.ViffRoot, entity.PermsSystem, entity.PermsKind}
	ents, err := entity.ListAtErr(store, mirror, len(mirror)+1, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range ents {
		switch e.Ref.Name() {
		case entity.OwnerField, entity.GrantsField, entity.ChildGrantsField:
			t.Errorf("the mirror lists %s, which is a field of the root's record and not a record",
				e.Ref)
		}
	}
	if got, want := entity.PermsRef(entity.RefAt()).String(),
		entity.ViffRef(entity.PermsSystem, entity.PermsRootKind).String(); got != want {
		t.Errorf("the root's record is at %s, want %s", got, want)
	}
}

// The seed is written under an absence precondition, so an operator's edit is what stands after
// a restart — the records are policy somebody may change, not a fact the process asserts.
func TestASecondSeedLeavesAnEditedRecordAlone(t *testing.T) {
	store := seeded(t, Perms{})
	root := entity.RefAt()
	// The operator closes the top: nothing is held out beneath it any more.
	if err := entity.SetGrants(store, root, nil); err != nil {
		t.Fatal(err)
	}
	if err := seedPerms(store, Perms{}); err != nil {
		t.Fatalf("seeding again: %v", err)
	}
	if mayCreate(t, store, "user:bob@example.com", entity.RefAt("demo", "pr", "1")) {
		t.Error("the second seed wrote over the operator's edit")
	}
}

// The root is OWNED by the deployment's admin identity, not by the system, because ownership
// from a record is the one authority the fence never holds back: a grant conferring ownership
// stops wherever somebody else's place stands beneath it, which is every place an administered
// viff has. That is what lets an admin put the top back — and, through `viff perms`, edit the
// root's own record on a running viff, which nobody could when the system owned it.
func TestTheRootIsTheAdminIdentitys(t *testing.T) {
	store := seeded(t, Perms{Admins: []string{"user:walter@example.com"}})
	rec, ok, err := store.Get(entity.PermsRef(entity.RefAt()))
	if err != nil || !ok {
		t.Fatalf("the root's record: %v %v", ok, err)
	}
	if got := entity.Str(rec.Payload, entity.OwnerField); got != AdminPrincipal {
		t.Errorf("the root is %q's, want %s", got, AdminPrincipal)
	}
	// It holds everything, the mirror included, since ownership descends — and it is what
	// `viff perms` asks for before it lets anybody set a record.
	for _, at := range []entity.Ref{
		entity.RefAt("github", "signadot", "viff"),
		entity.RefAt(),
		entity.ViffRef(entity.PermsSystem, entity.PermsKind, `demo.pr."1"`),
	} {
		if !holds(t, store, AdminPrincipal, entity.OwnerLevel, at) {
			t.Errorf("%s does not hold owner at %s", AdminPrincipal, at)
		}
	}
	// And it is in the group, so who may administer reads as one list.
	g, ok, err := store.Get(entity.GroupRef(AdminGroup))
	if err != nil || !ok {
		t.Fatalf("the admin group: %v %v", ok, err)
	}
	members := entity.Strs(g.Payload, entity.MembersField)
	var found bool
	for _, m := range members {
		found = found || m == AdminPrincipal
	}
	if !found {
		t.Errorf("the admin group is %v, without %s in it", members, AdminPrincipal)
	}
}

// A place somebody else holds is still theirs: the admin identity co-owns it, which is what an
// administrator is, and takes nothing away from the owner.
func TestTheAdminIdentityDoesNotDisplaceAnOwner(t *testing.T) {
	store := seeded(t, Perms{})
	place := entity.RefAt("github", "signadot", "viff")
	if err := entity.Claim(store, place, "user:alice@example.com"); err != nil {
		t.Fatal(err)
	}
	if !holds(t, store, "user:alice@example.com", entity.OwnerLevel, place) {
		t.Error("alice lost her own place")
	}
	if !holds(t, store, AdminPrincipal, entity.OwnerLevel, place) {
		t.Errorf("%s cannot reach alice's place", AdminPrincipal)
	}
}

// A policy that changed reaches a viff that already has a top only when somebody says so: the
// seed is once, because after it the records are where an operator's own edits live and nothing
// here can tell an edit from a config that moved.
func TestAConfigChangeNeedsAReseed(t *testing.T) {
	store := seeded(t, Perms{})
	bob, walter := "user:bob@example.com", "user:walter@example.com"
	// A place taken while the viff was open, which the narrowing must leave alone.
	place := entity.RefAt("github", "signadot", "viff")
	if err := entity.Claim(store, place, "user:alice@example.com"); err != nil {
		t.Fatal(err)
	}

	// Restarted with admins named, and without a reseed: what stands is what stood.
	admins := Perms{Admins: []string{walter}}
	if err := seedPerms(store, admins); err != nil {
		t.Fatal(err)
	}
	if !mayCreate(t, store, bob, entity.RefAt("spaces")) {
		t.Error("a restart applied a policy nobody asked it to apply")
	}

	// …and with one, the deployment's own policy is written over it.
	admins.Reseed = true
	if err := seedPerms(store, admins); err != nil {
		t.Fatal(err)
	}
	if mayCreate(t, store, bob, entity.RefAt("spaces")) {
		t.Error("a reseed did not narrow the top")
	}
	if !mayCreate(t, store, walter, entity.RefAt("spaces")) {
		t.Error("a reseed did not admit the admin it names")
	}
	// It touches the top and nothing else: the place alice took is still hers, and the admin
	// identity co-owns it rather than displacing her.
	if !holds(t, store, "user:alice@example.com", entity.OwnerLevel, place) {
		t.Error("a reseed took alice's place from her")
	}
	if !holds(t, store, AdminPrincipal, entity.OwnerLevel, place) {
		t.Error("the admin identity cannot reach alice's place after a reseed")
	}
}

// `perms: {child-grants: {}}` IS A SETTING, not the absence of one: a viff where what anybody creates
// is theirs and their grantees' alone. Testing it with `len(...) > 0` read the empty map as
// unset, and a deployment configured private created everything readable by everyone.
func TestAnEmptyDefaultIsPrivateNotUnset(t *testing.T) {
	store := seeded(t, Perms{ChildGrants: entity.Levels{}})
	alice, bob := "user:alice@example.com", "user:bob@example.com"
	note := entity.RefAt("alices", "note")
	if _, _, err := entity.As(store, alice).Write(entity.Write{
		At: note.At(), Value: entity.Obj{"n": 1}.Payload(), Principal: alice,
	}); err != nil {
		t.Fatal(err)
	}
	if holds(t, store, bob, entity.Read, note) {
		t.Error("bob reads what alice created in a viff whose default holds out nothing")
	}
	// …and unset is still the open viff's: readable by everyone.
	open := seeded(t, Perms{})
	if _, _, err := entity.As(open, alice).Write(entity.Write{
		At: note.At(), Value: entity.Obj{"n": 1}.Payload(), Principal: alice,
	}); err != nil {
		t.Fatal(err)
	}
	if !holds(t, open, bob, entity.Read, note) {
		t.Error("an unset default stopped being the open viff's")
	}
}

// A RESEED REPLACES the top, it does not merge into it. A write merges, so a line an operator
// had added a level to kept the level and a `child-grants` the config no longer said stayed: "reseed
// with no admins puts the open form back" left a root that was not the open form.
func TestAReseedReplacesWhatAnOperatorAdded(t *testing.T) {
	store := seeded(t, Perms{Admins: []string{"user:walter@example.com"}})
	root := entity.RefAt()
	// An operator widens the first line and sets a default, by hand.
	if err := entity.SetGrants(store, root, []entity.Grant{
		{Below: true, To: map[entity.Level][]string{
			entity.OwnerLevel: {"group:" + AdminGroup}, entity.ReadWrite: {entity.Everyone},
		}},
		{To: map[entity.Level][]string{entity.Read: {entity.Everyone}}},
	}); err != nil {
		t.Fatal(err)
	}
	if err := entity.SetChildGrants(store, root, entity.Levels{}); err != nil {
		t.Fatal(err)
	}
	bob := "user:bob@example.com"
	if !holds(t, store, bob, entity.ReadWrite, entity.RefAt("anywhere")) {
		t.Fatal("the fixture is wrong: the operator's read-write did not land")
	}

	if err := seedPerms(store, Perms{Admins: []string{"user:walter@example.com"}, Reseed: true}); err != nil {
		t.Fatal(err)
	}
	if holds(t, store, bob, entity.ReadWrite, entity.RefAt("anywhere")) {
		t.Error("the operator's read-write survived a reseed that does not say it")
	}
	rec, _, _ := store.Get(entity.PermsRef(root))
	if entity.Get(rec.Payload, entity.ChildGrantsField) != nil {
		t.Errorf("a default the config does not say survived the reseed: %v", rec.Payload)
	}
	// …and back to the open form, which is the documented way out of a narrowing gone wrong.
	if err := seedPerms(store, Perms{Reseed: true}); err != nil {
		t.Fatal(err)
	}
	if !mayCreate(t, store, bob, entity.RefAt("anywhere")) {
		t.Error("a reseed with no admins did not put the open form back")
	}
}

// WHO THE ADMINS ARE IS CHANGED LIVE, by the identity that owns the root: the config's list seeds
// the group, and after that membership is the group's, edited with `viff group` like any
// other's. So a list that differs from the group is a viff somebody administers, not drift —
// and it has to be true that the admin identity can make the edit, and that nobody else can.
func TestTheAdminIdentityChangesWhoTheAdminsAre(t *testing.T) {
	store := seeded(t, Perms{Admins: []string{"user:walter@example.com"}})
	group, ana := entity.GroupRef(AdminGroup), "user:ana@example.com"
	members := entity.Obj{entity.MembersField: []any{AdminPrincipal, "user:walter@example.com", ana}}.Payload()
	if _, _, err := entity.As(store, "user:walter@example.com").Write(entity.Write{
		At: group.At(), Value: members, Principal: "user:walter@example.com",
	}); err == nil {
		t.Error("an admin who is not the root's owner rewrote the admin group")
	}
	if mayCreate(t, store, ana, entity.RefAt("spaces")) {
		t.Fatal("ana is an admin before anybody made her one")
	}
	if _, _, err := entity.As(store, AdminPrincipal).Write(entity.Write{
		At: group.At(), Value: members, Principal: AdminPrincipal,
	}); err != nil {
		t.Fatalf("%s could not edit the admin group: %v", AdminPrincipal, err)
	}
	fresh := entity.As(store, entity.SystemDefault)
	if ok, err := entity.MayCreate(fresh, ana, entity.RefAt("spaces")); err != nil || !ok {
		t.Errorf("ana is in the group and still takes no top-level place: %v %v", ok, err)
	}
}

// A `perms.child-grants` THAT MOVED IS DRIFT, as a form of the top that moved is. The root's record is
// what a creation reads, written once, so a viff first started open and then configured private
// went on creating what everyone reads — and said nothing, since only the grants were compared.
func TestADefaultTheConfigNoLongerSaysIsDrift(t *testing.T) {
	store := seeded(t, Perms{ChildGrants: entity.Levels{entity.Read: {entity.Everyone}}})
	stands, ok, err := store.Get(entity.PermsRef(entity.RefAt()))
	if err != nil || !ok {
		t.Fatalf("no root record: %v %v", ok, err)
	}
	want := func(p Perms) entity.Payload {
		o := entity.Obj{}
		if p.ChildGrants != nil {
			o[entity.ChildGrantsField] = p.ChildGrants.Payload()
		}
		return o.Payload()
	}
	if got := drifted(stands.Payload, want(Perms{ChildGrants: entity.Levels{}})); len(got) != 1 || got[0] != entity.ChildGrantsField {
		t.Errorf("a viff configured private over a record that says read to everyone: drift = %v", got)
	}
	if got := drifted(stands.Payload, want(Perms{ChildGrants: entity.Levels{entity.Read: {entity.Everyone}}})); len(got) != 0 {
		t.Errorf("the default it was seeded with reads as drift: %v", got)
	}
	// A config that says no default has no opinion on the one standing.
	if got := drifted(stands.Payload, want(Perms{})); len(got) != 0 {
		t.Errorf("an unset default reads as drift: %v", got)
	}
	// …and a reseed is what applies it: what alice creates afterwards is hers alone.
	if err := seedPerms(store, Perms{ChildGrants: entity.Levels{}, Reseed: true}); err != nil {
		t.Fatal(err)
	}
	alice, note := "user:alice@example.com", entity.RefAt("alices", "note")
	if _, _, err := entity.As(store, alice).Write(entity.Write{At: note.At(), Value: entity.Obj{"n": 1}.Payload(), Principal: alice}); err != nil {
		t.Fatal(err)
	}
	if holds(t, store, "user:bob@example.com", entity.Read, note) {
		t.Error("bob reads what alice created after a reseed to a private default")
	}
}
