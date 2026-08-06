// Package linear reflects Linear teams — their issues and cycles — into the `linear:*` slice
// of the entity-verse.
//
// It is a deployed source: a program holding an API key, serving the two routes source/sourced
// serves, with verse dialling it and converging its answers. Like the Slack source and unlike
// the git one, the credential is the whole reason it cannot live in verse.
//
// THE TEAM IS THE SLICE. A source reflects a set of teams, every entity carries its team key,
// every cover is scoped by it, and every ownership claim is spelled with it — which is what
// makes two Linear sources over two teams provably disjoint, the same way two git sources over
// two repos are. It is also what decides what is NOT here; see Owns.
package linear

import (
	"context"
	"fmt"
	"net/http"
	"sort"
	"strings"

	"github.com/signadot/verse/entity"
	"github.com/signadot/verse/source"
)

// System is the entity slice a Linear source owns and is the single writer of.
const System = "linear"

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

	// Base overrides the GraphQL endpoint (a test server). HTTPClient overrides the transport.
	Base       string
	HTTPClient *http.Client
	// Log receives what a pass decided to carry on without. nil is silent.
	Log func(format string, args ...any)
}

// Linear is the Source.
type Linear struct {
	api      *api
	teams    []string // team keys, sorted, so two passes answer in one order
	pageSize int
	max      int
	log      func(string, ...any)
}

var _ source.Source = (*Linear)(nil)

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
		pageSize: cfg.PageSize,
		max:      cfg.MaxIssues,
		log:      cfg.Log,
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
		// A team key is a ref segment before it is anything else, and the ownership claim
		// `linear:issue:<KEY>-*` is a prefix comparison — so a key carrying the ref's own
		// punctuation, or the `-` the claim relies on, would address something it does not name.
		if strings.ContainsAny(key, ":/-") {
			return nil, fmt.Errorf("linear: team key %q contains : / or -, which are what its "+
				"entity ids and its ownership claim are built out of", key)
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

// System is the owned slice.
func (l *Linear) System() string { return System }

// Teams is the set of team keys this source reflects.
func (l *Linear) Teams() []string { return append([]string(nil), l.teams...) }

// Owns is the ownership claim an install proves disjointness with: four per team, because the
// four kinds spell their team four different ways.
//
//	linear:team:ENG                  the team itself, a bare key
//	linear:issue:ENG-*               Linear's OWN identifier, which is already team-prefixed
//	linear:cycle:ENG/*               a number, scoped the way git scopes a ref
//	linear:project:* at owners.ENG   every project, at this team's slot and nowhere else
//
// The issue claim leans on the hyphen, and it is load-bearing: `ENG-*` does not claim
// `ENGINE-1`, because that id does not start with `ENG-`. Dropping the hyphen for a bare
// `ENG*` would be the exact overclaim source/git.go names — not a false alarm but a real one,
// and one that would make two perfectly disjoint teams refuse to install alongside each other.
//
// THE PROJECT CLAIM IS THE INTERESTING ONE, and it used to be an absence. A Linear project
// belongs to several teams at once, so it has no positional owner: no team can claim it as an
// address, and two sources over two teams would each tombstone what the other reflected. This
// source therefore reflected no project at all, and that absence was the first real evidence
// against ownership-as-an-address.
//
// The way through was not a different vocabulary but a deeper address. A claim may name a path
// INSIDE a payload, so ENG is not a co-writer of `linear:project:<id>` — it is the single
// writer of that entity's `owners.ENG`, and OPS of its `owners.OPS`. Two of those are proved
// apart by the same comparison one segment further down, converge writes, compares and
// tombstones there and nowhere else, and the last owner out takes the entity with a conditional
// delete. Sharing an entity is not an exception to single-writership; it is single-writership at
// a deeper address (docs/working/shared-ownership.md).
//
// WHAT IT COSTS is a project with no top. Every field Linear has about a project — its name, its
// state — is GLOBAL, and a global field has no owner among the teams, so each slot carries its
// own copy and the entity has no `name` at its root. A charter reads a project through an owner
// (`owners.ENG.state`), and two owners' slots are the same bytes. That is the honest shape of an
// entity nobody owns the whole of, and it is worth knowing before writing a rule against one.
func (l *Linear) Owns() []source.Own {
	out := make([]source.Own, 0, 4*len(l.teams))
	for _, key := range l.teams {
		out = append(out,
			source.Own{Ref: System + ":team:" + key},
			source.Own{Ref: System + ":issue:" + key + "-*"},
			source.Own{Ref: System + ":cycle:" + key + "/*"},
			source.Own{Ref: System + ":project:*", At: slotOf(key)})
	}
	return out
}

// slotOf is the path inside a project this team is the single writer of. One spelling, used by
// the claim an install proves and by the cover a pass declares — they are the same statement
// about the same place, and two spellings of it is how they come to disagree.
func slotOf(key string) []string { return []string{"owners", key} }

// TeamRef is the ref that targets one team: a reconcile of it reconciles that team's issues
// and cycles, which is what a hint names.
func TeamRef(key string) entity.Ref {
	return entity.Ref{System: System, Kind: "team", ID: key}
}

// Reflect reads the configured teams and returns what `linear:*` should hold. A zero ref reads
// every team (the full pass); a team, issue or cycle ref reads just the team it belongs to.
func (l *Linear) Reflect(ctx context.Context, ref entity.Ref) (source.Reflection, error) {
	var out source.Reflection
	for _, key := range l.targets(ref) {
		rn, err := l.reflectTeam(ctx, key)
		if err != nil {
			return source.Reflection{}, fmt.Errorf("linear reflect %s: %w", key, err)
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
	if ref.System != System || ref.ID == "" {
		return l.teams
	}
	var key string
	switch ref.Kind {
	case "team":
		key = ref.ID
	case "issue":
		key, _, _ = strings.Cut(ref.ID, "-") // ENG-123
	case "cycle":
		key, _, _ = strings.Cut(ref.ID, "/") // ENG/42
	default:
		return l.teams // a kind this build does not know: a full pass cannot be wrong
	}
	for _, k := range l.teams {
		if k == key {
			return []string{k}
		}
	}
	// Another source's team. No targets means an empty reflection with NO covers, which claims
	// nothing and so tombstones nothing — the honest answer about somebody else's slice.
	return nil
}

// reflectTeam reads one team whole. Everything it returns is scoped by the `team` field —
// including the covers — so one team's pass never tombstones another's issues.
func (l *Linear) reflectTeam(ctx context.Context, key string) (source.Reflection, error) {
	// Declared BEFORE the read and unconditionally, as the git source declares its per-repo
	// covers: a team whose every issue was deleted must still cover `linear:issue:{team: key}`,
	// or the issues it no longer has would stand forever.
	mine := entity.Obj{"team": key}.Payload()
	rn := source.Reflection{Covers: []entity.Pattern{
		{System: System, Kind: "team", Predicate: mine},
		{System: System, Kind: "issue", Predicate: mine},
		{System: System, Kind: "cycle", Predicate: mine},
		// The project cover is narrowed to the projects that CARRY THIS TEAM'S SLOT, which is
		// what the null in the predicate says (a null in a match is a presence test). The slot
		// alone is not enough narrowing and the difference is not subtle: an unpredicated slot
		// cover claims completeness for its slot of every project in the workspace, so a pass
		// would write a tombstone into `owners.ENG` of projects ENG has never been on — once per
		// project per pass, forever — and, where one source reflects two teams, each team's
		// answer would read as "the other team's slot is empty" and delete it.
		//
		// Narrowed, it selects exactly the set a tombstone could be right about: a project this
		// team is recorded on and no longer owns. One it never owned is not covered, and one it
		// has just joined is written because the ANSWER carries the slot, which is what the
		// write side matches against.
		{System: System, Kind: "project", Slot: slotOf(key),
			Predicate: entity.Obj{"owners": map[string]any{key: nil}}.Payload()},
	}}
	page, err := l.api.readTeam(ctx, key, l.pageSize, l.max)
	if err != nil {
		return source.Reflection{}, err
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
	rn.Add(TeamRef(key), t)

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
		rn.Add(entity.Ref{System: System, Kind: "cycle",
			ID: fmt.Sprintf("%s/%d", key, c.Number)}, p)
	}

	for _, i := range page.Issues.Nodes {
		rn.Add(entity.Ref{System: System, Kind: "issue", ID: i.Identifier}, issuePayload(key, i))
	}

	// A project is written INTO THIS TEAM'S SLOT, which is what the answer's shape says: the
	// payload is entity-shaped and the cover above is what makes only `owners.<key>` of it
	// authoritative. A second team's source answers the same entity with its own slot, and
	// neither can reach the other's.
	for _, p := range page.Projects.Nodes {
		rn.Add(entity.Ref{System: System, Kind: "project", ID: p.ID},
			entity.Obj{"owners": map[string]any{key: projectPayload(key, p)}})
	}
	return rn, nil
}

// projectPayload is one team's view of a project, which is the whole of it: every field here is
// global, so two owners' slots hold the same bytes. See Owns for why that duplication is the
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
