package versed

import (
	"fmt"
	"log/slog"
	"slices"

	"github.com/signadot/verse/entity"
	verseio "github.com/signadot/verse/io"
	"github.com/signadot/verse/mirror"
	"github.com/signadot/verse/resolver"
	"github.com/signadot/verse/schedule"
	"github.com/signadot/verse/trigger"
)

// The seed: what the system holds out, so that the flows a verse runs today are admitted by
// the check rather than by its absence. Every kind-rooted node under verse's root is the
// system's, and a request that installs a charter, runs a task or registers a source creates
// beneath one of them — so each holds out `owner` beneath itself to everyone, and what is
// created there is the requester's. The world's half is the root record's to say (seedTop), and
// needs no row here.
//
// AND EACH HOLDS OUT `read` BENEATH ITSELF, in a line of its own. What a person makes beneath a
// row carries their record, which says who reads it, and a grant does not cross a record — so
// the line reaches what stands there with no record: what the system made, which is the
// nearest owner's, the system's, and would otherwise be nobody else's to see. The clock, the
// calendar and the base charter's rules are that today. It is said row by row because it is a
// decision about each: a row added later may hold out no sight of what the system makes there.
//
// A question is the one row that is not everyone's: it is posed by versed and answered by
// whoever may, and "whoever" is a person — the runner working the task that raised it holds
// its grant on the task and nothing here, which is what keeps an executor from resolving its
// own gate. So the row names the principal kinds a person acts as.
//
// IN CODE, AS ONE TABLE, because it is the deployment's policy and there is one deployment. A
// verse that wants a different one edits the records; every row is written under an absence
// precondition, so a restart writes nothing and an operator's edit stands.
var seed = []struct {
	at    entity.Ref
	level entity.Level
	to    []string
}{
	{entity.VerseRef(mirror.System, mirror.InstanceKind), entity.OwnerLevel, []string{entity.Everyone}},
	{entity.VerseRef(mirror.System, mirror.InstanceKind), entity.Read, []string{entity.Everyone}},
	{entity.VerseRef(mirror.System, mirror.RunKind), entity.OwnerLevel, []string{entity.Everyone}},
	{entity.VerseRef(mirror.System, mirror.RunKind), entity.Read, []string{entity.Everyone}},
	{entity.VerseRef(mirror.System, mirror.ClaimKind), entity.OwnerLevel, []string{entity.Everyone}},
	{entity.VerseRef(mirror.System, mirror.ClaimKind), entity.Read, []string{entity.Everyone}},
	{entity.VerseRef(trigger.CharterSystem, trigger.CharterKind), entity.OwnerLevel, []string{entity.Everyone}},
	{entity.VerseRef(trigger.CharterSystem, trigger.CharterKind), entity.Read, []string{entity.Everyone}},
	{entity.VerseRef(trigger.StrataSystem, trigger.StrataKind), entity.OwnerLevel, []string{entity.Everyone}},
	{entity.VerseRef(trigger.StrataSystem, trigger.StrataKind), entity.Read, []string{entity.Everyone}},
	{entity.VerseRef(trigger.CharterSystem, trigger.PackageKind), entity.OwnerLevel, []string{entity.Everyone}},
	{entity.VerseRef(trigger.CharterSystem, trigger.PackageKind), entity.Read, []string{entity.Everyone}},
	// A suspension is written by the route that holds its table, as the system; what it holds,
	// and who holds it, is everybody's to read, so a suspended thing is never a mystery.
	{entity.VerseRef(trigger.CharterSystem, trigger.SuspensionKind), entity.Read, []string{entity.Everyone}},
	{entity.VerseRef(resolver.System), entity.OwnerLevel, []string{entity.Everyone}},
	{entity.VerseRef(resolver.System), entity.Read, []string{entity.Everyone}},
	{entity.VerseRef(runnersSystem), entity.OwnerLevel, []string{entity.Everyone}},
	{entity.VerseRef(runnersSystem), entity.Read, []string{entity.Everyone}},
	{entity.VerseRef(verseio.System), entity.OwnerLevel, []string{entity.Everyone}},
	{entity.VerseRef(verseio.System), entity.Read, []string{entity.Everyone}},
	// A schedule is declared in a charter and is its installer's, as a rule is; its ticks are
	// written as the schedule, so they carry the installer's grant and are everybody's to read
	// unless the installer says otherwise.
	{entity.VerseRef(schedule.System), entity.OwnerLevel, []string{entity.Everyone}},
	{entity.VerseRef(schedule.System), entity.Read, []string{entity.Everyone}},
	// Who has been here is data anybody may read (`verse data verse principal`); the mirror,
	// beside it, is held out to nobody.
	{entity.VerseRef(entity.PrincipalSystem), entity.Read, []string{entity.Everyone}},
	// Anyone makes a group; the group is its maker's.
	{entity.VerseRef(entity.PrincipalSystem, entity.GroupKind), entity.OwnerLevel, []string{entity.Everyone}},
	{entity.VerseRef(entity.PrincipalSystem, entity.GroupKind), entity.Read, []string{entity.Everyone}},
	// THE DOCKET: every ask, whoever raised it. Anyone asks, and a person's ask is its asker's.
	// The place is everybody's to read, the runner working a task included, and a person answers
	// an ask nobody else was given. What reaches one ask is its own record's to say — the verb
	// writes a person's, the docket a rule's or a task's — and a grant does not reach through a
	// node carrying one.
	{entity.VerseRef(mirror.AskSystem), entity.OwnerLevel, []string{entity.Everyone}},
	{entity.VerseRef(mirror.AskSystem), entity.Read, []string{entity.Everyone}},
	{entity.VerseRef(mirror.AskSystem), entity.ReadWrite, []string{"user:*", "agent:*"}},
}

// childGrants is what the seed says a node created beneath a kind-rooted node holds out, where
// the root's default is not the right one. A question's record is the system's and an owner
// record, so what reaches a question is what its record says: the docket's openness has to be
// copied onto each question at pose, and this is where it is copied from.
var childGrants = []struct {
	at     entity.Ref
	levels entity.Levels
}{
	// The docket: everyone reads a question, and a person answers one nobody else was given.
	{entity.VerseRef(mirror.AskSystem), entity.Levels{
		entity.Read: {entity.Everyone}, entity.ReadWrite: {"user:*", "agent:*"},
	}},
	// The clock and the calendar, and a schedule's ticks: everybody's to read, whatever the
	// root's default says. The base charter derives `verse.schedule.time` and the rest as the
	// system, whose record copies the default above it — and under a root that holds out
	// nothing that would be a calendar no rule of anybody's could read.
	{entity.VerseRef(schedule.System), entity.Levels{entity.Read: {entity.Everyone}}},
}

// AdminGroup is the group a deployment's admins are seeded into, and the grantee the root holds
// `owner` out to when there are any. A group rather than a principal because membership is read
// when it is checked, so admins rotate without an edit to every record that names them — and
// because what an admin creates is stamped with the PERSON who acted, which is the chown a
// deployment wants. The group's record is the system's: the seed writes it, so nobody adds
// themselves to it by writing the entity.
const AdminGroup = "admin"

// AdminPrincipal OWNS THE ROOT, and it is a principal rather than the group because a group is
// never one — nothing acts as a group, and the root's owner has to be somebody.
//
// It is an identity of the deployment's rather than a person's: whoever administers rotates, and
// the root record would otherwise carry the name of whoever set the verse up long after they
// stopped. Ownership descends, so this principal co-owns every place in the verse, which is what
// makes it the one that can put the top back — a grant could not, since ownership concluded from
// a grant is held back wherever somebody else's place stands beneath (the fence).
//
// A principal is SELF-ASSERTED: `-as user:admin@verse` is available to anybody who can reach the
// api, as every other principal is, until there is authentication to make a name a credential.
// What this buys today is a place for the authority to live, and an identity to grant to; it is
// not a control.
const AdminPrincipal = "user:admin@verse"

// Perms is the deployment's say in the policy, parsed and validated by whoever read the config
// (cmd/verse/up.go refuses a bad value at startup rather than seeding one).
type Perms struct {
	// ChildGrants is what a node created with no default above it holds out — the root record's
	// `child-grants`. nil leaves the check's own, which is the open verse's `{read: ["*"]}`; EMPTY is
	// a setting, and it says what anybody creates is theirs and their grantees' alone.
	ChildGrants entity.Levels
	// Admins are the grantees the admin group holds. Empty is a verse with no administered
	// top: anybody takes a top-level place, which is what a dogfood verse runs as.
	Admins []string
	// Reseed writes the top records as this config says, OVER what stands. Without it the seed
	// writes them once and an operator's edits are what stand after — which is right until the
	// deployment's own policy changes, and then there is no way to apply it: the seed is absent
	// only on a verse nobody has started, and the records are not a person's to write by hand.
	//
	// It is a deployment's act and says so: an operator turns it on for one start, reads the
	// top back, and turns it off. It touches the three records this file writes and nothing
	// else — no other record, no entity, no gate, no task.
	Reseed bool
}

// seedPerms writes the seed, once: a row already standing — this verse's, or an operator's
// edit of it — is left as it is. A default is written where the record says none, so a verse
// seeded before defaults existed gets the docket's; a default an operator set stands.
func seedPerms(store entity.Store, p Perms) error {
	if err := seedTop(store, p); err != nil {
		return err
	}
	if err := seedGrants(store); err != nil {
		return err
	}
	for _, d := range childGrants {
		rec := entity.PermsRef(d.at)
		_, _, err := store.Write(entity.Write{
			At:        rec.At(entity.ChildGrantsField),
			Value:     d.levels.Payload(),
			When:      []entity.Require{{At: rec.At(entity.ChildGrantsField), Absent: true}},
			Principal: entity.SystemSeed,
		})
		if err != nil {
			return fmt.Errorf("seeding the default at %s: %w", d.at, err)
		}
	}
	return nil
}

// seedTop writes the three records the tree's own top needs: the admin group, the root, and
// verse's half.
//
// THE OPEN VERSE IS DATA. What an untaken node permits is the root's record, which an operator
// can see, narrow, and tell is what decided: `owner` beneath the root to everyone where no
// admins are named, and to the admin group where they are — one line between a dogfood verse
// and an administered one.
//
// AND `verse` GETS AN OWNER RECORD, which is what keeps the root's grant out of verse's half: a
// grant does not reach through a node carrying one. The rows below decide verse's half on their
// own, as they did, and the mirror beneath it stays the system's — an admin reads policy through
// `verse perms`, which is the verb that checks.
func seedTop(store entity.Store, p Perms) error {
	if err := clearForReseed(store, p,
		entity.GroupRef(AdminGroup), entity.PermsRef(entity.GroupRef(AdminGroup)),
		entity.PermsRef(entity.RefAt()), entity.PermsRef(entity.RefAt(entity.VerseRoot)),
	); err != nil {
		return err
	}
	{
		// The group always stands, with the root's owner in it: an administered verse names
		// more members, and an open one still has somewhere to say who may put the top back.
		members := []any{AdminPrincipal}
		for _, m := range p.Admins {
			if m != AdminPrincipal {
				members = append(members, m)
			}
		}
		if _, _, err := store.Write(entity.Write{
			At:        entity.GroupRef(AdminGroup).At(),
			Value:     entity.Obj{entity.MembersField: members}.Payload(),
			When:      once(p),
			Principal: entity.SystemSeed,
		}); err != nil {
			return fmt.Errorf("seeding the %s group: %w", AdminGroup, err)
		}
		// AND THE GROUP IS TAKEN, which is the difference between an admin group and a list
		// anybody edits: the seed row above it holds `owner` beneath verse.principal.group out
		// to everyone, because anyone makes a group and the group is its maker's — and a group
		// the seed wrote has no maker, so without a record of its own the first person to write
		// it adds themselves to it. A grant does not reach through an owner record; who is in
		// it stays readable.
		if _, _, err := store.Write(entity.Write{
			At: entity.PermsRef(entity.GroupRef(AdminGroup)).At(),
			Value: entity.Obj{
				entity.OwnerField: entity.SystemDefault,
				entity.GrantsField: entity.GrantsPayload(entity.GroupRef(AdminGroup).Segs(),
					[]entity.Grant{{At: entity.GroupRef(AdminGroup).Segs(),
						To: map[entity.Level][]string{entity.Read: {entity.Everyone}}}}),
			}.Payload(),
			When:      once(p),
			Principal: entity.SystemSeed,
		}); err != nil {
			return fmt.Errorf("seeding the record of the %s group: %w", AdminGroup, err)
		}
	}
	// Who may take a place of their own, and who may read the corridors between them. An
	// administered verse still reads: an unreadable top would hide every source's slice from
	// everyone, and what is held back is ownership rather than sight.
	owners := []string{entity.Everyone}
	root := entity.Obj{entity.OwnerField: AdminPrincipal}
	if len(p.Admins) > 0 {
		owners = []string{entity.GroupKind + ":" + AdminGroup}
	}
	grants := []entity.Grant{
		// `below`, so what is held out is the taking of a place BENEATH the root and never the
		// root itself: the record is the system's, and an owner at the root is somebody who
		// could rewrite the policy every other record hangs from.
		{Below: true, To: map[entity.Level][]string{entity.OwnerLevel: owners}},
		// …and the node itself is readable, which `below` would have left out. A read at the
		// root is `verse data` with no path, and it is where every wildcard read starts.
		{To: map[entity.Level][]string{entity.Read: {entity.Everyone}}},
	}
	root[entity.GrantsField] = entity.GrantsPayload(nil, grants)
	if p.ChildGrants != nil { // nil is unset; `{}` is a setting, and it says nothing is held out
		root[entity.ChildGrantsField] = p.ChildGrants.Payload()
	}
	// The root's record is a node of its own (entity.PermsRootKind), so it is asked about as
	// one: written once, and an operator's edit of it stands — unless this start is the one
	// that re-applies the deployment's policy over it.
	noteDrift(store, p, root)
	if _, _, err := store.Write(entity.Write{
		At: entity.PermsRef(entity.RefAt()).At(), Value: root.Payload(),
		When: once(p), Principal: entity.SystemSeed,
	}); err != nil {
		return fmt.Errorf("seeding the root's record: %w", err)
	}
	// And verse's half, whose record is a node of its own and so is asked about as one.
	//
	// `grants: []` said OUT LOUD, because a record carrying an owner and no grants field at all
	// is one from before defaults existed and is read as holding out the root's default — which
	// here would hand every reader verse's half, the mirror with it.
	if _, _, err := store.Write(entity.Write{
		At: entity.PermsRef(entity.RefAt(entity.VerseRoot)).At(),
		Value: entity.Obj{
			entity.OwnerField:  entity.SystemDefault,
			entity.GrantsField: entity.GrantsPayload(nil, nil),
		}.Payload(),
		When:      once(p),
		Principal: entity.SystemSeed,
	}); err != nil {
		return fmt.Errorf("seeding the record at %s: %w", entity.VerseRoot, err)
	}
	return nil
}

func seedGrants(store entity.Store) error {
	// One record per node, whatever the table says about it: the rows for a node are its
	// grants, and a second write under the absence precondition would be refused as the
	// record standing already.
	var order []entity.Ref
	grants := map[entity.Ref][]entity.Grant{}
	for _, row := range seed {
		if _, seen := grants[row.at]; !seen {
			order = append(order, row.at)
		}
		grants[row.at] = append(grants[row.at], entity.Grant{
			At: row.at.Segs(), Below: true, To: map[entity.Level][]string{row.level: row.to},
		})
	}
	for _, at := range order {
		_, _, err := store.Write(entity.Write{
			At:        entity.PermsRef(at).At(),
			Value:     entity.Obj{entity.GrantsField: entity.GrantsPayload(at.Segs(), grants[at])}.Payload(),
			When:      []entity.Require{entity.Created()},
			Principal: entity.SystemSeed,
		})
		if err != nil {
			return fmt.Errorf("seeding the grants at %s: %w", at, err)
		}
	}
	return nil
}

// once is the precondition the seed writes under: the record must be absent, so a restart writes
// nothing and what an operator edited stands. A reseed has none — it clears the record first
// (clearForReseed), so what it writes is the whole of what stands.
func once(p Perms) []entity.Require {
	if p.Reseed {
		return nil
	}
	return []entity.Require{entity.Created()}
}

// clearForReseed removes what stands at each address a reseed is about to write, so the write
// REPLACES it. A write merges: a line an operator had added a level to would keep the level,
// and a `child-grants` the config does not say would stay — so "reseed with no admins puts the open
// form back" would leave a root that is not the open form, with nothing to say so. Removing first is two
// writes with a moment between them where the record is gone, and that moment is at start,
// before anything is served and before the mirror is watched, which is the one place a record
// may be missing without anybody being refused by it.
func clearForReseed(store entity.Store, p Perms, refs ...entity.Ref) error {
	if !p.Reseed {
		return nil
	}
	for _, ref := range refs {
		if _, _, err := store.Write(entity.Write{At: ref.At(), Principal: entity.SystemSeed}); err != nil {
			return fmt.Errorf("clearing %s for the reseed: %w", ref, err)
		}
	}
	return nil
}

// noteDrift says when the root record standing is not what this config would write — the FORM
// of the top, open or administered, and the DEFAULT a new node holds out — which is otherwise
// silent. It does not watch WHO the admins are: the config's list seeds the admin group, and
// membership is changed live, by `user:admin@verse` with `verse group`, like any other group's.
// A list that differs from the group is therefore the ordinary state of a verse somebody
// administers, and not drift.
//
// It is said because the seed writes once and the record is what a check reads, so a deployment
// that changed `perms.admins` or `perms.child-grants` and restarted goes on running the policy it
// had: a verse set private still creating what everyone reads, with nothing to read but the old
// record. Said at every start rather than fixed, since the record is where an operator's own
// edits live and this cannot tell those from a config that moved.
func noteDrift(store entity.Store, p Perms, want entity.Obj) {
	if p.Reseed {
		return
	}
	e, ok, err := store.Get(entity.PermsRef(entity.RefAt()))
	if err != nil || !ok {
		return
	}
	if fields := drifted(e.Payload, want.Payload()); len(fields) > 0 {
		slog.Warn("perms: the root's record is not what this config would write, and the record "+
			"is what decides; start once with --reseed-perms to apply the config, or edit the "+
			"record with `verse perms --root`", "differs", fields, "admins", p.Admins)
	}
}

// drifted is the fields of the root's record that stand otherwise than the config would write
// them. A config that says no default has no opinion on the one standing.
func drifted(stands, want entity.Payload) []string {
	var out []string
	if s, w := entity.Get(stands, entity.GrantsField), entity.Get(want, entity.GrantsField); s != nil && w != nil && !s.DeepEqual(w) {
		out = append(out, entity.GrantsField)
	}
	if entity.Get(want, entity.ChildGrantsField) != nil {
		s, _, serr := entity.ParseLevels(stands, entity.ChildGrantsField)
		w, _, werr := entity.ParseLevels(want, entity.ChildGrantsField)
		if serr != nil || werr != nil || !sameLevels(s, w) {
			out = append(out, entity.ChildGrantsField)
		}
	}
	return out
}

func sameLevels(a, b entity.Levels) bool {
	if len(a) != len(b) {
		return false
	}
	for l, names := range a {
		if !slices.Equal(names, b[l]) {
			return false
		}
	}
	return true
}
