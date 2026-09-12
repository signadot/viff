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
}

// seedPerms writes the seed, once: a row already standing — this verse's, or an operator's
// edit of it — is left as it is.
func seedPerms(store entity.Store) error {
	for _, row := range seed {
		g := entity.Grant{At: row.at.Segs(), Below: true, To: map[entity.Level][]string{row.level: row.to}}
		_, _, err := store.Write(entity.Write{
			At:        entity.PermsRef(row.at).At(),
			Value:     entity.Obj{entity.GrantsField: entity.GrantsPayload([]entity.Grant{g})}.Payload(),
			When:      []entity.Require{entity.Created()},
			Principal: entity.SystemPrincipal,
		})
		if err != nil {
			return fmt.Errorf("seeding the grant at %s: %w", row.at, err)
		}
	}
	return nil
}
