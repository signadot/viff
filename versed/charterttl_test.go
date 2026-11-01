package versed

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/signadot/verse/runner/runnerd/runnerdtest"

	"github.com/signadot/verse/entity/entitytest"
)

// ttlPackage is a package that stands for ttl (none, for ""): two levels and a rule, so its
// going takes both kinds.
func ttlPackage(at, ttl string) string {
	doc := "at: " + at + "\n"
	if ttl != "" {
		doc += "ttl: " + ttl + "\n"
	}
	return doc + `strata:
- {name: low, level: 0, match: {path: ` + at + `.low}}
- {name: high, level: 1, match: {path: ` + at + `.high}}
rules:
- name: lift
  stratum: low
  condition: {subject: {path: "` + at + `.low.*"}}
  action:
    derive:
    - {path: ` + at + `.high, id: seen, value: {seen: true}}
`
}

// ttlVerse is a started verse on a clock the test moves.
func ttlVerse(t *testing.T) (*Server, *httptest.Server, *fakeClock) {
	t.Helper()
	srv := testServer(t)
	clock := clockOf(t, srv)
	srv.Start(t.Context())
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)
	return srv, ts, clock
}

// installTTL posts a charter file and answers what the install said.
func installTTL(t *testing.T, ts *httptest.Server, file string) string {
	t.Helper()
	code, body := postCharterFile(t, ts.URL, file)
	if code != http.StatusCreated {
		t.Fatalf("the install = %d %s", code, body)
	}
	return body
}

// charterGone waits for the package at an address to be removed, the clock having passed its
// ttl. Generous, as the asks' is: the timer is armed by the watch that sees the expiry written.
func charterGone(t *testing.T, srv *Server, at string) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for srv.packagesSeenAt(at) {
		if time.Now().After(deadline) {
			t.Fatalf("the charter at %s still stands after its ttl", at)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// stillStands checks, a while after the clock moved, that the package at an address stands.
func stillStands(t *testing.T, srv *Server, at, why string) {
	t.Helper()
	time.Sleep(200 * time.Millisecond)
	if !srv.packagesSeenAt(at) {
		t.Fatalf("the charter at %s went %s", at, why)
	}
}

// expiryArmed waits for this process to hold a timer for the package at an address: the watch
// has seen its expiry.
func expiryArmed(t *testing.T, srv *Server, at string) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		srv.charterExpiries.mu.Lock()
		armed := srv.charterExpiries.timers[at] != nil
		srv.charterExpiries.mu.Unlock()
		if armed {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("no expiry was armed for the charter at %s", at)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// A CHARTER GOES WHEN ITS TTL IS OVER, as `verse charter -r` takes it: its rule and its level, and
// only the package at its address. One under it is a package of its own.
func TestACharterGoesWhenItsTTLIsOver(t *testing.T) {
	srv, ts, clock := ttlVerse(t)
	said := installTTL(t, ts, ttlPackage("repos.r.hello", "2s"))
	want := clock.Now().Add(2 * time.Second).UTC().Format(time.RFC3339)
	if !strings.Contains(said, "expires: "+want) && !strings.Contains(said, `expires: "`+want+`"`) {
		t.Errorf("the install said %q, want it to say it expires at %s", said, want)
	}
	installTTL(t, ts, "at: repos.r.hello.sub\n")

	resp := getAs(t, "user:"+tester, ts.URL+"/charter?at=repos.r.hello&depth=0&format=tony")
	read := readBody(t, resp)
	if !strings.Contains(read, "ttl: 2s") || !strings.Contains(read, want) {
		t.Errorf("the charter reads %q, want its ttl and when it expires", read)
	}
	expiryArmed(t, srv, "repos.r.hello")

	clock.Advance(time.Second)
	stillStands(t, srv, "repos.r.hello", "before its ttl was over")
	clock.Advance(time.Second)
	charterGone(t, srv, "repos.r.hello")
	if _, ok := srv.standingPackage(mustAt(t, "repos.r.hello")); ok {
		t.Error("its engine outlived it")
	}
	if !srv.packagesSeenAt("repos.r.hello.sub") {
		t.Error("the charter under it went with it")
	}
}

// INSTALLED AGAIN, A CHARTER STANDS ITS TTL FROM THEN, and installed with none it stands until
// it is removed: the ttl is the declaration's, replaced whole as `reads:` is.
func TestACharterInstalledAgainStandsItsTTLFromThen(t *testing.T) {
	srv, ts, clock := ttlVerse(t)
	installTTL(t, ts, ttlPackage("repos.r.hello", "2s"))
	expiryArmed(t, srv, "repos.r.hello")
	clock.Advance(time.Second)
	installTTL(t, ts, ttlPackage("repos.r.hello", "2s"))
	clock.Advance(time.Second)
	stillStands(t, srv, "repos.r.hello", "at its first ttl, though installed again")
	clock.Advance(time.Second)
	charterGone(t, srv, "repos.r.hello")

	installTTL(t, ts, ttlPackage("repos.r.bye", "2s"))
	expiryArmed(t, srv, "repos.r.bye")
	installTTL(t, ts, ttlPackage("repos.r.bye", ""))
	resp := getAs(t, "user:"+tester, ts.URL+"/charter?at=repos.r.bye&depth=0&format=tony")
	if read := readBody(t, resp); strings.Contains(read, "ttl") || strings.Contains(read, "expires") {
		t.Errorf("installed with no ttl, the charter reads %q", read)
	}
	clock.Advance(3 * time.Second)
	stillStands(t, srv, "repos.r.bye", "though installed again with no ttl")
}

// A TTL IS A PACKAGE'S, AND A DURATION: baseline is not removed whole, and a ttl that is not a
// span of time is refused, by the install and by the check alike.
func TestACharterTTLIsAPackagesDuration(t *testing.T) {
	_, ts, _ := ttlVerse(t)
	for file, want := range map[string]string{
		"ttl: 2s\nrules: []\n":                  "ttl: a charter with no address",
		ttlPackage("repos.r.hello", "soon"):     "a charter's `ttl:` is how long it stands",
		ttlPackage("repos.r.hello", "0s"):       "a charter's `ttl:` is how long it stands",
		ttlPackage("repos.r.hello", "[1h, 2h]"): "a charter's `ttl:` is how long it stands",
	} {
		if code, body := postCharterFile(t, ts.URL, file); code != http.StatusBadRequest || !strings.Contains(body, want) {
			t.Errorf("%q installed = %d %s, want it refused saying %q", file, code, body, want)
		}
	}
	if got := checkAt(t, ts, "repos.r", ttlPackage("repos.r.hello", "soon")); got.Verdict != "refused" ||
		!strings.Contains(got.Refusal, "`ttl:`") {
		t.Errorf("the check of a ttl that is no duration = %+v", got)
	}
	if got := checkAt(t, ts, "repos.r", ttlPackage("repos.r.hello", "2s")); got.Verdict != "installs" {
		t.Errorf("the check of a charter with a ttl = %+v", got)
	}

	// In a scope a charter has no ttl: it goes with its scope. The install refuses it, and the
	// check says so rather than that it installs.
	resp, err := httpPost(ts.URL+"/charter?scope=sandbox", "application/tony", strings.NewReader(ttlPackage("repos.r.hello", "2s")))
	if err != nil {
		t.Fatal(err)
	}
	if body := readBody(t, resp); resp.StatusCode != http.StatusBadRequest || !strings.Contains(body, "a scope's charter has none") {
		t.Errorf("a ttl installed in a scope = %s %s, want it refused", resp.Status, body)
	}
	if got := checkAt(t, ts, "repos.r&scope=sandbox", ttlPackage("repos.r.hello", "2s")); got.Verdict != "refused" ||
		!strings.Contains(got.Refusal, "a scope's charter has none") {
		t.Errorf("the check of a ttl in a scope = %+v, want it refused as the install is", got)
	}
}

// A CHARTER'S TTL OUTLIVES THE PROCESS THAT ARMED IT: the declaration holds the instant, and the
// next start arms it again.
func TestACharterTTLHoldsAcrossARestart(t *testing.T) {
	addr := entitytest.Addr(t)
	first := serverWith(t, runnerdtest.Scripted, entitytest.StoreAt(t, addr))
	clock := clockOf(t, first)
	ctx, stop := context.WithCancel(t.Context())
	first.Start(ctx)
	ts := httptest.NewServer(first.Handler())
	installTTL(t, ts, ttlPackage("repos.r.hello", "3s"))
	expiryArmed(t, first, "repos.r.hello")
	first.charterExpiries.drop("repos.r.hello") // the timer goes with the process
	ts.Close()
	stop()

	second := serverWith(t, runnerdtest.Scripted, entitytest.StoreAt(t, addr))
	second.clock = clock
	second.Start(t.Context())
	expiryArmed(t, second, "repos.r.hello")
	clock.Advance(3 * time.Second)
	charterGone(t, second, "repos.r.hello")
}
