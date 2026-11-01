package repocharters_test

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/signadot/verse/runner/runnerd/runnerdtest"

	"github.com/signadot/verse/api"
	"github.com/signadot/verse/codec"
	"github.com/signadot/verse/entity"
	"github.com/signadot/verse/entity/entitytest"
	verseio "github.com/signadot/verse/io"
	"github.com/signadot/verse/mirror"
	"github.com/signadot/verse/runner/runnerd/stock"
	"github.com/signadot/verse/schedule"
	"github.com/signadot/verse/system/repocharters"
	"github.com/signadot/verse/versed"
	"github.com/signadot/verse/versed/versedtest"
)

// main moves the reflected main of r to sha with the charters tree given, as the git io would.
func mainAt(t *testing.T, srv *versed.Server, sha, tree string) {
	t.Helper()
	entitytest.Set(t, srv.Store, entity.RefAt("git", "r", "ref", "main"), entity.Obj{
		"repo": "r", "name": "main", "sha": sha, "type": "commit", "tree": entity.Obj{"charters": tree},
	}.Payload())
}

// liveAt moves the reflected verse/live of r to sha with the charters tree given, as the git io's
// reflection would once it made or moved the branch.
func liveAt(t *testing.T, srv *versed.Server, sha, tree string) {
	t.Helper()
	entitytest.Set(t, srv.Store, entity.RefAt("git", "r", "ref", "verse/live"), entity.Obj{
		"repo": "r", "name": "verse/live", "sha": sha, "type": "commit", "tree": entity.Obj{"charters": tree},
	}.Payload())
}

// prAt reflects an open pull request #n of r onto main, its head at sha, as the github io does
// with `-tree .verse/charters`: changing the charters to the tree given, or, given none, leaving
// them as they were where it branched.
func prAt(t *testing.T, srv *versed.Server, n int, sha, tree string) {
	t.Helper()
	pr := entity.Obj{
		"repo": "signadot/r", "number": n, "state": "open", "base": "main", "head": fmt.Sprintf("topic-%d", n), "headSha": sha,
		"updatedAt": time.Now().UTC().Format(time.RFC3339Nano),
	}
	if tree != "" {
		pr["changes"] = entity.Obj{"charters": tree}
	} else {
		pr["changes"] = entity.Removed() // a write merges; the io's reflection drops the field
	}
	entitytest.Set(t, srv.Store, entity.RefAt("github", "signadot", "r", "pr", fmt.Sprint(n)), pr.Payload())
}

// installedBy answers an install input as the git io's out half would: the status beside it
// saying which tree was installed, for which commit.
func installedBy(t *testing.T, srv *versed.Server, tree, sha string) {
	t.Helper()
	ioLatency()
	entitytest.Set(t, srv.Store, entity.RefAt("repo-charters", "r", "install", tree+"~status"), entity.Obj{
		"tree": tree, "sha": sha, "address": "repos.r", "charters": 1, "to": "install.repos.r",
		"at": time.Now().UTC().Format(time.RFC3339),
	}.Payload())
}

// checkedBy answers a check input as the git io's out half would: the tree, and the verse's
// verdict on it (io/gitcheck.go).
func checkedBy(t *testing.T, srv *versed.Server, tree, verdict, refusal string) {
	t.Helper()
	ioLatency()
	st := entity.Obj{"tree": tree, "address": "repos.r", "verdict": verdict, "to": "check.repos.r",
		"checkedAt": time.Now().UTC().Format(time.RFC3339)}
	if refusal != "" {
		st["refusal"] = refusal
	}
	entitytest.Set(t, srv.Store, entity.RefAt("repo-charters", "r", "check", tree+"~status"), st.Payload())
}

// statusUnder is the commit status a github input asks for under a context.
func statusUnder(p entity.Payload, context string) entity.Payload {
	return entity.Get(entity.Get(p, "commitStatus"), context)
}

// ioLatency is the time a real io takes between reading an input and answering it. The rule
// over the install place is woken by the input's own write, and a firing about the status that
// is still in flight when the status changes is not evaluated again for that change (the engine
// defers only a commit that did not change the subject: trigger/edge.go, claimOrDefer); the io's
// answer, seconds after its input on a deployment, never lands inside that window, and a test
// writing it in the same instant would.
func ioLatency() { time.Sleep(150 * time.Millisecond) }

// refusedBy answers an install input as the out half records a refusal for good: the error, and
// the tree, commit and address the io says beside it (io/gitinstall.go), and nothing else.
func refusedBy(t *testing.T, srv *versed.Server, tree, sha, why string) {
	t.Helper()
	ioLatency()
	entitytest.Set(t, srv.Store, entity.RefAt("repo-charters", "r", "install", tree+"~status"), entity.Obj{
		"tree": tree, "sha": sha, "address": "repos.r",
		"error": why, "failed": "permanent", "failedOn": "abcd", "failedAt": time.Now().UTC().Format(time.RFC3339),
	}.Payload())
}

func waitEntity(t *testing.T, srv *versed.Server, ref entity.Ref, want func(entity.Payload) bool) entity.Payload {
	t.Helper()
	deadline := time.After(20 * time.Second) // generous: the full suite runs beside it
	for {
		e, ok, _ := srv.Store.Get(ref)
		if ok && want(e.Payload) {
			return e.Payload
		}
		select {
		case <-deadline:
			var have any
			if ok {
				have = entity.ToObj(e.Payload)
			}
			t.Fatalf("%s never became what was waited for; have %v", ref, have)
		case <-time.After(5 * time.Millisecond):
		}
	}
}

// waitQuestionID waits for the question with id to stand.
func waitQuestionID(t *testing.T, srv *versed.Server, id string) {
	t.Helper()
	deadline := time.After(20 * time.Second) // generous: the full suite runs beside it
	for {
		if _, ok, _ := srv.Store.Get(mirror.AskRef(id)); ok {
			return
		}
		select {
		case <-deadline:
			for _, e := range entity.Below(srv.Store, mirror.IsTask, 1, entity.VerseRoot, mirror.System, mirror.InstanceKind) {
				t.Logf("TASKDUMP %s = %v", e.Ref, entity.ToObj(e.Payload))
			}
			t.Fatalf("question %s never stood", id)
		case <-time.After(5 * time.Millisecond):
		}
	}
}

// tagInputFor is the tag input for a tree, named by the tree in the git outbox, where the io
// reads it: an input under a name ending in ~status (the install's status, the rule's subject)
// is one the sink never reads, so the input is named and not keyed.
func tagInputFor(t *testing.T, srv *versed.Server, tree string) entity.Payload {
	t.Helper()
	ref := entity.RefAt("repo-charters", "r", "out", "git", tree)
	return waitEntity(t, srv, ref, func(p entity.Payload) bool { return entity.Str(p, "to") == "tag.live-verse/"+tree })
}

// A repository whose id is `verse` installs like any other: its package is
// `repo-charters.verse`, and its outboxes are plain names inside it, not `verse.install`, which
// the outbox check reads as verse's own half (staging's first ship refused it so).
func TestTheReconcilerInstallsForARepositoryCalledVerse(t *testing.T) {
	srv := versedtest.Server(t, runnerdtest.Scripted, entitytest.Store(t))
	srv.Start(t.Context())
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)
	for _, io := range []map[string]any{
		{"name": "git", "root": "git", "url": "http://git-iod:9801", "owns": []any{map[string]any{"at": "git.verse"}},
			"out": map[string]any{"places": []any{"git.verse.out"}, "as": "agent:git-iod"}},
		{"name": "github", "root": "github", "url": "http://github-iod:9801", "owns": []any{map[string]any{"at": "github.signadot.verse"}},
			"out": map[string]any{"places": []any{"github.signadot.verse.out"}, "as": "agent:github-iod"}},
	} {
		resp := installIO(t, ts, "user:"+tester, io)
		resp.Body.Close()
		if resp.StatusCode != http.StatusCreated {
			t.Fatalf("installing the %s io = %s", io["name"], resp.Status)
		}
	}
	charter, err := repocharters.Render(repocharters.Params{Repo: "verse", GitHub: "signadot/verse", InstallAt: "repos.verse", Answerers: []string{"user:*"}})
	if err != nil {
		t.Fatal(err)
	}
	installCharter(t, ts, "repo-charters.verse", string(charter))
}

// A reconciler rendered for another branch follows that branch: its tree is a candidate, and so is
// what a pull request onto it proposes, while main and a pull request onto main propose nothing.
func TestTheReconcilerFollowsItsBranch(t *testing.T) {
	srv := versedtest.Server(t, runnerdtest.Scripted, entitytest.Store(t))
	srv.Start(t.Context())
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)
	for _, io := range []map[string]any{
		{"name": "git", "root": "git", "url": "http://git-iod:9801", "owns": []any{map[string]any{"at": "git.r"}},
			"out": map[string]any{"places": []any{"git.r.out"}, "as": "agent:git-iod"}},
		{"name": "github", "root": "github", "url": "http://github-iod:9801", "owns": []any{map[string]any{"at": "github.signadot.r"}},
			"out": map[string]any{"places": []any{"github.signadot.r.out"}, "as": "agent:github-iod"}},
	} {
		resp := installIO(t, ts, "user:"+tester, io)
		resp.Body.Close()
		if resp.StatusCode != http.StatusCreated {
			t.Fatalf("installing the %s io = %s", io["name"], resp.Status)
		}
	}
	charter, err := repocharters.Render(repocharters.Params{Repo: "r", GitHub: "signadot/r", Branch: "master", InstallAt: "repos.r", Answerers: []string{"user:*"}})
	if err != nil {
		t.Fatal(err)
	}
	installCharter(t, ts, "repo-charters.r", string(charter))

	const onMain, onMaster, ontoMain, ontoMaster = "aaaa1111aaaa1111aaaa1111aaaa1111aaaa1111", "bbbb2222bbbb2222bbbb2222bbbb2222bbbb2222",
		"cccc3333cccc3333cccc3333cccc3333cccc3333", "dddd4444dddd4444dddd4444dddd4444dddd4444"
	// What does not concern it is written first, so by the time what does is seen, the rest has been.
	mainAt(t, srv, strings.Repeat("1", 40), onMain)
	prAt(t, srv, 1, strings.Repeat("3", 40), ontoMain)
	entitytest.Set(t, srv.Store, entity.RefAt("git", "r", "ref", "master"), entity.Obj{
		"repo": "r", "name": "master", "sha": strings.Repeat("2", 40), "type": "commit", "tree": entity.Obj{"charters": onMaster},
	}.Payload())
	pr := entity.Obj{
		"repo": "signadot/r", "number": 2, "state": "open", "base": "master", "head": "topic-2", "headSha": strings.Repeat("4", 40),
		"updatedAt": time.Now().UTC().Format(time.RFC3339Nano), "changes": entity.Obj{"charters": ontoMaster},
	}
	entitytest.Set(t, srv.Store, entity.RefAt("github", "signadot", "r", "pr", "2"), pr.Payload())

	waitEntity(t, srv, entity.RefAt("repo-charters", "r", "state", "main"), func(p entity.Payload) bool { return entity.Str(p, "tree") == onMaster })
	waitEntity(t, srv, entity.RefAt("repo-charters", "r", "candidate", ontoMaster), func(p entity.Payload) bool { return entity.Str(p, "tree") == ontoMaster })
	waitEntity(t, srv, entity.RefAt("repo-charters", "r", "pull", "2"), func(p entity.Payload) bool { return entity.Str(p, "tree") == ontoMaster })
	for _, ref := range []entity.Ref{
		entity.RefAt("repo-charters", "r", "candidate", onMain),
		entity.RefAt("repo-charters", "r", "candidate", ontoMain),
		entity.RefAt("repo-charters", "r", "pull", "1"),
	} {
		if _, ok, _ := srv.Store.Get(ref); ok {
			t.Errorf("%s stands: the reconciler for master read main", ref)
		}
	}
}

// The reconciler: main's charters tree is a candidate; an unanswered one is asked about; a yes on
// main's tree makes verse/live hold it, and the tree verse/live holds is handed to the git io to
// install; the io's answer records what runs and tags it. A candidate asked about while
// verse/live stands rides a pull request with verse's check on it, closed as superseded when
// main moves on; a commit to main that leaves the charters alone installs nothing; a rollback to
// a tree answered before moves verse/live with no second ask; a no closes the pull request with
// the check failed; and an install the io refuses is an issue.
func TestTheRepoChartersReconcilerInstallsWhatIsApproved(t *testing.T) {
	dir := t.TempDir()
	// Hosted as `verse up` hosts tasks, so a call list can ask.
	drivers := stock.New(stock.Open(), stock.Secrets{}, dir, io.Discard)
	srv := versedtest.Server(t, drivers.Build, entitytest.Store(t))
	srv.Start(t.Context())
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)

	// The git io and the github io, as registered on a deployment.
	for _, io := range []map[string]any{
		{"name": "git", "root": "git", "url": "http://git-iod:9801", "owns": []any{map[string]any{"at": "git.r"}},
			"out": map[string]any{"places": []any{"git.r.out"}, "as": "agent:git-iod"}},
		{"name": "github", "root": "github", "url": "http://github-iod:9801", "owns": []any{map[string]any{"at": "github.signadot.r"}},
			"out": map[string]any{"places": []any{"github.signadot.r.out"}, "as": "agent:github-iod"}},
	} {
		resp := installIO(t, ts, "user:"+tester, io)
		resp.Body.Close()
		if resp.StatusCode != http.StatusCreated {
			t.Fatalf("installing the %s io = %s", io["name"], resp.Status)
		}
	}
	// Answered by anybody verse knows, as on staging: the rendering adds the git io, whose sweep
	// settles questions nothing proposes any more.
	charter, err := repocharters.Render(repocharters.Params{Repo: "r", GitHub: "signadot/r", InstallAt: "repos.r", Answerers: []string{"user:*"}})
	if err != nil {
		t.Fatal(err)
	}
	installCharter(t, ts, "repo-charters.r", string(charter))
	// The outboxes the install holds out to the iods are the places the rules derive into: an
	// io reads what it is sent from exactly there, and a declaration that named another place
	// would leave every input unread (the trial found that one).
	for place, wants := range map[string][]string{
		"git.r.out":             {"repo-charters.r.install", "repo-charters.r.check", "repo-charters.r.out.git"},
		"github.signadot.r.out": {"repo-charters.r.out.github"},
	} {
		io := map[string]string{"git.r.out": "agent:git-iod", "github.signadot.r.out": "agent:github-iod"}[place]
		req, err := http.NewRequest(http.MethodGet, ts.URL+"/outboxes?to="+place+"&format=tony", nil)
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set(api.PrincipalHeader, io)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		body := readBody(t, resp)
		resp.Body.Close()
		for _, want := range wants {
			if !strings.Contains(body, "from: "+want) {
				t.Fatalf("outboxes to %s = %q, want one from %s", place, body, want)
			}
		}
	}

	const treeA, treeB = "aaaa1111aaaa1111aaaa1111aaaa1111aaaa1111", "bbbb2222bbbb2222bbbb2222bbbb2222bbbb2222"
	sha := func(n int) string { return strings.Repeat(fmt.Sprint(n), 40) }
	gitIn := func(id string) entity.Ref { return entity.RefAt("repo-charters", "r", "out", "git", id) }
	ghIn := func(id string) entity.Ref { return entity.RefAt("repo-charters", "r", "out", "github", id) }
	installIn := func(tree string) entity.Ref { return entity.RefAt("repo-charters", "r", "install", tree) }
	running := entity.RefAt("repo-charters", "r", "running")
	answerSaying := func(qid, choice, say string) {
		t.Helper()
		body := "{choice: " + choice + "}"
		if say != "" {
			body = "{choice: " + choice + ", say: [\"" + say + "\"]}"
		}
		if resp := postAs(t, "user:"+tester, ts.URL+answerPath(qid), "application/tony", body); resp.StatusCode/100 != 2 {
			t.Fatalf("answer %s = %s: %s", choice, resp.Status, readBody(t, resp))
		}
	}
	answer := func(qid, choice string) { answerSaying(qid, choice, "") }

	// 1. A tree on main: a candidate and its question; nothing asked of the iods yet, and no pull
	// request, since verse/live does not stand.
	mainAt(t, srv, sha(1), treeA)
	cand := entity.RefAt("repo-charters", "r", "candidate", treeA)
	waitEntity(t, srv, cand, func(p entity.Payload) bool { return entity.Str(p, "tree") == treeA })
	qid := repocharters.QuestionID("r", treeA)
	waitQuestionID(t, srv, qid)
	if _, ok, _ := srv.Store.Get(installIn(treeA)); ok {
		t.Fatal("an install was asked for before the question was answered")
	}
	// And checked, apart from the question: the git io is asked what an install would do.
	chk := waitEntity(t, srv, entity.RefAt("repo-charters", "r", "check", treeA), func(entity.Payload) bool { return true })
	if entity.Str(chk, "to") != "check.repos.r" || entity.Str(chk, "tree") != treeA {
		t.Fatalf("check input = %v", entity.ToObj(chk))
	}
	checkedBy(t, srv, treeA, "installs", "")
	waitEntity(t, srv, ghIn(treeA), func(p entity.Payload) bool { return entity.Str(statusUnder(p, "verse/check"), "state") == "success" })

	// 2. Yes: verse/live is made at the candidate's commit. Nothing is installed until verse/live
	// holds the tree.
	answer(qid, "proceed")
	waitEntity(t, srv, cand, func(p entity.Payload) bool { return strings.Contains(entity.Str(p, "answer"), "chose proceed") })
	made := waitEntity(t, srv, gitIn("live-made"), func(p entity.Payload) bool { return entity.Str(p, "sha") == sha(1) })
	if entity.Str(made, "to") != "branch.verse/live" || fmt.Sprint(entity.ToObj(made)["create"]) != "true" {
		t.Fatalf("the input making verse/live = %v", entity.ToObj(made))
	}
	if _, ok, _ := srv.Store.Get(installIn(treeA)); ok {
		t.Fatal("an install was asked for before verse/live held the tree")
	}
	// 3. The io made it; the tree verse/live holds is installed, at verse/live's commit.
	liveAt(t, srv, sha(1), treeA)
	in := waitEntity(t, srv, installIn(treeA), func(p entity.Payload) bool { return entity.Str(p, "sha") == sha(1) })
	if entity.Str(in, "to") != "install.repos.r" || entity.Str(in, "tree") != treeA {
		t.Fatalf("install input = %v", entity.ToObj(in))
	}
	installedBy(t, srv, treeA, sha(1))
	waitEntity(t, srv, running, func(p entity.Payload) bool { return entity.Str(p, "tree") == treeA && entity.Str(p, "sha") == sha(1) })
	if tag := tagInputFor(t, srv, treeA); entity.Str(tag, "sha") != sha(1) || fmt.Sprint(entity.ToObj(tag)["create"]) != "true" {
		t.Errorf("tag input = %v", entity.ToObj(tag))
	}
	// The first candidate never had a pull request: what stands under its name is the passed
	// check alone, with no `to`, which no io reads.
	ghA := waitEntity(t, srv, ghIn(treeA), func(p entity.Payload) bool { return statusUnder(p, "verse") != nil })
	if entity.Str(ghA, "to") != "" || entity.Str(statusUnder(ghA, "verse"), "state") != "success" ||
		entity.Str(statusUnder(ghA, "verse/check"), "state") != "success" {
		t.Fatalf("the first tree's github input = %v", entity.ToObj(ghA))
	}

	// 4. Main moves to another tree: a question, and a pull request onto verse/live with the
	// check pending; nothing installed or moved until it is answered.
	mainAt(t, srv, sha(2), treeB)
	qidB := repocharters.QuestionID("r", treeB)
	waitQuestionID(t, srv, qidB)
	branch := waitEntity(t, srv, gitIn(treeB), func(entity.Payload) bool { return true })
	if entity.Str(branch, "to") != "branch.verse/candidate/"+treeB || entity.Str(branch, "sha") != sha(2) {
		t.Errorf("candidate branch input = %v", entity.ToObj(branch))
	}
	pr := waitEntity(t, srv, ghIn(treeB), func(p entity.Payload) bool { return entity.Str(p, "asks") != "" })
	if entity.Str(pr, "to") != "branch.verse/candidate/"+treeB || entity.Str(entity.Get(pr, "open"), "base") != "verse/live" ||
		entity.Str(pr, "asks") != qidB || entity.Str(statusUnder(pr, "verse"), "state") != "pending" || entity.Str(statusUnder(pr, "verse"), "sha") != sha(2) {
		t.Errorf("candidate pull request input = %v", entity.ToObj(pr))
	}
	// Its check stands beside the decision's status on the same pull request.
	checkedBy(t, srv, treeB, "installs", "")
	prB0 := waitEntity(t, srv, ghIn(treeB), func(p entity.Payload) bool { return entity.Str(statusUnder(p, "verse/check"), "state") == "success" })
	if entity.Str(statusUnder(prB0, "verse"), "state") != "pending" {
		t.Errorf("the check overwrote the decision's status: %v", entity.ToObj(prB0))
	}

	// 5. Main goes back to the first tree at a new commit, before B was answered: verse/live
	// already holds that tree, so nothing moves and nothing installs; B's pull request is closed
	// as superseded, its question standing.
	mainAt(t, srv, sha(3), treeA)
	waitEntity(t, srv, ghIn(treeB), func(p entity.Payload) bool { return fmt.Sprint(entity.ToObj(p)["closed"]) == "true" })
	time.Sleep(300 * time.Millisecond)
	if p, ok, _ := srv.Store.Get(gitIn("live")); ok {
		t.Fatalf("verse/live was moved for a tree it already holds: %v", entity.ToObj(p.Payload))
	}
	if p, _, _ := srv.Store.Get(installIn(treeA)); entity.Str(p.Payload, "sha") != sha(1) {
		t.Fatalf("a commit to main reinstalled: %v", entity.ToObj(p.Payload))
	}

	// 6. Main carries B again and its standing question is answered yes: verse/live is moved to
	// the candidate's commit, and once it holds B, B is installed and its pull request's check
	// passes.
	mainAt(t, srv, sha(4), treeB)
	answer(qidB, "proceed")
	moved := waitEntity(t, srv, gitIn("live"), func(p entity.Payload) bool { return entity.Str(p, "sha") == sha(4) })
	if entity.Str(moved, "to") != "branch.verse/live" || entity.Get(moved, "create") != nil {
		t.Fatalf("the input moving verse/live = %v", entity.ToObj(moved))
	}
	liveAt(t, srv, sha(4), treeB)
	waitEntity(t, srv, installIn(treeB), func(p entity.Payload) bool { return entity.Str(p, "sha") == sha(4) })
	installedBy(t, srv, treeB, sha(4))
	waitEntity(t, srv, running, func(p entity.Payload) bool { return entity.Str(p, "tree") == treeB })
	waitEntity(t, srv, ghIn(treeB), func(p entity.Payload) bool { return entity.Str(statusUnder(p, "verse"), "state") == "success" })

	// 7. A rollback: main returns to A, answered before. verse/live is moved back with no second
	// ask, and A is installed again at the new commit.
	mainAt(t, srv, sha(5), treeA)
	waitEntity(t, srv, gitIn("live"), func(p entity.Payload) bool { return entity.Str(p, "sha") == sha(5) })
	liveAt(t, srv, sha(5), treeA)
	waitEntity(t, srv, installIn(treeA), func(p entity.Payload) bool { return entity.Str(p, "sha") == sha(5) })
	installedBy(t, srv, treeA, sha(5))
	waitEntity(t, srv, running, func(p entity.Payload) bool { return entity.Str(p, "sha") == sha(5) })
	for _, p := range srv.Reg.Pending() {
		if strings.HasPrefix(p.Question.ID, "repo-charters.r:propose@"+cand.String()) {
			t.Errorf("the rollback asked again: %v", p.Question.ID)
		}
	}

	// 8. A commit to main that leaves the charters alone moves nothing and installs nothing.
	mainAt(t, srv, sha(6), treeA)
	time.Sleep(300 * time.Millisecond)
	if p, _, _ := srv.Store.Get(installIn(treeA)); entity.Str(p.Payload, "sha") != sha(5) {
		t.Fatalf("an unrelated commit to main reinstalled: %v", entity.ToObj(p.Payload))
	}
	if p, _, _ := srv.Store.Get(gitIn("live")); entity.Str(p.Payload, "sha") != sha(5) {
		t.Fatalf("an unrelated commit to main moved verse/live: %v", entity.ToObj(p.Payload))
	}

	// 9. A tree answered yes that the io refuses to install: an issue says what the verse said,
	// the candidate's check fails, and what runs stays.
	const treeC = "cccc3333cccc3333cccc3333cccc3333cccc3333"
	mainAt(t, srv, sha(7), treeC)
	qidC := repocharters.QuestionID("r", treeC)
	waitQuestionID(t, srv, qidC)
	// The check finds it would be refused, before anybody answers: an issue, and the check's
	// status failed.
	waitEntity(t, srv, entity.RefAt("repo-charters", "r", "check", treeC), func(entity.Payload) bool { return true })
	checkedBy(t, srv, treeC, "refused", "trigger \"x\": a derive must land strictly above the rule")
	early := waitEntity(t, srv, ghIn("check-refused"), func(p entity.Payload) bool { return entity.Str(p, "to") == "issue" })
	if !strings.Contains(entity.Str(entity.Get(early, "open"), "body"), "strictly above") ||
		!strings.Contains(entity.Str(entity.Get(early, "open"), "body"), treeC) || fmt.Sprint(entity.ToObj(early)["closed"]) != "false" {
		t.Errorf("the check's issue input = %v", entity.ToObj(early))
	}
	waitEntity(t, srv, ghIn(treeC), func(p entity.Payload) bool { return entity.Str(statusUnder(p, "verse/check"), "state") == "failure" })
	answer(qidC, "proceed")
	waitEntity(t, srv, gitIn("live"), func(p entity.Payload) bool { return entity.Str(p, "sha") == sha(7) })
	liveAt(t, srv, sha(7), treeC)
	waitEntity(t, srv, installIn(treeC), func(p entity.Payload) bool { return entity.Str(p, "sha") == sha(7) })
	refusedBy(t, srv, treeC, sha(7), "trigger \"x\": a derive must land strictly above the rule")
	issue := waitEntity(t, srv, ghIn("install-failed"), func(p entity.Payload) bool { return entity.Str(p, "to") == "issue" })
	if !strings.Contains(entity.Str(entity.Get(issue, "open"), "body"), treeC) ||
		!strings.Contains(entity.Str(entity.Get(issue, "open"), "body"), "strictly above") || fmt.Sprint(entity.ToObj(issue)["closed"]) != "false" {
		t.Errorf("the install's issue input = %v", entity.ToObj(issue))
	}
	waitEntity(t, srv, ghIn(treeC), func(p entity.Payload) bool { return entity.Str(statusUnder(p, "verse"), "state") == "failure" })
	if p, _, _ := srv.Store.Get(running); entity.Str(p.Payload, "tree") != treeA {
		t.Errorf("running moved on a refused install: %v", entity.ToObj(p.Payload))
	}

	// 10. A tree answered no: its pull request is closed and its check fails, saying so; verse/live
	// is not moved for it.
	const treeD = "dddd4444dddd4444dddd4444dddd4444dddd4444"
	mainAt(t, srv, sha(8), treeD)
	qidD := repocharters.QuestionID("r", treeD)
	waitQuestionID(t, srv, qidD)
	answer(qidD, "deny")
	prD := waitEntity(t, srv, ghIn(treeD), func(p entity.Payload) bool { return entity.Str(statusUnder(p, "verse"), "state") == "failure" })
	if fmt.Sprint(entity.ToObj(prD)["closed"]) != "true" || !strings.Contains(entity.Str(statusUnder(prD, "verse"), "description"), "denied") {
		t.Errorf("the denied candidate's github input = %v", entity.ToObj(prD))
	}
	time.Sleep(200 * time.Millisecond)
	if p, _, _ := srv.Store.Get(gitIn("live")); entity.Str(p.Payload, "sha") == sha(8) {
		t.Fatal("verse/live was moved for a denied tree")
	}

	// 11. Answers read by the option chosen, whatever the words after it say: a yes whose words
	// mention a no is a yes, and a no whose words mention a yes is a no.
	const treeE, treeF = "eeee5555eeee5555eeee5555eeee5555eeee5555", "ffff6666ffff6666ffff6666ffff6666ffff6666"
	mainAt(t, srv, sha(9), treeE)
	qidE := repocharters.QuestionID("r", treeE)
	waitQuestionID(t, srv, qidE)
	// Main passes: E's check finds it installs, and the issue saying main would be refused is
	// closed.
	waitEntity(t, srv, entity.RefAt("repo-charters", "r", "check", treeE), func(entity.Payload) bool { return true })
	checkedBy(t, srv, treeE, "installs", "")
	waitEntity(t, srv, ghIn("check-refused"), func(p entity.Payload) bool { return fmt.Sprint(entity.ToObj(p)["closed"]) == "true" })
	answerSaying(qidE, "proceed", "last time they chose deny")
	waitEntity(t, srv, gitIn("live"), func(p entity.Payload) bool { return entity.Str(p, "sha") == sha(9) })
	// And the install that succeeds closes the refused install's issue.
	liveAt(t, srv, sha(9), treeE)
	waitEntity(t, srv, installIn(treeE), func(p entity.Payload) bool { return entity.Str(p, "sha") == sha(9) })
	installedBy(t, srv, treeE, sha(9))
	waitEntity(t, srv, ghIn("install-failed"), func(p entity.Payload) bool { return fmt.Sprint(entity.ToObj(p)["closed"]) == "true" })
	mainAt(t, srv, strings.Repeat("a", 40), treeF)
	qidF := repocharters.QuestionID("r", treeF)
	waitQuestionID(t, srv, qidF)
	answerSaying(qidF, "deny", "they chose proceed: not this time")
	waitEntity(t, srv, ghIn(treeF), func(p entity.Payload) bool { return fmt.Sprint(entity.ToObj(p)["closed"]) == "true" })
	time.Sleep(300 * time.Millisecond)
	if p, _, _ := srv.Store.Get(gitIn("live")); entity.Str(p.Payload, "sha") == strings.Repeat("a", 40) {
		t.Fatal("a no whose words mention a yes moved verse/live")
	}

	// 12. Main fails again: the same standing issue is asked open again, saying the new tree.
	const treeG = "9999aaaa9999aaaa9999aaaa9999aaaa9999aaaa"
	mainAt(t, srv, strings.Repeat("b", 40), treeG)
	waitEntity(t, srv, entity.RefAt("repo-charters", "r", "check", treeG), func(entity.Payload) bool { return true })
	checkedBy(t, srv, treeG, "refused", "stratum \"x\": two strata at one level")
	waitEntity(t, srv, ghIn("check-refused"), func(p entity.Payload) bool {
		return fmt.Sprint(entity.ToObj(p)["closed"]) == "false" && strings.Contains(entity.Str(entity.Get(p, "open"), "body"), treeG)
	})

	// 13. A pull request to main proposing a tree: the question is asked on it, with verse's
	// request for changes holding the merge and its status waiting on the head, and the check's
	// status beside it. No branch or pull request onto verse/live: main does not carry the tree.
	const treeH = "1111bbbb1111bbbb1111bbbb1111bbbb1111bbbb"
	prIn := func(n int) entity.Ref {
		return entity.RefAt("repo-charters", "r", "out", "github", "pr", fmt.Sprint(n))
	}
	headH := strings.Repeat("c", 40)
	prAt(t, srv, 7, headH, treeH)
	candH := waitEntity(t, srv, entity.RefAt("repo-charters", "r", "candidate", treeH), func(p entity.Payload) bool { return entity.Str(p, "tree") == treeH })
	if entity.Str(candH, "sha") != "" {
		t.Errorf("a tree only a pull request carries has main's commit: %v", entity.ToObj(candH))
	}
	qidH := repocharters.QuestionID("r", treeH)
	waitQuestionID(t, srv, qidH)
	onPR := waitEntity(t, srv, prIn(7), func(p entity.Payload) bool { return entity.Str(p, "asks") != "" })
	if entity.Str(onPR, "to") != "pr.7" || entity.Str(onPR, "asks") != qidH ||
		entity.Str(entity.Get(onPR, "review"), "event") != "request-changes" ||
		entity.Str(statusUnder(onPR, "verse"), "state") != "pending" || entity.Str(statusUnder(onPR, "verse"), "sha") != headH {
		t.Errorf("the pull request to main's input = %v", entity.ToObj(onPR))
	}
	waitEntity(t, srv, entity.RefAt("repo-charters", "r", "check", treeH), func(entity.Payload) bool { return true })
	checkedBy(t, srv, treeH, "installs", "")
	onPR = waitEntity(t, srv, prIn(7), func(p entity.Payload) bool { return entity.Str(statusUnder(p, "verse/check"), "state") == "success" })
	if entity.Str(statusUnder(onPR, "verse/check"), "sha") != headH || entity.Str(statusUnder(onPR, "verse"), "state") != "pending" {
		t.Errorf("the check on the pull request to main = %v", entity.ToObj(onPR))
	}
	// A pull request that leaves the charters alone proposes nothing, now or once main moves on.
	prAt(t, srv, 8, strings.Repeat("d", 40), "")
	// Yes: the hold is dismissed and the status passes; nothing moves until the merge.
	answer(qidH, "proceed")
	// The status is the answer and the hold needs the check too, written apart: wait for both.
	onPR = waitEntity(t, srv, prIn(7), func(p entity.Payload) bool {
		return entity.Str(statusUnder(p, "verse"), "state") == "success" && entity.Str(entity.Get(p, "review"), "event") == "dismiss"
	})
	if entity.Str(onPR, "to") != "pr.7" {
		t.Errorf("the answered pull request to main's input = %v", entity.ToObj(onPR))
	}
	time.Sleep(300 * time.Millisecond)
	for _, ref := range []entity.Ref{gitIn(treeH), ghIn(treeH), installIn(treeH)} {
		if p, ok, _ := srv.Store.Get(ref); ok {
			t.Errorf("%s stands before the merge: %v", ref, entity.ToObj(p.Payload))
		}
	}
	if p, ok, _ := srv.Store.Get(prIn(8)); ok && entity.Str(p.Payload, "to") != "" {
		t.Errorf("a pull request leaving the charters alone is spoken to: %v", entity.ToObj(p.Payload))
	}
	if p, _, _ := srv.Store.Get(gitIn("live")); entity.Str(p.Payload, "sha") == headH {
		t.Fatal("verse/live was moved to a pull request's head")
	}
	// The merge: main carries the answered tree, and it installs with no second question.
	mainAt(t, srv, sha(10), treeH)
	waitEntity(t, srv, gitIn("live"), func(p entity.Payload) bool { return entity.Str(p, "sha") == sha(10) })
	liveAt(t, srv, sha(10), treeH)
	waitEntity(t, srv, installIn(treeH), func(p entity.Payload) bool { return entity.Str(p, "sha") == sha(10) })
	installedBy(t, srv, treeH, sha(10))
	waitEntity(t, srv, running, func(p entity.Payload) bool { return entity.Str(p, "tree") == treeH })
	if p, ok, _ := srv.Store.Get(gitIn(treeH)); ok {
		t.Errorf("a tree answered on its pull request to main was branched for verse/live: %v", entity.ToObj(p.Payload))
	}
	if p, ok, _ := srv.Store.Get(prIn(8)); ok && entity.Str(p.Payload, "to") != "" {
		t.Errorf("after the merge, a pull request leaving the charters alone is spoken to: %v", entity.ToObj(p.Payload))
	}

	// 14. A pull request to main whose tree would be refused and is answered no: the refusal is
	// in its comment, the check fails, and the hold stands, saying the answer.
	const treeI = "2222cccc2222cccc2222cccc2222cccc2222cccc"
	headI := strings.Repeat("e", 40)
	prAt(t, srv, 9, headI, treeI)
	qidI := repocharters.QuestionID("r", treeI)
	waitQuestionID(t, srv, qidI)
	waitEntity(t, srv, entity.RefAt("repo-charters", "r", "check", treeI), func(entity.Payload) bool { return true })
	checkedBy(t, srv, treeI, "refused", "stratum \"y\": a pattern overlaps")
	onPR = waitEntity(t, srv, prIn(9), func(p entity.Payload) bool { return entity.Str(statusUnder(p, "verse/check"), "state") == "failure" })
	if entity.Str(statusUnder(onPR, "verse/check"), "sha") != headI || !strings.Contains(entity.Str(onPR, "text"), "a pattern overlaps") {
		t.Errorf("the refused check on the pull request to main = %v", entity.ToObj(onPR))
	}
	answer(qidI, "deny")
	onPR = waitEntity(t, srv, prIn(9), func(p entity.Payload) bool { return entity.Str(statusUnder(p, "verse"), "state") == "failure" })
	if entity.Str(entity.Get(onPR, "review"), "event") != "request-changes" ||
		!strings.Contains(entity.Str(entity.Get(onPR, "review"), "body"), "answered no") {
		t.Errorf("the denied pull request to main's input = %v", entity.ToObj(onPR))
	}

	// 15. A pull request that moves to another tree is asked about that one, in the same input;
	// an answer to the tree it left does not reach it; and once it leaves the charters alone, the
	// hold is dismissed and the comment says so.
	const treeJ, treeK = "3333dddd3333dddd3333dddd3333dddd3333dddd", "4444eeee4444eeee4444eeee4444eeee4444eeee"
	prAt(t, srv, 11, strings.Repeat("1", 40), treeJ)
	qidJ, qidK := repocharters.QuestionID("r", treeJ), repocharters.QuestionID("r", treeK)
	waitQuestionID(t, srv, qidJ)
	waitEntity(t, srv, prIn(11), func(p entity.Payload) bool { return entity.Str(p, "asks") == qidJ })
	headK := strings.Repeat("2", 40)
	prAt(t, srv, 11, headK, treeK)
	waitQuestionID(t, srv, qidK)
	onPR = waitEntity(t, srv, prIn(11), func(p entity.Payload) bool { return entity.Str(p, "asks") == qidK })
	if entity.Str(onPR, "tree") != treeK || entity.Str(entity.Get(onPR, "review"), "event") != "request-changes" ||
		entity.Str(statusUnder(onPR, "verse"), "sha") != headK || entity.Str(statusUnder(onPR, "verse"), "state") != "pending" {
		t.Errorf("the pull request moved to another tree = %v", entity.ToObj(onPR))
	}
	answer(qidJ, "proceed")
	waitEntity(t, srv, entity.RefAt("repo-charters", "r", "verdict", treeJ), func(p entity.Payload) bool {
		return entity.Str(entity.Get(p, "decision"), "state") == "success"
	})
	time.Sleep(300 * time.Millisecond)
	if p, _, _ := srv.Store.Get(prIn(11)); entity.Str(entity.Get(p.Payload, "review"), "event") != "request-changes" ||
		entity.Str(statusUnder(p.Payload, "verse"), "state") != "pending" {
		t.Errorf("an answer to a tree the pull request left lifted its hold: %v", entity.ToObj(p.Payload))
	}
	prAt(t, srv, 11, strings.Repeat("3", 40), "")
	onPR = waitEntity(t, srv, prIn(11), func(p entity.Payload) bool { return entity.Str(entity.Get(p, "review"), "event") == "dismiss" })
	if entity.Str(onPR, "to") != "pr.11" || !strings.Contains(entity.Str(onPR, "text"), "no longer changes the charters") ||
		len(entity.ToObj(onPR)["asks"].([]any)) != 0 {
		t.Errorf("the pull request that left the charters alone = %v", entity.ToObj(onPR))
	}

	// 16. Removing the charters directory: a pull request that removes it proposes the empty
	// tree, and the question says what that means. Answered yes and merged, verse/live moves to
	// it and it is installed: every charter at the address taken down.
	prAt(t, srv, 12, strings.Repeat("4", 40), verseio.EmptyTree)
	qidE0 := repocharters.QuestionID("r", verseio.EmptyTree)
	waitQuestionID(t, srv, qidE0)
	if q, _, _ := srv.Store.Get(mirror.AskRef(qidE0)); !strings.Contains(fmt.Sprint(entity.ToObj(q.Payload)), "Remove every charter at repos.r?") {
		t.Errorf("the empty tree's question does not say it removes every charter: %v", entity.ToObj(q.Payload))
	}
	waitEntity(t, srv, prIn(12), func(p entity.Payload) bool { return entity.Str(p, "asks") == qidE0 })
	// The hold lifts only when the answer and the check agree: a yes with the check still out
	// keeps it, a yes the check refuses keeps it, and a yes the check passes lifts it.
	answer(qidE0, "proceed")
	held := func(p entity.Payload, says string) bool {
		r := entity.Get(p, "review")
		return entity.Str(r, "event") == "request-changes" && strings.Contains(entity.Str(r, "body"), says)
	}
	waitEntity(t, srv, prIn(12), func(p entity.Payload) bool { return held(p, "waiting for verse's charter check") })
	checkedBy(t, srv, verseio.EmptyTree, "refused", "stratum \"z\": a level is taken")
	waitEntity(t, srv, prIn(12), func(p entity.Payload) bool { return held(p, "refuses these charters") })
	checkedBy(t, srv, verseio.EmptyTree, "installs", "")
	waitEntity(t, srv, prIn(12), func(p entity.Payload) bool { return entity.Str(entity.Get(p, "review"), "event") == "dismiss" })
	mainAt(t, srv, sha(11), verseio.EmptyTree)
	waitEntity(t, srv, gitIn("live"), func(p entity.Payload) bool { return entity.Str(p, "sha") == sha(11) })
	liveAt(t, srv, sha(11), verseio.EmptyTree)
	gone := waitEntity(t, srv, installIn(verseio.EmptyTree), func(p entity.Payload) bool { return entity.Str(p, "sha") == sha(11) })
	if entity.Str(gone, "to") != "install.repos.r" {
		t.Errorf("the empty tree's install input = %v", entity.ToObj(gone))
	}
	installedBy(t, srv, verseio.EmptyTree, sha(11))
	waitEntity(t, srv, running, func(p entity.Payload) bool { return entity.Str(p, "tree") == verseio.EmptyTree })

	// 17. Every minute verse/live is checked against what is installed. In step, nothing is said;
	// differing two rounds in a row at one commit, an issue says so; back in step, it closes; and
	// a verse/live that is gone says that.
	tickAt := func(at time.Time) string {
		t.Helper()
		due := schedule.Key(at)
		body, _ := codec.Marshal(api.ScheduleTick{Due: due}, codec.Tony)
		resp := postAs(t, "user:"+tester, ts.URL+"/schedules/repo-charters.r:minute/tick", "application/tony", string(body))
		if resp.StatusCode != http.StatusCreated {
			t.Fatalf("tick: %s: %s", resp.Status, readBody(t, resp))
		}
		resp.Body.Close()
		return due
	}
	liveIn := entity.RefAt("repo-charters", "r", "livecheck", "live")
	liveStatus := func(st entity.Obj) {
		t.Helper()
		ioLatency()
		entitytest.Set(t, srv.Store, entity.RefAt("repo-charters", "r", "livecheck", "live~status"), st.Payload())
	}
	// A day back: the schedule also ticks on its own every minute while the test runs, and a
	// tick of its own at the same instant would be read in place of this one.
	due := tickAt(time.Now().UTC().Truncate(time.Minute).Add(-24 * time.Hour))
	lc := waitEntity(t, srv, liveIn, func(p entity.Payload) bool { return entity.Str(p, "round") == due })
	if entity.Str(lc, "to") != "check.repos.r" || entity.Str(lc, "ref") != "verse/live" || entity.Str(lc, "of") != "charters" {
		t.Fatalf("the check of verse/live = %v", entity.ToObj(lc))
	}
	sweep := waitEntity(t, srv, gitIn("obsolete"), func(p entity.Payload) bool { return entity.Str(p, "before") == due })
	if entity.Str(sweep, "to") != "obsolete" || entity.Str(sweep, "prefix") != "repo-charters.r:propose@repo-charters.r.candidate." ||
		!strings.Contains(fmt.Sprint(entity.ToObj(sweep)["keep"]), verseio.EmptyTree) || !strings.Contains(fmt.Sprint(entity.ToObj(sweep)["keep"]), treeI) {
		t.Fatalf("the obsolete sweep = %v", entity.ToObj(sweep))
	}
	liveStatus(entity.Obj{"ref": "verse/live", "round": due, "verdict": "installs", "changes": 3, "differingFor": 1, "sha": sha(11), "tree": verseio.EmptyTree})
	time.Sleep(300 * time.Millisecond)
	if p, ok, _ := srv.Store.Get(ghIn("live-drift")); ok && entity.Str(p.Payload, "to") != "" {
		t.Fatalf("one round differing opened an issue: %v", entity.ToObj(p.Payload))
	}
	liveStatus(entity.Obj{"ref": "verse/live", "round": due, "verdict": "installs", "changes": 3, "differingFor": 2, "sha": sha(11), "tree": verseio.EmptyTree})
	drift := waitEntity(t, srv, ghIn("live-drift"), func(p entity.Payload) bool { return entity.Str(p, "to") == "issue" })
	if fmt.Sprint(entity.ToObj(drift)["closed"]) != "false" || !strings.Contains(entity.Str(entity.Get(drift, "open"), "body"), "by 3 charter(s)") {
		t.Errorf("the drift issue = %v", entity.ToObj(drift))
	}
	liveStatus(entity.Obj{"ref": "verse/live", "round": due, "verdict": "installs", "changes": 0, "differingFor": 0, "sha": sha(11), "tree": verseio.EmptyTree})
	waitEntity(t, srv, ghIn("live-drift"), func(p entity.Payload) bool { return fmt.Sprint(entity.ToObj(p)["closed"]) == "true" })
	liveStatus(entity.Obj{"ref": "verse/live", "round": due, "verdict": "missing"})
	waitEntity(t, srv, ghIn("live-drift"), func(p entity.Payload) bool {
		return fmt.Sprint(entity.ToObj(p)["closed"]) == "false" && strings.Contains(entity.Str(entity.Get(p, "open"), "body"), "gone")
	})
	liveStatus(entity.Obj{"ref": "verse/live", "round": due, "verdict": "installs", "changes": 0, "differingFor": 0, "sha": sha(11), "tree": verseio.EmptyTree})
	waitEntity(t, srv, ghIn("live-drift"), func(p entity.Payload) bool { return fmt.Sprint(entity.ToObj(p)["closed"]) == "true" })

	// 18. Main's charters answered no: an issue says so while main holds them, and closes when main
	// moves on.
	const treeM = "5555ffff5555ffff5555ffff5555ffff5555ffff"
	mainAt(t, srv, sha(12), treeM)
	qidM := repocharters.QuestionID("r", treeM)
	waitQuestionID(t, srv, qidM)
	answer(qidM, "deny")
	denied := waitEntity(t, srv, ghIn("main-denied"), func(p entity.Payload) bool {
		return strings.Contains(entity.Str(entity.Get(p, "open"), "body"), treeM)
	})
	if entity.Str(denied, "to") != "issue" || fmt.Sprint(entity.ToObj(denied)["closed"]) != "false" {
		t.Errorf("the denied main's issue = %v", entity.ToObj(denied))
	}
	mainAt(t, srv, sha(13), verseio.EmptyTree)
	waitEntity(t, srv, ghIn("main-denied"), func(p entity.Payload) bool { return fmt.Sprint(entity.ToObj(p)["closed"]) == "true" })

	// 19. A question settled obsolete, by the git io, once nothing proposed its tree, is asked again
	// when a pull request proposes the tree again.
	const treeL = "6666aaaa6666aaaa6666aaaa6666aaaa6666aaaa"
	prAt(t, srv, 13, strings.Repeat("7", 40), treeL)
	qidL := repocharters.QuestionID("r", treeL)
	waitQuestionID(t, srv, qidL)
	prAt(t, srv, 13, strings.Repeat("8", 40), "")
	time.Sleep(200 * time.Millisecond)
	if resp := postAs(t, "agent:git-iod", ts.URL+answerPath(qidL), "application/tony", "{choice: obsolete}"); resp.StatusCode/100 != 2 {
		t.Fatalf("the git io's obsolete answer = %s: %s", resp.Status, readBody(t, resp))
	}
	candL := entity.RefAt("repo-charters", "r", "candidate", treeL)
	waitEntity(t, srv, candL, func(p entity.Payload) bool { return strings.Contains(entity.Str(p, "answer"), "chose obsolete") })
	prAt(t, srv, 13, strings.Repeat("9", 40), treeL)
	waitEntity(t, srv, mirror.AskRef(qidL), func(p entity.Payload) bool { return entity.Str(p, "status") == "pending" })
	answer(qidL, "proceed")
	waitEntity(t, srv, candL, func(p entity.Payload) bool { return strings.Contains(entity.Str(p, "answer"), "chose proceed") })
}
