# A verse deployment we can actually use

Proposal for ENG-1096. Recommends where verse runs in our AWS infra, how people reach it,
what has to change in verse to make it expandable, and what it costs.

## Recommendation in one paragraph

Run one verse in a `verse` namespace on the existing `signadot-staging` EKS cluster, on a
**dedicated on-demand, single-AZ nodegroup** — no spot anywhere in the path. Expose it on the
tailnet with no TLS, using the `tailscale.com/expose` pattern the cluster already runs four
times over. Deploy **local-only first** so people can touch it immediately, and land
**runner-per-process in parallel** as the change that makes "bring your own runner" real.
Reflect `git issue` as the first Source and put `git issue serve` on the tailnet beside it.
Bound LLM spend with a Portkey virtual key. Delete 42 from staging and rotate its
credentials. Infra cost is **~$72/month**.

## Why this cluster, and not the alternatives

`signadot-staging` already has everything the deploy depends on, installed and load-bearing
for other things: the Tailscale operator, the `aws-ebs-csi-driver` addon, and argocd.
`deploy/profiles/staging.tony` in the verse repo was written against this cluster.

**A VM is genuinely the lightest option** and deserves an honest hearing — a small instance on
the tailnet running `verse up` under systemd is one moving part and about the same money.
What rules it out is the expandable half of the goal. `docs/process-control.md` and
`docs/sources.md` both note that RBAC lands when the k8s Jobs policy and k8s Source do, which
is the point where runners become Jobs and we want the cluster API in reach. Rebuilding onto
k8s later costs more than starting there.

**vcluster isn't wrong, it's early.** What it buys is per-verse cluster-scoped resources —
CRDs, ClusterRoles, isolated RBAC. That's the k8s-Jobs future, not today. Playground already
hosts vclusters, so it's the natural home if we get there.

**The one real risk of sharing staging** is an `llm`-driver agent with `run_bash` on a cluster
that also runs signadot-control and the e2e suites. Today the blast radius is the versed pod:
processes run in-process via the local supervisor, and there is no ServiceAccount with cluster
access. **That changes the moment the k8s Jobs policy lands**, and that is the trigger to
revisit isolation — not a date, a condition.

## No spot, and no burstable either

Staging's fleet is 20 spot `t3.medium` and zero on-demand. Node ages at time of writing: 112m,
127m, 9h, 15h, 17h. That is constant churn, and it is part of what made 42 unusable — its
session pods were per repo+developer with a warm nix cache on a PVC, so every eviction cost
someone their session. A prototype whose behaviour we are still learning cannot also be a
lesson in eviction semantics.

So verse gets a **dedicated on-demand nodegroup**, and *both* tiers pin to it. Not just the
stateful docd: versed holds the engine's trigger state and hosts the in-process supervisor
running agents, and losing it mid-flow is the same problem.

Drop burstable while we're at it. `t3` under sustained agent work exhausts CPU credits and
then throttles or silently bills surcharge — a failure that presents as "verse got slow and
nobody knows why," which is the same debugging tax as spot in a different costume.
`m6i.large` (2 vCPU / 8 GiB, non-burstable) is the recommendation.

**Pin the nodegroup to a single AZ** (`availabilityZones: [us-west-2a]`). docd's EBS volume is
AZ-bound; if the nodegroup spans AZs and the node is replaced into a different one, the volume
cannot attach and docd sits Pending behind a confusing error. This is cheap to get right now
and annoying to diagnose later.

**One node carrying both pods** to start. Co-locating removes failure modes rather than adding
them — no cross-node attach dance — and if the node dies, EBS survives and the managed
nodegroup replaces it. Splitting the tiers across two nodes is a later call.

## What must change in the deploy

Four concrete things, all small, all blocking:

1. **`staging.tony` is unusable as written.** It sets `onDemandDocd: true`, which becomes a
   `requiredDuringScheduling` nodeAffinity on `capacityType In [ON_DEMAND]`. There are no
   on-demand nodes today, so docd would hang Pending forever. With a dedicated nodegroup the
   right knob isn't the `onDemandDocd`/`spotVersed` capacityType booleans at all — it's a
   `nodeSelector` on a nodegroup label plus a toleration for its taint. New `internal` profile,
   and the two capacityType booleans in `build.tony` can go.

2. **Neither tier declares resource requests.** `versed-deployment.yaml` and
   `docd-statefulset.yaml` have no `resources:` block, which puts both pods in BestEffort QoS —
   first evicted under node pressure. A singleton engine holding trigger state at BestEffort is
   the same class of avoidable fragility as spot. Add modest requests.

3. **`signadot/verse:latest` is not published.** The Docker Hub repo returns not-found and
   nothing in the verse repo pushes it. Step zero is a real image.

4. **Registry and tagging.** Use ECR or GHCR rather than Docker Hub, which avoids the pull-limit
   `imagePullSecret` dance the infra README documents. And tag by content hash, not `:latest` —
   a singleton `Recreate` Deployment on a floating tag means you cannot tell what is running or
   roll back.

## Reaching it: the tailnet is the security model

Two annotations on the two Services, exactly the pattern argocd, rds and ci-gateway already use:

```yaml
tailscale.com/expose: "true"
tailscale.com/hostname: "verse"          # → my-cluster.example.ts.net:9797
tailscale.com/hostname: "verse-broker"   # → my-cluster.example.ts.net:9090
```

Then `export VERSE_ADDR=my-cluster.example.ts.net:9797` and every `verse` verb works from any
laptop on the tailnet. Plain HTTP over WireGuard: no certs, no ALB, no external-dns, nothing
public. That alone is most of the dogfooding unlock — 42 needed `kubectl port-forward` before
you could touch it, which is much of why nobody did.

Auth is an explicit non-goal, so this needs saying plainly:

> **The tailnet boundary is the entire security model.** Nothing in verse checks who is
> calling. This is fine while it is tailnet-only and catastrophic the first time someone puts
> an Ingress in front of it.

Operational note: the Tailscale operator's proxies need the `signadot-registry`
`imagePullSecrets` patch on their ServiceAccount — a documented bite in the infra README.

## Runner-per-process: the change that makes this expandable

Today the runner is not just singleton, it is singleton *and* mutually exclusive.
`versed/server.go:129` sets `s.runner = localRunner{procd}` as one server-wide field,
`SetRunner` (`:181`) rebinds the whole server, and `cmd/verse/flags_test.go:220` lists
`{Runner: "http://x", Broker: true}` as an error case. A verse picks exactly one of:
everything local, everything on one remote runner, everything through the broker.

That directly contradicts the goal. "Durable runners in the infra *and* laptop dial-in brokers
for ephemeral local work" is not a configuration of the current code — it is two mutually
exclusive deployments. And `--broker` alone means nothing executes until a worker dials in, so
a shared verse nobody is currently attached to would just accumulate unclaimed processes.

The good news: **the data model is already per-process.** `server.go:264` and `:1282` stamp
`Runner: s.runner.Ref()` onto each `process:instance` entity, and the comments at `:630` and
`:1295` describe processes as "each stamped with its runner" — as though the plural case was
always intended. What is global is only the *selection*.

And the selector already exists in the right shape. Provider entities
(`provider:<capability>:<id>`) contribute `driver`, `frame`, `tools`, `grant` to the spawn that
selected them — "what the doer *is*", in provider-selection.md's terms. Where a doer runs is
part of what it is, so `runner` is the same kind of field, chosen by the same `match`/`select`
machinery. No new concept: a provider entity says "this capability is served by the durable
in-infra runner" or "by whoever is dialed into the broker."

Sites to touch are the ones asking "which runner" globally: spawn (`:264`, `:278`, `:1281`),
attach/resolve (`:1161`), status (`:1190`, `:1213`), listing (`:1315`). A named registry plus a
selector on `Launch`. Not small, not a redesign.

**Sequencing: local-only first, runner work in parallel.** A local-only verse on the tailnet is
honest and immediately dogfoodable — it simply means "add your own runner" does not work yet.
The broker face is already served (`--broker-addr :9090` is in the manifest), so when
runner-per-process lands, dial-in workers light up without redeploying.

Worth noting for the local-ephemeral goal: the broker is **dial-in** — from `broker/broker.go`,
the worker connects *out* and pulls work; both parties are clients and only the broker is
reachable. So a laptop runner needs no inbound reachability at all, and the dial URL is
published on the process entity as `dialURL`, so nothing needs pre-configuring. Ephemeral by
construction, which is exactly the contract we want for local.

## Ticketing: `git issue` as Source #1

The goal is that verse is generally ticket-aware — Linear, GitHub issues, Notion, Slack, git
issue — and may serve ticketing itself. `git issue` is the right first Source, and not merely
as a stopgap.

Every other ticket system needs an API client, OAuth, rate-limit handling, and a
publicly-reachable webhook endpoint, which on a tailnet-only deployment is its own problem.
git-issue needs none of it: issues are git refs (`refs/issues/*`, `refs/closed/*`), reconcile is
`git ls-remote`, and the metadata is **already Tony** — the same format docd stores.

It also lands on the gap that blocks all the others. `docs/sources.md` is explicit that the
`Source` interface and the multi-repo git Source are built and tested, but "no command
registers a repo or starts a reflector, so `NewGit` has no caller outside its test." git-issue
gives `NewGit` its first real caller. Get webhook-is-a-hint / reconcile-is-truth working
against git refs and Linear and GitHub become the same shape with an API client bolted on,
rather than a new design each.

And we already use it — the `SessionStart` hook runs `git issue list --label agent`, and the
go-tony defects tracker lives there. So the first Source reflects work we actually track.

`git issue serve` deploys as the same tailnet pattern: a Deployment and a Service annotated to
hostname `issues`. It is read-only, `/` lists open issues, `/?all=1` includes closed, and
`/i/<xidr>` URLs keep resolving after an issue closes because resolution searches both ref
namespaces. Its README says, of `serve`: *"There is no authentication, and there should not be:
bind loopback unless you know exactly who else can reach the address you pick."* A tailnet
hostname is precisely that condition, and it is the same security model as verse itself — the
two agree, which is reassuring.

**The one hazard to flag before anyone builds write-back.** `serve` is read-only *by design,
not as a first cut*: issue sync is a force-push in both directions with no merge step, so a
second writer silently drops whichever update lost the race. That is the same law as
sources.md's "ownership = single writer = no CAS" — the two models agree — but it means
verse-writes-back and human-edits-via-CLI would race and lose each other's updates. Outbound
action on tickets is already a separate concern in sources.md and it is unbuilt; this is the
concrete reason it has to stay that way until the sync model gains a merge. **v1 is verse
reacting to ticket state, not writing it.**

Open item: where the served repo lives — a bare mirror on a small PVC that the reflector also
reads, or served from something that already has it.

## Multi-verses: one, with the door open

One shared verse. versed is a singleton by design (two engines double-fire triggers), so a
second verse means a second namespace with its own docd StatefulSet and its own EBS volume —
cost and ops multiplying linearly to buy isolation that cooperative users do not need.

Anyone who wants their own gets it locally with a bare `verse up`, free and ephemeral. And the
deploy is namespace-parameterized, so a second verse in the infra is `-e namespace=verse-<name>`
away. Expandable without being expanded.

(Intra-verse isolation via scopes is *not* on offer as a substitute — there is no
`verse up --scope`. Scopes are a store-level, per-session mechanism that `connect` uses, not a
way to partition a shared deployment.)

## Cost

| item | monthly |
|---|---|
| 1× `m6i.large` on-demand, us-west-2 | ~$70 |
| EBS gp3, 20 GiB | ~$1.60 |
| Tailscale devices + proxy pods | ~0 |
| registry | ~0 |
| **infra total** | **~$72** |

Against roughly $2–15 on spot. Paying about $60/month to stop debugging evictions on a system
we are actively designing is obviously correct, and it is the cheapest fragility we will ever
buy our way out of.

**LLM tokens are the only cost that was ever interesting, and Portkey bounds it.** verse reads
`PORTKEY_API_KEY`/`PORTKEY_VIRTUAL_KEY` from the `verse-llm` secret, so a virtual key with a
hard monthly budget turns spend into a number someone chose rather than an open tab.

This composes well with runner-per-process. If a runner carries its own virtual key, budget
attaches to *who does the work* — which is the provider-selection axis. Per-person keys give
per-person budgets and cost attribution for free, and an expensive capability can be bounded
separately from a cheap one, with verse needing no concept of accounting. It is also the
mildest useful version of the auth non-goal: no identity, no RBAC, but spend is bounded per
doer.

Cleaning up 42 returns capacity, so the net is smaller than the table.

## Obsoleting 42 in staging

The footprint is one Deployment, one Service, eleven dead ReplicaSets, two ServiceAccounts, a
Role and RoleBinding, two ConfigMaps, two Secrets, and one tailnet device. Zero session pods
and zero PVCs — nobody has used it in a long time. No cluster-scoped resources (the only
matching ClusterRole is istio's gateway controller).

So `kubectl delete ns ci-agent` takes essentially all of it. Then drop the `ci-gateway` device
from the tailnet admin if the operator leaves it stale, and optionally clean the
`signadot/ci-agent-poc` tags.

**The item that actually matters is not the pods.** `ci-agent-secrets` holds six live
credentials — `anthropic-api-key`, `openai-api-key`, `github-token`, `webhook-secret`, and a
static `aws-access-key-id`/`aws-secret-access-key` pair — sitting in a cluster secret for 185
days. Rotate or revoke them as part of the teardown regardless of what replaces the service.
Long-lived static AWS keys in a namespace nobody watches is the real finding here.

**One honest caveat on "obsolete":** verse does not currently replace 42's specific function.
42 intercepted `git push` and ran skills before forwarding to origin; verse's git Source is not
CLI-wired yet. If anyone is still pushing through it, the answer is "stop," not "use verse
instead." The zero session pods suggest nobody is. The 42 repo stays; only staging goes.

## Sequence

1. Rotate 42's credentials, `kubectl delete ns ci-agent`, clean the tailnet device.
2. Publish a content-hash-tagged verse image to ECR/GHCR.
3. Create the on-demand single-AZ nodegroup, labelled and tainted for verse.
4. Add the `internal` profile (nodeSelector + toleration, resource requests, gp3) and deploy
   local-only into namespace `verse`.
5. Expose `verse` on the tailnet; announce `VERSE_ADDR` and let people use it.
6. In parallel: runner-per-process, selected through provider entities.
7. Then: `git issue` Source wiring plus `git issue serve` on the tailnet.
8. Portkey virtual key with a monthly budget before agents run at any volume.

Argocd is deliberately out of the first cut — `make deploy` and `kubectl apply` until that
stops being fun. The door is open though: staging's argocd already runs a `tony-build` CMP
plugin and verse's deploy *is* an `o build`. The existing plugin is operator-specific (it pulls
an oras artifact for image tags), so verse would want a second, simpler entry — perhaps fifteen
lines, no oras. That is the point where "add a runner" becomes a PR instead of a kubectl,
which is probably where we want to end up.
