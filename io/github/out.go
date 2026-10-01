package github

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/signadot/verse/entity"
	verseio "github.com/signadot/verse/io"
)

// The out half: a comment on an issue or a pull request, kept in step with what the verse asks
// for. It is the Linear io's out half pointed at GitHub, and it is the first of what the review
// environment needs from GitHub (git-issue jdvbwvjah12krsstqsn0; the steps are on the branch's
// issue, 934azhtjh12kr7mtqsn0).
//
// Somebody writes an input under a repository's out place, `github.<org>.<name>.out.<name>`, or
// a package writes one in an outbox it declares for that place:
//
//	to:   pr.42              # or issue.42: GitHub numbers both in one sequence
//	text: "the trial found nothing new"
//
// and this answers at `<name>~status`:
//
//	comment:   the comment's id, which is what an update needs
//	url:       where a reader finds it
//	postedAt / updatedAt
//	posted:    the fingerprint of the text the comment now carries
//
// A comment that already carries this text is left alone, and a changed text is an update of the
// same comment rather than a second one.
type Sink struct {
	g   *GitHub
	now func() time.Time
}

// Sink is this source's out half, over every repository it reflects.
func (g *GitHub) Sink() *Sink { return &Sink{g: g, now: time.Now} }

// OutPlace is the segment a repository's out place ends in.
const OutPlace = "out"

// OutRefs are the places inputs are written under: one per repository, inside the repository's
// own claim and outside every cover the reflection carries (`repo`, `issue.*`, `pr.*`, `run.*`,
// `comment.*`), so no pass tombstones what somebody asked for.
func (g *GitHub) OutRefs() []entity.Ref {
	out := make([]entity.Ref, 0, len(g.repos))
	for _, r := range g.repos {
		out = append(out, entity.RefAt(g.place(r, OutPlace)...))
	}
	return out
}

// repoOf is the repository whose out place an input was asked at — an outbox's input arrives
// with the out place it sends to — or "" for a place this source does not serve.
func (g *GitHub) repoOf(place entity.Ref) string {
	for _, r := range g.repos {
		if entity.RefAt(g.place(r, OutPlace)...).String() == place.String() {
			return r
		}
	}
	return ""
}

// target reads an input's `to`: `pr.<n>` or `issue.<n>`.
func target(to string) (int, error) {
	kind, num, ok := strings.Cut(to, ".")
	if !ok || (kind != "pr" && kind != "issue") {
		return 0, fmt.Errorf("`to` is pr.<number> or issue.<number>, not %q", to)
	}
	n, err := strconv.Atoi(num)
	if err != nil || n <= 0 {
		return 0, fmt.Errorf("`to` %q: %q is not a pull request or issue number", to, num)
	}
	return n, nil
}

// Deliver comments on one issue or pull request, or updates the comment it already left there.
func (k *Sink) Deliver(ctx context.Context, in verseio.Input) (entity.Obj, error) {
	repo := k.g.repoOf(in.Place)
	if repo == "" {
		return nil, fmt.Errorf("%s is no repository's out place this io serves", in.Place)
	}
	text := strings.TrimSpace(entity.Str(in.Value, "text"))
	if text == "" {
		return nil, fmt.Errorf("an input carries {to, text}, and this one has no text")
	}
	fp := fingerprint(text)
	now := k.now().UTC().Format(time.RFC3339)
	if id, ok := commentID(in.Status); ok {
		if entity.Str(in.Status, "posted") == fp {
			return nil, nil // in step: the comment carries this text
		}
		if err := k.g.api.updateComment(ctx, repo, id, text); err != nil {
			return nil, err
		}
		return entity.Obj{"updatedAt": now, "posted": fp}, nil
	}
	number, err := target(in.To)
	if err != nil {
		return nil, err
	}
	c, err := k.g.api.createComment(ctx, repo, number, text)
	if err != nil {
		return nil, err
	}
	return entity.Obj{
		"comment": strconv.FormatInt(c.ID, 10), "url": c.HTMLURL,
		"postedAt": now, "updatedAt": now, "posted": fp,
	}, nil
}

// commentID is the comment a status says this input made, if it made one.
func commentID(status entity.Payload) (int64, bool) {
	s := entity.Str(status, "comment")
	if s == "" {
		return 0, false
	}
	id, err := strconv.ParseInt(s, 10, 64)
	return id, err == nil
}

// fingerprint is a short, stable digest of what a comment should say, so a changed text is an
// update and an unchanged one is nothing at all.
func fingerprint(text string) string {
	sum := sha256.Sum256([]byte(text))
	return hex.EncodeToString(sum[:8])
}
