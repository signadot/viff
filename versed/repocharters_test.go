package versed

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/signadot/verse/api"
	"github.com/signadot/verse/entity"
	"github.com/signadot/verse/entity/entitytest"
	"github.com/signadot/verse/mirror"
	"github.com/signadot/verse/system/repocharters"
	"github.com/signadot/verse/task"
)

// main moves the reflected main of r to sha with the charters tree given, as the git io would.
func mainAt(t *testing.T, srv *Server, sha, tree string) {
	t.Helper()
	entitytest.Set(t, srv.Store, entity.RefAt("git", "r", "ref", "main"), entity.Obj{
		"repo": "r", "name": "main", "sha": sha, "type": "commit", "tree": entity.Obj{"charters": tree},
	}.Payload())
}

// liveAt moves the reflected verse/live of r to sha with the charters tree given, as the git io's
// reflection would once it made or moved the branch.
func liveAt(t *testing.T, srv *Server, sha, tree string) {
	t.Helper()
	entitytest.Set(t, srv.Store, entity.RefAt("git", "r", "ref", "verse/live"), entity.Obj{
		"repo": "r", "name": "verse/live", "sha": sha, "type": "commit", "tree": entity.Obj{"charters": tree},
	}.Payload())
}

// installedBy answers an install input as the git io's out half would: the status beside it
// saying which tree was installed, for which commit.
func installedBy(t *testing.T, srv *Server, tree, sha string) {
	t.Helper()
	ioLatency()
	entitytest.Set(t, srv.Store, entity.RefAt("repo-charters", "r", "install", tree+"~status"), entity.Obj{
		"tree": tree, "sha": sha, "address": "repos.r", "charters": 1, "to": "install.repos.r",
		"at": time.Now().UTC().Format(time.RFC3339),
	}.Payload())
}

// checkedBy answers a check input as the git io's out half would: the tree, and the verse's
// verdict on it (io/gitcheck.go).
func checkedBy(t *testing.T, srv *Server, tree, verdict, refusal string) {
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
func refusedBy(t *testing.T, srv *Server, tree, sha, why string) {
	t.Helper()
	ioLatency()
	entitytest.Set(t, srv.Store, entity.RefAt("repo-charters", "r", "install", tree+"~status"), entity.Obj{
		"tree": tree, "sha": sha, "address": "repos.r",
		"error": why, "failed": "permanent", "failedOn": "abcd", "failedAt": time.Now().UTC().Format(time.RFC3339),
	}.Payload())
}

func waitEntity(t *testing.T, srv *Server, ref entity.Ref, want func(entity.Payload) bool) entity.Payload {
	t.Helper()
	deadline := time.After(5 * time.Second)
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
func waitQuestionID(t *testing.T, srv *Server, id string) {
	t.Helper()
	deadline := time.After(5 * time.Second)
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

// tagInputFor is the keyed tag input for a tree: a keyed derive stands under the rule and its
// subject, the install's status, and the key.
func tagInputFor(t *testing.T, srv *Server, tree string) entity.Payload {
	t.Helper()
	ref := entity.RefAt("repo-charters", "r", "out", "git", "installed", "repo-charters", "r", "install", tree+"~status", tree)
	return waitEntity(t, srv, ref, func(p entity.Payload) bool { return entity.Str(p, "to") == "tag.live-verse/"+tree })
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
	srv := NewWithStore((&Harness{Root: dir, Env: task.DefaultShellEnv, Log: io.Discard}).Build, entitytest.Store(t))
	t.Cleanup(srv.stopAllIOs)
	srv.Start(t.Context())
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)

	// The git io and the github io, as registered on a deployment.
	for _, io := range []map[string]any{
		{"name": "git", "root": "git", "url": "http://git-io:9801", "owns": []any{map[string]any{"at": "git.r"}},
			"out": map[string]any{"places": []any{"git.r.out"}, "as": "agent:git-io"}},
		{"name": "github", "root": "github", "url": "http://github-io:9801", "owns": []any{map[string]any{"at": "github.signadot.r"}},
			"out": map[string]any{"places": []any{"github.signadot.r.out"}, "as": "agent:github-io"}},
	} {
		resp := installIO(t, ts, "user:"+tester, io)
		resp.Body.Close()
		if resp.StatusCode != http.StatusCreated {
			t.Fatalf("installing the %s io = %s", io["name"], resp.Status)
		}
	}
	charter, err := repocharters.Render(repocharters.Params{Repo: "r", GitHub: "signadot/r", InstallAt: "repos.r"})
	if err != nil {
		t.Fatal(err)
	}
	installCharter(t, ts, "repo-charters", string(charter))
	// The outboxes the install holds out to the ios are the places the rules derive into: an
	// io reads what it is sent from exactly there, and a declaration that named another place
	// would leave every input unread (the trial found that one).
	for place, wants := range map[string][]string{
		"git.r.out":             {"repo-charters.r.install", "repo-charters.r.check", "repo-charters.r.out.git"},
		"github.signadot.r.out": {"repo-charters.r.out.github"},
	} {
		io := map[string]string{"git.r.out": "agent:git-io", "github.signadot.r.out": "agent:github-io"}[place]
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

	// 1. A tree on main: a candidate and its question; nothing asked of the ios yet, and no pull
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
		if strings.HasPrefix(p.Question.ID, "repo-charters:propose@"+cand.String()) {
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
}
