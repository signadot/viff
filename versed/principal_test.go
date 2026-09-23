package versed

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/signadot/verse/api"
	"github.com/signadot/verse/codec"
	"github.com/signadot/verse/entity"
	"github.com/signadot/verse/trigger"
)

// postAs is postTony with the request made by somebody: the principal header the CLI sends
// from its config's `user:`.
func postAs(t *testing.T, as, url, contentType, body string) *http.Response {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, url, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", contentType)
	if as != "" {
		req.Header.Set(api.PrincipalHeader, as)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("post %s: %v", url, err)
	}
	return resp
}

func owner(t *testing.T, srv *Server, ref entity.Ref) string {
	t.Helper()
	rec, ok, err := srv.Store.Get(entity.PermsRef(ref))
	if err != nil {
		t.Fatalf("reading the owner of %s: %v", ref, err)
	}
	if !ok {
		return ""
	}
	return entity.Str(rec.Payload, entity.OwnerField)
}

// whose is the owner of a node: the nearest owner record at or above it, which is what a node
// with no record of its own is its maker's or the system's BY.
func whose(t *testing.T, srv *Server, ref entity.Ref) string {
	t.Helper()
	segs := ref.Segs()
	for i := len(segs); i >= 0; i-- {
		if who := owner(t, srv, entity.RefAt(segs[:i]...)); who != "" {
			return who
		}
	}
	return ""
}

// A request names who makes it, and everything it writes is theirs: a raw put and a charter
// install alike leave the requester as the owner of what they created, and the requester
// on the verse's list of users. A request that names nobody leaves no owner and no user.
func TestARequestsPrincipalOwnsWhatItCreates(t *testing.T) {
	srv := testServer(t)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	put, err := codec.Marshal(api.PutEntity{At: "demo.pr.1", Payload: entity.Obj{"stage": "open"}.Payload()}, codec.Tony)
	if err != nil {
		t.Fatal(err)
	}
	resp := postAs(t, "alice@example.com", ts.URL+"/entity", "application/tony", string(put))
	resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		t.Fatalf("put: %s", resp.Status)
	}
	// The record stands at the place the request made — `demo`, which its write brought into
	// being — and the row beneath it is its owner's through that.
	if got := owner(t, srv, entity.RefAt("demo")); got != "user:alice@example.com" {
		t.Fatalf("owner of demo = %q, want the requester", got)
	}
	pr := entity.RefAt("demo", "pr", "1")
	if ok, err := entity.Holds(entity.As(srv.Store, entity.SystemDefault),
		"user:alice@example.com", entity.OwnerLevel, pr); err != nil || !ok {
		t.Fatalf("%s is not the requester's: %v %v", pr, ok, err)
	}
	if _, ok, err := srv.Store.Get(entity.UserRef("alice@example.com")); err != nil || !ok {
		t.Fatalf("alice is not on the list of users (ok=%v err=%v)", ok, err)
	}

	charter := `{ name: review,
	  condition: { subject: { path: "demo.pr.*", value: { stage: open } } },
	  action: { patch: { stage: reviewing } } }`
	resp = postAs(t, "scott@example.com", ts.URL+"/charter", "application/tony", charter)
	resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("install: %s", resp.Status)
	}
	if got := owner(t, srv, trigger.CharterRefAt("", "review")); got != "user:scott@example.com" {
		t.Fatalf("the rule record is owned by %q, want its installer", got)
	}

	// A put that names nobody is refused at the door, and nothing of it stands.
	put2, _ := codec.Marshal(api.PutEntity{At: "anon.pr.2", Payload: entity.Obj{"stage": "draft"}.Payload()}, codec.Tony)
	resp = postAs(t, "", ts.URL+"/entity", "application/tony", string(put2))
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("a put naming nobody: %s, want 400", resp.Status)
	}
	if _, ok, _ := srv.Store.Get(entity.RefAt("anon", "pr", "2")); ok {
		t.Fatal("a put naming nobody was refused and written anyway")
	}
}

// EVERY ROUTE THAT IS NOT A READ REFUSES A REQUEST THAT NAMES NOBODY, asked of the route table the
// server publishes rather than of a list kept here: a route added tomorrow is in it, and refuses
// until somebody registers it otherwise. The routes that may be written through unattributed are
// pinned by name, so widening that set is a change to this test and not a side effect.
//
// It is asked of the DOOR, with a body no route would accept, so what is being counted is who
// answered: the door's 400 says the header's name, and a route's own 400 does not.
func TestEveryWritingRouteRefusesARequestThatNamesNobody(t *testing.T) {
	srv := testServer(t)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()
	unattributed := map[string]bool{
		"POST /io/{name}/hint":                   true, // a hook's nudge: what is reflected is reflected as the source
		"POST /io/{name}/pass":                   true, // an out half saying what it just did, about its own half of the world
		"POST /resolvers/instance/{id}/verdicts": true, // a service answers as its binding, never as itself
		"POST /resolvers/instance/{id}/position": true,
	}
	fill := strings.NewReplacer("{system}", "git", "{name}", "x", "{kind}", "k", "{id}", "x")
	send := func(method, path, as string) (int, string) {
		t.Helper()
		req, err := http.NewRequest(method, ts.URL+fill.Replace(path), strings.NewReader("{"))
		if err != nil {
			t.Fatal(err)
		}
		if as != "" {
			req.Header.Set(api.PrincipalHeader, as)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		raw, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, string(raw)
	}
	atTheDoor := func(code int, msg string) bool {
		return code == http.StatusBadRequest && strings.Contains(msg, api.PrincipalHeader+" is required")
	}
	var writes, seen int
	for _, rt := range srv.routes {
		pattern := rt.Method + " " + rt.Path
		if rt.Method == http.MethodGet || rt.Method == "*" {
			continue
		}
		writes++
		code, msg := send(rt.Method, rt.Path, "")
		if unattributed[pattern] {
			seen++
			if atTheDoor(code, msg) {
				t.Errorf("%s is registered as acting for a party the route names, and the door refused it", pattern)
			}
			continue
		}
		if !atTheDoor(code, msg) {
			t.Errorf("%s answered a request naming nobody with %d %q, want the door's 400", pattern, code, strings.TrimSpace(msg))
		}
		// …and the same request as somebody gets past the door, to whatever the route says.
		if code, msg := send(rt.Method, rt.Path, tester); atTheDoor(code, msg) {
			t.Errorf("%s refused %s at the door", pattern, tester)
		}
	}
	if writes < 20 {
		t.Fatalf("the route table lists %d writing routes; the test is not reading it", writes)
	}
	if seen != len(unattributed) {
		t.Errorf("%d of the %d routes pinned as unattributed are in the table: the list has drifted", seen, len(unattributed))
	}
	if len(srv.unattributedRoutes()) != len(unattributed) {
		t.Errorf("the server registers %v as unattributed, and this test pins %d", srv.unattributedRoutes(), len(unattributed))
	}
	// The runtime's own surface is MOUNTED, so it is in no table: asked by path.
	for _, path := range []string{"/tasks/run-1/cancel", "/tasks/run-1/say", "/tasks/run-1/pause", "/tasks"} {
		if code, msg := send(http.MethodPost, path, ""); !atTheDoor(code, msg) {
			t.Errorf("POST %s answered a request naming nobody with %d %q", path, code, strings.TrimSpace(msg))
		}
	}
	for _, m := range []string{http.MethodPut, http.MethodPatch, http.MethodDelete} {
		if code, msg := send(m, "/nowhere/at/all", ""); !atTheDoor(code, msg) {
			t.Errorf("%s of a path no route serves answered %d %q: the refusal is the default, not a list", m, code, strings.TrimSpace(msg))
		}
	}
	// A read names nobody and is answered, as it always was.
	if code, _ := send(http.MethodGet, "/status", ""); code != http.StatusOK {
		t.Errorf("GET /status naming nobody = %d", code)
	}
}

// httpPost is http.Post as the package's tester. A request that writes names who makes it, so a
// test that posts is somebody; one ABOUT a request that names nobody builds its own.
func httpPost(url, contentType string, body io.Reader) (*http.Response, error) {
	req, err := http.NewRequest(http.MethodPost, url, body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", contentType)
	req.Header.Set(api.PrincipalHeader, tester)
	return http.DefaultClient.Do(req)
}

// sendAsTester sends a request the test built, as the package's tester unless it names somebody.
func sendAsTester(req *http.Request) (*http.Response, error) {
	if req.Header.Get(api.PrincipalHeader) == "" {
		req.Header.Set(api.PrincipalHeader, tester)
	}
	return http.DefaultClient.Do(req)
}

// AND BEHIND THE DOOR, an install that names nobody is refused by the install. The engine
// persists a rule through its own store, which is the system's, and an empty name there means
// the engine's: admitted, a charter posted with no header would stand with no owner on record,
// and `via:` it would hold read-write on the root's perms record.
func TestAnInstallThatNamesNobodyIsRefusedByTheInstall(t *testing.T) {
	srv := testServer(t)
	docs, err := codec.ParseNodes([]byte(`{name: anon, condition: {subject: {path: "demo.anon.*",
		value: {stage: open}}}, action: {patch: {stage: seen}}}`), codec.Tony)
	if err != nil {
		t.Fatal(err)
	}
	_, code, err := srv.installCharterAs(entity.As(srv.Store, ""), "", "", charterDocs(docs))
	if err == nil || code != http.StatusBadRequest {
		t.Fatalf("an install by nobody: %d %v, want 400", code, err)
	}
	rule := trigger.CharterRef("anon")
	if _, ok, _ := srv.Store.Get(rule); ok {
		t.Fatal("the rule was refused and written anyway")
	}
	// …and a rule that does stand with no owner on record — put there by hand — acts as nobody.
	if _, _, err := srv.Store.Write(entity.Write{At: rule.At(), Value: entity.Obj{"name": "anon"}.Payload()}); err != nil {
		t.Fatal(err)
	}
	if got := owner(t, srv, rule); got != "" {
		t.Fatalf("the fixture is wrong: the rule is %q's", got)
	}
	for _, at := range []entity.Ref{entity.PermsRef(entity.RefAt()), entity.RefAt("demo", "anon", "1")} {
		if ok, _ := entity.Holds(srv.Store, entity.Via(rule), entity.Read, at); ok {
			t.Errorf("a rule nobody owns holds read at %s", at)
		}
	}
}
