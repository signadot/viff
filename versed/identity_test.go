package versed

import (
	stdio "io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"github.com/signadot/verse/codec"
	"github.com/signadot/verse/entity"
)

// An account is claimed by its person, proved by the account saying the nonce in a comment the
// github io reflects, and bound by the system; an io then looks a comment's author up.

const (
	idAna  = "user:ana@example.com"
	idBo   = "user:bo@example.com"
	idIO   = "agent:github-io"
	anaSub = "101"
)

// identityServer is a server with a github io registered at `github`, reflecting signadot/verse.
func identityServer(t *testing.T) (*Server, *httptest.Server) {
	t.Helper()
	srv := testServer(t)
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)
	resp := installIO(t, ts, idAna, map[string]any{
		"name": "gh", "root": "github", "url": "http://github-io:9801",
		"owns": []any{map[string]any{"at": "github.signadot"}},
	})
	resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("install = %s", resp.Status)
	}
	if err := srv.SetAuth(Auth{Providers: map[string]AuthProvider{githubProvider: {IOs: []string{"gh"}}}}); err != nil {
		t.Fatal(err)
	}
	return srv, ts
}

// reflectComment stands a comment where the github io reflects one, as the reflection would.
func reflectComment(t *testing.T, srv *Server, id, login string, authorID int64, body string) entity.Ref {
	t.Helper()
	ref := entity.RefAt("github", "signadot", "verse", "comment", "42", id)
	p := entity.Obj{
		"repo": "signadot/verse", "on": 42, "kind": "conversation", "author": login, "body": body,
		"url": "https://github.com/signadot/verse/pull/42#issuecomment-" + id,
	}
	if authorID != 0 {
		p["authorId"] = authorID
	}
	if _, _, err := srv.Store.Write(entity.Write{At: ref.At(), Value: p.Payload()}); err != nil {
		t.Fatal(err)
	}
	return ref
}

// call makes one request as somebody and answers its status and its JSON body.
func call(t *testing.T, method, as, url, body string) (int, map[string]any) {
	t.Helper()
	var resp *http.Response
	switch method {
	case http.MethodPost:
		resp = postAs(t, as, url+"?format=tony", "application/json", body)
	case http.MethodGet:
		resp = getAs(t, as, url+"&format=tony")
	case http.MethodDelete:
		resp = deleteAs(t, as, url)
	}
	defer resp.Body.Close()
	out := map[string]any{}
	if resp.StatusCode < 300 && resp.StatusCode != http.StatusNoContent {
		decodeTony(t, resp, &out)
	}
	return resp.StatusCode, out
}

// decodeTony reads a response's body, rendered as tony, into v.
func decodeTony(t *testing.T, resp *http.Response, v any) {
	t.Helper()
	data, err := stdio.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	if err := codec.Unmarshal(data, v, codec.Tony); err != nil {
		t.Fatalf("%s: %v", data, err)
	}
}

// claim claims login as somebody and answers the nonce.
func claim(t *testing.T, ts *httptest.Server, as, login string) string {
	t.Helper()
	code, out := call(t, http.MethodPost, as, ts.URL+"/accounts/github", `{"login": "`+login+`"}`)
	if code != http.StatusCreated {
		t.Fatalf("claim as %s = %d", as, code)
	}
	if out["say"] != "/verse link "+out["nonce"].(string) {
		t.Errorf("claim says %v, want /verse link <nonce>", out["say"])
	}
	return out["nonce"].(string)
}

func verify(t *testing.T, ts *httptest.Server, as, nonce string) (int, map[string]any) {
	t.Helper()
	return call(t, http.MethodPost, as, ts.URL+"/accounts/github/verify", `{"nonce": "`+nonce+`"}`)
}

func TestAnAccountSaidFromIsBoundAndLookedUp(t *testing.T) {
	srv, ts := identityServer(t)
	nonce := claim(t, ts, idAna, "ana-gh")

	// Not said yet: the claim stands, to be verified once the comment is reflected.
	if code, _ := verify(t, ts, idAna, nonce); code != http.StatusNotFound {
		t.Fatalf("verify before the comment = %d, want 404", code)
	}
	// Said by somebody else, or quoted inside other words, is not the account saying it.
	reflectComment(t, srv, "9000", "mallory", 666, "/verse link "+nonce)
	reflectComment(t, srv, "9001", "ana-gh", 101, "run `/verse link "+nonce+"` to link")
	if code, _ := verify(t, ts, idAna, nonce); code != http.StatusNotFound {
		t.Fatalf("verify on others' words = %d, want 404", code)
	}
	// Nor is another principal's verify of the nonce.
	evidence := reflectComment(t, srv, "9002", "Ana-GH", 101, "/verse link "+nonce+"\n")
	if code, _ := verify(t, ts, idBo, nonce); code != http.StatusNotFound {
		t.Fatalf("verify of ana's nonce as bo = %d, want 404", code)
	}

	code, out := verify(t, ts, idAna, nonce)
	if code != http.StatusCreated {
		t.Fatalf("verify = %d, want 201", code)
	}
	proof, _ := out["proof"].(map[string]any)
	if out["principal"] != idAna || out["sub"] != anaSub || out["login"] != "Ana-GH" ||
		out["provider"] != githubProvider ||
		proof["method"] != "comment" || proof["evidence"] != evidence.String() {
		t.Errorf("binding = %v", out)
	}
	// The claim is spent.
	if code, _ := verify(t, ts, idAna, nonce); code != http.StatusNotFound {
		t.Errorf("a spent nonce verified again: %d", code)
	}

	// The io looks the author up, by number or by login; a stranger is not told.
	for _, q := range []string{"sub=" + anaSub, "login=ana-gh"} {
		if code, out := call(t, http.MethodGet, idIO, ts.URL+"/accounts/github/who?"+q, ""); code != http.StatusOK || out["principal"] != idAna {
			t.Errorf("who?%s as the io = %d %v", q, code, out)
		}
		if code, _ := call(t, http.MethodGet, idBo, ts.URL+"/accounts/github/who?"+q, ""); code != http.StatusNotFound {
			t.Errorf("who?%s as a stranger = %d, want 404", q, code)
		}
	}
	// A person cannot write a binding, their own or anybody's.
	if err := write(srv.Store, idBo, accountRef(githubProvider, "202"), entity.Obj{"principal": idBo, "sub": "202"}); err == nil {
		t.Error("a person wrote a binding")
	}
	if err := write(srv.Store, idAna, accountRef(githubProvider, anaSub), entity.Obj{"principal": idBo}); err == nil {
		t.Error("a person rewrote their own binding")
	}

	// Proving it again is the same binding.
	again := claim(t, ts, idAna, "ana-gh")
	reflectComment(t, srv, "9003", "ana-gh", 101, "/verse link "+again)
	if code, _ := verify(t, ts, idAna, again); code != http.StatusOK {
		t.Errorf("proving a bound account again = %d, want 200", code)
	}
}

func TestOneAccountIsOnePrincipalUntilRevoked(t *testing.T) {
	srv, ts := identityServer(t)
	n := claim(t, ts, idAna, "ana-gh")
	reflectComment(t, srv, "1", "ana-gh", 101, "/verse link "+n)
	if code, _ := verify(t, ts, idAna, n); code != http.StatusCreated {
		t.Fatalf("verify = %d", code)
	}

	// The same account proved for somebody else is refused while ana holds it.
	n = claim(t, ts, idBo, "ana-gh")
	reflectComment(t, srv, "2", "ana-gh", 101, "/verse link "+n)
	if code, _ := verify(t, ts, idBo, n); code != http.StatusConflict {
		t.Fatalf("a second principal's verify = %d, want 409", code)
	}

	// Only its principal revokes it.
	if code, _ := call(t, http.MethodDelete, idBo, ts.URL+"/accounts/github/"+anaSub, ""); code != http.StatusNotFound {
		t.Errorf("bo revoking ana's binding = %d, want 404", code)
	}
	for as, want := range map[string]int{idAna: 1, idBo: 0} {
		resp := getAs(t, as, ts.URL+"/accounts?format=tony")
		var rows []map[string]any
		decodeTony(t, resp, &rows)
		resp.Body.Close()
		if len(rows) != want || (want == 1 && rows[0]["sub"] != anaSub) {
			t.Errorf("%s's accounts = %v, want %d", as, rows, want)
		}
	}
	if code, _ := call(t, http.MethodDelete, idAna, ts.URL+"/accounts/github/"+anaSub, ""); code != http.StatusNoContent {
		t.Fatalf("revoke = %d, want 204", code)
	}
	if code, _ := call(t, http.MethodGet, idIO, ts.URL+"/accounts/github/who?sub="+anaSub, ""); code != http.StatusNotFound {
		t.Errorf("who after revoke = %d, want 404", code)
	}

	// Revoked, it binds to bo — and is bo's and the io's to read, not ana's any more.
	n = claim(t, ts, idBo, "ana-gh")
	reflectComment(t, srv, "3", "ana-gh", 101, "/verse link "+n)
	if code, _ := verify(t, ts, idBo, n); code != http.StatusCreated {
		t.Fatalf("bo's verify after revoke = %d, want 201", code)
	}
	if code, out := call(t, http.MethodGet, idIO, ts.URL+"/accounts/github/who?sub="+anaSub, ""); code != http.StatusOK || out["principal"] != idBo {
		t.Errorf("who after rebinding = %d %v", code, out)
	}
	if code, _ := call(t, http.MethodGet, idAna, ts.URL+"/accounts/github/who?sub="+anaSub, ""); code != http.StatusNotFound {
		t.Errorf("ana still reads the binding she revoked: %d", code)
	}
}

func TestAnExpiredClaimIsGone(t *testing.T) {
	srv, ts := identityServer(t)
	n := claim(t, ts, idAna, "ana-gh")
	if _, _, err := entity.As(srv.Store, entity.SystemIdentity).Write(entity.Write{
		At: claimRef(githubProvider, n).At("expires"), Value: entity.Val("2020-01-01T00:00:00Z"),
	}); err != nil {
		t.Fatal(err)
	}
	reflectComment(t, srv, "1", "ana-gh", 101, "/verse link "+n)
	if code, _ := verify(t, ts, idAna, n); code != http.StatusGone {
		t.Fatalf("verify of an expired claim = %d, want 410", code)
	}
	if code, _ := verify(t, ts, idAna, n); code != http.StatusNotFound {
		t.Errorf("an expired claim stood after it was refused: %d", code)
	}
}

func TestACommentWithoutTheAuthorsNumberIsNoEvidence(t *testing.T) {
	srv, ts := identityServer(t)
	n := claim(t, ts, idAna, "ana-gh")
	reflectComment(t, srv, "1", "ana-gh", 0, "/verse link "+n)
	if code, _ := verify(t, ts, idAna, n); code != http.StatusNotFound {
		t.Fatalf("verify on a comment with no authorId = %d, want 404", code)
	}
}

// A binding says how its claimant was authenticated: a caller who is whoever their request said
// leaves an `asserted` one, on the record and in every answer that carries the binding.
func TestABindingSaysItsClaimant(t *testing.T) {
	srv, ts := identityServer(t)
	n := claim(t, ts, idAna, "ana-gh")
	reflectComment(t, srv, "1", "ana-gh", 101, "/verse link "+n)
	code, out := verify(t, ts, idAna, n)
	if code != http.StatusCreated || out["claimant"] != claimantAsserted {
		t.Fatalf("verify = %d %v, want 201 and claimant %s", code, out, claimantAsserted)
	}
	rec, found, err := entity.As(srv.Store, entity.SystemIdentity).Get(accountRef(githubProvider, anaSub))
	if err != nil || !found || entity.Str(rec.Payload, "claimant") != claimantAsserted {
		t.Errorf("the record's claimant = %q (found=%v err=%v)", entity.Str(rec.Payload, "claimant"), found, err)
	}
	if code, out := call(t, http.MethodGet, idIO, ts.URL+"/accounts/github/who?sub="+anaSub, ""); code != http.StatusOK || out["claimant"] != claimantAsserted {
		t.Errorf("who = %d %v, want claimant %s", code, out, claimantAsserted)
	}
}

// Bindings are kept per provider, and a sub is one address segment whatever its issuer put in it:
// a dot, a colon and a slash are all characters an address or a URL would otherwise split on.
func TestASubIsOneSegmentUnderItsProvider(t *testing.T) {
	srv, ts := identityServer(t)
	sys := entity.As(srv.Store, entity.SystemIdentity)
	const provider, sub = "dex", "Cg.a:b/c=="
	binding := func(who string) entity.Obj {
		return entity.Obj{"principal": who, "iss": "https://idp.example.com", "sub": sub, "claimant": claimantAsserted}
	}
	if code, err := srv.bind(sys, provider, binding(idAna), idAna, sub); err != nil || code != http.StatusCreated {
		t.Fatalf("bind = %d %v", code, err)
	}
	if segs := accountRef(provider, sub).Segs(); len(segs) != 5 || segs[4] != sub {
		t.Fatalf("the binding's address = %q, want the sub as its fifth segment", segs)
	}
	// The same sub under another provider is another account.
	if code, err := srv.bind(sys, "okta", binding(idBo), idBo, sub); err != nil || code != http.StatusCreated {
		t.Fatalf("the sub under a second provider = %d %v, want 201", code, err)
	}

	who := ts.URL + "/accounts/" + provider + "/who?sub=" + url.QueryEscape(sub)
	if code, out := call(t, http.MethodGet, idIO, who, ""); code != http.StatusOK || out["principal"] != idAna || out["sub"] != sub || out["provider"] != provider {
		t.Errorf("who = %d %v", code, out)
	}
	resp := getAs(t, idAna, ts.URL+"/accounts?format=tony")
	var rows []map[string]any
	decodeTony(t, resp, &rows)
	resp.Body.Close()
	if len(rows) != 1 || rows[0]["sub"] != sub || rows[0]["provider"] != provider {
		t.Errorf("ana's accounts = %v, want the one under %s", rows, provider)
	}
	if code, _ := call(t, http.MethodDelete, idAna, ts.URL+"/accounts/"+provider+"/"+url.PathEscape(sub), ""); code != http.StatusNoContent {
		t.Fatalf("revoke = %d, want 204", code)
	}
	if code, _ := call(t, http.MethodGet, idIO, who, ""); code != http.StatusNotFound {
		t.Errorf("who after revoke = %d, want 404", code)
	}
	if code, out := call(t, http.MethodGet, idIO, ts.URL+"/accounts/okta/who?sub="+url.QueryEscape(sub), ""); code != http.StatusOK || out["principal"] != idBo {
		t.Errorf("the second provider's binding after the first was revoked = %d %v", code, out)
	}
}
