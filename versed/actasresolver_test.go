package versed

import (
	"net/http"
	"strings"
	"testing"

	"github.com/signadot/verse/api"
	"github.com/signadot/verse/mirror"
)

func getAsResolver(t *testing.T, as, name, url string) int {
	code, _ := getAsResolverBody(t, as, name, url)
	return code
}

func getAsResolverBody(t *testing.T, as, name, url string) (int, string) {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		t.Fatal(err)
	}
	if as != "" {
		req.Header.Set(api.PrincipalHeader, as)
	}
	req.Header.Set(api.ResolverHeader, name)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	return resp.StatusCode, bodyOf(t, resp)
}

// A RESOLVER'S SERVICE IS WHOEVER INSTALLED IT, ACTING AS IT (9ce3bva0, S1): a request presenting
// the installer and naming the resolver is made as `via:` the resolver, and reads what the
// resolver reads. Presented by anybody else, or by nobody, it is refused.
func TestAResolverIsActedAsByWhoeverInstalledIt(t *testing.T) {
	srv, ts := shortKeep(t, askKeepDefault)
	const alice, bob = "user:alice@example.com", "user:bob@example.com"
	if resp := postAs(t, alice, ts.URL+"/resolvers", "application/tony",
		`{name: helper, match: {}, method: {driver: service}}`); resp.StatusCode != http.StatusCreated {
		t.Fatalf("install: %s: %s", resp.Status, bodyOf(t, resp))
	}
	id := askID(t, ask(t, "carol@example.com", ts.URL, `{name: land, question: "land it?", answerers: [bob@example.com]}`))
	posed(t, srv, mirror.AskRef(id))
	if code, msg := grantStatus(t, bob, ts.URL, "helper", `{}`); code != http.StatusNoContent {
		t.Fatalf("bob's grant: %d %s", code, msg)
	}
	at := ts.URL + "/entity?at=" + mirror.AskRef(id).String()

	if resp := getAs(t, alice, at); resp.StatusCode != http.StatusForbidden {
		t.Fatalf("alice, as herself, read carol's ask to bob: %s", resp.Status)
	}
	for _, name := range []string{"helper", "verse.resolvers.helper"} {
		if code := getAsResolver(t, alice, name, at); code != http.StatusOK {
			t.Fatalf("alice as %s could not read the ask it serves: %d", name, code)
		}
	}
	if code := getAsResolver(t, bob, "helper", at); code != http.StatusForbidden {
		t.Errorf("bob acted as a resolver alice installed: %d", code)
	}
	if code, msg := getAsResolverBody(t, "", "helper", at); code != http.StatusForbidden || !strings.Contains(msg, "names nobody") {
		t.Errorf("nobody acted as a resolver alice installed: %d %s", code, msg)
	}
	if code := getAsResolver(t, alice, "nobody", at); code != http.StatusForbidden {
		t.Errorf("alice acted as a resolver that does not stand: %d", code)
	}
}
