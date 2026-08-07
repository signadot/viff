// Package github reflects the collaboration around a repository — its issues and its pull
// requests — into the `github:*` slice of the entity-verse.
//
// It is a deployed source: a program holding a token, serving the two routes source/sourced
// serves, with verse dialling it and converging its answers.
//
// WHAT IT IS NOT is a second view of the code. A git source reflects what a repository
// CONTAINS — refs, commits, content-addressed and cursorless, from a fetch that costs no API
// budget — and this reflects what GitHub KNOWS about it. The default branch and where it points
// is the one fact that is genuinely both, and it is here because taking it from GitHub costs
// one un-paginated call where taking it from git costs a deployed mirror. Refs and commits stay
// on the far side of that line for the same reason in reverse: through the API they are the
// highest-cardinality paginated reads in the repository, and paying a rate limit for what git
// gives away free is the wrong trade at one repo and impossible at many.
//
// See docs/working/gh-source.md for the design and docs/working/gh-source-plan.md for what is
// built so far — this is step 1, which is the repository, its issues and its pull requests.
// Discussions, Actions runs, the webhook and the throttle are still ahead.
package github

import (
	"context"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"

	"github.com/signadot/verse/entity"
	"github.com/signadot/verse/source"
)

// DefaultSystem is the slice a GitHub source owns unless the operator says otherwise.
const DefaultSystem = "github"

// Defaults for one repository's read.
const (
	DefaultSettled  = 50
	DefaultMaxOpen  = 2000
	DefaultRuns     = 50
	DefaultComments = 200
)

// Config is what a GitHub source is built from.
type Config struct {
	// Token is the credential, sent as a bearer. A fine-grained PAT with read access to the
	// repositories' contents, issues and pull requests is what a dogfood deployment wants.
	Token string
	// Repos are the repositories to reflect, as `<org>/<name>`.
	Repos []string
	// System is the slice this source writes, empty meaning DefaultSystem.
	//
	// THE OPERATOR'S TO CHOOSE, because more than one of these gets deployed — different
	// credentials, different budgets, different networks — and the partition between them is
	// drawn by whoever runs them rather than by how GitHub organises itself. Two deployments
	// sharing `github` is the ordinary case and needs only disjoint claims; one that wants no
	// coordination at all takes a system of its own, and two systems are disjoint on the first
	// segment. It is also the answer to a second GitHub instance, where `org/repo#42` is unique
	// within a host and not across two. See docs/working/gh-source.md.
	System string
	// Settled is how many recently-closed issues and pull requests each repository keeps
	// (0 → DefaultSettled). MaxOpen refuses a repository with more open items than this
	// (0 → DefaultMaxOpen) rather than answering partially for a slice it covers completely.
	Settled, MaxOpen int
	// Runs is how many of a repository's most recent workflow runs it keeps (0 → DefaultRuns).
	Runs int
	// Comments is how many of a repository's most recently updated comments it keeps
	// (0 → DefaultComments), across issues and pull requests together.
	Comments int
	// SkipDiscussions stops this source reading discussions at all, and is the one lever the
	// budget measurement turned up.
	//
	// Every REST read here is conditional and a 304 does not count against the rate limit, so a
	// repository where nothing happened costs nothing — except for discussions, which are
	// GraphQL, and a POST carries no ETag. Measured, that is the whole of a warm pass: two
	// chargeable requests out of nine issued, both of them discussions. So a deployment that
	// does not care about them reflects an effectively unbounded number of repositories on one
	// token, and one that does is capped near forty. Nothing else moves that number.
	//
	// Skipping drops the discussion covers with the entities, exactly as an unavailable read
	// does: a source that says nothing about discussions must not be read as saying there are
	// none, or turning this on would tombstone every discussion it had reflected.
	SkipDiscussions bool

	// Base overrides the API root (GitHub Enterprise Server, or a test server). HTTPClient
	// overrides the transport.
	Base       string
	HTTPClient *http.Client
	// Log receives what a pass decided to carry on without. nil is silent.
	Log func(format string, args ...any)
}

// GitHub is the Source.
type GitHub struct {
	api      *api
	system   string
	repos    []string // `<org>/<name>`, sorted, so two passes answer in one order
	settled  int
	maxOpen  int
	runs     int
	comments int
	noDiscus bool
	log      func(string, ...any)
}

var _ source.Source = (*GitHub)(nil)

// New builds a GitHub source and asks the token whether it can see what it was pointed at.
//
// That question is the startup check, as Slack's auth.test and Linear's team read are: a
// revoked token or a misspelled repository is a task that does not start, in front of whoever
// just deployed it, rather than a source that installs clean and writes a failure into
// lastError once a minute forever.
func New(ctx context.Context, cfg Config) (*GitHub, error) {
	if cfg.Token == "" {
		return nil, fmt.Errorf("github: a token is required")
	}
	if len(cfg.Repos) == 0 {
		return nil, fmt.Errorf("github: at least one repository is required — a source that " +
			"reflects nothing would install, claim nothing, and never write")
	}
	g := &GitHub{
		api:      newAPI(cfg.Token, cfg.Base, cfg.HTTPClient),
		system:   cfg.System,
		settled:  cfg.Settled,
		maxOpen:  cfg.MaxOpen,
		runs:     cfg.Runs,
		comments: cfg.Comments,
		noDiscus: cfg.SkipDiscussions,
		log:      cfg.Log,
	}
	if g.system == "" {
		g.system = DefaultSystem
	}
	// The system is a segment of the address and of the route that serves the registration, so
	// neither punctuation survives — the same check source.Entity.Check makes, made here so a
	// bad `-system` fails the program rather than the install.
	if strings.ContainsAny(g.system, ":/") {
		return nil, fmt.Errorf("github: system %q may not contain : or / — it is a segment of "+
			"the address (sources:<system>:<name>) and of the route that serves it", g.system)
	}
	if g.settled <= 0 {
		g.settled = DefaultSettled
	}
	if g.maxOpen <= 0 {
		g.maxOpen = DefaultMaxOpen
	}
	if g.runs <= 0 {
		g.runs = DefaultRuns
	}
	if g.comments <= 0 {
		g.comments = DefaultComments
	}
	if g.log == nil {
		g.log = func(string, ...any) {}
	}
	for _, r := range cfg.Repos {
		r = strings.TrimSpace(r)
		org, name, ok := strings.Cut(r, "/")
		if !ok || org == "" || name == "" || strings.Contains(name, "/") {
			return nil, fmt.Errorf("github: %q is not a repository — want <org>/<name>", r)
		}
		// `#` separates a repository from an item number in every id this source writes, and
		// the ownership claim `<org>/<name>#*` is a prefix comparison over exactly that. A
		// repository carrying one would address something it does not name.
		if strings.ContainsAny(r, "#:") {
			return nil, fmt.Errorf("github: repository %q contains # or :, which are what its "+
				"entity ids and its ownership claim are built out of", r)
		}
		g.repos = append(g.repos, r)
	}
	sort.Strings(g.repos)
	// Ask the credential about each one, which is the check and also the first cache fill.
	for _, r := range g.repos {
		if _, _, err := g.api.readRepo(ctx, r); err != nil {
			return nil, fmt.Errorf("github: cannot read %s: %w", r, err)
		}
	}
	return g, nil
}

// System is the owned slice.
func (g *GitHub) System() string { return g.system }

// Repos is the set of repositories this source reflects.
func (g *GitHub) Repos() []string { return append([]string(nil), g.repos...) }

// Owns is the ownership claim an install proves disjointness with: one per kind per repository,
// and the kinds are NAMED rather than globbed.
//
// A `github:*:<org>/<name>#*` claim would be shorter and would work today. It would also refuse
// one particular future install, and it is the one this design is most likely to want: a source
// reflecting PEOPLE, claiming `github:user:*` and nothing else, since a GitHub user belongs to
// GitHub rather than to any repository. Against a kind-globbed claim that is refused — a `*`
// kind is disjoint from nothing, and the user claim's id is a bare `*`, disjoint from nothing
// either — while against these it is disjoint on the kind segment and installs clean.
//
// The `#` is load-bearing in the way Linear's hyphen is: `signadot/verse#*` does not claim
// `signadot/verse2#1`, because that id does not start with `signadot/verse#`. A single glob
// `signadot/verse*` would, which is not a false alarm but a real overclaim, and one that would
// make two perfectly disjoint repositories refuse to install alongside each other.
//
// THE LIST GROWS WITH THE CONNECTOR, and must not run ahead of it. A source claims what it
// writes and no more — claiming a kind this build does not reflect refuses a neighbour over
// entities nobody is writing. Discussions and Actions runs add theirs when they arrive.
func (g *GitHub) Owns() []source.Own {
	out := make([]source.Own, 0, 6*len(g.repos))
	for _, r := range g.repos {
		out = append(out,
			source.Own{Ref: g.system + ":repo:" + r},
			source.Own{Ref: g.system + ":issue:" + r + "#*"},
			source.Own{Ref: g.system + ":pr:" + r + "#*"},
			// A run's id is GitHub's own number under the repository, so it is scoped the way
			// git scopes a ref rather than with the `#` the numbered kinds share.
			source.Own{Ref: g.system + ":run:" + r + "/*"},
			source.Own{Ref: g.system + ":discussion:" + r + "#*"},
			source.Own{Ref: g.system + ":comment:" + r + "#*"})
	}
	return out
}

// RepoRef is the ref that targets one repository: a reconcile of it reconciles that repository
// whole, which is what a hint about a push or a repository event names.
func (g *GitHub) RepoRef(repo string) entity.Ref {
	return entity.Ref{System: g.system, Kind: "repo", ID: repo}
}

// RunRef is the ref of one workflow run.
func (g *GitHub) RunRef(repo string, id int64) entity.Ref {
	return entity.Ref{System: g.system, Kind: "run", ID: repo + "/" + strconv.FormatInt(id, 10)}
}

// CommentRef is the ref of one comment: the item it is on, then GitHub's own id.
//
// Under the item rather than beside it, so a charter reading "the comments on #42" is a prefix
// of one address rather than a payload predicate over the whole repository — and so the
// ownership claim is the same `<org>/<name>#*` the numbered kinds already share.
func (g *GitHub) CommentRef(repo string, number int, id int64) entity.Ref {
	return entity.Ref{System: g.system, Kind: "comment",
		ID: repo + "#" + strconv.Itoa(number) + "/" + strconv.FormatInt(id, 10)}
}

// ItemRef is the ref of one issue or pull request.
func (g *GitHub) ItemRef(kind, repo string, number int) entity.Ref {
	return entity.Ref{System: g.system, Kind: kind, ID: repo + "#" + strconv.Itoa(number)}
}

// Reflect reads what `<system>:*` should hold. A zero ref reads every repository; a repo ref
// reads that repository whole; an issue or pull-request ref reads just that item.
func (g *GitHub) Reflect(ctx context.Context, ref entity.Ref) (source.Reflection, error) {
	if ref.System != "" && ref.System != g.system {
		// Another source's slice. No targets means an empty reflection with NO covers, which
		// claims nothing and so tombstones nothing.
		return source.Reflection{}, nil
	}
	if item, ok := g.targetItem(ref); ok {
		return g.reflectItem(ctx, item)
	}
	if repo, id, ok := g.targetRun(ref); ok {
		return g.reflectRun(ctx, repo, id)
	}
	if repo, number, id, ok := g.targetComment(ref); ok {
		return g.reflectComment(ctx, repo, number, id)
	}
	var out source.Reflection
	for _, repo := range g.targetRepos(ref) {
		rn, err := g.reflectRepo(ctx, repo)
		if err != nil {
			return source.Reflection{}, fmt.Errorf("github reflect %s: %w", repo, err)
		}
		out.Covers = append(out.Covers, rn.Covers...)
		out.Entities = append(out.Entities, rn.Entities...)
	}
	return out, nil
}

// targetRepos reads a ref as "which repositories": all of them for a zero ref, else the one it
// names, and none at all for a repository this source does not reflect.
func (g *GitHub) targetRepos(ref entity.Ref) []string {
	if ref.ID == "" {
		return g.repos
	}
	repo := ref.ID
	if i := strings.Index(repo, "#"); i >= 0 {
		repo = repo[:i]
	}
	for _, r := range g.repos {
		if r == repo {
			return []string{r}
		}
	}
	return nil
}

// targetItem reads a ref as one issue or pull request, which is the finest a pass gets.
//
// It is worth having, and it is the first source where it is: git's targeted pass is a whole
// repository because git is content-addressed and a finer target costs the same, and Linear's
// is a whole team because that is what one query reads. GitHub answers a single issue in one
// request, so a targeted pass over a busy repository is one call rather than six.
type targetedItem struct {
	kind   string
	repo   string
	number int
}

func (g *GitHub) targetItem(ref entity.Ref) (targetedItem, bool) {
	if ref.Kind != "issue" && ref.Kind != "pr" {
		return targetedItem{}, false
	}
	repo, num, ok := strings.Cut(ref.ID, "#")
	if !ok {
		return targetedItem{}, false
	}
	n, err := strconv.Atoi(num)
	if err != nil || n <= 0 {
		return targetedItem{}, false
	}
	for _, r := range g.repos {
		if r == repo {
			return targetedItem{kind: ref.Kind, repo: repo, number: n}, true
		}
	}
	return targetedItem{}, false
}

// reflectItem answers for ONE item, and covers exactly it.
//
// THE COVER IS THE WHOLE OF WHY THIS IS SEPARATE. A targeted pass that answered with the live
// cover — `{repo: X, state: open}` — while carrying one entity would tombstone every other open
// issue in the repository, which is the one mistake in this connector that destroys a slice. So
// the cover names the item by the fields the payload carries, and a pass about #42 can say
// nothing at all about #43.
//
// An item GitHub no longer has is covered and absent, which tombstones it — a deleted or
// transferred issue, which is exactly what a targeted pass should conclude.
func (g *GitHub) reflectItem(ctx context.Context, t targetedItem) (source.Reflection, error) {
	mine := entity.Obj{"repo": t.repo, "number": t.number}.Payload()
	rn := source.Reflection{Covers: []entity.Pattern{
		{System: g.system, Kind: t.kind, Predicate: mine},
	}}
	if t.kind == "pr" {
		var p pull
		if err := g.api.get(ctx, "/repos/"+t.repo+"/pulls/"+strconv.Itoa(t.number), &p); err != nil {
			if NotFound(err) {
				return rn, nil // gone: covered and absent, which tombstones it
			}
			return source.Reflection{}, err
		}
		rn.Add(g.ItemRef("pr", t.repo, p.Number), pullPayload(t.repo, p))
		return rn, nil
	}
	var i issue
	if err := g.api.get(ctx, "/repos/"+t.repo+"/issues/"+strconv.Itoa(t.number), &i); err != nil {
		if NotFound(err) {
			return rn, nil
		}
		return source.Reflection{}, err
	}
	if i.isPullRequest() {
		// The issues endpoint answers for a pull request under the same number. Writing it as
		// an issue would put one thing in two kinds; the pr half of the hint is what covers it.
		return rn, nil
	}
	rn.Add(g.ItemRef("issue", t.repo, i.Number), issuePayload(t.repo, i))
	return rn, nil
}

// reflectRepo reads one repository whole. Everything it returns is scoped by the `repo` field —
// including the covers — so one repository's pass never tombstones another's.
//
// TWO COVERS PER SET-SHAPED KIND, which is the decision this connector exists to make. Neither
// precedent fits: git keeps history (commits covered by nothing, so they stand forever) and
// Slack keeps a window (messages covered, so what falls out is tombstoned). Issues and pull
// requests are a third shape — an unbounded set whose LIVE part is bounded — so the live set is
// covered complete and the settled set is covered as a window. An item that closes appears in
// the settled half carrying its new state, ages, and leaves.
//
// Both halves must be covered or neither works. Covering only `{repo}` and answering with the
// live set plus a recent tail would tombstone the whole back catalogue on the first pass;
// covering only the live half would leave a husk per item ever closed while this source watched,
// which is the warehouse arriving slowly rather than at once.
func (g *GitHub) reflectRepo(ctx context.Context, repo string) (source.Reflection, error) {
	mine := entity.Obj{"repo": repo}.Payload()
	open := entity.Obj{"repo": repo, "state": "open"}.Payload()
	closed := entity.Obj{"repo": repo, "state": "closed"}.Payload()
	// Declared BEFORE the read and unconditionally, as git's and Linear's are: a repository
	// whose every issue was closed must still cover `{repo, state: open}`, or the ones it no
	// longer has open would stand forever.
	rn := source.Reflection{Covers: []entity.Pattern{
		{System: g.system, Kind: "repo", Predicate: mine},
		{System: g.system, Kind: "issue", Predicate: open},
		{System: g.system, Kind: "issue", Predicate: closed},
		{System: g.system, Kind: "pr", Predicate: open},
		{System: g.system, Kind: "pr", Predicate: closed},
		// Runs are a WINDOW and nothing else — Slack's messages rather than issues' two
		// halves. A completed run is never interesting again and an unbounded stream of them
		// is the warehouse a source is not supposed to be, so the last `-runs` created are
		// kept and what falls out is tombstoned.
		//
		// Covered WHOLE rather than split by status, and that costs something worth naming.
		// GitHub's runs listing offers no ordering of its own and its `status` filter takes a
		// single value, so there is no read that returns "everything not finished" — an
		// enumeration of statuses would leave whatever GitHub adds next uncovered, standing
		// forever. So the window covers every status, and a run still in flight when more than
		// `-runs` newer ones have been created falls out of it and is tombstoned: a charter
		// waiting on that run never sees its conclusion. That is this connector's version of
		// Slack's aged-out thread parent, and the lever is the same — `-runs` wants to be
		// comfortably more than a repository creates while its longest job runs.
		{System: g.system, Kind: "run", Predicate: mine},
		// Comments are a WINDOW, and Slack's messages are the precedent rather than issues' two
		// halves: a conversation is an unbounded stream, and an entity-verse holding all of one
		// is the warehouse a source is not supposed to be. The most recently updated `-comments`
		// are kept and what falls out is tombstoned — which also makes a comment DELETED inside
		// the window go, where an uncovered one would stand forever.
		{System: g.system, Kind: "comment", Predicate: mine},
	}}

	r, sha, err := g.api.readRepo(ctx, repo)
	if err != nil {
		return source.Reflection{}, err
	}
	rn.Add(g.RepoRef(repo), repoPayload(repo, r, sha))

	// The live set, to exhaustion. Bounded by maxOpen, which errors rather than truncating.
	openIssues, err := g.api.listIssues(ctx, repo, "open", "", g.maxOpen, 0)
	if err != nil {
		return source.Reflection{}, err
	}
	// The settled window: the most recently updated closed items, which is a window and a
	// catch-up at once — see the note in api.go.
	closedIssues, err := g.api.listIssues(ctx, repo, "closed", "updated", 0, g.settled)
	if err != nil {
		return source.Reflection{}, err
	}
	for _, i := range append(openIssues, closedIssues...) {
		if i.isPullRequest() {
			// GitHub returns pull requests from the issues endpoint. Reflecting them here would
			// put one thing under two kinds sharing a number, and would make "every open issue"
			// a question that quietly includes review traffic.
			//
			// Its comment COUNT is not harvested either, though this row carries one and the
			// pulls listing does not. The two listings are windowed separately, so a closed pull
			// request can sit inside the pulls window and past the end of the issues one, and
			// the count would then read 0 and flip as items updated. The comments themselves are
			// entities now, which is the better answer to the question that field was for.
			continue
		}
		rn.Add(g.ItemRef("issue", repo, i.Number), issuePayload(repo, i))
	}

	openPulls, err := g.api.listPulls(ctx, repo, "open", "", g.maxOpen, 0)
	if err != nil {
		return source.Reflection{}, err
	}
	closedPulls, err := g.api.listPulls(ctx, repo, "closed", "updated", 0, g.settled)
	if err != nil {
		return source.Reflection{}, err
	}
	// DISCUSSIONS ARE A SEPARABLE CLAIM, and the asymmetry is the whole of what Covers is for.
	//
	// A repository with the feature turned off, or a token that cannot see it, or a GraphQL
	// endpoint having a bad minute, all mean the same thing: this pass has nothing true to say
	// about discussions. So it says nothing — the covers are dropped AND the entities with
	// them, which leaves whatever is standing exactly where it is rather than deleting a set
	// nobody looked at.
	//
	// The issues and pull requests get no such treatment, and the difference is not squeamish-
	// ness: a pass that lost them has nothing true left to say at all, so it fails. This is
	// Slack's people beside its channels, one connector later, and it is a job that would
	// otherwise have to be a flag.
	if ds, err := g.readDiscussions(ctx, repo); err != nil {
		g.log("github: %s: reflecting no discussions this pass: %v", repo, err)
	} else {
		rn.Covers = append(rn.Covers,
			entity.Pattern{System: g.system, Kind: "discussion", Predicate: open},
			entity.Pattern{System: g.system, Kind: "discussion", Predicate: closed})
		for _, d := range ds {
			rn.Add(g.ItemRef("discussion", repo, d.Number), discussionPayload(repo, d))
		}
	}

	// Both listings, and both or neither: an issue-comment read that succeeded beside a review-
	// comment read that failed would cover the whole window while carrying half of it, which
	// tombstones every review comment in the repository.
	conversation, err := g.api.listComments(ctx, repo, "issues", g.comments)
	if err != nil {
		return source.Reflection{}, err
	}
	review, err := g.api.listComments(ctx, repo, "pulls", g.comments)
	if err != nil {
		return source.Reflection{}, err
	}
	for _, c := range conversation {
		rn.Add(g.CommentRef(repo, c.number(), c.ID), commentPayload(repo, c, "conversation"))
	}
	for _, c := range review {
		rn.Add(g.CommentRef(repo, c.number(), c.ID), commentPayload(repo, c, "review"))
	}

	runs, err := g.api.listRuns(ctx, repo, g.runs)
	if err != nil {
		return source.Reflection{}, err
	}
	sort.Slice(runs, func(i, j int) bool { return runs[i].ID < runs[j].ID })
	for _, r := range runs {
		rn.Add(g.RunRef(repo, r.ID), runPayload(repo, r))
	}

	// CI state lands on the pull request as a field, not as entities of its own. "Is this PR
	// green" is a property of the proposal rather than of the seventeen check runs that
	// produced it, and a kind per check would multiply the busiest slice in the verse to answer
	// a question nobody asks in that form. A charter that wants the failing job follows `url`.
	//
	// Derived from the runs already read, by head sha, so it costs no request at all — where
	// asking GitHub per pull request would cost one each and put the budget somewhere it does
	// not belong. A pull request whose head has no run in the window carries no `checks` field,
	// which is absent rather than wrong.
	checks := checksByHeadSHA(runs)
	for _, p := range append(openPulls, closedPulls...) {
		payload := pullPayload(repo, p)
		if p.Head != nil {
			if c, ok := checks[p.Head.SHA]; ok {
				payload["checks"] = c
			}
		}
		rn.Add(g.ItemRef("pr", repo, p.Number), payload)
	}
	return rn, nil
}

// readDiscussions reads both halves, live and settled, or reports why it could not.
//
// Both reads or neither: a live half that succeeded beside a settled half that failed would
// cover the closed set as a window while carrying nothing of it, which tombstones every closed
// discussion the repository has. The covers are declared by the caller only when this returns.
func (g *GitHub) readDiscussions(ctx context.Context, repo string) ([]discussion, error) {
	if g.noDiscus {
		return nil, errDiscussionsSkipped
	}
	live, err := g.api.listDiscussions(ctx, repo, "OPEN", g.maxOpen, 0)
	if err != nil {
		return nil, err
	}
	settled, err := g.api.listDiscussions(ctx, repo, "CLOSED", 0, g.settled)
	if err != nil {
		return nil, err
	}
	return append(live, settled...), nil
}

// errDiscussionsSkipped is the operator having turned discussions off, which reaches converge by
// the same route an unavailable read does: covers dropped, entities dropped, whatever is
// standing left alone.
var errDiscussionsSkipped = fmt.Errorf("github: discussions are switched off for this source")

// checksByHeadSHA reduces the runs to one answer per head sha: what CI most recently said about
// that commit.
//
// The NEWEST run wins, by id, because a re-run is a new run over the same sha and its answer is
// the current one. Deterministic for the same reason every list in this file is sorted — a
// payload that depended on map order would differ between two reads of unchanged data, and
// every entity would be rewritten on every pass.
func checksByHeadSHA(runs []workflowRun) map[string]string {
	newest := map[string]workflowRun{}
	for _, r := range runs {
		if r.HeadSHA == "" {
			continue
		}
		if prev, ok := newest[r.HeadSHA]; !ok || r.ID > prev.ID {
			newest[r.HeadSHA] = r
		}
	}
	out := make(map[string]string, len(newest))
	for sha, r := range newest {
		out[sha] = runState(r)
	}
	return out
}

// runState is the one word a charter reads: what a run concluded, or what it is still doing.
func runState(r workflowRun) string {
	if r.Conclusion != "" {
		return r.Conclusion
	}
	return r.Status
}

// ── payloads ─────────────────────────────────────────────────────────────────
//
// NOTHING HERE MAY DIFFER BETWEEN TWO READS OF UNCHANGED DATA. converge compares the answer
// against the store, so a list in whatever order the API returned it, or anything derived from
// the time of the read, makes every entity differ on every pass: the whole slice is rewritten
// every minute, every trigger over it fires, and lastReflect is stamped forever. Every list
// below is sorted and nothing is clock-derived. Linear sorts an issue's labels for exactly this
// reason; GitHub carries more lists than Linear does.

func repoPayload(repo string, r repository, defaultBranchSha string) entity.Obj {
	org, name, _ := strings.Cut(repo, "/")
	p := entity.Obj{
		"repo": repo, "org": org, "name": name,
		"private": r.Private, "archived": r.Archived, "fork": r.Fork,
	}
	for k, v := range map[string]string{
		"defaultBranch": r.DefaultBranch, "defaultBranchSha": defaultBranchSha,
		// The remote a git mirror is cloned FROM, which is the one field that identifies this
		// repository across the `git:*` slice as well: a git source's repo id is the operator's,
		// so nothing else lines the two up, and a source inventing a cross-system key is what
		// this design forbids. A charter that wants them paired joins on this and writes a
		// derived entity, as scripts/lineage.sh does.
		"cloneUrl": r.CloneURL, "sshUrl": r.SSHURL,
		"description": text(r.Description), "homepage": r.Homepage, "url": r.HTMLURL,
	} {
		if v != "" {
			p[k] = v
		}
	}
	if len(r.Topics) > 0 {
		p["topics"] = sorted(r.Topics)
	}
	return p
}

// issuePayload is what a charter reads an issue as.
//
// `state` is `open`/`closed` — GitHub's own vocabulary, and what the covers are spelled with, so
// it is never absent. A rule matching a label or a milestone title is a rule that breaks when
// somebody tidies a board; a rule matching `state` is not.
func issuePayload(repo string, i issue) entity.Obj {
	p := entity.Obj{
		"repo": repo, "number": i.Number, "title": i.Title, "state": i.State,
		"comments": i.Comments,
	}
	if i.User != nil {
		p["author"] = i.User.Login
	}
	for k, v := range map[string]string{
		"url": i.HTMLURL, "createdAt": i.CreatedAt, "updatedAt": i.UpdatedAt,
		"closedAt": i.ClosedAt, "stateReason": i.StateReason, "body": text(i.Body),
	} {
		if v != "" {
			p[k] = v
		}
	}
	if i.Milestone != nil && i.Milestone.Title != "" {
		p["milestone"] = i.Milestone.Title
	}
	if names := labelNames(i.Labels); len(names) > 0 {
		p["labels"] = names
	}
	if logins := userLogins(i.Assignees); len(logins) > 0 {
		p["assignees"] = logins
	}
	return p
}

// pullPayload is what a charter reads a pull request as.
//
// `headSha` is the join to a git source: a `git:commit:<repo>/<sha>` is addressed by the same
// sha, so "the commit this proposes" is a field read rather than a key anybody minted.
func pullPayload(repo string, p pull) entity.Obj {
	out := entity.Obj{
		"repo": repo, "number": p.Number, "title": p.Title, "state": p.State,
		"draft": p.Draft, "merged": p.Merged,
	}
	if p.User != nil {
		out["author"] = p.User.Login
	}
	if p.Base != nil {
		out["base"] = p.Base.Ref
	}
	if p.Head != nil {
		out["head"] = p.Head.Ref
		out["headSha"] = p.Head.SHA
	}
	for k, v := range map[string]string{
		"url": p.HTMLURL, "createdAt": p.CreatedAt, "updatedAt": p.UpdatedAt,
		"closedAt": p.ClosedAt, "mergedAt": p.MergedAt, "body": text(p.Body),
	} {
		if v != "" {
			out[k] = v
		}
	}
	if names := labelNames(p.Labels); len(names) > 0 {
		out["labels"] = names
	}
	if logins := userLogins(p.Assignees); len(logins) > 0 {
		out["assignees"] = logins
	}
	return out
}

// runPayload is what a charter reads a workflow run as.
//
// MATCH `conclusion`, NOT A TRANSITION, and this is the connector where somebody will expect
// otherwise. A run goes queued → in_progress → completed inside a minute, so a pass will
// frequently collapse all three into one write and a rule matching `status: in_progress` may
// never fire for a run that fired every trigger it was supposed to. That is not a defect to
// fix: verse promises the last observed state rather than every state, and the trigger model
// says the corresponding thing about creates and updates — express "only the first time" as a
// state latch, not an edge.
func runPayload(repo string, r workflowRun) entity.Obj {
	p := entity.Obj{
		"repo": repo, "id": r.ID, "status": r.Status,
		"runNumber": r.RunNumber, "attempt": r.RunAttempt,
	}
	if r.Actor != nil {
		p["actor"] = r.Actor.Login
	}
	for k, v := range map[string]string{
		"workflow": r.Name, "event": r.Event, "conclusion": r.Conclusion,
		"branch": r.HeadBranch,
		// The join to a git source and to the pull request this ran for, both by the same
		// field: a `git:commit:<repo>/<sha>` is addressed by this sha, and so is a PR's head.
		"headSha": r.HeadSHA, "url": r.HTMLURL,
		"startedAt": r.CreatedAt, "updatedAt": r.UpdatedAt,
	} {
		if v != "" {
			p[k] = v
		}
	}
	return p
}

// discussionPayload is what a charter reads a discussion as.
//
// `state` is spelled open/closed like an issue's rather than carrying GitHub's `closed` boolean
// straight through, because it is what the covers are spelled with and what a rule about "an
// open thing" matches — one vocabulary across the three set-shaped kinds rather than two.
//
// A discussion's number space is its OWN: `github:issue:org/repo#1` and
// `github:discussion:org/repo#1` are different things with the same number, which is fine
// because a ref is (system, kind, id) — and worth saying because `#1` alone reads like an
// identifier and is not one.
func discussionPayload(repo string, d discussion) entity.Obj {
	state := "open"
	if d.Closed {
		state = "closed"
	}
	p := entity.Obj{
		"repo": repo, "number": d.Number, "title": d.Title, "state": state,
		"answered": d.IsAnswered, "comments": d.Comments.TotalCount,
	}
	if d.Author != nil {
		p["author"] = d.Author.Login
	}
	if d.Category != nil && d.Category.Name != "" {
		p["category"] = d.Category.Name
	}
	for k, v := range map[string]string{
		"url": d.URL, "createdAt": d.CreatedAt, "updatedAt": d.UpdatedAt,
		"closedAt": d.ClosedAt, "body": text(d.Body),
	} {
		if v != "" {
			p[k] = v
		}
	}
	return p
}

// commentPayload is what a charter reads a comment as.
//
// `on` is the number of the issue or pull request it hangs off, and the join is the ref rather
// than a guess: `github:issue:<repo>#<n>` and `github:pr:<repo>#<n>` are two addresses and
// exactly one of them exists, so a rule that has a comment can read the thing it is about
// without this payload having to say which kind it was.
//
// `kind` is the comment's own nature: a conversation comment — which is what an issue has and
// what a pull request's discussion is, since a PR's conversation IS an issue's — or a review
// comment, which hangs off a line of a diff.
func commentPayload(repo string, c comment, kind string) entity.Obj {
	p := entity.Obj{"repo": repo, "on": c.number(), "id": c.ID, "kind": kind}
	if c.User != nil {
		p["author"] = c.User.Login
	}
	for k, v := range map[string]string{
		"body": text(c.Body), "url": c.HTMLURL,
		"createdAt": c.CreatedAt, "updatedAt": c.UpdatedAt,
	} {
		if v != "" {
			p[k] = v
		}
	}
	return p
}

func labelNames(ls []label) []string {
	out := make([]string, 0, len(ls))
	for _, l := range ls {
		if l.Name != "" {
			out = append(out, l.Name)
		}
	}
	return sorted(out)
}

func userLogins(us []user) []string {
	out := make([]string, 0, len(us))
	for _, u := range us {
		if u.Login != "" {
			out = append(out, u.Login)
		}
	}
	return sorted(out)
}

// text normalises a free-text field from GitHub — a body, a comment — to LF line endings.
//
// NOT COSMETIC. GitHub's web UI submits CRLF, so nearly every comment body carries `\r\n`, and a
// string containing one renders as a tony block scalar whose NEXT SIBLING KEY comes back at
// column zero: the document is then imbalanced and verse refuses the whole reflection as
// undecodable. Every pass fails, the slice freezes at whatever it last held, and the source
// looks alive while reflecting nothing — which is exactly how this was found, against a real
// repository, one comment after it started working.
//
// Filed upstream as item 21 of tony-format#f69agjyeh12ksa25cnn0. The normalisation is worth
// having regardless of the fix: a line ending is a convention of whatever typed the text, not a
// fact about it, and a charter matching a body should not have to know which.
func text(s string) string {
	if !strings.ContainsRune(s, '\r') {
		return s
	}
	return strings.NewReplacer("\r\n", "\n", "\r", "\n").Replace(s)
}

// sorted returns the list in one order, so a re-read of the same item is not a change.
func sorted(in []string) []string {
	if len(in) == 0 {
		return nil
	}
	out := append([]string(nil), in...)
	sort.Strings(out)
	return out
}
