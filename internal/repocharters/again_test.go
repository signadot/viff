package repocharters_test

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"go.viff.dev/viff/internal/repocharters"
	"go.viff.dev/viff/internal/viffd/viffdtest"
	"go.viff.dev/viff/pkg/api"
	"go.viff.dev/viff/pkg/codec"
	"go.viff.dev/viff/pkg/entity"
	"go.viff.dev/viff/pkg/entity/entitytest"
	"go.viff.dev/viff/pkg/runner/runnerd/stock"
	"go.viff.dev/viff/pkg/schedule"
)

// AN INSTALL REFUSED BY THE VIFF IS ASKED AGAIN once the viff has moved: each time the minute's
// check of an entry of viff/live finds its tree would install, where it did not before, the
// install of that tree is asked again, once. Not every minute: a round that finds it so again
// asks nothing. And per entry: an entry found to install, as the one before it was, is asked
// again too.
func TestARefusedInstallIsAskedAgainWhenTheCheckFindsItInstalls(t *testing.T) {
	drivers := stock.New(stock.Open(), stock.Secrets{}, t.TempDir(), io.Discard)
	srv := viffdtest.Server(t, drivers.Build, entitytest.Store(t))
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
	charter, err := repocharters.Render(repocharters.Params{Repo: "r", GitHub: "signadot/r", InstallAt: "repos.r", Answerers: []string{"user:*"}})
	if err != nil {
		t.Fatal(err)
	}
	installCharter(t, ts, "repo-charters.r", string(charter))

	const tree, main, entry = "aaaa1111aaaa1111aaaa1111aaaa1111aaaa1111", "1111111111111111111111111111111111111111", "e111111111111111111111111111111111111111"
	installIn := entity.RefAt("repo-charters", "r", "install", tree)
	mainAt(t, srv, main, tree)
	qid := repocharters.QuestionID("r", tree)
	waitQuestionID(t, srv, qid)
	if resp := postAs(t, "user:"+tester, ts.URL+answerPath(qid), "application/tony", "{choice: proceed}"); resp.StatusCode/100 != 2 {
		t.Fatalf("answer = %s: %s", resp.Status, readBody(t, resp))
	}
	liveAt(t, srv, entry, tree, main)
	waitEntity(t, srv, installIn, func(p entity.Payload) bool { return entity.Str(p, "sha") == entry })
	refusedBy(t, srv, tree, entry, main, "trigger \"work\": spawn: no runner offers it")

	tick := func(at time.Time) string {
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
	// checked answers the round's check of viff/live as the git io would, at an entry, saying
	// since which round the verdict has held there (pkg/io/gitcheck.go).
	checked := func(due, verdict, since, entry, tree string) {
		t.Helper()
		waitEntity(t, srv, entity.RefAt("repo-charters", "r", "livecheck", "live"), func(p entity.Payload) bool { return entity.Str(p, "round") == due })
		st := entity.Obj{"record": "live", "round": due, "verdict": verdict, "verdictSince": since, "sha": entry, "tree": tree, "to": "check.repos.r"}
		if verdict == "installs" {
			st["changes"], st["differingFor"] = 0, 0
		} else {
			st["refusal"] = "trigger \"work\": spawn: no runner offers it"
		}
		ioLatency()
		entitytest.Set(t, srv.Store, entity.RefAt("repo-charters", "r", "livecheck", "live~status"), st.Payload())
	}
	again := func() string {
		p, _, _ := srv.Store.Get(installIn)
		return entity.Str(p.Payload, "again")
	}
	// Days back, a minute apart: the schedule ticks on its own while the test runs, and a tick of
	// its own at the same instant would be read in place of one of these.
	base := time.Now().UTC().Truncate(time.Minute).Add(-48 * time.Hour)
	round := func(n int) time.Time { return base.Add(time.Duration(n) * time.Minute) }

	// The check refuses as the install did: nothing asked again.
	r1 := tick(round(1))
	checked(r1, "refused", r1, entry, tree)
	time.Sleep(300 * time.Millisecond)
	if a := again(); a != "" {
		t.Fatalf("a check that refuses asked the install again: %q", a)
	}
	// The viff moved (a runner came up): the check finds it installs, and the install is asked
	// again, carrying the round.
	r2 := tick(round(2))
	checked(r2, "installs", r2, entry, tree)
	in := waitEntity(t, srv, installIn, func(p entity.Payload) bool { return entity.Str(p, "again") == r2 })
	if entity.Str(in, "to") != "install.repos.r" || entity.Str(in, "tree") != tree || entity.Str(in, "sha") != entry || entity.Str(in, "from") != main {
		t.Fatalf("the install asked again = %v", entity.ToObj(in))
	}
	// Still installs a round later: not asked again.
	r3 := tick(round(3))
	checked(r3, "installs", r2, entry, tree)
	time.Sleep(300 * time.Millisecond)
	if a := again(); a != r2 {
		t.Fatalf("a round that found it installs again asked again: %q", a)
	}
	// Refused, then installs: asked again, once more.
	r4 := tick(round(4))
	checked(r4, "refused", r4, entry, tree)
	r5 := tick(round(5))
	checked(r5, "installs", r5, entry, tree)
	waitEntity(t, srv, installIn, func(p entity.Payload) bool { return entity.Str(p, "again") == r5 })
	// And the install that then succeeds is what runs, closing the refused install's issue.
	installedBy(t, srv, tree, entry, main)
	waitEntity(t, srv, entity.RefAt("repo-charters", "r", "running"), func(p entity.Payload) bool { return entity.Str(p, "sha") == entry })
	waitEntity(t, srv, entity.RefAt("repo-charters", "r", "out", "github", "install-failed"), func(p entity.Payload) bool {
		return fmt.Sprint(entity.ToObj(p)["closed"]) == "true"
	})

	// A NEW ENTRY, found to install as the one before it was. What runs is checked and installs;
	// a yes appends another tree, whose install is refused; the viff moves; and the check of the
	// new entry finds it installs. The verdict never stopped being `installs`, and the install is
	// asked again all the same, since the verdict begins again at the new entry.
	r6 := tick(round(6))
	checked(r6, "installs", r5, entry, tree)
	const tree2, main2, entry2 = "bbbb2222bbbb2222bbbb2222bbbb2222bbbb2222", "2222222222222222222222222222222222222222", "e222222222222222222222222222222222222222"
	installIn2 := entity.RefAt("repo-charters", "r", "install", tree2)
	mainAt(t, srv, main2, tree2)
	qid2 := repocharters.QuestionID("r", tree2)
	waitQuestionID(t, srv, qid2)
	if resp := postAs(t, "user:"+tester, ts.URL+answerPath(qid2), "application/tony", "{choice: proceed}"); resp.StatusCode/100 != 2 {
		t.Fatalf("answer = %s: %s", resp.Status, readBody(t, resp))
	}
	liveAt(t, srv, entry2, tree2, main2)
	waitEntity(t, srv, installIn2, func(p entity.Payload) bool { return entity.Str(p, "sha") == entry2 })
	refusedBy(t, srv, tree2, entry2, main2, "trigger \"work\": spawn: no runner offers it")
	r7 := tick(round(7))
	checked(r7, "installs", r7, entry2, tree2)
	waitEntity(t, srv, installIn2, func(p entity.Payload) bool { return entity.Str(p, "again") == r7 })
}
