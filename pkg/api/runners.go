package api

import (
	"fmt"
	"net/url"
	"strings"

	"github.com/signadot/verse/pkg/entity"
	"github.com/signadot/verse/pkg/runner"
)

// Runner is one installed runner, as `POST /runners` takes it and `GET /runners/{name}` answers
// it: an address and somewhere to reach it, stored at `verse.runners.<name>`.
//
// It carries a url and who the runner acts as, and nothing about what work may do there. A
// grant ceiling, a tool allowlist, a label — anything saying "work here may only do this" — is
// expressible and deliberately absent: it is the same narrowing a spawn's environment answers,
// and answering it in two places without deciding how they compose is how a ceiling comes to
// mean two things.
//
//tony:schemagen=verse-installed-runner,notag
type Runner struct {
	// Name is the address: `verse.runners.<name>`.
	Name string `tony:"field=name"`
	// URL is the runner's verse-facing base: what client.HTTP drives, and what a restart
	// reconstructs from. Required, with no default and no local special case — `local` is what
	// the ABSENCE of a runner means, which keeps every runner entity somewhere to reach.
	URL string `tony:"field=url"`
	// Identity is the id of the principal the runner acts as, `runner:<identity>`: what a task's
	// reports are written as, and what the spawn grants `read-write` and `control` on the task.
	// A runner acts as itself, not as whoever installed it or whoever's work it runs. Defaults
	// to the runner's name.
	Identity string `tony:"field=identity,omitzero"`
	// Subject is how verse recognises the runner, and every registration names one: the subject
	// of its workload token, which on Kubernetes is `system:serviceaccount:<namespace>:<name>`,
	// from the issuer the deployment names for its iods (`auth.iod.issuer`), or a static token's
	// hash (`verse runner add -token-file`). The runner proves it is the runner at the describe,
	// and at the door when it calls verse; a `runner:` named with no proof is refused. A runner is
	// never taken at its word. The decision about who a runner is belongs here, at registration,
	// and nowhere else (internal/versed/runnerauth.go).
	Subject string `tony:"field=subject,omitzero"`
	// Described is what the runner offers, as it described itself (GET /describe) when it was
	// installed or last checked: the agents and scripts it runs, what each takes, and the
	// systems each holds. Every describe is a proof, so a description is one the runner proved
	// it is the author of. Verse writes it, from the runner, and checks a spawn naming this runner
	// against it; `POST /runners` refuses it. None is a runner that was installed unprobed, has
	// not described itself yet, or answered and did not prove it is its subject; and one that no
	// longer passes Check (kept from an older runner or verse) is read as none: the runner is
	// asked again (Current).
	Described *runner.Description `tony:"field=described,omitzero"`
}

// OfferNames is the names of the doers the runner described: an agent's name, or `script`.
func (e Runner) OfferNames() []string {
	out := []string{}
	for _, o := range e.Description().Offers {
		out = append(out, o.Name())
	}
	return out
}

// Description is what the runner offers, as a description.
func (e Runner) Description() runner.Description {
	if e.Described == nil {
		return runner.Description{}
	}
	return *e.Described
}

// Current is whether the registration holds a description this verse reads: one there is, and
// that passes Check. Otherwise the runner is asked again, and what it says replaces it.
func (e Runner) Current() bool {
	return e.Described != nil && e.Described.Check() == nil
}

// Principal is who the runner acts as: `runner:` its identity, or its name.
func (e Runner) Principal() string {
	if e.Identity != "" {
		return entity.RunnerKind + ":" + e.Identity
	}
	return entity.RunnerKind + ":" + e.Name
}

// Check validates a runner before it is installed.
//
// What it covers is what cannot be discovered later — an address that cannot be addressed, and
// a url that is not one. Whether the far side is a runner at all is a separate question,
// answered by probing, because it is the only one of the three that can change on its own.
func (e Runner) Check() error {
	if e.Name == "" {
		return fmt.Errorf("runner: name is required — it is the address")
	}
	if strings.Contains(e.Name, "/") {
		return fmt.Errorf("runner %q: a name may not contain / — it is a path segment on the "+
			"route that serves it, so this one would address something else", e.Name)
	}
	// The identity is the id alone: the runner acts as `runner:<identity>`, so an identity that
	// spells a kind of its own would act as `runner:user:…`, which is nobody anybody granted.
	if _, err := entity.ParsePrincipal(e.Principal()); err != nil || (e.Identity != "" && entity.KindOf(e.Identity) != "") {
		return fmt.Errorf("runner %q: identity %q is the id of the principal it acts as, "+
			"runner:<identity>, and carries no kind or space of its own", e.Name, e.Identity)
	}
	// A subject is a token's `sub`, compared byte for byte at the door: one with space in it
	// matches no token anybody issues.
	if err := runner.CheckSubject(e.Subject); err != nil {
		return fmt.Errorf("runner %q: %w", e.Name, err)
	}
	if e.URL == "" {
		return fmt.Errorf("runner %q: url is required — there is no default and no local "+
			"special case here; `local` is what the ABSENCE of a runner on a spawn means, so "+
			"every installed runner is somewhere to reach", e.Name)
	}
	u, err := url.Parse(e.URL)
	if err != nil {
		return fmt.Errorf("runner %q: url %q does not parse: %w", e.Name, e.URL, err)
	}
	// Parse alone is far too forgiving — it accepts "gpu-pool:9797" as a scheme and an opaque
	// body, which is exactly the typo an operator makes, and it would install as a runner
	// nothing can dial.
	if u.Scheme != "http" && u.Scheme != "https" {
		return fmt.Errorf("runner %q: url %q needs an http:// or https:// scheme — it is what "+
			"client.HTTP dials and what a restart reconstructs from", e.Name, e.URL)
	}
	if u.Host == "" {
		return fmt.Errorf("runner %q: url %q has no host", e.Name, e.URL)
	}
	// A runner is never taken at its word: a registration says how verse recognises it.
	if e.Subject == "" {
		return fmt.Errorf("runner %q: say how verse recognises it: a subject, the workload token's "+
			"(`verse runner add -subject`) or a static token's hash (`-token-file`). A runner is never "+
			"taken at its word", e.Name)
	}
	return nil
}

// RunnerRow is a runner as `GET /runners` lists it and `POST /runners` answers it: the runner as
// installed, and what verse has recorded of the work on it. Running is now, Total is ever, and
// LastSpawn is when it was last used, so a runner nobody names is visible as such.
//
// A listing row leaves out what the runner described (`GET /runners/{name}` has it); the row an
// install answers keeps it, since what the runner offers is what the operator installing it
// wants to see. Reach and Why are what a probe found: on a listing only with `?probe=true`,
// since a reachability column is as of now or it is a cache that can be stale in the direction
// that costs, and on an install unless `?probe=false`. Warning is what an install says the
// operator should act on, such as a runner nothing answers yet; only POST /runners sets it.
// Offers is the names of the doers the runner described (an agent's name, or `script`), on every
// row, empty where it has described none: what a spawn could run there, at a glance.
//
//tony:schemagen=verse-runner-row,notag
type RunnerRow struct {
	Runner
	At        string   `tony:"field=at"`
	Running   int      `tony:"field=running"`
	Total     int      `tony:"field=total"`
	LastSpawn string   `tony:"field=lastSpawn,omitzero"`
	Reach     string   `tony:"field=reach,omitzero"`
	Why       string   `tony:"field=why,omitzero"`
	Warning   string   `tony:"field=warning,omitzero"`
	Offers    []string `tony:"field=offers"`
	// Unreadable is why the stored registration does not decode as this verse reads it. Nothing
	// else on such a row is read from the record: the row is there so the registration can be
	// seen and removed by its name.
	Unreadable string `tony:"field=unreadable,omitzero"`
}

// RunnerCheck is what `GET /runners/{name}/check` answers: the runner probed now. A runner that
// answers describes itself afresh, and verse keeps what it says on the registration, so this
// GET writes `described`.
//
//tony:schemagen=verse-runner-check,notag
type RunnerCheck struct {
	At     string         `tony:"field=at"`
	URL    string         `tony:"field=url"`
	Reach  string         `tony:"field=reach"`
	Why    string         `tony:"field=why,omitzero"`
	Offers []runner.Offer `tony:"field=offers,omitzero"`
}

// Removed is what a removal that did more than delete answers: `DELETE /runners/{name}` and,
// shared with the iods, `DELETE /iods/{name}`. Every other DELETE answers 204. Each first refuses with 409 unless a
// flag says what to do with what it holds, and the report says what that did.
//
// Removed is the address taken out. A runner's removal with `?cancel` reports the tasks it
// Cancelled and those on a runner it could not reach (Unreachable), which are not counted as
// ended. An iod's removal reports how many reflected places `?forget` tombstoned (Forgotten),
// or how many `?keep` left standing (Kept).
//
//tony:schemagen=verse-removed,notag
type Removed struct {
	Removed     string   `tony:"field=removed"`
	Cancelled   []string `tony:"field=cancelled,omitzero"`
	Unreachable []string `tony:"field=unreachable,omitzero"`
	Forgotten   *int     `tony:"field=forgotten,omitzero"`
	Kept        int      `tony:"field=kept,omitzero"`
}
