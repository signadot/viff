package versed

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"

	"github.com/signadot/verse/pkg/api"
	"github.com/signadot/verse/pkg/codec"
	"github.com/signadot/verse/pkg/entity"
	"github.com/signadot/verse/pkg/idptest"
	"github.com/signadot/verse/pkg/launch"
	"github.com/signadot/verse/pkg/mirror"
	"github.com/signadot/verse/pkg/task"
)

// A rule's spawn that declares a credential gets a token verse signed, once its gate lets the
// firing through, and a relying party outside verse verifies it and reads what the gate was.

const credAudience = "sts.example.com"

// credentialServer is a started server that issues tokens as its own address and takes people's
// from a fake IdP, `corp`.
func credentialServer(t *testing.T) (*Server, *httptest.Server, *idptest.IdP) {
	t.Helper()
	idp := idptest.New(t)
	srv := testServer(t)
	clockOf(t, srv)
	srv.Start(t.Context())
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)
	if err := srv.SetIssuer(ts.URL, issuerKey(t), "config"); err != nil {
		t.Fatal(err)
	}
	if err := srv.SetAuth(Auth{Mode: AuthOptional, Providers: map[string]AuthProvider{
		authProvName: {Issuer: idp.URL, ClientID: authClient, Domains: []string{authDomain}},
	}}); err != nil {
		t.Fatal(err)
	}
	return srv, ts, idp
}

// deployRule is a rule over `demo.pr.*` at stage ready, with the gates and the spawn a case
// writes. Its task prints what its shell was told of a credential.
func deployRule(gates, spawn string) string {
	return `{
		name: deploy,
		condition: {subject: {path: "demo.pr.*", value: {stage: ready}}},
		gates: ` + gates + `,
		action: {patch: {stage: deploying}, spawn: {environment: {systems: {network: {mode: open}}}, autonomy: [{then: allow}], ` + spawn + `}},
	}`
}

// showsCredential is a spawn's command that prints the path its shell was given for a credential
// file, and copies the file to dir/token where it was given one, for the test to read.
func showsCredential(dir string) string {
	return `script: "echo \"[$VERSE_CREDENTIAL_FILE]\"; if [ -n \"$VERSE_CREDENTIAL_FILE\" ]; then cp \"$VERSE_CREDENTIAL_FILE\" ` + dir + `/token; fi"`
}

// ruleTask waits for the task the deploy rule started about a subject to end, and answers its
// record.
func ruleTask(t *testing.T, srv *Server, subject string) map[string]any {
	t.Helper()
	deadline := time.After(10 * time.Second)
	for {
		for _, ent := range entity.Below(srv.Store, mirror.IsTask, 1, entity.VerseRoot, mirror.System, mirror.InstanceKind) {
			p := entity.ToObj(ent.Payload)
			if p["flow"] == subject && task.Status(fmt.Sprint(p["status"])).Terminal() {
				p["id"] = ent.Ref.Name()
				return p
			}
		}
		select {
		case <-deadline:
			t.Fatalf("the rule's task about %s never ended", subject)
		case <-time.After(10 * time.Millisecond):
		}
	}
}

// credentialOf is the path a showsCredential task's shell was given, and the token it copied to dir.
func credentialOf(t *testing.T, rec map[string]any, dir string) (path, token string) {
	t.Helper()
	first, _, _ := strings.Cut(fmt.Sprint(rec["lastOutput"]), "\n")
	data, err := os.ReadFile(dir + "/token")
	if err != nil {
		t.Fatalf("the task copied no credential: %v (it printed %q)", err, first)
	}
	return strings.Trim(strings.TrimSpace(first), "[]"), string(data)
}

func TestAGatedFiringsTaskHoldsACredentialThatNamesItsGate(t *testing.T) {
	srv, ts, idp := credentialServer(t)
	dir := t.TempDir()
	ana := idp.Token(authClient, "sub-ana", person("ana@example.com"))
	if code, out := bearer(t, http.MethodPost, ts.URL+"/charter", ana, "",
		deployRule(`[{name: ok, mode: ask}]`, showsCredential(dir)+`, credential: {audience: `+credAudience+`}`)); code != http.StatusCreated {
		t.Fatalf("install = %d %s", code, out)
	}
	if code, out := bearer(t, http.MethodPost, ts.URL+"/entity", ana, "", func() string {
		b, _ := codec.Marshal(api.PutEntity{At: "demo.pr.1", Payload: entity.Obj{"stage": "ready"}.Payload()}, codec.Tony)
		return string(b)
	}()); code/100 != 2 {
		t.Fatalf("put = %d %s", code, out)
	}
	qid := waitRuleQuestion(t, srv, "deploy", "demo.pr.\"1\"")
	if code, out := bearer(t, http.MethodPost, ts.URL+answerPath(qid), ana, "", `{choice: proceed}`); code/100 != 2 {
		t.Fatalf("answer = %d %s", code, out)
	}
	rec := ruleTask(t, srv, "demo.pr.\"1\"")
	if rec["status"] != string(task.Done) {
		t.Fatalf("the task = %v", rec)
	}
	path, token := credentialOf(t, rec, dir)

	// The relying party: it knows the issuer's URL and its own audience, and nothing of verse.
	rp, err := oidc.NewProvider(t.Context(), ts.URL)
	if err != nil {
		t.Fatalf("discovery: %v", err)
	}
	tok, err := rp.Verifier(&oidc.Config{ClientID: credAudience}).Verify(t.Context(), token)
	if err != nil {
		t.Fatalf("the task's credential did not verify: %v\n%s", err, token)
	}
	const rule = "verse.trigger.rule.deploy"
	if tok.Subject != rule+":ballots" {
		t.Errorf("sub = %q, want the rule and how its gate was passed", tok.Subject)
	}
	if life := tok.Expiry.Sub(tok.IssuedAt); life != credentialLife {
		t.Errorf("the credential is good for %s, want %s", life, credentialLife)
	}
	var claims struct {
		Task      string `json:"task"`
		Rule      string `json:"rule"`
		Subject   string `json:"subject"`
		Principal string `json:"principal"`
		JTI       string `json:"jti"`
		Gates     []struct {
			Question  string `json:"question"`
			Passed    string `json:"passed"`
			Answerers []struct {
				Principal     string `json:"principal"`
				Authenticated string `json:"authenticated"`
			} `json:"answerers"`
		} `json:"gates"`
	}
	if err := tok.Claims(&claims); err != nil {
		t.Fatal(err)
	}
	if claims.Rule != rule || claims.Subject != "demo.pr.\"1\"" || claims.Task != fmt.Sprint(rec["id"]) ||
		claims.Principal != "via:"+rule || claims.JTI == "" {
		t.Errorf("claims = %+v (task record id %v)", claims, rec["id"])
	}
	if len(claims.Gates) != 1 || claims.Gates[0].Question != mirror.QuestionRef(qid).String() || claims.Gates[0].Passed != "ballots" ||
		len(claims.Gates[0].Answerers) != 1 || claims.Gates[0].Answerers[0].Principal != idAna ||
		claims.Gates[0].Answerers[0].Authenticated != authProvName {
		t.Errorf("gates = %+v, want the question, passed on ballots, answered by ana as %s verified her", claims.Gates, authProvName)
	}

	// The token reached the task as a file outside its workspace, and the file went when the
	// task ended.
	wd, _ := os.Getwd()
	if path == "" || strings.HasPrefix(path, wd) {
		t.Errorf("the credential's file was at %q, want a path outside the workspace %s", path, wd)
	}
	gone := false
	for range 200 {
		if _, err := os.Stat(path); os.IsNotExist(err) {
			gone = true
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if !gone {
		t.Errorf("the credential's file %s outlived its task", path)
	}
	// And the ballot on the question's record says how its caster was authenticated.
	q, _, _ := srv.Store.Get(mirror.QuestionRef(qid))
	if got := entity.Str(entity.Get(entity.Get(q.Payload, "ballots"), idAna), "authenticated"); got != authProvName {
		t.Errorf("ana's ballot says authenticated %q, want %s", got, authProvName)
	}
}

// How a gate was passed is in the credential, a gate written `auto` included; what a credential
// does not vouch for, it is not minted for; and a spawn that asked for none gets none.
func TestWhoGetsWhichCredential(t *testing.T) {
	const wants = `, credential: {audience: ` + credAudience + `}`
	for _, c := range []struct {
		name          string
		gates, spawn  string // spawn is what the spawn says beside its command
		answer        string // the choice to answer the gate with, "" to leave it to its clock
		noIssuer      bool
		refused       string      // what the install is refused saying, "" where it is taken
		status        task.Status // the rule's task, "" where none is started
		sub, inResult string
		passed        string // how the token says its one gate was passed
	}{
		{name: "a spawn that asked for none", gates: `[{name: ok, mode: ask}]`,
			answer: "proceed", status: task.Done, inResult: "[]"},
		{name: "a gate that was denied", gates: `[{name: ok, mode: ask}]`, spawn: wants, answer: "deny"},
		{name: "a gate its deadline settled", gates: `[{name: ok, mode: ask, deadline: {within: "1s", default: proceed}}]`, spawn: wants,
			status: task.Done, sub: "verse.trigger.rule.deploy:deadline", passed: "deadline"},
		{name: "a gate written auto", gates: `[{name: ok, mode: auto}]`, spawn: wants,
			status: task.Done, sub: "verse.trigger.rule.deploy:auto", passed: "auto"},
		{name: "a rule that writes no gate", gates: `[]`, spawn: wants, refused: "the rule has no gate"},
		{name: "a verse that issues nothing", gates: `[{name: ok, mode: ask}]`, spawn: wants, answer: "proceed", noIssuer: true,
			status: task.Failed, inResult: "issues none"},
	} {
		t.Run(c.name, func(t *testing.T) {
			srv, ts, _ := credentialServer(t)
			if c.noIssuer {
				srv.issuer = nil
			}
			dir := t.TempDir()
			resp := installAs(t, idAna, ts.URL, deployRule(c.gates, showsCredential(dir)+c.spawn))
			said := bodyOf(t, resp)
			if c.refused != "" {
				if resp.StatusCode/100 == 2 || !strings.Contains(said, c.refused) {
					t.Fatalf("install = %s %s, want it refused saying %q", resp.Status, said, c.refused)
				}
				return
			}
			if resp.StatusCode != http.StatusCreated {
				t.Fatalf("install = %s %s", resp.Status, said)
			}
			if err := write(srv.Store, idAna, entity.RefAt("demo", "pr", "1"), entity.Obj{"stage": "ready"}); err != nil {
				t.Fatal(err)
			}
			const subject = "demo.pr.\"1\""
			if c.answer != "" {
				qid := waitRuleQuestion(t, srv, "deploy", subject)
				resp := postAs(t, idAna, ts.URL+answerPath(qid), "application/tony", `{choice: `+c.answer+`}`)
				resp.Body.Close()
				if resp.StatusCode/100 != 2 {
					t.Fatalf("answer = %s", resp.Status)
				}
			} else if c.passed == "deadline" {
				waitStamped(t, srv, waitRuleQuestion(t, srv, "deploy", subject))
				srv.clock.(*fakeClock).Advance(time.Second)
			}
			if c.status == "" {
				time.Sleep(300 * time.Millisecond)
				if tasks := entity.Below(srv.Store, mirror.IsTask, 1, entity.VerseRoot, mirror.System, mirror.InstanceKind); len(tasks) != 0 {
					t.Fatalf("a denied gate started %d task(s)", len(tasks))
				}
				return
			}
			rec := ruleTask(t, srv, subject)
			if rec["status"] != string(c.status) {
				t.Fatalf("the task = %v, want it %s", rec, c.status)
			}
			if c.inResult != "" && !strings.Contains(fmt.Sprint(rec["result"])+fmt.Sprint(rec["err"]), c.inResult) {
				t.Errorf("the task says %v / %v, want %q", rec["result"], rec["err"], c.inResult)
			}
			if c.sub != "" {
				_, token := credentialOf(t, rec, dir)
				rp, err := oidc.NewProvider(t.Context(), ts.URL)
				if err != nil {
					t.Fatal(err)
				}
				tok, err := rp.Verifier(&oidc.Config{ClientID: credAudience}).Verify(t.Context(), token)
				if err != nil {
					t.Fatalf("the credential did not verify: %v", err)
				}
				var claims struct {
					Gates []struct {
						Gate      string           `json:"gate"`
						Question  string           `json:"question"`
						Passed    string           `json:"passed"`
						Answerers []map[string]any `json:"answerers"`
					} `json:"gates"`
				}
				_ = tok.Claims(&claims)
				if tok.Subject != c.sub || len(claims.Gates) != 1 || claims.Gates[0].Gate != "ok" ||
					claims.Gates[0].Passed != c.passed || len(claims.Gates[0].Answerers) != 0 {
					t.Errorf("sub = %q, gates = %+v, want %s, gate ok passed as %s, and nobody's answer", tok.Subject, claims.Gates, c.sub, c.passed)
				}
				// A gate that asked nobody has no question to name.
				if asked := claims.Gates[0].Question != ""; asked != (c.passed != "auto") {
					t.Errorf("the gate's question = %q for a gate passed as %s", claims.Gates[0].Question, c.passed)
				}
				// And the task's record says a credential was minted for it: what for and until
				// when, and not the token.
				minted := entity.ToObj(entity.Val(rec["credential"]))
				if minted["audience"] != credAudience || minted["sub"] != c.sub || minted["jti"] == nil || minted["expires"] == nil {
					t.Errorf("the task's record says of its credential %v", rec["credential"])
				}
				if strings.Contains(fmt.Sprint(rec), token) {
					t.Error("the task's record holds the token")
				}
			}
		})
	}
}

// A launch the engine says no gate admitted gets no credential, whatever it asks for: a selection
// resumed after a restart is one, since what its firing was told is not kept. And a gate that asked
// and was let through by neither a ballot nor its clock vouches for nothing.
func TestNoCredentialWithoutAnAnswerToVouchFor(t *testing.T) {
	srv, _, _ := credentialServer(t)
	spec := launch.Spec{Chain: "deploy", Subject: "demo.pr.1", Credential: &launch.Credential{Audience: credAudience}}
	mint := func() (string, *mirror.Minted, error) {
		return srv.mintCredential(srv.Store, srv.Eng, "deploy@demo.pr.1", spec, "via:verse.trigger.rule.deploy")
	}
	if _, _, err := mint(); err == nil || !strings.Contains(err.Error(), "no gate let this launch through") {
		t.Errorf("a launch that passed no gate: %v", err)
	}
	spec.Passed = []launch.Passed{{Gate: "ok", Question: "q1", Choice: "proceed"}}
	if _, _, err := mint(); err == nil || !strings.Contains(err.Error(), "neither a ballot nor its deadline") {
		t.Errorf("a gate nobody and no clock settled: %v", err)
	}

	// What several gates come to: `deadline` if any that asked ran out its clock, `ballots` if
	// every one that asked was answered, and `auto` only where none asked.
	ballot := launch.Passed{Gate: "a", Question: "q1", Choice: "proceed", How: launch.PassedBallots, By: idAna}
	clock := launch.Passed{Gate: "b", Question: "q2", Choice: "proceed", How: launch.PassedDeadline}
	free := launch.Passed{Gate: "c", How: launch.PassedAuto}
	const rule = "verse.trigger.rule.deploy"
	for want, passed := range map[string][]launch.Passed{
		rule + ":ballots":  {free, ballot},
		rule + ":deadline": {ballot, clock, free},
		rule + ":auto":     {free},
	} {
		spec.Passed = passed
		_, minted, err := mint()
		if err != nil || minted.Sub != want || minted.Audience != credAudience || minted.JTI == "" || minted.Expires == "" {
			t.Errorf("%+v: minted %+v, %v, want sub %s", passed, minted, err, want)
		}
	}
}

// A credential is for a system outside verse, and a runner is not one: a spawn declaring a
// runner's subject as its audience is minted nothing, since the token would be one verse signed
// for that runner's name.
func TestNoCredentialForARunner(t *testing.T) {
	srv, _, _ := credentialServer(t)
	regs, _, err := readRunners(srv.Store)
	if err != nil || len(regs) == 0 {
		t.Fatalf("the local runner's registration: %v, %v", regs, err)
	}
	spec := launch.Spec{Chain: "deploy", Subject: "demo.pr.1", Credential: &launch.Credential{Audience: regs[0].Subject},
		Passed: []launch.Passed{{Gate: "ok", How: launch.PassedAuto}}}
	if _, _, err := srv.mintCredential(srv.Store, srv.Eng, "deploy@demo.pr.1", spec, "via:verse.trigger.rule.deploy"); err == nil ||
		!strings.Contains(err.Error(), "which is the runner "+regs[0].Name) {
		t.Errorf("a credential for the runner %s: %v, want a refusal naming it", regs[0].Name, err)
	}
}
