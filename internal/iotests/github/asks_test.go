package github_test

import (
	"context"
	"go.viff.dev/viff/internal/viffd/viffdtest"
	"go.viff.dev/viff/pkg/runner/runnerd/runnerdtest"

	"github.com/signadot/tony-format/go-tony/ir"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"go.viff.dev/viff/internal/viffd"
	"go.viff.dev/viff/pkg/client"
	"go.viff.dev/viff/pkg/entity"
	"go.viff.dev/viff/pkg/entity/entitytest"
	viffio "go.viff.dev/viff/pkg/io"
	"go.viff.dev/viff/pkg/io/github"
	"go.viff.dev/viff/pkg/mirror"
	"go.viff.dev/viff/pkg/task"
)

// A question asked on a pull request is answered there, as the author of the `/viff` comment,
// through their GitHub account binding; an author viff does not know is told so, once.

const (
	askedOnPR = "mirror.trigger.deploy.ok~1"
	ioToken   = "the-github-iods-own-token"
)

// asksRig is a viff instance that verifies people and knows this io by a static token, a fake GitHub with
// one pull request, and the out half with a way to answer as a person.
type asksRig struct {
	store     *entity.LogdStore
	srv       *viffd.Server
	fake      *fakeGitHub
	out       *viffio.Out
	addr      string // the viff instance, for a test that asks it things as somebody
	tokenFile string // the io's own token, for a test that sets the auth again
}

func newAsksRig(t *testing.T) *asksRig {
	t.Helper()
	store := entitytest.Store(t)
	srv := viffdtest.Server(t, runnerdtest.Scripted, store)
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)
	addr := strings.TrimPrefix(ts.URL, "http://")

	file := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(file, []byte(ioToken+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := srv.SetAuth(viffd.Auth{Mode: viffd.AuthRequired, Providers: map[string]viffd.AuthProvider{
		"corp":   {Issuer: "https://idp.example.com", ClientID: "viff-cli"},
		"github": {IOs: []string{"github"}},
	}, Iod: viffd.AuthIod{Static: []viffd.IodStatic{{File: file, Principal: "agent:github-iod", IO: "github"}}}}); err != nil {
		t.Fatal(err)
	}
	bearer := func(context.Context) (string, error) {
		b, err := os.ReadFile(file)
		return strings.TrimSpace(string(b)), err
	}

	f := newFake()
	f.repo("signadot/viff").pr(42, "a change", "open", "2026-09-01T00:00:00Z", "abc")
	src := newSource(t, f, github.Config{Repos: []string{"signadot/viff"}})
	viff := client.NewAs(addr, "agent:github-iod", bearer)
	sink := src.Sink()
	sink.Viff = viff
	sink.As = viffio.As(addr, bearer)
	out := viffio.NewOut(sink, src.OutRefs()[0], viff, nil)
	return &asksRig{store: store, srv: srv, fake: f, out: out, addr: addr, tokenFile: file}
}

func (r *asksRig) pass(t *testing.T) {
	t.Helper()
	if err := r.out.Pass(context.Background()); err != nil {
		t.Fatalf("pass: %v", err)
	}
}

// raise raises the gate a firing would, and waits until its question stands.
func (r *asksRig) raise(t *testing.T) {
	t.Helper()
	go func() {
		_, _ = r.srv.Reg.Resolve(context.Background(),
			task.GateQuestion{ID: askedOnPR, Origin: "deploy", Gate: "ok", Reason: "Deploy #42?"},
			task.ToolUse{Name: "mirror.trigger.deploy"})
	}()
	for range 400 {
		if _, ok, _ := r.store.Get(mirror.QuestionRef(askedOnPR)); ok {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("question %s never stood", askedOnPR)
}

// bind binds a GitHub account to a person, as a verified proof would have.
func (r *asksRig) bind(t *testing.T, userID, principal string) {
	t.Helper()
	// The value before the record, as viffd's own bind writes one.
	ref := entity.ViffRef("identity", "github", "account", userID)
	if _, _, err := entity.As(r.srv.Store, entity.SystemIdentity).Write(entity.Write{At: ref.At(), Value: entity.Obj{
		"principal": principal, "iss": "https://github.com", "sub": userID, "claimant": "corp",
	}.Payload()}); err != nil {
		t.Fatal(err)
	}
	grants := []entity.Grant{{At: ref.Segs(), To: map[entity.Level][]string{entity.Read: {principal, "agent:*"}}}}
	if err := entity.Establish(r.srv.Store, ref, entity.SystemIdentity, entity.Ref{}, grants); err != nil {
		t.Fatal(err)
	}
}

// reflect stands a comment on #42 as the in half's reflection would (github.go, commentPayload).
func (r *asksRig) reflect(t *testing.T, id int64, login string, userID int64, body string) {
	t.Helper()
	r.reflectOn(t, 42, id, login, userID, body)
}

// reflectOn is reflect, on the pull request numbered on.
func (r *asksRig) reflectOn(t *testing.T, on int, id int64, login string, userID int64, body string) {
	t.Helper()
	n := strconv.Itoa(on)
	ref := entity.RefAt("github", "signadot", "viff", "comment", n, strconv.FormatInt(id, 10))
	entitytest.Set(t, r.store, ref, entity.Obj{
		"repo": "signadot/viff", "on": on, "id": id, "kind": "conversation",
		"author": login, "authorId": userID, "body": body,
		"url": "https://github.com/signadot/viff/pull/" + n + "#issuecomment-" + strconv.FormatInt(id, 10),
	}.Payload())
}

// status is what the out half recorded beside the input named.
func (r *asksRig) status(t *testing.T, name string) entity.Obj {
	t.Helper()
	e, ok, err := r.store.Get(entity.RefAt("github", "signadot", "viff", "out", name+viffio.StatusSuffix))
	if err != nil || !ok {
		return nil
	}
	return entity.ToObj(e.Payload)
}

func (r *asksRig) question(t *testing.T) entity.Payload {
	t.Helper()
	e, ok, err := r.store.Get(mirror.QuestionRef(askedOnPR))
	if err != nil || !ok {
		t.Fatalf("reading the question: ok %v, %v", ok, err)
	}
	return e.Payload
}

func TestAQuestionOnAPullRequestIsAnsweredByTheAuthorOfAViffComment(t *testing.T) {
	r := newAsksRig(t)
	r.raise(t)
	entitytest.Set(t, r.store, entity.RefAt("github", "signadot", "viff", "out", "deploy"),
		entity.Obj{"to": "pr.42", "text": "Ready to deploy.", "asks": askedOnPR}.Payload())

	// The comment says what is asked and how to answer.
	r.pass(t)
	got := r.fake.commentsOn("signadot/viff", 42)
	if len(got) != 1 || !strings.Contains(got[0], "Deploy #42?") || !strings.Contains(got[0], "`/viff proceed`") {
		t.Fatalf("the comment = %q, want the question and how to answer it", got)
	}

	// Somebody viff does not know says proceed: told so, once, and nothing is answered.
	r.reflect(t, 9500, "stranger", 202, "/viff proceed")
	r.pass(t)
	got = r.fake.commentsOn("signadot/viff", 42)
	if len(got) != 2 || !strings.Contains(got[1], "@stranger") || !strings.Contains(got[1], "do not know who") {
		t.Fatalf("after a stranger's command: comments %q, want one reply telling them", got)
	}
	if entity.Str(r.question(t), "verdict") != "" {
		t.Fatalf("a stranger's command answered: %v", r.question(t))
	}
	r.pass(t)
	if got = r.fake.commentsOn("signadot/viff", 42); len(got) != 2 {
		t.Fatalf("the stranger was told twice: %q", got)
	}

	// Ana, whose GitHub account is bound, says proceed with a word: the ballot is hers, made
	// through this io, vouched for by her GitHub account.
	r.bind(t, "101", "user:ana@example.com")
	r.reflect(t, 9600, "ana", 101, "Looks good.\n/viff proceed ship it\n")
	r.pass(t)
	q := r.question(t)
	if entity.Str(q, "verdict") != "proceed" || entity.Str(q, "by") != "user:ana@example.com" {
		t.Fatalf("ana's command answered %q by %q, want proceed by ana; comments %q; question %v", entity.Str(q, "verdict"), entity.Str(q, "by"), r.fake.commentsOn("signadot/viff", 42), q)
	}
	ballot := entity.Get(entity.Get(q, "ballots"), "user:ana@example.com")
	if entity.Str(ballot, "authenticated") != "github" || entity.Str(ballot, "via") != "agent:github-iod" {
		t.Errorf("ana's ballot = %v, want authenticated by github via this io", ballot)
	}
	if said := strings.Join(entity.Strs(ballot, "say"), " "); !strings.Contains(said, "ship it") {
		t.Errorf("ana's words were dropped: %v", ballot)
	}

	// The comment is redrawn as settled, and a later pass acts on nothing.
	r.pass(t)
	got = r.fake.commentsOn("signadot/viff", 42)
	if !strings.Contains(got[0], "**proceed**") || !strings.Contains(got[0], "ana@example.com") {
		t.Errorf("the comment after settling = %q, want the verdict and who gave it", got[0])
	}
	edits := r.fake.editCount()
	r.pass(t)
	if r.fake.editCount() != edits || len(r.fake.commentsOn("signadot/viff", 42)) != len(got) {
		t.Errorf("a pass after settling wrote to GitHub")
	}
}

// A command naming no option the question offers is told the options, once; the io's own
// comments and comments before its own are never commands.
func TestACommandWithNoSuchOptionIsToldTheOptions(t *testing.T) {
	r := newAsksRig(t)
	r.raise(t)
	r.reflect(t, 8000, "ana", 101, "/viff proceed")
	entitytest.Set(t, r.store, entity.RefAt("github", "signadot", "viff", "out", "deploy"),
		entity.Obj{"to": "pr.42", "asks": askedOnPR}.Payload())
	r.pass(t)
	r.bind(t, "101", "user:ana@example.com")
	// The io's own words, reflected back, are never a command; nor is the account proof, which
	// is told its own outcome and answers nothing; nor is a review comment on a line of the diff,
	// which GitHub numbers from another sequence.
	r.reflect(t, 9650, fakeSelf, 1, "/viff proceed")
	r.reflect(t, 9660, "ana", 101, "/viff link 0123456789abcdef")
	entitytest.Set(t, r.store, entity.RefAt("github", "signadot", "viff", "comment", "42", "9670"), entity.Obj{
		"repo": "signadot/viff", "on": 42, "id": 9670, "kind": "review", "author": "ana", "authorId": 101,
		"body": "/viff proceed", "url": "https://github.com/signadot/viff/pull/42#discussion_r9670",
	}.Payload())
	r.reflect(t, 9700, "ana", 101, "/viff maybe")
	r.pass(t)
	got := r.fake.commentsOn("signadot/viff", 42)
	if len(got) != 3 || !strings.Contains(got[1], "not linked") || !strings.Contains(got[2], "names no option") || !strings.Contains(got[2], "`/viff proceed`") || strings.HasPrefix(got[0], "\n") {
		t.Fatalf("comments %q, want the io's comment with no text before the question, the nonce refused, and one reply naming the options", got)
	}
	r.pass(t)
	if len(r.fake.commentsOn("signadot/viff", 42)) != 3 {
		t.Errorf("ana was told twice")
	}
	if entity.Str(r.question(t), "verdict") != "" {
		t.Errorf("a command before the io's comment, or one with no option, answered: %v", r.question(t))
	}
}

// Two pending questions on one pull request both taking `proceed`: a bare `/viff proceed` is
// unplaceable and told so, and one naming the question answers that one.
func TestACommandTwoQuestionsCouldTakeNamesItsQuestion(t *testing.T) {
	r := newAsksRig(t)
	r.raise(t)
	const second = "mirror.trigger.deploy.ok~2"
	go func() {
		_, _ = r.srv.Reg.Resolve(context.Background(),
			task.GateQuestion{ID: second, Origin: "deploy", Gate: "ok", Reason: "Deploy #42 to prod?"},
			task.ToolUse{Name: "mirror.trigger.deploy"})
	}()
	for range 400 {
		if _, ok, _ := r.store.Get(mirror.QuestionRef(second)); ok {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	entitytest.Set(t, r.store, entity.RefAt("github", "signadot", "viff", "out", "deploy"),
		entity.Obj{"to": "pr.42", "asks": []any{askedOnPR, second}}.Payload())
	r.pass(t)
	r.bind(t, "101", "user:ana@example.com")
	r.reflect(t, 9800, "ana", 101, "/viff proceed")
	r.pass(t)
	got := r.fake.commentsOn("signadot/viff", 42)
	if len(got) != 2 || !strings.Contains(got[1], "could answer") || !strings.Contains(got[1], second) {
		t.Fatalf("a bare proceed with two questions: comments %q, want it told which", got)
	}
	r.reflect(t, 9900, "ana", 101, "/viff proceed "+second+" the prod one")
	r.pass(t)
	e, _, _ := r.store.Get(mirror.QuestionRef(second))
	if entity.Str(e.Payload, "verdict") != "proceed" || entity.Str(r.question(t), "verdict") != "" {
		t.Errorf("naming the second question answered: second %q, first %q", entity.Str(e.Payload, "verdict"), entity.Str(r.question(t), "verdict"))
	}
	ballot := entity.Get(entity.Get(e.Payload, "ballots"), "user:ana@example.com")
	if said := strings.Join(entity.Strs(ballot, "say"), " "); said != "the prod one" {
		t.Errorf("the words after the question's id = %q, want them kept without the id", said)
	}
}

// A reply is judged against the edit the comment showed when it was made, which the status
// keeps, and not the edit the question has by the time the pass runs.
func TestAReplyConsentsToTheEditTheCommentShowed(t *testing.T) {
	r := newAsksRig(t)
	r.raise(t)
	entitytest.Set(t, r.store, entity.RefAt("github", "signadot", "viff", "out", "deploy"),
		entity.Obj{"to": "pr.42", "asks": askedOnPR}.Payload())
	r.pass(t)
	st, _, _ := r.store.Get(entity.RefAt("github", "signadot", "viff", "out", "deploy"+viffio.StatusSuffix))
	if n := ir.Uncomment(entity.Get(entity.Get(st.Payload, "edits"), askedOnPR)); n == nil {
		t.Fatalf("the status does not keep the edit the comment showed: %v", st.Payload)
	}
	// The comment, as the status has it, showed an edit the question has since moved past.
	entitytest.Set(t, r.store, entity.RefAt("github", "signadot", "viff", "out", "deploy"+viffio.StatusSuffix),
		entity.Obj{"edits": entity.Obj{askedOnPR: 7}}.Payload())
	r.bind(t, "101", "user:ana@example.com")
	r.reflect(t, 9600, "ana", 101, "/viff proceed")
	r.pass(t)
	if v := entity.Str(r.question(t), "verdict"); v != "" {
		t.Fatalf("a reply to an edit the question moved past answered: %q", v)
	}
	got := r.fake.commentsOn("signadot/viff", 42)
	if len(got) != 2 || !strings.Contains(got[1], "did not take that answer") {
		t.Errorf("comments %q, want ana told the viff instance refused the stale consent", got)
	}
}

// A `/viff link <nonce>` reply binds the author's GitHub account to whoever the viff instance gave the
// nonce to, on this io's word, and their next command counts.
func TestALinkReplyBindsTheAuthorsAccount(t *testing.T) {
	r := newAsksRig(t)
	if err := r.srv.SetAuth(viffd.Auth{Mode: viffd.AuthOptional, Providers: map[string]viffd.AuthProvider{
		"github": {IOs: []string{"github"}},
	}, Iod: viffd.AuthIod{Static: []viffd.IodStatic{{File: r.tokenFile, Principal: "agent:github-iod", IO: "github"}}}}); err != nil {
		t.Fatal(err)
	}
	r.raise(t)
	entitytest.Set(t, r.store, entity.RefAt("github", "signadot", "viff", "out", "deploy"),
		entity.Obj{"to": "pr.42", "text": "Ready to deploy.", "asks": askedOnPR}.Payload())
	r.pass(t)

	// Bo claims his GitHub login from the CLI, asserted.
	resp, err := client.New(r.addr, "user:bo@example.com").Post("/accounts/github", nil, map[string]any{"login": "bo"})
	if err != nil {
		t.Fatal(err)
	}
	var claim struct {
		Nonce string `tony:"field=nonce"`
	}
	if resp.StatusCode != 201 {
		t.Fatalf("claim = %s", resp.Status)
	}
	if err := client.Decode(resp, &claim); err != nil {
		t.Fatal(err)
	}

	// Said by somebody else, the nonce binds nobody and the sayer is told; said by bo, the
	// account is his, and said again it is spent.
	r.reflect(t, 9700, "mallory", 666, "/viff link "+claim.Nonce+"\nmine now")
	r.pass(t)
	got := r.fake.commentsOn("signadot/viff", 42)
	if len(got) != 2 || !strings.Contains(got[1], "@mallory") || !strings.Contains(got[1], "not linked") {
		t.Fatalf("after mallory's line: comments %q", got)
	}
	r.reflect(t, 9701, "bo", 303, "/viff link "+claim.Nonce)
	r.pass(t)
	got = r.fake.commentsOn("signadot/viff", 42)
	if len(got) != 3 || !strings.Contains(got[2], "@bo linked: in viff, bo is user:bo@example.com") {
		t.Fatalf("after bo's line: comments %q", got)
	}
	rec, ok, err := entity.As(r.store, entity.SystemIdentity).Get(entity.ViffRef("identity", "github", "account", "303"))
	if err != nil || !ok || entity.Str(rec.Payload, "principal") != "user:bo@example.com" || entity.Str(rec.Payload, "login") != "bo" ||
		entity.Str(entity.Get(rec.Payload, "proof"), "method") != "heard" {
		t.Fatalf("the binding = %v (ok=%v err=%v)", rec.Payload, ok, err)
	}
	r.reflect(t, 9702, "bo", 303, "/viff proceed")
	r.pass(t)
	q := r.question(t)
	if entity.Str(q, "verdict") != "proceed" || entity.Str(q, "by") != "user:bo@example.com" {
		t.Fatalf("bo's command after linking answered %q by %q; comments %q", entity.Str(q, "verdict"), entity.Str(q, "by"), r.fake.commentsOn("signadot/viff", 42))
	}
	// Nothing is told twice.
	r.pass(t)
	if n := len(r.fake.commentsOn("signadot/viff", 42)); n != 3 {
		t.Errorf("comments after a quiet pass = %d, want 3", n)
	}
}

// A question asked by opening the pull request it is about: one `open` input carrying `asks`
// opens the pull request, keeps the question's comment on it, hears the answer there, merges it
// at the head decided on when told to, and records a close of a merged one as already closed.
func TestAnOpenInputAsksOnThePullRequestItOpens(t *testing.T) {
	r := newAsksRig(t)
	r.raise(t)
	in := entity.Obj{
		"to":   "branch.viff/candidate/aaaa",
		"open": entity.Obj{"base": "viff/live", "title": "charters: aaaa"},
		"text": "What is on main against what runs.",
		"asks": askedOnPR,
	}
	set := func() {
		t.Helper()
		entitytest.Set(t, r.store, entity.RefAt("github", "signadot", "viff", "out", "candidate"), in.Payload())
	}
	set()
	r.pass(t)
	st := r.status(t, "candidate")
	number, _ := st["number"].(int64)
	if number == 0 || st["comment"] == nil || st["commentPosted"] == nil || st["posted"] == nil {
		t.Fatalf("status after opening with a question = %v, want the pull request and the comment both", st)
	}
	got := r.fake.commentsOn("signadot/viff", int(number))
	if len(got) != 1 || !strings.Contains(got[0], "Deploy #42?") || !strings.Contains(got[0], "What is on main") {
		t.Fatalf("the comment on the opened pull request = %q", got)
	}
	// Nothing changed: nothing written.
	r.pass(t)
	if r.fake.openedCount() != 1 || r.fake.editCount() != 0 {
		t.Fatalf("a quiet pass wrote: %d opened, %d edits", r.fake.openedCount(), r.fake.editCount())
	}
	// Answered there, by a bound author.
	r.bind(t, "101", "user:ana@example.com")
	r.reflectOn(t, int(number), 9800, "ana", 101, "/viff proceed")
	r.pass(t)
	if q := r.question(t); entity.Str(q, "verdict") != "proceed" || entity.Str(q, "by") != "user:ana@example.com" {
		t.Fatalf("the answer on the opened pull request: %v", q)
	}
	// Told to merge, at the head decided on: merged once, by this io, and the record says so.
	in["merge"] = entity.Obj{"sha": "cccc3333", "method": "merge"}
	set()
	r.pass(t)
	if st := r.status(t, "candidate"); st["merged"] != true || st["commit"] == nil || r.fake.merges != 1 {
		t.Fatalf("merging: status %v, merges %d", st, r.fake.merges)
	}
	r.pass(t)
	if r.fake.merges != 1 {
		t.Errorf("merged twice")
	}
	// Told to close once merged: recorded as already closed, and nothing is closed.
	in["closed"] = true
	set()
	r.pass(t)
	if st := r.status(t, "candidate"); st["closed"] != true || st["already"] != true || r.fake.closeCount() != 0 {
		t.Fatalf("closing a merged pull request: status %v, closes %d", st, r.fake.closeCount())
	}
}

// A superseded candidate's pull request is closed unmerged, once.
func TestAnOpenInputClosesThePullRequestItOpened(t *testing.T) {
	r := newAsksRig(t)
	r.raise(t)
	in := entity.Obj{"to": "branch.viff/candidate/bbbb", "open": entity.Obj{"base": "viff/live", "title": "charters: bbbb"}, "asks": askedOnPR}
	entitytest.Set(t, r.store, entity.RefAt("github", "signadot", "viff", "out", "superseded"), in.Payload())
	r.pass(t)
	in["closed"] = true
	entitytest.Set(t, r.store, entity.RefAt("github", "signadot", "viff", "out", "superseded"), in.Payload())
	r.pass(t)
	if st := r.status(t, "superseded"); st["closed"] != true || st["already"] != nil || r.fake.closeCount() != 1 {
		t.Fatalf("closing: status %v, closes %d", st, r.fake.closeCount())
	}
	r.pass(t)
	if r.fake.closeCount() != 1 {
		t.Errorf("closed twice")
	}
}

// A PULL REQUEST SOMEBODY ELSE OPENED IS ASKED ABOUT AND HELD: one input keeps the question's
// comment, viff's request for changes and its pending status on the head together; the answer
// comes from a reply as ever; then a dismissal lifts every request for changes the io left, and
// the status turns green. A quiet pass does nothing, and a later request for changes is posted
// again.
func TestAPullRequestToMainIsAskedAboutAndHeldUntilAnswered(t *testing.T) {
	r := newAsksRig(t)
	r.raise(t)
	ref := entity.RefAt("github", "signadot", "viff", "out", "main-pr")
	in := entity.Obj{
		"to":           "pr.42",
		"text":         "The charters change here; it installs once answered and merged.",
		"asks":         askedOnPR,
		"review":       entity.Obj{"event": "request-changes", "body": "viff: waiting for an answer to the question below"},
		"commitStatus": entity.Obj{"viff": entity.Obj{"state": "pending", "description": "waiting for an answer"}},
	}
	set := func() { t.Helper(); entitytest.Set(t, r.store, ref, in.Payload()) }
	set()
	r.pass(t)
	st := r.status(t, "main-pr")
	if st["comment"] == nil || st["reviewEvent"] != "request-changes" || st["review"] == nil || st["error"] != nil {
		t.Fatalf("status after asking = %v", st)
	}
	pr := r.fake.repos["signadot/viff"].find(42)
	if len(pr.reviews) != 1 || pr.reviews[0].state != "CHANGES_REQUESTED" {
		t.Fatalf("reviews on the pull request = %+v", pr.reviews)
	}
	if got := r.fake.statusesOn("signadot/viff"); len(got) != 1 || got[0].sha != "abc" || got[0].context != "viff" || got[0].state != "pending" {
		t.Fatalf("statuses = %+v", got)
	}
	if got := r.fake.commentsOn("signadot/viff", 42); len(got) != 1 || !strings.Contains(got[0], "Deploy #42?") {
		t.Fatalf("comments = %q", got)
	}
	// Quiet: nothing more is posted.
	r.pass(t)
	if len(pr.reviews) != 1 || len(r.fake.statusesOn("signadot/viff")) != 1 {
		t.Fatalf("a quiet pass posted: %d reviews, %d statuses", len(pr.reviews), len(r.fake.statusesOn("signadot/viff")))
	}
	// Answered on the pull request by a bound author.
	r.bind(t, "101", "user:ana@example.com")
	r.reflect(t, 9900, "ana", 101, "/viff proceed")
	r.pass(t)
	if q := r.question(t); entity.Str(q, "verdict") != "proceed" {
		t.Fatalf("the answer on the pull request: %v", q)
	}
	// Answered yes: every request for changes the io left is dismissed, and the status is green.
	in["review"] = entity.Obj{"event": "dismiss", "body": "viff: answered yes; the charters install once this merges"}
	in["commitStatus"] = entity.Obj{"viff": entity.Obj{"state": "success", "description": "answered yes; installs once merged"}}
	set()
	r.pass(t)
	st = r.status(t, "main-pr")
	if st["reviewEvent"] != "dismiss" || st["dismissed"] != int64(1) || pr.reviews[0].state != "DISMISSED" {
		t.Fatalf("after the yes: status %v, reviews %+v", st, pr.reviews)
	}
	if got := r.fake.statusesOn("signadot/viff"); len(got) != 2 || got[1].state != "success" {
		t.Fatalf("statuses after the yes = %+v", got)
	}
	// A later request for changes (a new change pushed) is posted again.
	in["review"] = entity.Obj{"event": "request-changes", "body": "viff: a new change waits for an answer", "sha": "abc"}
	set()
	r.pass(t)
	if len(pr.reviews) != 2 || pr.reviews[1].state != "CHANGES_REQUESTED" {
		t.Fatalf("a new request for changes: %+v", pr.reviews)
	}
	if st := r.status(t, "main-pr"); st["reviewSha"] != "abc" {
		t.Fatalf("status %v, want the sha the review names", st)
	}
	// The pull request stops changing what was asked about: the question leaves the comment, which
	// says so in place, and the hold is dismissed.
	in["text"] = "This pull request no longer changes the charters, so nothing is asked here."
	in["asks"] = []any{}
	in["review"] = entity.Obj{"event": "dismiss", "body": "viff: this pull request no longer changes the charters"}
	set()
	r.pass(t)
	got := r.fake.commentsOn("signadot/viff", 42)
	if len(got) != 1 || strings.Contains(got[0], "Deploy #42?") || !strings.Contains(got[0], "no longer changes the charters") {
		t.Fatalf("the comment once nothing is asked = %q", got)
	}
	if pr.reviews[1].state != "DISMISSED" {
		t.Fatalf("the hold once nothing is asked: %+v", pr.reviews)
	}
}
