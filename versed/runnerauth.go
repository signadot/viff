package versed

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/signadot/verse/api"
	"github.com/signadot/verse/entity"
	"github.com/signadot/verse/runner"
	"github.com/signadot/verse/runner/client"
)

// Runners at the door (git-issue pb6fwh8gh12krmb7vhn0).
//
// WHO A RUNNER IS, VERSE DECIDES AT REGISTRATION AND NOWHERE ELSE. A registration may name the
// subject of the runner's workload token (api.Runner.Subject): on Kubernetes the pod's projected
// service-account token, `system:serviceaccount:<namespace>:<name>`, from the issuer the
// deployment names for its iods (auth.iod.issuer). The one who writes the registration is the one
// who vouches for the runner, at the moment they are already doing something deliberate, and
// there is no per-launch check to forget.
//
// ONE TABLE AT THE DOOR. A workload token is verified as an iod's is (iod.go), and its subject is
// looked up first among the iods (auth.iod.accounts) and then among the registrations. A match is
// `runner:<identity>`, under every mode, and `whoami` says `by: runner`. A runner's own requests
// are made as itself, so a bearer that proves a runner beside a header naming anyone else is
// refused, as a person's is. A task's verb on verse is the task's, not the runner's: it names
// the task's owner beside the runner's token, and is admitted through the task's record, which
// versed wrote at the spawn and which names the runner. That relay comes with the verbs.
//
// A NAME VERSE CAN VERIFY IS NO LONGER TAKEN ON SOMEBODY'S WORD. A header naming the principal of
// a runner registered with a subject, with no bearer that proves it, is refused; a runner
// registered without one is asserted, as every runner was, and the refusal names what the
// registration lacks. So `verse up`'s embedded runner, a laptop's runner and the test suite's
// faces go on as they were, and a deployment that registers its runners with their subjects
// closes the door on their names.
//
// WHY IT MATTERS NOW. Versed writes a task's reports as the runner itself, from what it mirrors
// off the runner's stream, so a runner has never had to call verse and nothing at the door has
// had to recognise one. The first verb a runner performs on verse as itself (a charter it installs
// for a task) is gated by verse's permissions only if the performer is a principal they can
// name: this is what makes `runner:<identity>` such a principal.

// runnerProvider is what `whoami` says a runner was verified `by`, as iodProvider is an iod's.
const runnerProvider = "runner"

// runnerBySubject is the registration naming sub as its runner's subject, read as the system:
// the registrations are a handful, and a token presenting a subject that is no iod's is rare.
func (s *Server) runnerBySubject(sub string) (api.Runner, bool) {
	for _, e := range installedRunners(s.Store) {
		if e.Subject != "" && e.Subject == sub {
			return e, true
		}
	}
	return api.Runner{}, false
}

// verifiedRunner is the registration of the runner acting as principal, where that registration
// names a subject: a runner whose name verse can verify, and so does not take on its word.
func (s *Server) verifiedRunner(principal string) (api.Runner, bool) {
	if entity.KindOf(principal) != entity.RunnerKind {
		return api.Runner{}, false
	}
	for _, e := range installedRunners(s.Store) {
		if e.Subject != "" && e.Principal() == principal {
			return e, true
		}
	}
	return api.Runner{}, false
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
// THE TOKEN GOES WHERE A VOUCHED IDENTITY IS. Versed presents a token to a registration that
// names a subject, and to the default runner `verse up -runner` names, which is the operator's
// own configuration and gets the audience `runner`, which a registered runner's door does not
// take. A registration without a subject is a runner taken at its word, and the handshake runs
// in neither direction: its url was written at runtime by whoever may register a runner, and a
// token presented there before anything proved what answers is a token given away. Such a runner
// with a door of its own installs on its word and refuses the launches.

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
			"iss": s.issuer.url, "sub": "verse", "aud": aud,
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

// transportFor is how versed reaches the runner e: with a token minted for its subject where the
// registration names one, and bare otherwise.
func (s *Server) transportFor(e api.Runner) http.RoundTripper {
	if e.Subject == "" {
		return nil
	}
	return s.runnerTransport(e.Subject)
}

// Dial is the default runner at url, the one `verse up -runner` names, reached as this verse
// with a token for the audience `runner`.
func (s *Server) Dial(url string) client.Launcher {
	return client.HTTPOver(url, s.runnerTransport(runner.DefaultAudience))
}

// dialAt is the registered runner at url, reached as its registration says: with the token
// where the registration names a subject. A url no registration holds any more is reached bare,
// since nothing says what was vouched for there; a guarded runner then reads as unreachable,
// which is what the orphan path expects of a runner whose registration is gone.
func (s *Server) dialAt(url string) client.Launcher {
	if e, ok := s.registeredAt(url); ok {
		return client.HTTPOver(url, s.transportFor(e))
	}
	return client.HTTP(url)
}

// registeredAt is the registration whose url is url.
func (s *Server) registeredAt(url string) (api.Runner, bool) {
	for _, e := range installedRunners(s.Store) {
		if e.URL == url {
			return e, true
		}
	}
	return api.Runner{}, false
}

// iodAccountNamed is whether sub is an iod's, in auth.iod.accounts: a subject the door would
// take for the iod before it looked among the registrations.
func (s *Server) iodAccountNamed(sub string) bool {
	if s.auth == nil || s.auth.iod == nil {
		return false
	}
	_, ok := s.auth.iod.accounts[sub]
	return ok
}

// probeRegistered asks a registered runner what it is, as this verse, and holds it to its
// registration: where the registration names a subject, a runner that answered as a runner and
// did not prove it is that subject is Unverified, with why, and what it said is not read.
func (s *Server) probeRegistered(e api.Runner) (client.Reach, runner.Description, error) {
	reach, desc, token, why := client.ProbeOver(e.URL, s.transportFor(e))
	if reach != client.IsRunner || e.Subject == "" {
		return reach, desc, why
	}
	if err := s.verifyRunnerToken(token, e.Subject); err != nil {
		return client.Unverified, runner.Description{}, err
	}
	return reach, desc, nil
}

// verifyRunnerToken is whether the token a runner answered its describe with proves it is
// subject: a workload token from the deployment's issuer, naming that subject.
func (s *Server) verifyRunnerToken(token, subject string) error {
	if token == "" {
		return fmt.Errorf("it answered no %s: the runner has no token of its own (VERSE_TOKEN_FILE), "+
			"or no `verse:` block names the verse it would answer one to", runner.TokenHeader)
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

// verifiedNow is what a registration records as `verified` for a runner that proved itself now.
func (s *Server) verifiedNow(e api.Runner) string {
	if e.Subject == "" {
		return ""
	}
	return s.now().UTC().Format(time.RFC3339)
}

// writeUnverified records why a runner did not prove it is its registration's subject, in place
// of what it described: a runner nobody verified has its words recorded as nobody's, so a spawn
// naming it is refused until `verse runner check` finds it proving itself.
func writeUnverified(w entity.Store, name string, why error) error {
	_, _, err := w.Write(entity.Write{At: runnerEntityRef(name).At(), Value: entity.Obj{
		"described": entity.Removed(), "offers": entity.Removed(), "verified": entity.Removed(),
		"unverified": why.Error(),
	}.Payload()})
	return err
}

// THE HELLO. A runner registered with a subject says it is here: POST /runners/hello with its
// own token, in a loop until a 2xx (runnerd.Guard.Announce). The door is the acknowledgement,
// since a token whose subject no registration names is refused before this handler runs; what
// the handler adds is the describe, then and there, so a pod that comes up after its
// registration, or comes back after a restart, is verified within seconds and without anyone
// running `verse runner check`. The answer is the check's, and a 200 with `not verified` or
// `unreachable` in it is still a 200: the runner's identity is settled, and what is wrong is
// its url or its token, which trying again will not fix and which the runner logs.

// helloRetries is how many times the hello's describe is asked of a runner nothing answered at,
// and how long between: the pod announced itself before its Service routed to it.
const helloRetries = 3

var helloRetryWait = time.Second

// helloRunner is POST /runners/hello: the check, asked by the runner itself.
func (s *Server) helloRunner(w http.ResponseWriter, r *http.Request) {
	if authenticatedBy(r) != runnerProvider {
		http.Error(w, "a runner announces itself with its workload token, and this request carries none "+
			"that proves a registered runner: a runner registered without a subject has nothing to "+
			"announce, and is described when verse asks", http.StatusBadRequest)
		return
	}
	// The door settled the principal from the token's subject, and one verified principal is
	// one registration (addRunner), so the principal names the registration.
	e, ok := s.verifiedRunner(principalOf(r))
	if !ok {
		http.Error(w, "the token proves a runner no registration names any more", http.StatusNotFound)
		return
	}
	out := api.RunnerCheck{At: runnerEntityRef(e.Name).String(), URL: e.URL}
	if _, busy := s.helloing.LoadOrStore(e.Name, true); busy {
		out.Reach, out.Why = "being described", "a describe of this registration is in flight already"
		writeOut(w, r, http.StatusOK, out)
		return
	}
	defer s.helloing.Delete(e.Name)
	out, code, err := s.describeAgain(s.Store, e, helloRetries)
	if err != nil {
		http.Error(w, err.Error(), code)
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
		if err := writeDescribed(store, e.Name, desc, s.verifiedNow(e)); err != nil {
			return out, http.StatusInternalServerError, err
		}
	case client.Unverified:
		if err := writeUnverified(store, e.Name, why); err != nil {
			return out, http.StatusInternalServerError, err
		}
	}
	return out, http.StatusOK, nil
}
