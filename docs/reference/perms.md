# Permissions reference

This page is the detail of permissions. [The model](../perms.md) defines the words, and
[Permissions, walked](../perms-guide.md) shows them by example. This page has:

- the grammar of names;
- the fields of a record;
- what verse checks, and when;
- who a request is from, and accounts in other systems;
- what each act writes.

The reason for each rule is in the source, beside the code that enforces it
(`entity/attribution.go`, `versed/perms.go`, `versed/place.go`).

## The grammar

```
principal ::= user:<email>          # a person
            | agent:<name>          # an agent driving verse through the api
            | runner:<identity>     # a runner
            | system:<part>         # verse itself
            | via:<address>         # something installed, acting as whoever installed it
grantee   ::= principal
            | *                     # everyone, a read that names nobody included
            | <kind>:*              # every principal of the kind
            | group:<name>          # the group's members, each as themselves
            | audience:<address>    # whoever holds the same level at the address
            | answering:<address>   # the resolvers granted `answer` at the address
            | <email>               # shorthand for user:<email>, at the keyboard only
```

Every place that takes a name reads this grammar, and refuses what it does not read:

- `-as`;
- the config's `user:` and `agent:`;
- a grant line and a `child-grants`;
- a group's members;
- a gate's `answerers`, and an `ask` row's;
- a resolver grant's `answer` line.

The rules of the grammar:

- A bare email is read as `user:<email>` and stored with its kind. Any other bare word names
  nothing. A bare `verse.…` address names nothing too.
- **A request presents `user:`, `agent:` or `runner:`.** `via:` and `system:` are names that
  verse makes. Verse refuses a request that carries one of them with 400, and the same for a
  name the grammar does not read.
- **A request that is not a read presents one.** Verse refuses it with 400 when it names
  nobody. Some routes name their own party
  ([routes that name their own party](../platform/perms.md#routes-that-name-their-own-party), in
  the repository).
- Verse lists a [principal](../glossary.md#principal) the first time it presents itself, under
  `verse.principal.<kind>.<name>`.
- `system:<part>` names the part of verse that wrote, such as `system:docket` for questions.
  All the parts are one owner
  ([the system parts](../platform/perms.md#the-system-parts), in the repository).

**How a grantee matches.**

| grantee | admits |
|---|---|
| `<kind>:<id>` | the principal named exactly that, and anything installed that acts as them |
| `<kind>:*` | any principal of the kind, and anything installed that acts as one |
| `via:<address>` | the installed thing at that address and nothing else. It does not admit the thing's owner. A trailing `*` covers what acts beneath (`via:verse.iods.*`). Only a rule, an iod or a resolver is named this way. A runner acts as `runner:<identity>`. |
| `group:<name>` | the group's members, each matched as above. A member may be another group, and verse follows it. Verse does not enter a group it already met on the walk, so groups that name each other do not loop. They admit exactly the members found along the walk. |
| `audience:<address>` | whoever holds, at the address, the level this grantee is listed under: in `read`, whoever may read the address, and in `read-write`, whoever may write it. It is allowed in `read` and `read-write` only, since owning a place is not owning what is about it. Verse follows it one address deep: an `audience:` met while answering another admits nobody. It is never a principal. |
| `answering:<address>` | whoever holds `answer` at the address: the resolvers answering for a person at `verse.for.<person>`, or for a place. It is allowed in `read` only. An ask's `read` names `answering:` for each of its parties, so the resolvers serving it can read it. Verse follows it one address deep, as it does `audience:`. It is never a principal. |
| `*` | everyone |

A stored name that the grammar does not read admits nobody. Nothing rewrites it. `verse status`
counts each check that meets one under `perms.unparsed`, and versed logs it once per record.

## Records

Records live at `verse.perms.node.<address>`. The root's record is at `verse.perms.root`. Only
verse writes records, and only the system reads them directly. Everybody else reads and sets
them with `verse perms`.

| field | meaning |
|---|---|
| `owner` | the principal who owns the node. It is absent on a record that only holds grants (a seed row). |
| `grants` | a list of lines. `[]` grants nothing. |
| `child-grants` | permission levels mapped to grantees: the grants a record made beneath this node starts with. Verse copies it when that record is written, unless the creation gives grants of its own. It is not a fallback: a change to it touches nothing that exists. The nearest `child-grants` up the tree applies, and the root's is the last. It never holds `answer`, which only the party a resolver answers for can grant. |
| `under` | an address. Whoever co-owns that node co-owns this one. Verse writes it on a question: what asked it. |

A grant line:

| field | meaning |
|---|---|
| `read`, `read-write`, `control`, `answer` | grantees of that level |
| `child-owner` | grantees who may create beneath, and own what they create. It is always about what is beneath, so it takes no `below`. There is no grant of `owner` at a node. |
| `below: true` | the line covers what is beneath the node, and not the node |
| `at: [segs…]` | the line is about another address, with at most a trailing `*`. Without `at`, the line is about the node the record is on, as nearly every line is. |

**A grant does not reach through an owner record.** Verse walks up from a node. The grants on
each record apply, up to and including the first record that names an owner. The grants above
that record do not apply.

**A group** is `verse.principal.group.<name>`. It has `members`, a list of grantees, and whoever
made it owns it. Verse reads the membership when it checks the group.

## Levels

A permission level is what a principal may do at a node.

| level | admits |
|---|---|
| `read` | reading the node and what is beneath it |
| `read-write` | writing into the node, with fields removed as well as set |
| `owner` | everything: deleting the node (which takes what is beneath it), replacing it whole, and setting its record. The owner on record and the owners above hold it. `child-owner` gives it only on a creation. |
| `control` | steering work: run, cancel, pause, resume, say, change an autonomy, suspend. An owner by record holds it, and so does whoever it is granted to. |
| `answer` | which resolvers may answer for the party the node stands for: a place's owner, at the place, or a person, at `verse.for.<person>`. It is held only by being named: owning the node gives none of it, and a resolver gets none through whoever installed it. An owner above may take it back but not give it, and no `child-grants` passes it down. It reaches beneath its node as a grant does. `verse resolver grant` writes it. |

A spawn carries the identity of whoever runs it, and verse writes the task's record as that
identity. That write is the check for `run`.

A refusal that says "needs owner" about a creation means that no `child-owner` line reaches
there.

## Creating, and taking a place

- **Who owns a created node.** A writer may hold `owner` where they write, by owning above or by
  a `child-owner` line. That writer owns what they create. Verse writes a record if the writer
  is not the owner above, or if the write gives the node grants
  (`verse data … -put … -grants`). A writer who holds only `read-write` creates the node too, but
  the node belongs to the owner of the place. Verse writes no record, and refuses grants on the
  write.
- **Where the record goes.** At the topmost node that the write brought into being. The walk up
  stops at anything already there, as a node or as a place somebody took. A node created with
  grants has its own record too, written in the same commit as the node.
- **`child-owner` applies to a creation only.** Verse commits a write that only that line admits
  only if nothing stands where the write lands: at the node, and at each node its value reaches.
  Of two people who make one node at once, one owns it and verse refuses the other. Where
  something does stand, verse admits the write on whatever else the writer holds there, or
  refuses it and names the owner.
- **Taking a place** (`verse perms <path>` where no record stands) needs `owner` there. Through
  a `child-owner` line, that means nothing may stand at the node yet. Under `verse`, a place can
  be taken only where something stands, and only by its owner. So nobody can reserve a name the
  system has not used yet.
- **First come, first served.** A `child-owner` line says who may create beneath a node. It does
  not say which name each of them may create. Where `spaces` grants it to `user:*`, anybody may
  take `spaces.sally`. The owner of `spaces` co-owns `spaces.sally` and can remove it. Nothing
  maps a name to a principal (`b737wbpb`).
- **Not above somebody else's place.** `owner` from a `child-owner` line does not apply at a
  node that has another principal's record beneath it. The refusal names the place in the way.
  Ownership by record is unchanged: the owner of `spaces` co-owns every space in it.
- **A task's id names one party's work.** Verse refuses a spawn under an id that already
  stands as somebody else's, a rule's spawn included.
- **Deleting** a node forgets its record and every record beneath it.

## What is checked on a write

Every write verse makes for anybody goes through one view of the store, and that view acts as a
principal. So verse checks compare-and-swap writes and its own projections too, and the code
that makes the write does not need to know. A refusal is a 403 with the check's words, counted under `perms.refused`.

- **The node written is the whole address the request spells.** The api's `sub` beneath an
  address is part of the address where it spells names: `{at: team, sub: c.secret}` is a write
  at `team.c.secret`.
- **Every node the value reaches is checked**, wherever a record or a grant could change the
  answer from the node above. `{c: {secret: x}}` written at `team` is also a write at `team.c`. A
  `!delete` inside a value replaces its node whole, and so does a value that is not an object.
  Either needs `owner` wherever something beneath could be somebody else's.
- **A writer may read what the write asks about.** Verse refuses a `when:` precondition on a
  node the writer may not read, as it would refuse a read of the node. Verse checks a
  [pattern](../glossary.md#pattern) field by field, so it never refuses a pattern for what the
  pattern does not mention.
- **A removal is checked before it starts.** Removing a rule, a level, a package, an iod or a
  runner needs `owner` on it. Verse checks that before it stops or deletes anything. Replacing a
  rule or a level is a write over what stands: the thing's record and grants stay. A co-owner who
  edits a rule does not become its owner.
- **A charter file is applied as one step.** A refusal anywhere puts back what stood, content
  and record.
- **Two checks come before their write**, because what follows cannot be undone. Verse checks an
  answer before it delivers it to the work, and a control call before it relays it.

## What is checked on a read

- Verse refuses a read of one thing the reader may not read, as it refuses a write.
- A listing, a replay, a watch and a snapshot **leave out** what may not be read. The same holds
  inside a payload: a node under a record the reader may not read is left out of what holds it.
  Verse counts what it leaves out under `perms.hidden`.
- Verse refuses a listing or a watch *at* an address the reader may not read into, as a whole.
- **A task is read as its record is.** The task's prompt, its transcript, its result and the
  answers waiting for its worker each need `read` on the task's record,
  `verse.task.instance.<id>`. The routes, and the one read that is not checked, are in
  [reads by a worker](../platform/perms.md#reads-by-a-worker-and-by-a-tasks-routes), in the
  repository.
- **There is one gap, one commit wide.** The record follows the write it records. So the commit
  that creates a node reaches a live watch before the record that would hide it.
- **A rule reads as its owner.** Verse narrows three things to what the rule's owner may read:
  the snapshot that a condition is asked of, the snapshot that a reference resolves against, and
  the commit that wakes the rule. Verse refuses at install a rule that reads the records
  themselves, unless the rule is the system's.

## Installed things

A rule, an iod, a resolver and a schedule act as `via:<address>`. That principal is the owner
on the record **at that address**. Where the address has no record, it is nobody. It does not
fall back to the owner above, because above is verse's own half, which is the system's.

- **An install leaves its installer's record, or verse refuses it** with 409.
- **Whose a rule is, is said on its record and nowhere else.** A rule's own entity carries no
  installer.
- **The deployment's charter** is installed as the system, and fires as the system.
- **No rule but the system's writes under `verse`.** Verse refuses a `patch` of a subject there,
  or a `derive` into it, with 403. It refuses at install where the rule names the address, and at
  the write. A rule may select a subject there and spawn about it.
- **A charter package takes the address it is installed at** for its installer. An iod takes
  the nodes its claims name in the same way.

How each installed thing gets its record, and what the system makes under `verse`, is in
[installed things and their records](../platform/perms.md#installed-things-and-their-records),
in the repository.

## Questions

A gate's [question](../glossary.md#question) is an ask, at `verse.ask.<id>`, with the docket's
record. Its `under` is what asked it. A person's ask has its asker's record instead, which
`verse ask` writes with who reads it and who answers it ([asking](../asking.md)).

- **Who may read it.** A question about something is read by whoever may read that thing:
  its `read` line is `audience:<subject>`. A question about nothing is read as the
  `child-grants` on `verse.ask` say.
- **Who may answer.** Whoever co-owns the question through `under`: the task's owner, or the
  charter's owner. Also whoever the question's own `read-write` line names. Verse takes that
  line from the first of these that applies:
  1. for a rule's gate, the gate's `answerers`;
  2. for a question a task raises, the `answerers` of the autonomy row that asked; else whom the
     worker named when it asked; else whom the task acts for (`$principal`), where it acts for
     somebody;
  3. for a question about something, `audience:<subject>`: whoever may write it;
  4. the `child-grants` on `verse.ask` (`read-write: [user:*, agent:*]`).
- **Nobody answers what they cannot read.** A question naming answerers who may not read its
  subject is refused when it is asked: the firing or the task that asked gets the refusal as its
  error, and `verse ask` gets 403. A whole kind (`user:*`) passes only where the subject grants
  `read` to that kind or to `*`. An answer from somebody who may no longer read the subject is
  refused with 403.

  A runner's grant is on its task, and reaches no question.
- **Through a resolver**, an answer is its party's: a resolver answering for a person or a place
  that granted it `answer` answers as them, held to their line
  ([Resolvers](resolvers.md#whom-it-answers-for)). Nothing moves the line.
- **Asked again** after it stood orphaned, a question keeps its id, and who may answer belongs
  to the new asking. A question that was answered is not asked again: a gate that fires after
  the answer asks a new question, under an id of its own.
- **Answerable once posed.** Posing is two writes: the record, then the question. An answer that
  arrives before both waits a moment, and then gets `503 … still being posed`.
- **By whoever made the request.** A ballot is keyed by the principal the check admitted, and a
  record names that principal. This holds for an answer, a `say`, a cancel, a change of autonomy,
  and the `principal` a spawn is answerable to. `need` counts one ballot for each.
- **What a worker reports is not evidence of who answered.** For a task on a remote runner, who
  works the task is who claimed it. How verse reads what such a worker sends is in
  [a relayed task's frames](../platform/perms.md#a-relayed-tasks-frames), in the repository.

## Who a request is from

In short: a request is made as a **principal**, and verse decides which one from what the
request carries.

- A name alone is taken as given, where the mode allows.
- A token from an identity provider the deployment names is verified. The principal is whoever
  the token's account is bound to.
- An iod of the deployment presents its own token. When it acts for a person, it also names the
  person's account, and verse finds the person through that account's binding.
- A runner registered with a `subject` presents its own token too, and acts as itself.

Bindings are records at `verse.identity.<provider>.account.<id>`. Only the system writes them,
from evidence it checked itself ([accounts in other systems](#accounts-in-other-systems)).

A request says who makes it in one of three ways.

- **A token.** `Authorization: Bearer <id_token>` carries an OpenID Connect id_token from an
  **identity provider** the deployment names (`auth.principal` in its
  [config](up.md#configuration)). Verse verifies it, and the request is made as the person it
  proves.
- **A name.** `Verse-Principal: <principal>` is taken as given. It is `user:`, `agent:` or
  `runner:`.
- **An iod's token, and a person it relays.** An iod of the deployment presents its own token
  ([iods at the door](#iods-at-the-door)). When it acts for a person, it also sends that person's
  name and the account the person is: `Verse-For-Principal: <provider>:<id>`. Verse admits the
  request as the person only through the account's binding, and only from an iod the deployment
  trusts for that provider ([a person relayed by a trusted iod](#a-person-relayed-by-a-trusted-iod)).

A token is checked before the name is read:

1. Its `iss` names one of the deployment's providers.
2. Its signature verifies against the keys that provider publishes. Verse reads a provider's
   discovery document the first time a token names it. It reads the keys again when it meets a
   key id it has not seen.
3. Its `aud` holds the provider's `clientId`, and it is in date (`exp`, `nbf`).
4. **The principal is the binding, not the claim.** The token's `sub` is an account, and the
   [binding](#accounts-in-other-systems) at `verse.identity.<provider>.account.<sub>` says which
   principal that account is. So a person whose email changes at the provider keeps their
   principal.
5. **First sight.** An account with no binding is bound to `user:<email>` when the token carries
   an `email` with `email_verified: true` in one of the provider's `domains`. The email is
   lower-cased. The binding's proof is `{method: oidc, iss, sub, iat, at}`, and its `claimant` is
   the provider. Verse never stores the token.

| a request with | is answered |
|---|---|
| a token that passes | as the principal its account is bound to |
| a token that fails any check above | 401, saying which. It never falls back to the name beside it. |
| a token, at a verse that names no provider or whose mode is `off` | 401 |
| a token whose provider cannot be reached | 503. Requests with no token are still served. |
| a token that passes, and a `Verse-Principal` naming somebody else | 403 |
| an iod's token | as the iod, under every mode |
| an iod's token, a person's name and their account, from an iod trusted for the account's provider | as the person, through the binding ([below](#a-person-relayed-by-a-trusted-iod)) |
| an iod's token and a person's name, with no account or from an iod not trusted for it | 403 |
| a registered runner's token | as the runner, under every mode ([below](#runners-at-the-door)) |
| a registered runner's token and a name that is not the runner's, with no task | 403 |
| a registered runner's token and a task it hosts (`Verse-Task`) | as the task's owner, through the task's record ([below](#a-task-carried-by-its-runner)) |
| a registered runner's token and an account to relay (`Verse-For-Principal`) | 403: a runner relays a task, never an account, and the request is not carried out as the runner instead |
| a `Verse-Task` beside any token but a runner's, or beside none | 403 or 401: only the runner hosting a task carries a request for it |
| no token, under `off` or `optional` | as the `Verse-Principal` it names, or as nobody |
| no token and the name of a runner registered with a `subject`, under every mode | 401: the runner presents its token |
| no token and a `user:` name, under `required` | 401, saying to log in |
| no token and an `agent:` name, or a `runner:` registered with no `subject`, under `required` | as that name |
| no token and no name, under `required` | as nobody: it reads what `*` holds, and writes nothing |

`auth.principal.mode` says how strictly the verse requires a token.

| mode | a person is |
|---|---|
| `off` | whoever the request says. No token is verified. |
| `optional` | whoever their token proves, or whoever the request says where it carries none |
| `required` | whoever their token proves, and nobody else |

**Verse verifies people, iods and registered runners, not agents.** The modes above verify
people, `auth.iod` verifies the deployment's own iods, and a runner's registration says whether
verse verifies it ([runners at the door](#runners-at-the-door)). An `agent:` principal, and a
`runner:` whose registration names no `subject`, is taken as given, under every mode. An agent
may read bindings, so `required` does not close the door to everybody who is not verified.

An [`asserted` binding](#accounts-in-other-systems) is not answered to an iod's lookup under
`required`, until a verified caller proves the account again.

Under `optional`, a `user:` that carries no token is also taken as given. A binding records
which kind of caller proved it (`claimant`), so a reader can tell a verified name from an
asserted one.

`GET /whoami` answers who the request is from and how verse knows: `{principal, by}`. `by` is
the provider that verified the caller, or `asserted`. Where an iod relayed the caller, `via`
names it. A request that names nobody is answered `{}`.

### Your account

| command | does |
|---|---|
| `verse account` | lists the accounts in other systems that are bound to you |
| `verse account login [<provider>]` | logs in to an identity provider in the CLI's config, and keeps the tokens. The provider may be left off when the config names one. |
| `verse account -link <provider>` | binds an account in another system to who you are. At an identity provider, it logs in there and presents the token as evidence; the login in use does not change. At a provider whose accounts an iod proves (`slack`, `github`), it claims the account and prints the words to say from it; `-account <login>` names a GitHub login. For GitHub it then waits, asking `verify` until verse has read the words in a comment, and ends when the account is bound or the claim expires; a login already bound to you is said so, and not claimed. |
| `verse whoami` | prints who the verse takes you for, and how: `user:ana@example.com (google)`, or `(asserted)` |
| `verse account logout [<provider>]` | drops the login toward this verse: every provider's, or the one named |

- **How it logs in.** Where the provider offers the device authorization grant (RFC 8628), the
  CLI prints a URL and a code, and waits while you confirm the code in any browser. Otherwise it
  prints the provider's login URL and listens on a loopback port for the browser to come back
  (authorization code with PKCE). It asks for the scopes `openid` and `email`, and for
  `offline_access` where the provider lists it.
- **What is kept.** The id_token, when it expires, the refresh token if the provider gave one,
  and the principal the verse answered. There is one entry for each verse address and provider.
- **What is sent.** After a login, every request carries the id_token and no `Verse-Principal`.
  The login comes before `user` in the config.
- **When the CLI acts as somebody else.** With `-as`, or with `agent` in the config, the CLI
  sends that name alone and no token. A token beside another name is refused.
- **Renewal.** An id_token that has expired is renewed with the refresh token before the request
  is sent. Where it cannot be renewed, the command fails and says to log in again. It is not
  sent under the config's `user` instead. A provider that does not answer within 30 seconds
  fails the command too.
- **A login the verse refuses is not kept.** `verse account login` asks the verse who the token
  proves before it reports success. That is also when a first sight is bound.

### Iods at the door

An iod is a program of the deployment's own. It proves which iod it is with the token its runtime
gives it. `auth.iod` in the [config](up.md#configuration) says what that token is:

- **On Kubernetes**, the pod's projected service-account token, asked for with the audience
  `verse`. Its issuer is the cluster's own. On EKS that is
  `https://oidc.eks.<region>.amazonaws.com/id/<id>`, which is public. On minikube or kubeadm it
  is the API server itself, behind the cluster's CA, which `auth.iod.ca` names. Verse verifies it
  as it verifies a person's token, against the issuer's published keys, with `aud` checked
  against `auth.iod.audience`.
- **With no cluster**, a token in a file the deployment wrote. Verse reads the file at start and
  keeps the token's hash.

The config says which iod a token is: `auth.iod.accounts` maps a token's subject
(`system:serviceaccount:<namespace>:<name>`) to the `agent:` the iod acts as and the name it is
installed under. At every start, verse writes the accounts to `verse.identity.iod.account.<sub>`
and a static token's hash to `verse.identity.iod.static.<hash>`, where anyone can read them. An
iod is never bound at first sight. A verified iod is its principal under every mode, and `whoami`
says `by: iod`.

### Runners at the door

A [runner](runners.md) proves which runner it is the same way, with the workload token its
runtime gives it, from the same issuer (`auth.iod.issuer`, audience `verse`). What differs is
where verse is told which runner a token is: not in the config, but on the runner's
registration, whose `subject` is the token's (`verse runner add -subject
system:serviceaccount:<namespace>:<name>`). Whoever writes the registration vouches for the
runner, once, and that is the whole decision. Who that may be is the record's to say: the seed
holds `owner` beneath `verse.runners` out to everyone, the open verse's answer, and a deployment
narrows it in the record, as `deploy/repopulate.sh` does to `user:admin@verse` with everyone
still reading. What a runner proved is recorded as verse, whoever asked.

The door keeps one table for both: a workload token's subject is looked up among the iods first
and the registrations second. A match is `runner:<identity>`, the identity on the registration,
under every mode; `whoami` says `by: runner`. A runner's own requests are made as itself, so its
token beside a name that is not its own is refused.

<a id="a-task-carried-by-its-runner"></a>
**A task's request is carried by its runner.** A verb a task performs on verse is the task
owner's act, and the runner hosting the task carries it: the runner's token, and `Verse-Task`
naming the task. Verse admits it through the task's record, which verse wrote at the spawn. The
record's `runnerName` is the runner's registration, its `runnerSubject` is the subject the runner
proved, and the task has not ended. The request is then made as the record's owner: whoever ran
the task, or the owner of the charter whose rule spawned it, or the person a rule's
`as: subject` acts for. `whoami` says `by: runner` and `via: runner:<identity>`. A runner
carries what the [verse system](../systems/verse.md)'s tools do on verse, raising an ask
(`POST /asks`, `verse-ask-create`), reading the owner's asks (`GET /asks`, `verse-ask-get`), and
`whoami`; any other route named with a task is refused, since what follows a task's ask is held within the task only
where the ask is raised. A refusal at this door carries `WWW-Authenticate`, so a runner tells
its own fault from a refusal of what it carried. The runner names
nobody, and a `Verse-Principal` beside the task names that owner or is refused. A registration
rewritten under the same name with another subject carries nothing of the tasks the old one
hosted, and a task the system owns is carried for nobody. The embedded runner's tasks reach verse
in-process and are not carried this way.

**A name verse can verify is not taken at its word.** A request naming the principal of a
runner registered with a `subject`, with no token that proves it, is refused with `401`,
whatever the mode: the runner presents its token, and a request made as it by hand cannot. A
runner registered with no `subject` is taken as given, as every runner was, and the refusal
says what the registration lacks. So a verse's embedded runner, a runner on a laptop and a
verse with no workload issuer go on as they were.

**A static token proves a runner off Kubernetes.** A bearer that is not a JWT, and is no iod's
static token, is looked up by its hash among the registrations, whose `subject` names a static
token as `sha256:<hex>` (`verse runner add -token-file`). A match is the runner, as a workload
token's is. Verse holds only the hash, and each kind of proof finds only its own kind of
registration.

**A registrations read that did not happen refuses.** Whether a name needs proof is the
registrations' to say, so where they cannot be read the door answers `503`, to a runner's token
and to a bare runner name alike, rather than taking the name as one nobody registered.

The registration also holds the runner to its subject when verse asks it what it offers
([runners](runners.md#who-the-runner-is)): a runner that does not prove it is the one registered
takes no spawn. And verse proves itself to the runner the other way round, with a token it signs
as the issuer `auth.issuer` makes it, minted for that runner's subject alone.

### A person relayed by a trusted iod

An iod that acts for a person does not act on its own authority. It relays a person the external
system identified: the Slack member who typed an answer, or the GitHub login who commented.
Verse admits such a request as the person when all of these hold, in order:

1. The bearer verifies as an iod.
2. That iod is in the provider's `iods` (`auth.principal.providers.<provider>.iods`): the deployment
   said this iod's word counts for that provider's accounts, and for nobody else's.
3. The request names the account, `Verse-For-Principal: <provider>:<id>`, beside the person's name in
   `Verse-Principal`. The account is what the iod saw; the name is who it concluded.
4. The account resolves to the presented person: through its
   [binding](#accounts-in-other-systems) at `verse.identity.<provider>.account.<id>`, or, on
   first sight, where the provider has `domains` and the person's email is in one. A first sight
   writes the binding with the provider as its `claimant`, the iod as `via`, and
   `proof: {method: relay, iod, account, at}`. An account bound to somebody else is refused with
   409.
5. The act is one a trusted iod does for a person: a read, or answering a question, saying on it,
   and steering a task or a flow. A provider's entry may say `acts: any`.

The request then goes on as the person. The permission check, the write and the record are
theirs, and a ballot records `authenticated: <provider>` and `via: <the iod>`. Each part of the
trust is stated: verse checked that the iod is the iod, the external system checked the person,
and the deployment said the iod counts for these accounts.

It is not delegation between people. A person's token beside another person's name is still
refused. An iod presenting an `agent:` or a `runner:` is refused. A provider with no `domains`
admits a relayed person only through a binding that already stands. A stricter deployment wants
that: a person logs in once, links their account, and only then answers from Slack.

## Accounts in other systems

An **account binding** says which account in another system is which principal. With it, an iod
acts as the person who wrote something there, without keeping a table of its own.

In short: a person binds their own accounts, one per system, and the system writes the record.

- For Slack and GitHub, the proof is a nonce. `verse account -link <provider>` gives it, and the
  person says it from the account. For Slack, the iod that heard it reports it. For GitHub, the
  CLI asks verse to find it in the comments the github iod reflects. Either way the binding is
  made.
- For an identity provider, the proof is a verified token.

A binding says how it was proved (`proof`) and how the person who proved it was authenticated
(`claimant`), so a reader can tell a verified name from an asserted one. One account is one
principal, and a person may hold many accounts.

An account is keyed by its issuer and the issuer's own id for it, `(iss, sub)`, as OIDC keys one.
Bindings are kept per **provider**: the name a deployment gives an issuer, or gives the iods it
trusts where no issuer is involved. There are two providers of that second kind:

- `github`: its `iss` is `https://github.com`, and its `sub` is the user's number. A login can be
  renamed, so it is kept only as a hint, and a binding survives a rename.
- `slack`: its accounts are the workspace's members, as the slack iod names them. One is bound
  when the person says a nonce to the app (heard, below), or, where a domain admits it, when the
  iod first [relays the person](#a-person-relayed-by-a-trusted-iod).

**The person claims, and the system binds.** Only `system:identity` writes a binding, from
evidence verse read itself. A binding is not on the person's own records, because a person could
rewrite what they own.

| route | as | does |
|---|---|---|
| `POST /accounts/<provider> {login}` | the person | claims an account under a provider whose accounts an iod proves (`github` with the login; `slack` with nothing), and answers `{nonce, expires, say}`. The nonce stands for an hour. |
| `POST /accounts/github/verify {nonce}` | the person | binds the GitHub account, once the account has said `/verse link <nonce>` in a comment a trusted iod reflects |
| `POST /accounts/<provider>/heard {nonce, account, login}` | a trusted iod, verified | binds the claimed account from the iod's word that it said the nonce: the Slack iod hearing it from a member, the github iod reading it in a comment. Where the claim names a login, the `login` that said it must be that one. |
| `POST /accounts/<provider>/link {idToken}` | the person, verified | binds the account that the id_token names, at an identity provider the verse names |
| `GET /accounts` | anyone | the caller's bindings, under every provider |
| `GET /accounts/<provider>/who?sub=…` or `?login=…` | an iod | the binding of that account: `{provider, principal, iss, sub, login, claimant, proof}` |
| `DELETE /accounts/<provider>/{sub}` | the person | revokes their own binding |

- **The proof** of a GitHub account is a comment that says `/verse link <nonce>` on a line of its
  own. The claimed login must write it, on a repository that a trusted
  [github iod](iod.md#github) watches, and it must carry the author's number (`authorId`). Until
  the iod has written that comment into the store, verify answers 404 and the claim still
  stands. An expired claim answers 410, and is removed. Two routes read the same comment: the
  github iod reports it itself (heard, next) when the comment replies to a question it is asking,
  and the person asks for `verify` for a comment on any issue or pull request the iod watches.
  `verse account -link github` asks for `verify` until the comment is read, so the person need not
  find a question to reply to.
- **A nonce heard.** The same words, said where a trusted iod hears them, bind the account that
  said them. The iod itself reports it (`heard`). The Slack iod hears `/verse link <nonce>` in a
  message from a member, and names `slack:<their id>`. The github iod reads it in a comment, and
  names `github:<the author's number>`. The iod is verified by its own token and trusted for the
  provider, as for the [relay](#a-person-relayed-by-a-trusted-iod). The binding's `claimant` is
  whoever claimed, as they were authenticated then, and its proof is
  `{method: heard, iod, account, at}`.
  - A GitHub claim names a login. The nonce said by any other login binds nothing, and the claim
    stands.
  - A Slack claim names no account. The nonce binds whoever says it, because a person says it
    from their own account, as they would use a credential.
  - A nonce nobody claimed binds nobody.
- **Trusted iods.** Verse reads a comment only under the root of an iod the deployment trusts,
  and only an iod the deployment trusts relays a person. The deployment names a provider's iods
  in its config, `auth.principal.providers.<provider>.iods`. At every start verse writes each list
  to `verse.identity.<provider>.trust`, where anyone can read it. Nothing else writes it. Where no
  iod is trusted for `github`, a claim and a verify are refused with 409.
- **One account is one principal.** Proving an account that is bound to somebody else is refused
  with 409, until they revoke it. Proving it again for the same principal answers 200. One
  principal may hold many accounts.
- **Who reads what.** A claim is read by its principal. A binding is read by its principal and by
  `agent:*`, so an iod can look an author up. Anyone else asking `who` gets 404.
- **Where they stand.** Claims are at `verse.identity.<provider>.claim.<nonce>`, and bindings are
  at `verse.identity.<provider>.account.<sub>`. A `sub` is one segment of the address, whatever
  characters its issuer put in it. A binding's `proof` is one of:

  | proof | for |
  |---|---|
  | `{method: comment, evidence: <the comment's address>, at}` | a comment |
  | `{method: heard, iod, account, at}` | a nonce an iod heard |
  | `{method: oidc, iss, sub, iat, at}` | a token |
  | `{method: relay, iod, account, at}` | a relay |
- **Linking a second provider.** A caller that one provider has verified presents an id_token
  from another provider as evidence, and verse binds that account to them. No nonce is needed,
  because the token proves the account. The token must be the named provider's and must verify,
  or the link is refused with 400. A caller who is only asserted is refused with 403: the binding
  would let the account's token act as a name nobody proved. `verse account -link <provider>`
  does this from the CLI.
- **The claimant.** A binding also records how the caller who proved it was authenticated:
  `claimant` is the provider that verified them, or `asserted` for a caller who was whoever their
  request said. A binding is only as good as the name it binds to, so it says which kind of name
  that was. When a verified caller proves an `asserted` binding again, the binding takes that
  provider as its claimant. Proving a binding again as an asserted caller changes nothing.
- **Trust.** The evidence for a GitHub account is as trustworthy as the iod that saw the comment,
  and a relayed person is as trustworthy as the iod that relayed them. So the deployment says
  which iods those are, and an iod proves which iod it is ([iods at the door](#iods-at-the-door)).
  An account at an identity provider is bound from a [verified token](#who-a-request-is-from):
  the same binding, with a different proof.

## What each act writes

| act | writes |
|---|---|
| a raw put | what it names, as the requester |
| a spawn | the task, as the requester. For a firing, the task is written `via:` the rule, which is the charter's owner. |
| a worker's claim | `read-write` and `control` for the claimant on the task and the run |
| posing and settling a question | as `system:docket`, in the request's scope, whoever is working the task |
| a grant to a resolver | its `answer` line, at `verse.for.<you>` or at the place |
| an install | the thing, as its installer, with their record |
| a suspension | as the system, naming who holds it under `held` |
| verifying an account, or an iod reporting its nonce heard | its binding, as `system:identity` |
| a first request with a provider's token | the account's binding, as `system:identity` |
| a first request a trusted iod relays for a person | the person's account binding, as `system:identity` |

## The root, and the config

The root's record stands at `verse.perms.root`. Its `child-grants` are the grants a node created
with no record above it starts with: `{read: ["*"]}` unless the config says otherwise.

`verse` has the system's record, with no grants. That record keeps the root's grants out of
verse's own half.

Who owns the root, how the config sets it, and the rows verse writes at start (the seed) are in
[the root and the config](../platform/perms.md#the-root-and-the-config), in the repository.

## `verse perms`

```
verse perms                      the records of every node you may read
verse perms --root               the root's record
verse perms <path>               that node's record, to anyone who may read the node
verse perms <path> -grant '{read: [ana@example.com]}' [-below]    add a line
verse perms <path> -revoke '{read: [ana@example.com]}'            take one back
verse perms <path> -grants '[…]'                                  replace the node's grants
verse perms <path> -child-grants '{…}'                            the grants what is created beneath starts with
```

Setting a record on a node that has none takes the place first
([above](#creating-and-taking-a-place)). A node created without a record of its own keeps no
copy of the `child-grants` it was created under: it is its owner's through the place. So
closing the place closes what is already in it.

`verse status` counts these under `perms`, since the process started:

| Field | What it counts |
|---|---|
| `refused` | refusals, by level |
| `unreadable` | checks that the mirror could not answer |
| `hidden` | what listings withheld |
| `unparsed` | grantee names on a record that the grammar does not read. Each admits nobody, and versed logs each once per record. On a verse whose records were all written under the grammar, `unparsed` is zero. |

<a id="suspending"></a>
## Suspending

`verse flow suspend -charter … -subject …` is a control write ([control](../control.md)). Verse
checks who may make one twice.

**When it is written**, the writer must hold `control` over the charter it names, or at the
subject it names. Either one is enough. The charter is a package's address, or a rule's own
record. For a selector with `*` in it, verse checks the part before the first `*`. Verse refuses
a suspension that names neither.

**When it applies**: every time work would go on, verse checks the scope's records at that
moment. A suspension covers the work while its holder holds `control` over the work's charter or
at its subject. So a wildcard does not reach further as new owners appear beneath it. A check
that cannot be answered covers the work.

A rule the system installed is covered only by a system holder. When a holder loses that
authority, the suspension stops covering new work at once. What it already held stays held until
somebody resumes it.

<a id="scopes"></a>
## Scopes

Baseline's grants apply in a [scope](../glossary.md#scope). Verse reads a scope's records
through the scope's own view of the store, which lies over baseline. So every grant in baseline
stands in the scope, and a record that changes in baseline is seen in every open scope at once.

A grant made in a scope is copy-on-write, like any write to the world. `verse perms -scope me`,
and the record every creation in a scope leaves, land in the scope's layer. They apply to the
scope's writes on top of baseline's, and baseline never sees them. Two scopes may hold different
owners for one address. Dropping a scope drops its grants.

A task or a gate cannot be acted on from a scope that did not create it
([what a scope owns](scopes.md#what-a-scope-owns)). Grants layer and tasks do not because a grant
has no live part. A task is a handle and a gate is an awaiter, and each runs in one engine. A
grant is a record that a cache reads, and a cache layers as records do.
