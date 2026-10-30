package versed

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/signadot/verse/entity"
	"github.com/signadot/verse/launch"
	"github.com/signadot/verse/mirror"
	"github.com/signadot/verse/trigger"
)

// The credential a gate mints (git-issue hfh7km3th12ksd6nqsn0, step 2): a token verse signs, as an
// issuer, for the task a rule spawned once its gate let the firing through. A system verse does
// not own is set up to accept only such a token, and so refuses a promotion no gate approved,
// whoever is doing the work.
//
// `sub` IS THE CONTRACT WITH A TARGET. A cloud's IAM matches a custom issuer's tokens on `sub`
// and `aud` and little else, so what a role has to tell apart is in `sub`: the rule's address,
// and how its gate was passed. `verse.trigger.rule.deploy:ballots` is the deploy rule, admitted
// by an answer somebody gave; `…:deadline` is the same rule let through when its clock ran out
// with nobody deciding; `…:auto` is a rule whose gate was written to ask nobody. It names no task
// and no subject, so a role is granted once per rule and holds at any rate of firing; and a
// target that wants a person's answer grants only `:ballots`.
//
// WHAT THE RULE'S QUESTIONS CAME TO, where it has several gates: `deadline` if any gate that
// asked was settled by its clock, `ballots` if every gate that asked was answered, and `auto`
// only where no gate asked at all. A gate that asks nobody beside one that asks takes nothing
// from the answer the other was given.
//
// THE REST IS FOR THE TARGET'S AUDIT LOG: which task, about what, on whose account, which
// question, and who answered it and how each of them was authenticated. Registered JWT names
// where one exists, a plain word otherwise.
//
// A GATE, WRITTEN. A credential says which gate let the work through, so the rule writes one:
// `mode: auto` counts, and says so in `sub`, and a rule with no gate at all gets no credential.
// Work that reaches another system unattended does so because its charter says it may. A spawn
// that is not the rule's own gets none either (launch.Spec.Passed is the engine's account, set
// for that spawn alone).
//
// THE TASK'S RECORD SAYS ONE WAS MINTED: the audience, the `sub`, the token's id and when it
// expires (mirror.Minted), and never the token. A credential for an `auto` gate has no question's
// record behind it, so this is verse's own account that it went out.
//
// MINTED ONCE, AT THE SPAWN, AND GOOD FOR TEN MINUTES. Verse cannot take back a token it has
// signed, so the lifetime is how long a cancelled task could still act; and a token minted later
// would vouch for an approval of a state that has since moved. A task that reaches for it after
// that is refused by the target, and gates its promotion as a short rule of its own.

// credentialLife is how long a minted credential is good for.
const credentialLife = 10 * time.Minute

// mintCredential signs the credential for the task a rule's spawn starts: id is the task, by who
// it acts as, and store where the firing's questions stand. It answers the token, and what the
// task's record says of it.
func (s *Server) mintCredential(store entity.Store, eng *trigger.Engine, id string, spec launch.Spec, by string) (string, *mirror.Minted, error) {
	if s.issuer == nil {
		return "", nil, fmt.Errorf("the spawn asks for a credential, and this verse issues none: its " +
			"config names no issuer (auth.issuer)")
	}
	aud := strings.TrimSpace(spec.Credential.Audience)
	if aud == "" {
		return "", nil, fmt.Errorf("the spawn asks for a credential for no audience: `credential: " +
			"{audience: …}` names the system the token is shown to")
	}
	// NOT FOR A RUNNER. A runner's door is held to tokens for its own name, and admits the
	// verse's alone (runner.VerseSubject); a credential naming a runner as its audience would be
	// a token the door turns away at best, and has nothing else to be for.
	regs, _, err := readRunners(s.Store)
	if err != nil {
		return "", nil, errors.New(notRead("the runners' registrations, which a credential's audience is checked against", err))
	}
	for _, reg := range regs {
		if reg.Subject == aud {
			return "", nil, fmt.Errorf("the spawn asks for a credential for %s, which is the runner %s: a "+
				"credential vouches for a task to a system outside verse, and a runner hears from verse alone", aud, reg.Name)
		}
	}
	if len(spec.Passed) == 0 {
		return "", nil, fmt.Errorf("the spawn asks for a credential, and no gate let this launch " +
			"through: a credential says which gate did, so it goes to the spawn of a rule that " +
			"writes one, and not to a hook's or a sequence step's")
	}
	asked, timedOut := false, false
	sys := entity.As(store, entity.SystemDocket)
	gates := make([]map[string]any, 0, len(spec.Passed))
	for _, p := range spec.Passed {
		gate := map[string]any{"passed": p.How}
		if p.Gate != "" {
			gate["gate"] = p.Gate
		}
		switch p.How {
		case launch.PassedAuto:
			gates = append(gates, gate)
			continue
		case launch.PassedBallots:
		case launch.PassedDeadline:
			timedOut = true
		default:
			return "", nil, fmt.Errorf("the spawn asks for a credential, and question %s was let "+
				"through by neither a ballot nor its deadline: there is no answer to vouch for", p.Question)
		}
		asked = true
		answerers, err := answerersOf(sys, p)
		if err != nil {
			return "", nil, fmt.Errorf("reading who answered %s: %w", p.Question, err)
		}
		gate["question"], gate["answerers"] = mirror.QuestionRef(p.Question).String(), answerers
		gates = append(gates, gate)
	}
	// What the rule's questions came to: see the note on several gates above.
	how := launch.PassedAuto
	switch {
	case timedOut:
		how = launch.PassedDeadline
	case asked:
		how = launch.PassedBallots
	}
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", nil, err
	}
	rule := strings.TrimPrefix(eng.Principal(spec.Chain), entity.ViaKind+":")
	now := s.now()
	minted := &mirror.Minted{
		Audience: aud, Sub: rule + ":" + how, JTI: hex.EncodeToString(b[:]),
		Expires: now.Add(credentialLife).UTC().Format(time.RFC3339),
	}
	claims := map[string]any{
		"iss": s.issuer.url, "aud": aud, "sub": minted.Sub,
		"iat": now.Unix(), "nbf": now.Unix(), "exp": now.Add(credentialLife).Unix(),
		"jti":  minted.JTI,
		"task": id, "rule": rule, "subject": spec.Subject, "principal": by,
		"gates": gates,
	}
	if at := eng.At(); at != "" {
		claims["charter"] = at
	}
	token, err := s.issuer.sign(claims)
	if err != nil {
		return "", nil, err
	}
	return token, minted, nil
}

// answerersOf is who answered a gate the firing passed: everybody whose ballot chose what it
// settled on, each with how they were authenticated, in name order. The ballot that settled it is
// taken from the firing's own account, since the question's record is written after the answer
// is delivered and may not hold it yet; the ballots before it are on the record.
func answerersOf(sys entity.Store, p launch.Passed) ([]map[string]any, error) {
	by := map[string]map[string]any{}
	rec, found, err := sys.Get(mirror.QuestionRef(p.Question))
	if err != nil {
		return nil, err
	}
	if found {
		ballots := entity.ToObj(entity.Get(rec.Payload, "ballots"))
		for who, v := range ballots {
			b, _ := v.(map[string]any)
			if b == nil || b["choice"] != p.Choice {
				continue
			}
			by[who] = answerer(who, str(b, "via"), str(b, "authenticated"))
		}
	}
	if p.By != "" {
		by[p.By] = answerer(p.By, p.Via, p.Authenticated)
	}
	names := make([]string, 0, len(by))
	for who := range by {
		names = append(names, who)
	}
	sort.Strings(names)
	out := make([]map[string]any, 0, len(names))
	for _, who := range names {
		out = append(out, by[who])
	}
	return out, nil
}

// answerer is one answerer as a credential names them: the principal, the party their answer came
// through where it did not come from them directly (a resolver that gave it for them, or an io
// that relayed them), and how they were authenticated. A resolver's answer has `via` and no
// `authenticated`; a relayed person's has both.
func answerer(who, via, authenticated string) map[string]any {
	a := map[string]any{"principal": who}
	if via != "" {
		a["via"] = via
	}
	if authenticated != "" {
		a["authenticated"] = authenticated
	}
	return a
}

func str(m map[string]any, k string) string { s, _ := m[k].(string); return s }
