package linear

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
	"time"

	"github.com/signadot/verse/entity"
	verseio "github.com/signadot/verse/io"
)

// The out half: a comment on a Linear issue, kept in step with what the verse asks for.
//
// It is the second sink, and it exists as much to prove the framework is not Slack's as to
// comment on issues. What it shares with Slack is the whole of io.Out — the pass, the status
// beside each input, the failure written once. What it does not share is every detail below,
// and that is the point:
//
//	Slack                              Linear
//	a channel and a message ts         one comment id, and no channel at all
//	the target is where it is said     the target is WHAT it is about (an issue)
//	an id verse can spell              an identifier (ENG-42) that must be resolved to a uuid
//	a draw that folds in gate records  the text as written
//
// Somebody writes `linear.out.<issue>.<name>` as `{text}` — `<issue>` being the identifier a
// person types, `ENG-42` — and this answers at `<name>~status`:
//
//	comment:   the comment's id, which is what an update needs
//	url:       where a reader finds it
//	postedAt / updatedAt
//	posted:    the fingerprint of the text the comment now carries
//
// A comment that already carries this text is left alone, and a changed text is an update of the
// same comment rather than a second one — the same level-triggered law the reflection runs under,
// pointed the other way.
type Sink struct {
	l   *Linear
	now func() time.Time
}

// Sink is this workspace's out half.
func (l *Linear) Sink() *Sink { return &Sink{l: l, now: time.Now} }

// OutRefs are the places inputs are written under: one per team, inside the team's own claim and
// outside every cover the reflection carries (`team`, `issue.*`, `cycle.*`), so no pass tombstones
// what somebody asked for.
//
// ONE PER TEAM rather than one for the connector, because a claim is what keeps two installs from
// writing the same slice: two Linear ios over different teams would both claim `linear.out` and be
// refused as overlapping, while `linear.ENG.out` and `linear.OPS.out` are as disjoint as the teams
// they belong to.
func (l *Linear) OutRefs() []entity.Ref {
	out := make([]entity.Ref, 0, len(l.teams))
	for _, key := range l.teams {
		out = append(out, entity.RefAt(l.place(key, OutPlace)...))
	}
	return out
}

// OutPlace is the segment those places end in.
const OutPlace = "out"

// Deliver comments on one issue, or updates the comment it already left there.
func (k *Sink) Deliver(ctx context.Context, in verseio.Input) (entity.Obj, error) {
	text := strings.TrimSpace(entity.Str(in.Value, "text"))
	if text == "" {
		return nil, verseio.Permanent(fmt.Errorf("an input carries {text}, and this one has none"))
	}
	fp := fingerprint(text)
	id := entity.Str(in.Status, "comment")
	if id != "" && entity.Str(in.Status, "posted") == fp {
		return nil, nil // in step: the comment carries this text
	}
	now := k.now().UTC().Format(time.RFC3339)
	if id != "" {
		if err := k.l.api.updateComment(ctx, id, text); err != nil {
			return nil, err
		}
		return entity.Obj{"updatedAt": now, "posted": fp}, nil
	}

	// A person types `ENG-42`; Linear's mutation takes the issue's uuid. The lookup is the one
	// extra call this connector has and Slack does not, and it is why the id lands on the status:
	// an update never pays for it again.
	issue, err := k.l.api.issueID(ctx, in.To)
	if err != nil {
		return nil, err
	}
	comment, url, err := k.l.api.createComment(ctx, issue, text)
	if err != nil {
		return nil, err
	}
	return entity.Obj{
		"comment": comment, "url": url, "postedAt": now, "updatedAt": now, "posted": fp,
	}, nil
}

// fingerprint is a short, stable digest of what a comment should say, so a changed text is an
// update and an unchanged one is nothing at all.
func fingerprint(text string) string {
	sum := sha256.Sum256([]byte(text))
	return hex.EncodeToString(sum[:8])
}
