# proposal: identity for people and agents — OIDC at the door, then credentials a gate mints, then lifecycle sync

# proposal: identity for people and agents — OIDC at the door, then credentials a gate mints, then lifecycle sync

**A design proposal; nothing is built.** It builds on `verse/github-auth` and on the SSO working note (amwvs9t2). The plan for building steps 1 and 2 was a separate (since-closed) branch issue.

## Constitution

This design answers to the draft charter: `docs/charter.md` on `verse/charter`, at 00a05eca. It is draft v1 and not yet adopted, so where this proposal leans on it, it leans on a draft. Its purposes, quoted:

> 1. To govern SDLC work by people and agents across systems it does not own, so that the work is organised, observable, gated, steerable, and controllable.
> 2. To govern that work whoever does it: verse's own agent loop, where gates are enforced by construction, or the agents, tools and people already in use. And to state what each one's gates guarantee in a fashion which best serves 1.
> 3. To achieve 1 and 2 in a fashion which maximises the return value of where it seeks human attention and which scales to a massive rate of change.
> 4. To contain no work outside this charter, and to change that only through changing the charter.
> 5. To act so as to be something others can freely build on and use, initially via internal use.
> 6. To take on work and people's or organisation's time and resources, only in order to carry out the first five.

Two lines of its "What verse is" bear on identity directly:

> - Every reaction passes a **gate**: auto, ask, or park.
> - Every write names who made it, and there is a permissions model over both data and control.

How it binds this design:

- **Every choice rests on a purpose, named by its number.**
- **A choice no purpose decides is said to be undecided by the charter.** It is then made on the least work and the least of other people's time (purpose 6).
- **Work no purpose asks for is left out** (purpose 4).
- **Where two purposes pull apart, both are named and the choice is put to the team.**
- **The charter is a draft.** If it changes, the choices that rest on a changed purpose are checked again.

## Why: what the charter asks of identity

An adopter's case is both people and agents. Agents will run infra-sensitive promotion steps under verse. For a gated promotion, purpose 2 asks verse to state what the gate guarantees. That guarantee has two parts:

1. **Somebody entitled to approve did approve.** Today it does not hold. A request is whoever `Verse-Principal` says it is, so anyone who can reach the API can answer any gate as anyone. That includes the merge train's merge-to-main gate (008d137e). Slack answers are better, because Slack vouches for the email. But the API path sits beside it, unverified. github-auth inherits the same weakness: an account is claimed under a self-asserted principal. So anyone can say they are `user:ana@…`, prove their own GitHub login, and from then on their comments count as Ana's. "Every write names who made it" is true today only of the name, not of who made it.
2. **The doer cannot promote without that approval.** Today this holds only for verse's own loop, where the gate is enforced by construction. Every other doer holds standing credentials: the github io's token, whatever an external agent carries. For those doers the gate is advisory, and the honest statement of what it guarantees is "the agent behaves".

Part 2 is the guarantee an adopter asks about first, and it is the one that distinguishes verse. But it rests on part 1. A credential minted because "ana@ approved" means nothing while "ana@" is self-asserted. That gives the order:

1. **OIDC at the door, for people.** Every gate answer, binding and grant then rests on a verified principal. Fully designed below.
2. **Credentials minted when a gate passes, for agents.** The system verse does not own enforces the gate, which is purpose 1's "across systems it does not own" made literal. Sketched below.
3. **Lifecycle sync (SCIM or a directory read), for departures and groups.** Sketched below.

Step 1 is people, because they come first. Agents, runners and ios are next at the door: each gets its own block of `auth` (§1.2), and they are self-asserted only until those are built. That work is named under "Next: agents, runners and ios" and is not designed here.

Purpose 5 is why step 1 is OIDC. OIDC plugs into whatever IdP an adopter already runs. That IdP is the only identity source their security team will accept. Verse being an identity source itself is a hard sell for login. It is the right shape only for step 2.

## Step 1: OIDC at the door

"OIDC" here means **verse as a relying party**: it accepts id_tokens from IdPs a deployment configures, such as Google Workspace, Okta, Entra, Keycloak or Dex.

### What github-auth gives us

- `verse.identity.github.account.<sub>`: one account maps to one principal. Only `system:identity` writes it, from evidence. It is readable by its principal and by `agent:*`.
- The person claims and the system binds. A binding means the same thing whichever method proved it.
- The proof (method, evidence address) is kept on the binding.

### 1.1 Bindings are per provider, and record their claimant

`verse.identity.github.*` becomes `verse.identity.<provider>.*`. `<provider>` is the name the deployment gives an issuer, because an issuer URL is a poor address segment. The record keeps `iss`. `github` is the first provider, and its verifier is proof by action. Every rule on the branch carries over.

**A binding records how its claimant was authenticated**: `claimant: asserted | <provider>`. Once the door is verified (`required`, §1.5), an `asserted` binding does not count until an authenticated caller proves it again. That closes the hole in part 1 above without discarding the GitHub work. It is purpose 2 at the level of one record: the binding states what it guarantees.

### 1.2 Providers in config

`auth:` holds one block per party. This proposal fills `principal`, the people. The others get their own blocks when their mechanisms are designed (see Out of scope):

- `runner`: workload identity for runners. A runner may also need to specify its access to a broker, i.e. what it presents to reach one, and that belongs here, with the runner.
- `iod`: the io daemon and the ios it hosts.
- `broker`: the broker's own side, meaning how it admits whoever reaches it. It does not cover everything about brokers. A party's access *to* a broker is specified in that party's block, as `runner`'s is.

A verse with none configured behaves as it does today.

```tony
auth:
  principal:
    mode: optional            # off | optional | required (§1.5)
    providers:
      google:
        issuer: https://accounts.google.com
        clientId: 1234.apps.googleusercontent.com
        domains: [example.com]        # a first sight needs a verified email in one of these
      okta:
        issuer: https://example.okta.com
        clientId: 0oa…
  runner: TBD                 # including its access to a broker
  iod: see §1.7
  broker: TBD                 # the broker's own side
```

The CLI reads `auth.principal` to know which providers it can log in to. CLI clients are public clients (PKCE, no secret).

### 1.3 The door validates a bearer

The `principals` middleware (versed/server.go) gains one step ahead of the header:

1. `Authorization: Bearer <id_token>`. The token's `iss` names a provider under `auth.principal`. Verse checks the signature against that provider's JWKS (found by discovery, cached, refetched on an unknown `kid`). It checks that `aud` contains the `clientId`, and checks `exp` and `nbf` with a small clock skew allowed.
2. **The principal is the binding, not the claim.** Verse reads `verse.identity.<provider>.account.<sub>`. If a binding exists, its principal is who the request is.
3. **First sight.** If no binding exists and the token has `email` with `email_verified: true` in one of the provider's `domains`, then the principal is `user:<email>`. Verse writes the binding as `system:identity`, with `method: oidc` and `claimant: <provider>`. Evidence is the token's `iss`, `sub` and `iat`, never the token itself. Otherwise the request is refused 401 with the reason named.
4. Verse sets `Verse-Principal` from the result, so everything below the door (checks, authorship, `notePrincipal`) reads what it reads today.
5. A request with a verified bearer *and* a `Verse-Principal` naming somebody else is refused 403, until delegation exists (amwvs9t2 step 3).

A person whose email changes at the IdP keeps their principal: `sub` finds it, and the email is used only at first sight. That settles amwvs9t2's question of email or `sub`. The principal string stays the email, which is what grants use.

Why the id_token and not an access token: Google's access tokens are opaque, and the id_token's `aud` is our own client. `kubectl`'s OIDC plugin made the same choice. Whether a refresh yields a fresh id_token is checked per provider in slice 3.

### 1.4 The CLI logs in

- `verse login [<provider>]` uses the device authorization grant (RFC 8628) where the provider offers it. Otherwise it uses authorization code + PKCE with a loopback redirect. Tokens are cached beside the config, mode 0600, one entry per verse URL and provider.
- `client.Transport` sends the bearer and refreshes it when it has expired. It sends `Verse-Principal` only for `--as`.
- `verse whoami` says who verse thinks you are and how it knows: `user:ana@example.com (google)` or `(asserted)`.
- `verse logout` drops the cache.

An external agent driven through the CLI, such as Claude Code, acts as the person who logged in, with their authority (v27eah17). Narrowing that is delegation, which comes later.

### 1.5 Modes, so it can roll out

`auth.principal.mode`:

- `off`: today.
- `optional`: a verified bearer is used, and a bare header is still admitted. This is purpose 5's "initially via internal use": the team moves over before anything is refused.
- `required`: a `user:` principal is admitted only from a bearer. A request that names nobody reads what `*` holds (open question 1).

`required` covers **people, who come first**. `runner:` and `agent:` are next, and are self-asserted until their own `auth` blocks are filled (amwvs9t2 steps 2 and 4). Until then that is a real gap: `agent:*` can read bindings. It is named so that `required` is not read as "the door is closed", which purpose 2 asks for: state what is guaranteed, and no more.

### 1.6 Linking, and ios trusted per provider

- Claim and verify (`POST /accounts/<provider>`, `…/verify`) keep their shape. In `required` mode the caller must come with a bearer, and the claimant is recorded. Linking a second OIDC provider needs no nonce: `verse login --link okta`, made while authenticated through Google, presents the Okta id_token as evidence. The rule about one account per principal still holds.
- The `verse.identity.<provider>` node names the ios whose reflections count as evidence for that provider. The admin writes it, as `system:identity` (how is open question 4). This answers an earlier open point that verify trusts any registered io. An io that answers as a person (Slack's `Sink.As`, the github io's `/verse` commands) is admitted for a principal only through a binding in a provider it is trusted for. That is the general form of 8q4azhtj. How the io itself is authenticated is `auth.iod`'s question.

### 1.7 ios at the door, and a person relayed by a trusted io

An io is a program of the deployment's own, and today it is whoever its header says, twice over: `agent:slack-io` by assertion, and `user:<email>` by assertion when it answers for the person who typed in Slack. Under `required` (§1.5) the second is refused, so the Slack io stops answering. This section verifies both, and it is the part of "agents, runners and ios" that comes with step 1, because without it `required` cannot be turned on anywhere people answer from Slack or GitHub. Agents and runners stay next.

**`auth.iod` names a workload issuer and its accounts.** An io's credential is a token its runtime gives it. On Kubernetes that is the pod's projected service-account token with audience `verse`, whose issuer is the cluster's own; on EKS a public URL of the form `https://oidc.eks.<region>.amazonaws.com/id/<id>`, the same one IRSA uses, with discovery and keys on the public internet. On minikube the issuer is reachable only in-cluster. On a machine with no cluster there is none, so the block also takes static tokens. The door validates an iod bearer with the same verifier it uses for a person's provider, checking `aud` against the audience. Its accounts are bindings the deployment writes at start as `system:identity`, at `verse.identity.iod.account.<sub>`, never at first sight. A verified io is its principal under every mode.

```tony
auth:
  iod:
    issuer: https://oidc.eks.<region>.amazonaws.com/id/<id>
    audience: verse
    accounts:
      "system:serviceaccount:verse:slack-io":  agent:slack-io
      "system:serviceaccount:verse:github-io": agent:github-io
    static:                                     # a deployment with no issuer
      - {file: /var/run/verse/slack-io.token, principal: agent:slack-io}
```

**A provider may have no issuer.** An entry under `providers` has an issuer and a client id, where people log in through it, or `ios`, where its accounts are proved through the ios it names; `domains` goes with either. `github` is the first provider of the second kind, and stops being a special case. `slack` is the second.

**The one exception to "a verified bearer beside a header naming another is refused".** A request whose bearer verifies to an iod principal, where that io is in `providers.<p>.ios`, may present `Verse-Principal: user:<email>` together with `Verse-Account: <p>:<account>`. The door admits it as that person when the binding at `verse.identity.<p>.account.<account>` names them, or, on first sight, when `<p>` has `domains` and the email is in one, in which case it writes the binding with `claimant: <p>`, `via: agent:<io>`, and the account as evidence. A binding naming somebody else is 409. No binding and no domains is 403. The request then proceeds as the person: the permission check, the write and the record are theirs, and a ballot records `authenticated: <p>` and `via: agent:slack-io`. The exception admits control acts by default, which is all an io does, and an io's entry may widen it. It is not delegation between people: a person's bearer beside another person's name stays refused (amwvs9t2 step 3).

What is trusted is then stated exactly: verse checked that the io is the io; Slack checked that the member who typed holds that email; the deployment said Slack's io counts for these domains. That is the chain the github io's comment evidence already rests on, and the record shows it.

- *Purpose 1:* every write still names who made it, and now says how verse knows: `via` and `authenticated`.
- *Purpose 2:* what a Slack or GitHub answer guarantees is stated on the ballot, in the same terms as a CLI answer through Dex.
- *Purpose 3:* people answer where they are. `required` without this makes Slack mute and sends every answer to a `verse login`.
- *Purpose 5:* a cluster issuer is standard Kubernetes; the static token covers a laptop and CI; nothing is vendor-specific.
- *Purpose 6:* the verifier, the bindings, the trusted-io list and first sight exist. This adds one issuer kind, one header and one rule.

Open, with recommendations, in the plan (step 3): first sight through an io where the provider has domains; control acts only by default; a GitHub binding written from Dex's federated claims; the header's name; the static credential's shape.

### GitHub is not an OIDC provider for people

GitHub issues no id_token for a user login. Its OIDC issuer (`token.actions.githubusercontent.com`) is for Actions workloads. GitHub accounts therefore stay on proof by action, or on OAuth later (8k4azhtj). OIDC sits beside github-auth's verifier and does not replace it. An adopter who wants GitHub as their login can put Dex with its GitHub connector in front. Dex's `sub` is its own encoding, so that is a different `(iss, sub)`, which can bind to the same principal (§1.6).

### Slices

Each slice is tested against a fake IdP: an httptest server with discovery and JWKS, signing with a generated key. Each test is also run with the check disabled, to see that it fails. Likely dependencies are `github.com/coreos/go-oidc/v3` and `golang.org/x/oauth2`; neither is in go.mod.

1. Per-provider bindings and `claimant`.
2. `auth.principal` config and bearer validation at the door, `optional` mode, first-sight bindings.
3. `verse login` / `logout` / `whoami`, the token cache and the transport, checked against real IdPs for refresh.
4. Linking from an authenticated caller, and re-proof of `asserted` bindings.
5. `required` for `user:`, and ios trusted per provider.

Slices 1–2 need no IdP registered anywhere. Slice 3 needs one public OAuth client, which an IdP admin makes once.

## Step 2: credentials a gate mints

This makes part 2 of the guarantee hold for doers verse does not run. It follows the GitHub Actions model. Verse becomes a **minimal OIDC issuer**: a signing key, a discovery document and a JWKS, with no login flows. When a firing's gate lets it through, verse mints a short-lived token for the task. Its claims are:

- the task;
- the charter and the rule;
- the principal the task acts for;
- the gate's address, and how it was passed: on ballots, on its deadline's default, or `auto`;
- who answered the gate, verified by step 1.

The target system is configured to accept only tokens with the right claims, for example "tasks under this rule, through a gate a person answered". Targets include cloud IAM (AWS and GCP workload identity federation), Vault's JWT auth and Kubernetes structured authentication. The gate is then enforced by a system verse does not own. The standing credentials a doer holds today can be withdrawn.

The plan (step 2) splits this into the issuer, the token, and a spawn that asks for one. It holds the decisions still open, each with a recommendation in the charter's terms:

- the shape of `sub`, which is the contract with every target;
- the claim names and the lifetime;
- the spawn field, and how a task receives the token;
- whether a firing whose gate was `auto` may get a credential;
- what a task that outlives its token does.

Costs to settle before it is used in a deployment:

- **The JWKS must be reachable by the target.** AWS and GCP fetch it from the public internet. The keys would have to be published publicly (static discovery + JWKS in a bucket, as self-hosted Kubernetes issuers do). Vault or an in-cluster target can reach versed directly.
- **Key custody and rotation for versed's signing key.**
- **Which doers can present the token.** Verse's runner, an external agent the task hands it to, or both, and how the hand-off is itself attributed.
- **A first trial.** Purpose 5 points it at verse's own promotions: the merge train's merge, done with a gate-minted credential instead of the github io's token.

## Step 3: lifecycle sync (later)

An OIDC client is enough for login. When a person leaves, the IdP stops issuing them tokens, so their direct access ends within a token lifetime. It is not enough for verse, though. Rules and resolvers act for whoever installed them, long after that person last logged in. OIDC tells verse about a person only when they log in, so it never learns that an installer is gone. SCIM, or reading the IdP's directory, is how verse would find out. It would also reflect the IdP's groups into `verse.principal.group.<prefix>:*` (amwvs9t2 step 5). This is the step adopters will tick off a checklist, but it is not what makes a promotion safe, so it comes third. What verse does with a departed installer's rules (suspend them, reassign them, ask their charter's owners) is that issue's question.

## Next: agents and runners

People are first, and ios come with them (§1.7), because `required` is not usable without the ios that relay people. Agents and runners are next, and are not designed here (purpose 4). Until they are built, an agent and a runner are whoever they say they are.

- `auth.runner` (including a runner's access to a broker), `auth.broker`: workload identity for in-cluster runners, sources and resolvers (Kubernetes SA tokens), and verse-minted tokens for out-of-cluster services (amwvs9t2 steps 2 and 4). Step 2 touches these but does not replace them.
- Delegation for agents, i.e. what `--as` becomes (amwvs9t2 step 3; v27eah17 open question 1).

## Out of scope for all three (purpose 4)

- A trusted-proxy header (oauth2-proxy). It could be another validator in §1.3 writing the same bindings, but it is a network trust, not a signature.

## Open questions

Each has a recommendation, and the purposes it rests on. Step 2's are in the plan.

1. **Under `required`, what a request without a bearer gets.**
   - *Recommended:* a request that names nobody keeps reading what `*` holds. A request that names a `user:` it has not proved is refused 401.
   - *Purpose 5:* reads that name nobody stay, so a `*` grant keeps meaning something, such as a read surface others build on. Writes are untouched: every write still names who made it.
   - *Purpose 1 (observable):* served as nobody, a CLI that has not logged in gets empty answers, which look like an empty verse. A refusal says what is wrong.
   - *Purpose 3:* a refusal costs a person one `verse login`. Empty answers cost them an investigation.
2. **Domains as the only gate on first sight, or a principal allowlist too.**
   - *Recommended:* domains only.
   - *Purpose 3:* nobody has to add each person by hand, at any size of team.
   - *Purpose 5:* "initially via internal use" is one organisation, where a domain is the right boundary. A verse shared by several organisations would seed `verse.principal.user.*` instead.
   - *Purpose 6:* an allowlist is taken on when such a verse exists.
3. **Where the CLI keeps tokens: a file beside the config, or the OS keychain.**
   - *Recommended:* the file, mode 0600.
   - *Purpose 5:* it behaves the same on every machine an adopter runs the CLI on, including CI and containers, which have no keychain.
   - *Purpose 6:* a keychain is per-platform work, taken on when somebody needs it.
4. **How an admin names the ios a provider trusts** (§1.6).
   - *Recommended:* a list in the provider's config, which versed writes to the `verse.identity.<provider>` node at start.
   - *Purpose 2:* which ios' reflections count as evidence decides what a binding guarantees. So it is set by something the door cannot be talked into. While `agent:` and `runner:` are self-asserted, any API route that sets it is one an asserted caller could use. The deployment's file is the one input that does not come through the door.
   - *Purpose 1 (observable):* written to the node, the trusted set is readable in the store like any record.
   - *Purpose 6:* it is the path `systemCharter` already takes at start.
5. **Which real IdPs slice 3 is checked against.**
   - *Recommended:* the IdP the team already signs in with, and a Keycloak or Dex run locally as the second.
   - *Purpose 5:* internal use first makes the team's own IdP the one that must work, and "others" asks that the flow is not fitted to one vendor.
   - *Purpose 6:* a local IdP costs no admin's time.


Related issues:
  amwvs9t2h12ksxtxmsn0 [32m[open][0m auth for verse: options for plugging into an adopter's SSO (working note)
  8q4azhtjh12krrbfqsn0 [32m[open][0m proposal: perms — one principal for a person's accounts in external systems, kept in sync
  v27eah17h12ksycpq5n0 [32m[open][0m proposal: identity and authority in the user face of verse's MCP server
