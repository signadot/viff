package github

import (
	"context"
	"fmt"
	"net/url"
	"strconv"
	"strings"

	"github.com/signadot/tony-format/go-tony/ir"

	"github.com/signadot/verse/pkg/entity"
	verseio "github.com/signadot/verse/pkg/io"
)

// Dependabot alerts, reflected with -alerts (Config.Alerts): what GitHub knows about a vulnerable
// dependency of the repository, so a charter can react to one arriving and to one going fixed or
// dismissed.
//
// THE SHAPE IS ISSUES', two covers: the open alerts complete, and the settled ones (fixed,
// dismissed, auto_dismissed) as a window of the newest -settled by update. An alert that is fixed
// appears in the settled half carrying its new state, ages, and leaves.
//
// THE READ IS DISCUSSIONS', a separable claim: a credential without Dependabot alerts read, or a
// repository with the alerts switched off, answers 403, and that is this pass having nothing true
// to say about alerts rather than there being none. So the covers go with the entities and what
// stands is left alone (reflectRepo).

// alertSettled is the settled half's states, as the listing's `state` filter takes them.
const alertSettled = "fixed,dismissed,auto_dismissed"

// AlertRef is the ref of one Dependabot alert, by the repository's own alert number.
func (g *GitHub) AlertRef(repo string, number int) entity.Ref {
	return entity.RefAt(g.place(repo, "alert", strconv.Itoa(number))...)
}

// alertCovers are the two halves: the open alerts, and every other state. The settled half is
// `!not open` rather than the three states spelled out, so a state GitHub adds later is covered
// by the window, and tombstoned, rather than standing forever.
func (g *GitHub) alertCovers(repo string) []entity.Pattern {
	open := g.cover(repo, "alert", "*")
	open.Predicate = entity.Obj{"state": "open"}.Payload()
	settled := g.cover(repo, "alert", "*")
	settled.Predicate = entity.Obj{"state": ir.FromString("open").WithTag("!not")}.Payload()
	return []entity.Pattern{open, settled}
}

// readAlerts reads both halves, live and settled, or reports why it could not. Both or neither,
// for readDiscussions' reason: a live half beside a failed settled half would cover the window
// while carrying nothing of it.
func (g *GitHub) readAlerts(ctx context.Context, repo string) ([]alert, error) {
	live, err := g.api.listAlerts(ctx, repo, "open", g.maxOpen, 0)
	if err != nil {
		return nil, err
	}
	settled, err := g.api.listAlerts(ctx, repo, alertSettled, 0, g.settled)
	if err != nil {
		return nil, err
	}
	return append(live, settled...), nil
}

// targetAlert reads a ref as one alert: `<root>.<org>.<name>.alert.<n>`.
func (g *GitHub) targetAlert(ref entity.Ref) (repo string, number int, ok bool) {
	repo, rest, under := g.under(ref)
	if !under || len(rest) != 2 || rest[0] != "alert" {
		return "", 0, false
	}
	n, err := strconv.Atoi(rest[1])
	if err != nil || n <= 0 {
		return "", 0, false
	}
	return repo, n, true
}

// reflectAlert answers for ONE alert, and covers exactly it: a targeted pass declaring the open
// cover while carrying one alert would tombstone every other open alert. An alert GitHub no
// longer has (404) is covered and absent, which tombstones it.
//
// With alerts switched off it answers nothing and asks nothing, and a refusal (403) answers
// nothing too: no cover, so what stands is left alone, as the repository pass leaves it.
func (g *GitHub) reflectAlert(ctx context.Context, repo string, number int) (verseio.Reflection, error) {
	if !g.alerts {
		return verseio.Reflection{}, nil
	}
	rn := verseio.Reflection{Covers: []entity.Pattern{g.cover(repo, "alert", strconv.Itoa(number))}}
	var a alert
	if err := g.api.get(ctx, "/repos/"+repo+"/dependabot/alerts/"+strconv.Itoa(number), &a); err != nil {
		switch {
		case NotFound(err):
			return rn, nil // gone: covered and absent, which tombstones it
		case refused(err):
			g.log("github: %s: reflecting no Dependabot alert %d: %v", repo, number, err)
			return verseio.Reflection{}, nil
		}
		return verseio.Reflection{}, err
	}
	rn.Add(g.AlertRef(repo, a.Number), alertPayload(repo, a))
	return rn, nil
}

// refused is whether err is GitHub refusing the request itself (httpError.refusal).
func refused(err error) bool {
	var he *httpError
	return asHTTPError(err, &he) && he.refusal()
}

// alert is one item of `GET /repos/{o}/{r}/dependabot/alerts`, and the single read's answer.
type alert struct {
	Number     int    `json:"number"`
	State      string `json:"state"`
	Dependency struct {
		Package struct {
			Ecosystem string `json:"ecosystem"`
			Name      string `json:"name"`
		} `json:"package"`
		ManifestPath string `json:"manifest_path"`
		Scope        string `json:"scope"`
		Relationship string `json:"relationship"`
	} `json:"dependency"`
	SecurityAdvisory struct {
		GHSAID   string `json:"ghsa_id"`
		CVEID    string `json:"cve_id"`
		Summary  string `json:"summary"`
		Severity string `json:"severity"`
	} `json:"security_advisory"`
	SecurityVulnerability struct {
		VulnerableVersionRange string `json:"vulnerable_version_range"`
		FirstPatchedVersion    *struct {
			Identifier string `json:"identifier"`
		} `json:"first_patched_version"`
	} `json:"security_vulnerability"`
	HTMLURL         string `json:"html_url"`
	CreatedAt       string `json:"created_at"`
	UpdatedAt       string `json:"updated_at"`
	FixedAt         string `json:"fixed_at"`
	DismissedAt     string `json:"dismissed_at"`
	DismissedReason string `json:"dismissed_reason"`
}

// alertPayload is what a charter reads an alert as. `state` is GitHub's own word (open, fixed,
// dismissed, auto_dismissed) and never absent; the rest is there where GitHub gives it, so an
// alert with no patched version carries no `patched` and one with no CVE carries no `cve`.
func alertPayload(repo string, a alert) entity.Obj {
	p := entity.Obj{"repo": repo, "number": a.Number, "state": a.State}
	patched := ""
	if v := a.SecurityVulnerability.FirstPatchedVersion; v != nil {
		patched = v.Identifier
	}
	for k, v := range map[string]string{
		"severity": a.SecurityAdvisory.Severity, "ecosystem": a.Dependency.Package.Ecosystem,
		"package": a.Dependency.Package.Name, "manifest": a.Dependency.ManifestPath,
		"relationship": a.Dependency.Relationship, "scope": a.Dependency.Scope,
		"vulnerableRange": a.SecurityVulnerability.VulnerableVersionRange, "patched": patched,
		"ghsa": a.SecurityAdvisory.GHSAID, "cve": a.SecurityAdvisory.CVEID,
		"summary": text(a.SecurityAdvisory.Summary), "url": a.HTMLURL,
		"createdAt": a.CreatedAt, "updatedAt": a.UpdatedAt, "fixedAt": a.FixedAt,
		"dismissedAt": a.DismissedAt, "dismissedReason": a.DismissedReason,
	} {
		if v != "" {
			p[k] = v
		}
	}
	return p
}

// listAlerts reads one state filter's alerts. refuseOver and window mean what they do for the
// issues listing: the live set is exhaustive or fails, the settled set is a window of the newest
// by update.
//
// PAGED BY CURSOR, which is the difference from every other listing here: this endpoint is
// closing down `page`, so the next page is the `after` of the Link header's `rel="next"`, and
// there is no next page when there is no such link.
func (a *api) listAlerts(ctx context.Context, repo, state string, refuseOver, window int) ([]alert, error) {
	var out []alert
	after := ""
	for {
		per := pageSize
		if window > 0 && window-len(out) < per {
			per = window - len(out)
		}
		v := url.Values{"per_page": {strconv.Itoa(max(per, 1))}, "state": {state}}
		if window > 0 {
			v.Set("sort", "updated")
			v.Set("direction", "desc")
		}
		if after != "" {
			v.Set("after", after)
		}
		var batch []alert
		link, err := a.readPage(ctx, "/repos/"+repo+"/dependabot/alerts?"+v.Encode(), &batch, true)
		if err != nil {
			return nil, err
		}
		out = append(out, batch...)
		if refuseOver > 0 && len(out) > refuseOver {
			return nil, fmt.Errorf("github: %s has more than %d open Dependabot alerts, and this "+
				"pass covers them as complete — a partial list under a complete cover tombstones "+
				"what it did not read. Raise -max-open, or narrow what this source reflects",
				repo, refuseOver)
		}
		if window > 0 && len(out) >= window {
			return out[:window], nil // the window is full, which is what a window means
		}
		after = nextCursor(link)
		if after == "" || len(batch) == 0 {
			return out, nil
		}
	}
}

// nextCursor is the `after` of a Link header's `rel="next"`, or "" where there is none.
func nextCursor(link string) string {
	for _, part := range strings.Split(link, ",") {
		target, params, ok := strings.Cut(part, ";")
		if !ok || !strings.Contains(params, `rel="next"`) {
			continue
		}
		u, err := url.Parse(strings.Trim(strings.TrimSpace(target), "<>"))
		if err != nil {
			return ""
		}
		return u.Query().Get("after")
	}
	return ""
}
