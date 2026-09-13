# auth for verse: options for plugging into an adopter's SSO (working note)

Working note, 2026-09-13. Verse has users and permissions and no auth: a request is whoever
`Verse-Principal` says (the CLI's `user:`, `VERSE_USER`, `--as`), the door refuses only
`system`, and a runner names itself at claim. The permissions model was built so that this is
the one place that changes when a principal is verified — the check, the records, the author
on every commit take a principal string and do not care how it was established. This note is
the options for establishing it, on the premise that an adopter plugs verse into the SSO they
already run.

## Three parties at the door

The answers differ by who is asking, and verse already tells them apart by kind.

**1. People, through the adopter's SSO.** OIDC is the plug: every IdP an adopter runs (Okta,
Entra, Google Workspace, GitHub Enterprise, Keycloak) speaks it, and the SAML-only shops have
OIDC beside it. Two ways to put it in front of versed:

- *An authenticating proxy, and versed trusts a header.* oauth2-proxy, Pomerium, Cloudflare
  Access. Versed
  accepts `Verse-Principal` only from the proxy (a shared secret header, or the proxy's
  network), and the mapping from IdP claims to a principal is the proxy's. No protocol code in
  verse; the adopter brings what they have. The cost: the CLI has to get through a proxy built
  for browsers — device-code and PKCE flows are what those proxies do worst — and the trust is
  "this header came from the proxy", which is a network fact rather than a cryptographic one.
- *Versed validates OIDC tokens itself.* The CLI runs the device-code flow (`verse login`),
  caches the token beside its config, and sends it as a bearer; versed checks issuer, audience
  and signature against the IdP's JWKS (cached, refreshed on unknown kid) and takes the
  principal from the `email` claim, kind `user`. Config is three fields under `auth:` — issuer,
  clientId, audience — and a verse with none configured stays as it is today. This is the one
  that fits a CLI-first tool, and the one to build first.

**2. Machines: runners, sources, resolver services, the coderunner.** No browser, and today no
identity at all beyond a self-asserted name at claim. Two mechanisms, and an adopter wants both:

- *Workload identity in the cluster.* A projected ServiceAccount token with audience `verse`
  (`serviceAccountToken` volume, `expirationSeconds`), validated by versed against the API
  server's OIDC discovery/JWKS or by TokenReview. The runner's identity IS its service
  account (`system:serviceaccount:verse:coderunner` → runner `coderunner`), nothing is
  distributed, rotation is the kubelet's. This is how the coderunner, the sources' hint
  deliveries and an in-cluster resolver should present themselves.
- *Tokens verse mints.* `verse token add <name> [--for <principal>]` writes a hashed token
  as an entity, `verse.principal.token.<id>`, owned by whoever minted it, so it is listed,
  revoked with `--rm`, and watched like anything else; the bearer resolves to the principal it
  was minted for. For the resolver service outside the cluster, a laptop runner, CI. Hash at
  rest, show once at mint, expiry optional.

**3. Agents, and where `--as` goes.** Today `--as` is free, which is what makes the `agent`
kind unmotivated. With auth, `--as` becomes *delegation*: a harness acting for a person
carries a token that names both. Either OAuth token exchange (RFC 8693) with an `act` claim
from the IdP, or a verse-minted delegation the person creates (`verse token add --for
ana@… --as-agent claude`), bounded in time and narrowable by grants later. The write records
`claude (acting for ana@…)`, the refusal text already has the words for it, and a `--as`
that names somebody the token does not delegate for is refused at the door. This is the
motivation the `agent` principal was waiting for.

## Groups

Adopters expect IdP groups to mean something. Two designs:

- *Claim-based*: membership read from the token's `groups` claim at check time. Cheap, and it
  puts a second source of truth beside the mirror: the check reads something that is not in
  the store, membership is invisible to a charter and unauditable in the log.
- *Reflected*: a source reflects the IdP's groups (SCIM, or the IdP's API) into
  `verse.principal.group.*`. Groups stay data with one writer, the check reads nothing but the
  mirror, and a membership change is a commit somebody can watch and a rule can react to.
  This is the one that fits verse. The IdP's group names become verse group names under a
  prefix the source owns (`verse.principal.group.okta:eng`), so a hand-made group and a
  reflected one cannot collide.

## What does not change

The perms model, the records, the check, the author on every commit, the CLI's verbs. What
changes is the door: a validator per token kind (OIDC bearer, SA token, verse token), a
mapping from what it verified to a principal string and a kind, a trusted-proxy header as an
alternative validator, and the rule that `--as` is admitted only by delegation. A request the
door cannot attribute is unattributed, as today, and holds what `*` holds.

Open questions worth deciding before building:

- Whether an unattributed request is still admitted at all once `auth:` is configured, or
  refused at the door. Refusing is simpler to reason about; admitting keeps `*` grants
  meaningful for a public read surface.
- The principal string for a person: the email claim, which is what the seed and the guide
  use, or `sub`, which is stable across an email change and unreadable. Email, with `sub`
  recorded on `verse.principal.user.<email>` so a change can be followed.
- Whether verse-minted tokens are needed at all once workload identity exists, or only for
  the out-of-cluster cases. Probably only those, which keeps the token store small.

## Order

1. OIDC bearer validation in versed and `verse login` (device code) in the CLI: people are
   the only party that can be spoofed from outside the cluster today.
2. ServiceAccount tokens for the in-cluster runners and the sources' hints.
3. Delegation for agents, which is the `--as` rule.
4. Verse-minted tokens for the out-of-cluster services.
5. Groups reflected from the IdP by a source, last, since group grants are rare until 1–3
   exist.

Related: docs/perms.md (the model), docs/perms-guide.md (the door as a person meets it),
docs/roadmap/authority.md (what a task may do, a different axis).