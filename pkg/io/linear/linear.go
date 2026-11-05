// Package linear reflects Linear teams — their issues and cycles — into the `linear:*` slice
// of the entity-verse.
//
// It is a deployed source: a program holding an API key, serving the two routes source/iod
// serves, with verse dialling it and converging its answers. Like the Slack source and unlike
// the git one, the credential is the whole reason it cannot live in verse.
//
// THE TEAM IS THE SLICE, and it is a LEVEL OF THE ADDRESS: a source reflects a set of teams,
// and everything one of them has stands under its place.
//
//	linear.ENG.team           the team itself — its name, whether it is private
//	linear.ENG.issue.ENG-12   its issues, keyed by Linear's own identifier
//	linear.ENG.cycle."4"      its cycles, keyed by number
//	linear.project.p1.owners.ENG   its place inside a project several teams share
//
// That is what makes two Linear sources over two teams provably disjoint, the same way two git
// sources over two repos are — a comparison of places rather than of id prefixes. It also
// decides what a claim has to say; see Owns.
package linear

import (
	"context"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"

	"github.com/signadot/verse/pkg/entity"
	verseio "github.com/signadot/verse/pkg/io"
)

// Root is the entity slice a Linear source owns and is the single writer of.
const Root = "linear"

// Defaults for one team's read.
const (
	DefaultPageSize  = 100
	DefaultMaxIssues = 5000
)

// Config is what a Linear source is built from.
type Config struct {
	// Auth is the Authorization header, verbatim: a personal API key (`lin_api_…`) is sent
	// bare, an OAuth access token as `Bearer lin_oauth_…`.
	Auth string
	// Teams are the team keys to reflect (`ENG`). Empty is every team the credential can see —
	// which is the right default for a workspace-wide key and the wrong one for a workspace
	// with a team the size of a warehouse, hence MaxIssues.
	Teams []string
	// PageSize and MaxIssues bound one team's read (0 → the defaults). MaxIssues is a runaway
	// guard, and exceeding it is an ERROR rather than a truncation — see api.readTeam.
	PageSize, MaxIssues int

	// Root is the slice this source writes, empty meaning the package's Root. The operator's to choose,
	// for the reason the GitHub source's is: more than one gets deployed, and the partition
	// between them is drawn by whoever runs them rather than by how Linear is organised.
	Root string

	// Base overrides the GraphQL endpoint (a test server). HTTPClient overrides the transport.
	Base       string
	HTTPClient *http.Client
	// Log receives what a pass decided to carry on without. nil is silent.
	Log func(format string, args ...any)
}

// Linear is the Source.
type Linear struct {
	api      *api
	root     string
	teams    []string // team keys, sorted, so two passes answer in one order
	pageSize int
	max      int
	log      func(string, ...any)
}

var _ verseio.Source = (*Linear)(nil)

// New builds a Linear source and asks the credential which teams it can see.
//
// That question is the startup check — a revoked key or a misspelled team key is a task
// that does not start, in front of whoever just deployed it, rather than a source that
// installs clean and writes a failure into lastError once a minute forever. It is also what
// resolves "every team" into the concrete list Owns is spelled from, so an install can prove
// disjointness against something that names real teams.
func New(ctx context.Context, cfg Config) (*Linear, error) {
	if cfg.Auth == "" {
		return nil, fmt.Errorf("linear: an API key or OAuth token is required")
	}
	l := &Linear{
		api:      newAPI(cfg.Auth, cfg.Base, cfg.HTTPClient),
		root:     cfg.Root,
		pageSize: cfg.PageSize,
		max:      cfg.MaxIssues,
		log:      cfg.Log,
	}
	if l.root == "" {
		l.root = Root
	}
	if strings.ContainsAny(l.root, ":/.") {
		return nil, fmt.Errorf("linear: root %q may not contain : / or . — it is the first "+
			"segment of every address this source writes", l.root)
	}
	if l.pageSize <= 0 {
		l.pageSize = DefaultPageSize
	}
	if l.max <= 0 {
		l.max = DefaultMaxIssues
	}
	if l.log == nil {
		l.log = func(string, ...any) {}
	}
	visible, err := l.api.teams(ctx)
	if err != nil {
		return nil, err
	}
	have := map[string]bool{}
	for _, t := range visible {
		have[t.Key] = true
	}
	if len(cfg.Teams) == 0 {
		for _, t := range visible {
			l.teams = append(l.teams, t.Key)
		}
	} else {
		for _, key := range cfg.Teams {
			key = strings.ToUpper(strings.TrimSpace(key))
			if !have[key] {
				return nil, fmt.Errorf("linear: this credential cannot see team %q (it can see %s)",
					key, strings.Join(keysOf(visible), ", "))
			}
			l.teams = append(l.teams, key)
		}
	}
	if len(l.teams) == 0 {
		return nil, fmt.Errorf("linear: this credential can see no teams — a source that " +
			"reflects nothing would install, claim nothing, and never write")
	}
	for _, key := range l.teams {
		// A team key is one segment of an address — the place its entities stand under — so the
		// only things it may not hold are what a segment cannot hold. The hyphen is ordinary
		// again: an issue's id is a name under its team rather than a prefix a claim leans on.
		if strings.ContainsAny(key, ":/.") {
			return nil, fmt.Errorf("linear: team key %q contains : / or . — it is the place its "+
				"issues and cycles stand under, and those are a segment's own punctuation", key)
		}
		if strings.EqualFold(key, projectKind) {
			return nil, fmt.Errorf("linear: team key %q is the name the shared projects stand "+
				"at (%s.%s.<id>), so a team keyed that way would address them", key,
				l.root, projectKind)
		}
	}
	sort.Strings(l.teams)
	return l, nil
}

func keysOf(ts []team) []string {
	out := make([]string, 0, len(ts))
	for _, t := range ts {
		out = append(out, t.Key)
	}
	sort.Strings(out)
	return out
}

// Root is the owned slice.
func (l *Linear) Root() string { return l.root }

// Teams is the set of team keys this source reflects.
func (l *Linear) Teams() []string { return append([]string(nil), l.teams...) }

// Owns is the ownership claim an install proves disjointness with: TWO PLACES PER TEAM, and
// neither of them holds a glob inside a segment.
//
//	linear.ENG                     the team's place: its own fields, its issues, its cycles
//	linear.project.*.owners.ENG    every project, at this team's path and nowhere else
//
// The first is one claim for everything a team has alone, at whatever depth: its own node, its
// issues, its cycles. Nothing in it leans on how an id is spelled — `linear.ENG` and
// `linear.ENGINE` are two places, which a comparison of names says without being told, where a
// claim over ids would need the hyphen in `ENG-*` to hold `ENGINE-1` out.
//
// THE PROJECT CLAIM IS THE INTERESTING ONE. A Linear project belongs to several teams at once,
// so it has no positional owner: no team can claim the whole of it, and two sources over two
// teams would each tombstone what the other reflected.
//
// The way through is a DEEPER ADDRESS: ENG is not a co-writer of `linear.project.<id>` — it is
// the single writer of `linear.project.<id>.owners.ENG`, which is a node like any other. Two of
// those are proved apart by the same comparison one segment further down, converge writes,
// compares and tombstones there, and the last owner out empties `owners` and takes the project
// with a conditional delete. Sharing is not an exception to single-writership; it is
// single-writership at a deeper address (docs/io.md, sharing).
//
// WHAT IT COSTS is a project with no top. Every field Linear has about a project — its name, its
// state — is GLOBAL, and a global field has no owner among the teams, so each owner's path
// carries its own copy and the node has nothing at its root. A charter reads a project through
// an owner (`linear.project.<id>.owners.ENG.state`), and two owners hold the same bytes. That is
// the honest shape of a node nobody owns the whole of, and it is worth knowing before writing a
// rule against one.
func (l *Linear) Owns() []verseio.Own {
	out := make([]verseio.Own, 0, 2*len(l.teams))
	for _, key := range l.teams {
		out = append(out,
			verseio.Own{At: verseio.ClaimAt(l.root, key)},
			verseio.Own{At: verseio.ClaimAt(l.root, projectKind, "*", ownersField, key)})
	}
	return out
}

// projectKind is where the shared projects stand, beside the teams' own places, and ownersField
// is the level beneath a project that one team's place hangs from.
const (
	projectKind = "project"
	ownersField = "owners"
)

// place is the address segments one team's entities stand under, followed by more.
func (l *Linear) place(key string, more ...string) []string {
	return append([]string{l.root, key}, more...)
}

// TeamRef is the place one team's entities stand under: a reconcile of it reconciles that
// team's issues and cycles, which is what a hint names.
func TeamRef(key string) entity.Ref { return entity.RefAt(Root, key) }

// TeamRef is the same place in THIS source's root, which is what the webhook must hint: a
// hint at `linear.ENG` reaches nothing when the source writes `issues.ENG`.
func (l *Linear) TeamRef(key string) entity.Ref { return entity.RefAt(l.place(key)...) }

// SummaryRef is the node describing the team itself — its name, and whether it is private.
//
// A child of the team's place rather than the place itself, which is the shape the github
// source's `repo` summary has and for the same reason: the team's issues and cycles stand under
// that place, and a node written whole at it would be written over them every pass.
func (l *Linear) SummaryRef(key string) entity.Ref {
	return entity.RefAt(l.place(key, "team")...)
}

// IssueRef is the ref of one issue, under its team and keyed by Linear's own identifier.
func (l *Linear) IssueRef(key, identifier string) entity.Ref {
	return entity.RefAt(l.place(key, "issue", identifier)...)
}

// CycleRef is the ref of one cycle, under its team and keyed by its number alone: the team is
// the level above it, so the number has nothing to carry but itself.
func (l *Linear) CycleRef(key string, number int) entity.Ref {
	return entity.RefAt(l.place(key, "cycle", strconv.Itoa(number))...)
}

// ProjectRef is the shared node a project stands at: one node for every team that owns it.
func (l *Linear) ProjectRef(id string) entity.Ref {
	return entity.RefAt(l.root, projectKind, id)
}

// ProjectOwnerRef is the node one team owns inside a project — the whole of what it writes
// there. Its claim is the same address with the id a wildcard, spelled by verseio.ClaimAt.
func (l *Linear) ProjectOwnerRef(id, key string) entity.Ref {
	return entity.RefAt(l.root, projectKind, id, ownersField, key)
}

// Reflect reads the configured teams and returns what `linear:*` should hold. A zero ref reads
// every team (the full pass); a team, issue or cycle ref reads just the team it belongs to.
func (l *Linear) Reflect(ctx context.Context, ref entity.Ref) (verseio.Reflection, error) {
	var out verseio.Reflection
	for _, key := range l.targets(ref) {
		rn, err := l.reflectTeam(ctx, key)
		if err != nil {
			return verseio.Reflection{}, fmt.Errorf("linear reflect %s: %w", key, err)
		}
		out.Covers = append(out.Covers, rn.Covers...)
		out.Entities = append(out.Entities, rn.Entities...)
	}
	return out, nil
}

// targets reads a ref as "which teams": all of them for a zero ref, else the one the ref
// belongs to, and none at all for a team this source does not reflect.
//
// An issue ref narrows to its team rather than to itself, which is the same subsumption git
// makes for a repo: the team is what one query reads, so a finer target would cost the same
// request.
func (l *Linear) targets(ref entity.Ref) []string {
	// This source's own layout is a path: `<root>.<team>.<kind>.<id>`, so the team is a
	// segment rather than something to cut out of an id.
	segs := ref.Segs()
	if len(segs) < 2 || segs[0] != l.root {
		return l.teams // no place of this source's: a full pass cannot be wrong
	}
	if segs[1] == projectKind {
		// A project is owned by whoever owns a path inside it, so a hint about one is a hint
		// about every team here: each pass writes its own owner's place and nobody else's.
		return l.teams
	}
	for _, k := range l.teams {
		if k == segs[1] {
			return []string{k}
		}
	}
	// Another source's team. No targets means an empty reflection with NO covers, which claims
	// nothing and so tombstones nothing — the honest answer about somebody else's slice.
	return nil
}

// reflectTeam reads one team whole. Everything it returns stands under that team's place —
// covers included — so one team's pass never tombstones another's issues.
func (l *Linear) reflectTeam(ctx context.Context, key string) (verseio.Reflection, error) {
	// Declared BEFORE the read and unconditionally, as the git source declares its per-repo
	// covers: a team whose every issue was deleted must still cover `linear:issue:{team: key}`,
	// or the issues it no longer has would stand forever.
	rn := verseio.Reflection{Covers: []entity.Pattern{
		// PLACES UNDER THE TEAM, which is what makes them this team's — rather than a pattern
		// over every id of a kind, narrowed back to one team by a payload predicate every
		// entity would have to carry.
		verseio.Cover(l.place(key, "team")...),
		verseio.Cover(l.place(key, "issue", "*")...),
		verseio.Cover(l.place(key, "cycle", "*")...),
		// THE PROJECT COVER IS THIS TEAM'S PLACE INSIDE EVERY PROJECT:
		// `linear.project.*.owners.ENG` claims completeness for what ENG writes there and
		// reaches nothing else. A project ENG has never been on holds nothing at that address,
		// so a pass that does not carry it tombstones nothing; one ENG has just joined is
		// written because the answer carries it. Another team's place under the same project is
		// a different address, and neither claim reaches it.
		verseio.Cover(l.root, projectKind, "*", ownersField, key),
	}}
	page, err := l.api.readTeam(ctx, key, l.pageSize, l.max)
	if err != nil {
		return verseio.Reflection{}, err
	}
	t := entity.Obj{
		"team":    key,
		"id":      page.ID,
		"name":    page.Name,
		"private": page.Private,
	}
	if page.Description != "" {
		t["description"] = page.Description
	}
	rn.Add(l.SummaryRef(key), t)

	for _, c := range page.Cycles.Nodes {
		p := entity.Obj{"team": key, "id": c.ID, "number": c.Number}
		for k, v := range map[string]string{
			"name": c.Name, "startsAt": c.StartsAt, "endsAt": c.EndsAt,
			"completedAt": c.CompletedAt,
		} {
			if v != "" {
				p[k] = v
			}
		}
		rn.Add(l.CycleRef(key, c.Number), p)
	}

	for _, i := range page.Issues.Nodes {
		rn.Add(l.IssueRef(key, i.Identifier), issuePayload(key, i))
	}

	// A project is written AT THIS TEAM'S PLACE INSIDE IT — `linear.project.<id>.owners.<key>`
	// — which is an address like any other, so nothing about writing it is special. A second
	// team's source writes its own place under the same project, and neither can reach the
	// other's.
	for _, p := range page.Projects.Nodes {
		rn.Add(l.ProjectOwnerRef(p.ID, key), entity.Obj(projectPayload(key, p)))
	}
	return rn, nil
}

// projectPayload is one team's view of a project, which is the whole of it: every field here is
// global, so two owners' nodes hold the same bytes. See Owns for why that duplication is the
// shape rather than a defect.
func projectPayload(key string, p project) map[string]any {
	out := map[string]any{"team": key, "id": p.ID, "name": p.Name, "url": p.URL}
	for k, v := range map[string]string{
		"description": p.Description, "state": p.State,
		"startDate": p.StartDate, "targetDate": p.TargetDate,
	} {
		if v != "" {
			out[k] = v
		}
	}
	if p.Progress > 0 {
		out["progress"] = p.Progress
	}
	return out
}

// issuePayload is what a charter reads an issue as.
//
// `state` and `stateType` are both carried, and the second is the one to write a rule against:
// a workspace renames "In Review" on a Tuesday, while the type behind it (`triage`, `backlog`,
// `unstarted`, `started`, `completed`, `canceled`) is Linear's own vocabulary and does not
// move. A charter matching the display name is a charter that breaks when somebody tidies a
// board.
func issuePayload(key string, i issue) entity.Obj {
	p := entity.Obj{
		"team":       key,
		"id":         i.ID,
		"identifier": i.Identifier,
		"title":      i.Title,
		"state":      i.State.Name,
		"stateType":  i.State.Type,
		"priority":   i.Priority,
		"url":        i.URL,
		"createdAt":  i.CreatedAt,
		"updatedAt":  i.UpdatedAt,
	}
	if i.PriorityLabel != "" {
		p["priorityLabel"] = i.PriorityLabel
	}
	if i.CompletedAt != "" {
		p["completedAt"] = i.CompletedAt
	}
	if i.Estimate != nil {
		p["estimate"] = *i.Estimate
	}
	if i.Assignee != nil {
		if i.Assignee.DisplayName != "" {
			p["assignee"] = i.Assignee.DisplayName
		} else {
			p["assignee"] = i.Assignee.Name
		}
		if i.Assignee.Email != "" {
			p["assigneeEmail"] = i.Assignee.Email
		}
	}
	if n := len(i.Labels.Nodes); n > 0 {
		labels := make([]string, 0, n)
		for _, l := range i.Labels.Nodes {
			labels = append(labels, l.Name)
		}
		sort.Strings(labels) // one order, so a re-read of the same issue is not a change
		p["labels"] = labels
	}
	if i.Project != nil && i.Project.Name != "" {
		// The project's NAME, as a field on the issue — not a `linear:project:*` entity. See
		// Owns for why there is no such entity.
		p["project"] = i.Project.Name
	}
	if i.Cycle != nil {
		p["cycle"] = i.Cycle.Number
	}
	if i.Parent != nil && i.Parent.Identifier != "" {
		p["parent"] = i.Parent.Identifier
	}
	return p
}
