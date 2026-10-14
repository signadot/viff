package versed

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/signadot/verse/entity"
	verseio "github.com/signadot/verse/io"
	"github.com/signadot/verse/mirror"
	"github.com/signadot/verse/resolver"
	"github.com/signadot/verse/trigger"
)

// The places the grammar says act are the places that do: a rule, a source and a resolver each
// spell a principal the grammar reads, and a runner's address — which has a record and acts as
// its identity instead — is refused. entity names the three systems because it cannot import
// the packages that own them; this is where the two spellings are held together.
func TestTheGrammarsActorsAreTheOnesThatAct(t *testing.T) {
	for _, ref := range []entity.Ref{
		trigger.CharterRef("x"),
		trigger.CharterRefAt("drift", "head-moved"),
		verseio.Ref("cluster"),
		resolver.Ref("gate"),
	} {
		if _, err := entity.ParsePrincipal(entity.Via(ref)); err != nil {
			t.Errorf("%s acts, and its principal does not parse: %v", ref, err)
		}
	}
	if _, err := entity.ParseGrantee(entity.Via(runnerEntityRef("coderunner"))); err == nil {
		t.Errorf("a runner's address parsed as a place that acts")
	}
}

// The seed is written in the grammar and says which part wrote it: the docket is answered by
// `user:*` and `agent:*`, and the row's commit is system:seed's.
func TestTheSeedIsSpelledInTheGrammar(t *testing.T) {
	srv := testServer(t)
	docket := entity.VerseRef(mirror.AskSystem)
	rec, ok, err := srv.Store.Get(entity.PermsRef(docket))
	if err != nil || !ok {
		t.Fatalf("no seed row at the docket: ok=%v err=%v", ok, err)
	}
	grants, err := entity.ParseGrants(docket.Segs(), rec.Payload)
	if err != nil {
		t.Fatal(err)
	}
	var answer []string
	for _, g := range grants {
		answer = append(answer, g.To[entity.ReadWrite]...)
	}
	if strings.Join(answer, ",") != "user:*,agent:*" {
		t.Fatalf("the docket is answered by %v, want user:* and agent:*", answer)
	}
	commits, err := srv.Store.Since(0, entity.PrefixPattern([]string{entity.VerseRoot, entity.PermsSystem, entity.PermsKind}, 4))
	if err != nil {
		t.Fatal(err)
	}
	by := ""
	for _, c := range commits {
		for _, ev := range c.Changes {
			if ev.Ref == entity.PermsRef(docket) && by == "" {
				by = c.Author
			}
		}
	}
	if by != entity.SystemSeed {
		t.Fatalf("the seed row was written by %q, want %s", by, entity.SystemSeed)
	}
}

// Every edge a grantee is written at reads it in the grammar: a group's member, a person or a
// group resolver's answerer. A spelling the grammar does not read is refused, saying the
// grammar; one it reads is stored as the grammar spells it.
func TestTheEdgesRefuseWhatTheGrammarDoesNotRead(t *testing.T) {
	srv := testServer(t)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	resp := groupAs(t, "walter@example.com", ts.URL, "release", "ana@example.com", "verse.principal.group.x")
	body := readBody(t, resp)
	if resp.StatusCode != http.StatusBadRequest || !strings.Contains(body, entity.Grammar()) {
		t.Fatalf("a member spelled as a group's address: %s %s", resp.Status, body)
	}
	resp = groupAs(t, "walter@example.com", ts.URL, "release", "ana@example.com", "group:qa", "runner:*")
	resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("group add: %s", resp.Status)
	}
	e, _, _ := srv.Store.Get(entity.GroupRef("release"))
	if got := strings.Join(entity.Strs(e.Payload, entity.MembersField), ","); got != "user:ana@example.com,group:qa,runner:*" {
		t.Fatalf("the members are stored as %s", got)
	}
}
