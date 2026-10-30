package versed

import (
	"cmp"
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/signadot/verse/api"
	"github.com/signadot/verse/entity"
	"github.com/signadot/verse/mirror"
	"github.com/signadot/verse/runner"
	"github.com/signadot/verse/runner/client"
	"github.com/signadot/verse/task"
)

// Runners at the door (git-issue pb6fwh8gh12krmb7vhn0).
//
// WHO A RUNNER IS, VERSE DECIDES AT REGISTRATION AND NOWHERE ELSE. Every registration names the
// subject of the runner's token (api.Runner.Subject): its workload token's, on Kubernetes the
// pod's projected service-account token, `system:serviceaccount:<namespace>:<name>`, from the
// issuer the deployment names for its iods (auth.iod.issuer); or a static token's hash, for a
// runner off Kubernetes. The one who writes the registration is the one
// who vouches for the runner, at the moment they are already doing something deliberate, and
// there is no per-launch check to forget.
//
// ONE TABLE AT THE DOOR. A workload token is verified as an iod's is (iod.go), and its subject is
// looked up first among the iods (auth.iod.accounts) and then among the registrations. A match is
// `runner:<identity>`, under every mode, and `whoami` says `by: runner`. A runner's own requests
// are made as itself, so a bearer that proves a runner beside a header naming anyone else is
// refused, as a person's is. A task's verb on verse is the task's, not the runner's: it names
// the task beside the runner's token (api.TaskHeader), and is admitted as the task's owner
// through the task's record, which versed wrote at the spawn and which names the runner
// (relayTask).
//
// A RUNNER IS NEVER TAKEN AT ITS WORD. A header naming a `runner:` with no bearer that proves it
// is refused at the door, on the name alone and without reading the registrations, under every
// mode. `verse up`'s local runner, like any runner off Kubernetes, proves itself with a static
// token.
//
// WHY IT MATTERS NOW. Versed writes a task's reports as the runner itself, from what it mirrors
// off the runner's stream, so a runner has never had to call verse and nothing at the door has
// had to recognise one. The first verb a runner performs on verse as itself (a charter it installs
// for a task) is gated by verse's permissions only if the performer is a principal they can
// name: this is what makes `runner:<identity>` such a principal.

// runnerProvider is what `whoami` says a runner was verified `by`, as iodProvider is an iod's.
const runnerProvider = "runner"

// THE DOOR READS THE REGISTRATIONS AS KEPT, NOT AS STORED. Every request a runner makes, and
// every request naming one, asks who the registrations say it is, so they are read from the store
// at most once per registrationsFresh and kept on the Server between. An add or a remove through
// verse forgets what was kept, so what an operator changes is what the next request meets; a
// registration written past those, straight into the store, is met within registrationsFresh.
//
// A READ THAT DID NOT HAPPEN IS NOT A RUNNER THAT IS NOT REGISTERED. Each lookup answers the
// read's error beside its answer, and every caller refuses on it: a door that could not look up
// whether a name needs proof has not found that it needs none.
const registrationsFresh = 5 * time.Second

// keptRegistrations is the registrations as last read, and when.
type keptRegistrations struct {
	mu   sync.Mutex
	at   time.Time
	list []api.Runner // nil is nothing kept
}

// registrations is the baseline registrations, as kept while fresh and read again otherwise.
func (s *Server) registrations() ([]api.Runner, error) {
	k := &s.keptRegistrations
	k.mu.Lock()
	defer k.mu.Unlock()
	if k.list != nil && time.Since(k.at) < registrationsFresh {
		return k.list, nil
	}
	list, err := installedRunnersErr(s.Store)
	if err != nil {
		return nil, fmt.Errorf("the runners' registrations could not be read: %w", err)
	}
	if list == nil {
		list = []api.Runner{}
	}
	k.list, k.at = list, time.Now()
	return list, nil
}

// forgetRegistrations drops what was kept, after a write to the registrations through verse.
func (s *Server) forgetRegistrations() {
	s.keptRegistrations.mu.Lock()
	s.keptRegistrations.list = nil
	s.keptRegistrations.mu.Unlock()
}

// runnerBySubject is the registration naming sub as its runner's subject, read as the system.
// A static token's subject is found only by a static token (staticRunner), and a workload
// token's only by a workload token, so neither kind of proof can claim the other's registration.
func (s *Server) runnerBySubject(sub string) (api.Runner, bool, error) {
	if sub == "" || runner.IsStaticSubject(sub) {
		return api.Runner{}, false, nil
	}
	return s.registrationWhere(func(e api.Runner) bool { return e.Subject == sub })
}

// staticRunner is the registration a bearer that is not a JWT proves, where one names the
// bearer's hash as its subject.
func (s *Server) staticRunner(token string) (api.Runner, bool, error) {
	sub, ok := runner.StaticSubjectOf(token)
	if !ok {
		return api.Runner{}, false, nil
	}
	return s.registrationWhere(func(e api.Runner) bool { return e.Subject == sub })
}

// verifiedRunner is the registration of the runner acting as principal. Every registration names
// a subject, so every registered runner is one verse verifies.
func (s *Server) verifiedRunner(principal string) (api.Runner, bool, error) {
	if entity.KindOf(principal) != entity.RunnerKind {
		return api.Runner{}, false, nil
	}
	return s.registrationWhere(func(e api.Runner) bool { return e.Principal() == principal })
}

// registrationWhere is the first registration match holds for.
func (s *Server) registrationWhere(match func(api.Runner) bool) (api.Runner, bool, error) {
	list, err := s.registrations()
	if err != nil {
		return api.Runner{}, false, err
	}
	for _, e := range list {
		if match(e) {
			return e, true, nil
		}
	}
	return api.Runner{}, false, nil
}

// THE TOKEN TOWARD A RUNNER IS MINTED FOR THAT RUNNER ALONE. Versed is an issuer of its own
// (auth.issuer, issuer.go), and what it presents to a runner is a token it signs: `iss` its
// issuer url, `sub` verse, and `aud` the subject on the runner's registration, good for ten
// minutes. The runner holds versed's tokens to its own name, so a copy taken at one runner, or
// sent to a url somebody typed wrong, opens no other runner's door; and the key is the proof,
// so there is no projected token of versed's to mount and no subject of versed's for a runner
// to be told. A verse that issues nothing presents nothing, and refuses to register a subject it
// could never prove itself to (addRunner).
//
// THE TOKEN GOES WHERE A VOUCHED IDENTITY IS. Versed presents a token to every registration, for
// its subject, and to nothing else.

// How long a token toward a runner is good for, and how long before its expiry the next call
// mints a fresh one rather than present it.
const (
	runnerTokenLife  = 10 * time.Minute
	runnerTokenRenew = time.Minute
)

// runnerTokens is the tokens versed has minted toward runners, by audience, each kept until it
// is about to expire: one signature per runner per ten minutes, not one per call.
type runnerTokens struct {
	mu sync.Mutex
	by map[string]mintedRunnerToken
}

type mintedRunnerToken struct {
	token   string
	expires time.Time
}

// runnerTokenFor is the token source for calls to a runner whose door takes aud: the kept token
// while it is good, a fresh one otherwise. Nil where this verse issues nothing.
func (s *Server) runnerTokenFor(aud string) func() (string, error) {
	if s.issuer == nil {
		return nil
	}
	return func() (string, error) {
		s.runnerTokens.mu.Lock()
		defer s.runnerTokens.mu.Unlock()
		now := s.now()
		if kept, ok := s.runnerTokens.by[aud]; ok && now.Before(kept.expires.Add(-runnerTokenRenew)) {
			return kept.token, nil
		}
		var b [16]byte
		if _, err := rand.Read(b[:]); err != nil {
			return "", err
		}
		expires := now.Add(runnerTokenLife)
		tok, err := s.issuer.sign(map[string]any{
			"iss": s.issuer.url, "sub": runner.VerseSubject, "aud": aud,
			"iat": now.Unix(), "nbf": now.Unix(), "exp": expires.Unix(),
			"jti": hex.EncodeToString(b[:]),
		})
		if err != nil {
			return "", fmt.Errorf("minting this verse's token toward a runner: %w", err)
		}
		if s.runnerTokens.by == nil {
			s.runnerTokens.by = map[string]mintedRunnerToken{}
		}
		s.runnerTokens.by[aud] = mintedRunnerToken{token: tok, expires: expires}
		return tok, nil
	}
}

// runnerTransport is how versed reaches a runner whose door takes aud: presenting a token minted
// for that audience, where this verse issues; bare otherwise.
func (s *Server) runnerTransport(aud string) http.RoundTripper {
	source := s.runnerTokenFor(aud)
	if source == nil {
		return nil
	}
	return client.Bearer(nil, source)
}

// transportFor is how versed reaches the registered runner e: with a token minted for its subject.
func (s *Server) transportFor(e api.Runner) http.RoundTripper {
	return s.runnerTransport(e.Subject)
}

// dialAt is the registered runner at url, reached as its registration says: with a token for its
// subject. A url no registration holds any more is reached bare,
// since nothing says what was vouched for there; a guarded runner then reads as unreachable,
// which is what the orphan path expects of a runner whose registration is gone.
//
// A registrations read that failed is reached bare too: presenting no token is the side that
// gives nothing away, and the next reconcile or spawn reads them again.
func (s *Server) dialAt(url string) client.Launcher {
	if e, ok, _ := s.registrationWhere(func(e api.Runner) bool { return e.URL == url }); ok {
		return client.HTTPOver(url, s.transportFor(e))
	}
	return client.HTTP(url)
}

// iodAccountNamed is whether sub is an iod's: in auth.iod.accounts, or the hash of a token in
// auth.iod.static. Either is a subject the door would take for the iod before it looked among the
// registrations.
func (s *Server) iodAccountNamed(sub string) bool {
	if s.auth == nil || s.auth.iod == nil {
		return false
	}
	if _, ok := s.auth.iod.accounts[sub]; ok {
		return true
	}
	for h := range s.auth.iod.static {
		if runner.StaticSubjectOfHash(h) == sub {
			return true
		}
	}
	return false
}

// probeRegistered asks a registered runner what it is, as this verse, and holds it to its
// registration: a runner that answered as a runner and did not prove it is the registration's
// subject is Unverified, with why, and what it said is not read.
func (s *Server) probeRegistered(e api.Runner) (client.Reach, runner.Description, error) {
	reach, desc, token, why := client.ProbeOver(e.URL, s.transportFor(e))
	if reach != client.IsRunner {
		return reach, desc, why
	}
	if err := s.verifyRunnerToken(token, e.Subject); err != nil {
		return client.Unverified, runner.Description{}, err
	}
	return reach, desc, nil
}

// verifyRunnerToken is whether the token a runner answered its describe with proves it is
// subject: a workload token from the deployment's issuer naming that subject, or, where the
// subject is a static token's, a token hashing to it.
func (s *Server) verifyRunnerToken(token, subject string) error {
	if token == "" {
		return fmt.Errorf("it answered no %s: the runner has no token of its own (VERSE_TOKEN_FILE), "+
			"or no `verse:` block names the verse it would answer one to", runner.TokenHeader)
	}
	if runner.IsStaticSubject(subject) {
		got, ok := runner.StaticSubjectOf(token)
		if !ok || subtle.ConstantTimeCompare([]byte(got), []byte(subject)) != 1 {
			return fmt.Errorf("its %s is not the static token the registration names by its hash", runner.TokenHeader)
		}
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), discoveryTimeout)
	defer cancel()
	v, refused := s.verifyToken(ctx, token)
	if refused != nil {
		return fmt.Errorf("its %s did not verify: %s", runner.TokenHeader, refused.msg)
	}
	if !v.provider.iod {
		return fmt.Errorf("its %s is from %s, which is not the deployment's workload issuer", runner.TokenHeader, v.provider.name)
	}
	if v.sub != subject {
		return fmt.Errorf("its %s names %s, and the registration says %s", runner.TokenHeader, v.sub, subject)
	}
	return nil
}

// removeDescribed takes what a runner described off its registration, where the runner did not
// prove it is its registration's subject: a runner nobody verified has its words recorded as
// nobody's, so a spawn naming it is refused until `verse runner check` finds it proving itself.
// Why is the caller's to say, not the registration's to keep: an unverified answer is one way a
// describe fails, and the others (no answer) leave the description as it was.
func removeDescribed(w entity.Store, name string) error {
	_, _, err := w.Write(entity.Write{At: runnerEntityRef(name).At(), Value: entity.Obj{
		"described": entity.Removed(), "offers": entity.Removed(), "verified": entity.Removed(),
		"unverified": entity.Removed(),
	}.Payload()})
	return err
}

// THE HELLO. A registered runner says it is here: POST /runners/hello with its
// own token, in a loop until a 2xx (runnerd.Guard.Announce). The door is the acknowledgement,
// since a token whose subject no registration names is refused before this handler runs; what
// the handler adds is the describe, then and there, so a pod that comes up after its
// registration, or comes back after a restart, is verified within seconds and without anyone
// running `verse runner check`. The answer is the check's.
//
// A 2XX MEANS THE RUNNER WAS DESCRIBED, AND NOTHING LESS. A runner nothing answered at is
// answered 503, and so is one whose describe another caller has in flight: in both, verse has
// not yet heard what the runner is, and the runner's own backoff is what tries again, for as long
// as it takes its Service to route to it. A runner that answered and did not prove itself is
// answered 200 with `not verified`: its url reached it, and what is wrong is its token, which
// trying again will not fix and which the runner logs.

// helloRetries is how many times the hello's describe is asked of a runner nothing answered at,
// and how long between: the pod announced itself before its Service routed to it.
const helloRetries = 3

var helloRetryWait = time.Second

// helloRunner is POST /runners/hello: the check, asked by the runner itself.
func (s *Server) helloRunner(w http.ResponseWriter, r *http.Request) {
	if authenticatedBy(r) != runnerProvider {
		http.Error(w, "a runner announces itself with its own token, and this request carries none "+
			"that proves a registered runner", http.StatusBadRequest)
		return
	}
	// The door settled the principal from the token's subject, and one verified principal is
	// one registration (addRunner), so the principal names the registration.
	e, ok, err := s.verifiedRunner(principalOf(r))
	if err != nil {
		http.Error(w, err.Error(), http.StatusServiceUnavailable)
		return
	}
	if !ok {
		http.Error(w, "the token proves a runner no registration names any more", http.StatusNotFound)
		return
	}
	out := api.RunnerCheck{At: runnerEntityRef(e.Name).String(), URL: e.URL}
	if _, busy := s.helloing.LoadOrStore(e.Name, true); busy {
		out.Reach, out.Why = "being described", "a describe of this registration is in flight already"
		writeOut(w, r, http.StatusServiceUnavailable, out)
		return
	}
	defer s.helloing.Delete(e.Name)
	out, code, err := s.describeAgain(s.Store, e, helloRetries)
	if err != nil {
		http.Error(w, err.Error(), code)
		return
	}
	if out.Reach == client.Unreachable.String() {
		writeOut(w, r, http.StatusServiceUnavailable, out)
		return
	}
	writeOut(w, r, http.StatusOK, out)
}

// describeAgain asks the runner e what it is now and holds it to its registration, recording on
// the registration in store what it said, or that it did not prove itself. The answer is the
// check's. A status other than 200 is a failure to record, with why. A runner nothing answers at
// is asked tries times, helloRetryWait apart, before it is called unreachable.
func (s *Server) describeAgain(store entity.Store, e api.Runner, tries int) (api.RunnerCheck, int, error) {
	var (
		reach client.Reach
		desc  runner.Description
		why   error
	)
	for i := 0; ; i++ {
		reach, desc, why = s.probeRegistered(e)
		if reach != client.Unreachable || i+1 >= tries {
			break
		}
		time.Sleep(helloRetryWait)
	}
	out := api.RunnerCheck{At: runnerEntityRef(e.Name).String(), URL: e.URL, Reach: reach.String()}
	if why != nil {
		out.Why = why.Error()
	}
	switch reach {
	case client.IsRunner:
		if err := desc.Check(); err != nil {
			return out, http.StatusBadGateway, fmt.Errorf("runner %q: %s does not say what it offers and the networks it builds: %v; its registration "+
				"keeps what it said before", e.Name, e.URL, err)
		}
		out.Offers = desc.Offers
		if err := writeDescribed(store, e.Name, desc); err != nil {
			return out, http.StatusInternalServerError, err
		}
	case client.Unverified:
		if err := removeDescribed(store, e.Name); err != nil {
			return out, http.StatusInternalServerError, err
		}
	}
	return out, http.StatusOK, nil
}

// relayTask admits a request runner e carries for a task it hosts, as the task's owner. THE
// BINDING IS THE TASK'S RECORD, which versed wrote at the spawn: its `runnerName` is e's
// registration, its `runnerSubject` the subject e proved, and the task has not ended. A runner
// can forge none of it, since it would have to have been given the task by a spawn versed
// performed. A registration rewritten under the same name with another subject carries nothing
// of the tasks the old one hosted, and a record from before subjects were recorded carries
// nothing either.
//
// The owner is the record's (entity.OwnerOf): whoever launched the task, or the owner of the
// charter whose rule spawned it, or the asker a rule's `as: subject` acts for. The runner names
// nobody, and a name beside the task names that owner or is refused. A task the system owns is
// relayed for nobody, since a request as the system is one no runner makes.
//
// via is the runner, as an io is the via of a person it relays (whoami), and the handler knows
// the task (relayedTask).
func (s *Server) relayTask(e api.Runner, presented, id string) (principal, provider, via string, _ *turnedAway) {
	refuse := func(why string, args ...any) (string, string, string, *turnedAway) {
		return "", "", "", turnAway(http.StatusForbidden, "runner %s carries a request for task %s, and "+
			why, append([]any{e.Principal(), id}, args...)...)
	}
	scope, _ := s.hostedScope(id)
	store := entity.As(s.storeForScope(scope), entity.SystemMirror)
	ref := mirror.Ref(id)
	rec, ok, err := store.Get(ref)
	if err != nil {
		return "", "", "", turnAway(http.StatusServiceUnavailable, "task %s's record could not be read, so "+
			"whether runner %s hosts it is not known: %v", id, e.Principal(), err)
	}
	if !ok {
		return refuse("no task %s stands", id)
	}
	if got, want := entity.Str(rec.Payload, "runnerName"), runnerEntityRef(e.Name).String(); got != want {
		return refuse("its record says it runs on %s, not %s", cmp.Or(got, "this verse's own runtime"), want)
	}
	if got := entity.Str(rec.Payload, "runnerSubject"); got == "" || got != e.Subject {
		return refuse("its record does not say it was launched to the subject %s proves", e.Principal())
	}
	if st := task.Status(entity.Str(rec.Payload, "status")); st == "" || st.Terminal() {
		return refuse("it is not running (%s)", cmp.Or(string(st), "no status"))
	}
	owner, err := entity.OwnerOf(store, ref)
	if err != nil {
		return "", "", "", turnAway(http.StatusServiceUnavailable, "whose task %s is could not be read: %v", id, err)
	}
	if owner == "" || entity.IsSystem(owner) {
		return refuse("the task is the system's, and a runner carries no request as the system")
	}
	if presented != "" && presented != owner {
		return refuse("%s names %s, where the task is %s's: the request is made as the task's owner",
			api.PrincipalHeader, presented, owner)
	}
	return owner, runnerProvider, e.Principal(), nil
}

// relayTaskKey is the context key of the task a runner's request was carried for.
type relayTaskKey struct{}

// relayingTask marks r as carried by its runner for the task id, admitted through the task's record.
func relayingTask(r *http.Request, id string) *http.Request {
	return r.WithContext(context.WithValue(r.Context(), relayTaskKey{}, id))
}

// relayedTask is the task a runner carried r for, "" where it carried none.
func relayedTask(r *http.Request) string {
	id, _ := r.Context().Value(relayTaskKey{}).(string)
	return id
}
