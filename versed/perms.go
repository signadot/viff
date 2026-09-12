package versed

import (
	"fmt"

	"github.com/signadot/verse/entity"
	"github.com/signadot/verse/mirror"
	"github.com/signadot/verse/resolver"
	"github.com/signadot/verse/runner"
	"github.com/signadot/verse/source"
	"github.com/signadot/verse/trigger"
)

// The seed: what the system holds out, so that the flows a verse runs today are admitted by
// the check rather than by its absence. Every kind-rooted node under verse's root is the
// system's, and a request that installs a charter, runs a task or registers a source creates
// beneath one of them — so each holds out `owner` beneath itself to everyone, and what is
// created there is the requester's. The world's half is open by the check's own base case and
// needs no row.
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
	{entity.VerseRef(mirror.System, mirror.RunKind), entity.OwnerLevel, []string{entity.Everyone}},
	{entity.VerseRef(mirror.System, mirror.ClaimKind), entity.OwnerLevel, []string{entity.Everyone}},
	{entity.VerseRef(trigger.CharterSystem, trigger.CharterKind), entity.OwnerLevel, []string{entity.Everyone}},
	{entity.VerseRef(trigger.StrataSystem, trigger.StrataKind), entity.OwnerLevel, []string{entity.Everyone}},
	{entity.VerseRef(trigger.CharterSystem, trigger.PackageKind), entity.OwnerLevel, []string{entity.Everyone}},
	{entity.VerseRef(resolver.System), entity.OwnerLevel, []string{entity.Everyone}},
	{entity.VerseRef(runner.System), entity.OwnerLevel, []string{entity.Everyone}},
	{entity.VerseRef(source.System), entity.OwnerLevel, []string{entity.Everyone}},
	{entity.VerseRef(mirror.QuestionSystem, mirror.QuestionKind), entity.ReadWrite, []string{entity.UserKind, entity.AgentKind}},
	// The docket is everybody's to read, the runner working a task included; answering is
	// the row above.
	{entity.VerseRef(mirror.QuestionSystem, mirror.QuestionKind), entity.Read, []string{entity.Everyone}},
	// Who has been here is data anybody may read (`verse data verse principal`); the mirror,
	// beside it, is held out to nobody.
	{entity.VerseRef(entity.PrincipalSystem), entity.Read, []string{entity.Everyone}},
}

// seedPerms writes the seed, once: a row already standing — this verse's, or an operator's
// edit of it — is left as it is.
func seedPerms(store entity.Store) error {
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
			Value:     entity.Obj{entity.GrantsField: entity.GrantsPayload(grants[at])}.Payload(),
			When:      []entity.Require{entity.Created()},
			Principal: entity.SystemPrincipal,
		})
		if err != nil {
			return fmt.Errorf("seeding the grants at %s: %w", at, err)
		}
	}
	return nil
}
