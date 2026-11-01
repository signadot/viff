package versed

import (
	"testing"

	"github.com/signadot/verse/runner/runnerd/runnerdtest"
	"github.com/signadot/verse/trigger/charter"

	"github.com/signadot/verse/entity"
	"github.com/signadot/verse/entity/entitytest"
	verseio "github.com/signadot/verse/io"
	"github.com/signadot/verse/mirror"
	"github.com/signadot/verse/resolver"
	"github.com/signadot/verse/schedule"
)

// What the system made beneath a row is everybody's to read BY THE ROW'S OWN LINE, and not by a
// record the maker remembered to write beside it.
//
// Each case stands a node beneath a row as the system and takes its record away, which leaves
// what the row alone says: the nearest owner is the system, so nobody else holds anything there
// but what a line holds out. The rows are named, one by one, because the line is a decision
// about each and not a property of rows — and under a root default that holds out nothing, since
// a default is copied onto records and these have none.
func TestWhatTheSystemMadeBeneathARowIsReadThroughTheRowsLine(t *testing.T) {
	rows := []entity.Ref{
		entity.VerseRef(mirror.System, mirror.InstanceKind),
		entity.VerseRef(mirror.System, mirror.RunKind),
		entity.VerseRef(mirror.System, mirror.ClaimKind),
		entity.VerseRef(charter.System, charter.RuleKind),
		entity.VerseRef(charter.System, charter.StratumKind),
		entity.VerseRef(charter.System, charter.PackageKind),
		entity.VerseRef(resolver.System),
		entity.VerseRef(runnersSystem),
		entity.VerseRef(verseio.System),
		entity.VerseRef(schedule.System),
		entity.VerseRef(entity.PrincipalSystem, entity.GroupKind),
	}
	for _, c := range []struct {
		what string
		opts []Option
	}{
		{"an open verse", nil},
		{"a root default that holds out nothing", []Option{WithPerms(Perms{ChildGrants: entity.Levels{}})}},
	} {
		t.Run(c.what, func(t *testing.T) {
			srv := serverWith(t, runnerdtest.Scripted, entitytest.Store(t), c.opts...)
			perms := entity.As(srv.Store, entity.SystemPerms)
			for _, row := range rows {
				made := entity.RefAt(append(row.Segs(), "made-by-the-system")...)
				entitytest.Set(t, srv.Store, made, entity.Obj{"stage": "standing"}.Payload())
				if _, _, err := perms.Write(entity.Write{At: entity.PermsRef(made).At()}); err != nil {
					t.Fatalf("taking the record at %s away: %v", made, err)
				}
				if _, stands := ownerOfRecord(t, srv.Store, made); stands {
					t.Fatalf("%s still carries a record, so this says nothing about the row", made)
				}
				for _, who := range []string{"user:mallory@example.com", "runner:verse-runner", "agent:harness"} {
					if !holds(t, srv.Store, who, entity.Read, made) {
						t.Errorf("%s does not read %s, which the system made", who, made)
					}
				}
			}
		})
	}
}
