# Verse as groundskeeper

A groundskeeper takes care of a place, so the people who use it do not have to. Verse can do
this for a repository: it does routine upkeep while your team builds.

Three things are true of verse as groundskeeper:

- Verse runs on its own. It sees when something needs doing, and does it.
- Everything happens through the repository. Verse reads the repository and its pull requests.
  Its work appears there as branches, pull requests, issues and comments.
- Each process is small. Something happens in the repository, verse does one piece of work, and
  you get one result to look at.

You stay in control as you do today: you review what verse proposes, and you answer what it
asks.

## The charters live in the repository

Each process is a [charter](glossary.md#charter). The charters live in the repository they
take care of, in `.verse/charters/`. Each file is one charter, and its `at:` says where it
installs ([one file is one charter](reference/charter.md#a-charter-can-be-installed-at-an-address)).

A deployment names the repository in its configuration. Verse then installs one more charter
for it, the **reconciler** ([`repoCharters`](reference/up.md)):

```tony
repoCharters:
- repo: verse
  github: signadot/verse
  at: repos.verse
  answerers: ["user:*"]
```

The reconciler keeps one thing true: **what runs in verse is the last state of
`.verse/charters/` on main that somebody said yes to.** So a change to a charter is a change to
the repository, and it is reviewed like any other change.

- **A pull request that changes the charters** gets a question: install this? Verse also checks
  that the new charters install. It reports the answer and the check on the pull request, as the
  statuses `verse` and `verse/check`. Neither holds the merge. A yes given on the pull request
  installs the charters once it merges, with no second question.
- **A change that reached main without a yes**, such as a direct push, gets an issue that asks
  the question and says how to see the change against what runs. The statuses go on main's
  commit.
- **What is installed follows `verse/live`.** It is a record the git [iod](glossary.md#iod)
  keeps in the repository, `refs/verse/live`: not a branch, but a chain of commits, each holding
  the charters it installs and naming the commit of main they came from. A yes adds the change to
  it, and the iod installs the charters it holds at `repos.verse`, removing a charter that is not
  in the directory. `git log refs/verse/live` is what ran, and from where.
- **Every problem has an issue.** A no, a check that fails, an install that is refused, and
  installed charters that differ from `verse/live` each open an issue. The issue closes when the
  cause is gone. A refused install is asked again when verse's minute check finds it would
  install.

The reconciler is itself a charter that verse ships.
[Its text](../system/repocharters/charter.tony.tmpl) explains all of this in its comments, and a
deployment installs [a rendering of it](reference/examples/repo-charters/repo-charters.tony).

## The GitHub App

Verse acts on GitHub as a GitHub App. Its pull requests, reviews, comments and statuses are the
App's, as `<app>[bot]`. So verse can review a pull request that a person opened, and a person
can review one that verse opened. With a person's token, neither works: GitHub refuses a review
from a pull request's own author.

**1. Create the App** in your organization's settings (Developer settings → GitHub Apps). Turn
its webhook off: the events come from the repository's webhook (step 4). Give it these
repository permissions:

| Permission | Access | What verse does with it |
|---|---|---|
| Metadata | read | reads the repository |
| Actions | read | reads the checks on a pull request |
| Contents | write | merges, and writes `refs/verse/live` |
| Issues | write | opens and closes the issues that say what went wrong |
| Pull requests | write | comments, opens, reviews and closes pull requests |
| Commit statuses | write | posts `verse` and `verse/check` |

Leave Workflows off. Then GitHub refuses the App a merge of a pull request that changes
`.github/workflows/`. That is what you want from an App that merges.

**2. Install it** on the repository (Configure → Repository access). Verse cannot see a
repository that the App is not installed on.

**3. Give both iods its keys.** The github iod and the git iod read the same three variables, and
with them each acts as the App:

| Variable | What it is |
|---|---|
| `GITHUB_APP_ID` | the App's id |
| `GITHUB_APP_INSTALLATION_ID` | the id of its installation on your organization |
| `GITHUB_APP_PRIVATE_KEY` | the App's private key, as PEM |

Run the github iod and the git iod each with `-out` and `-tree .verse/charters`. With `-tree`,
each iod reports the charters directory, so verse sees a pull request that changes it. With
`-out`, each iod carries verse's writes back to GitHub. The git iod installs the charters as
itself, so it must own the address they install at, or hold `child-owner` above it
([the out half of git](reference/iod.md#the-out-half-of-git)).

**4. On the repository**, add a webhook to `https://<host>/github/webhook`, with the content type
`application/json` and the secret that the github iod reads as `GITHUB_WEBHOOK_SECRET`.

[The deployment guide](../deploy/README.md#the-github-app) creates verse's own App
from a manifest and puts its keys on a cluster.

## What runs on verse's own repository

Three processes, in [.verse/charters/](../.verse/charters/):

| Process | When it runs | What you get |
|---|---|---|
| [Comment drift](../.verse/charters/comment-drift/drift.tony) | new commits land on main | the Go comments that no longer match the code are fixed, each fix on a branch `verse/auto/<task>` |
| [The merge gate](../.verse/charters/comment-drift/merge.tony) | there are fixes that still apply to main | one question, "merge these?", for every fix that applies; a yes fast-forwards main |
| [Issue next](../.verse/charters/issue-next.tony) | every three hours | each open issue gets a proposal for its next step |

**Comment drift** reviews everything that landed since the last review, against the code as it
is now. An agent does the review, and fixes each stale comment in place. The runner delivers the
work as a branch, `verse/auto/<task>`. The spawn's environment lets the task push only there, so a
fix is a proposal, never a change to main. The agent's answer has a fixed shape (an
[answer contract](reference/action.md#the-answer-contract)), so the charter can read the
findings. A finding stays open until a later review reads main and finds the comment true.

**The merge gate** collects the fixes that still apply to main into one batch, and asks once.
The question goes to any user, in Slack. If nobody answers in a day, the answer is no
([a deadline settles](reference/gates.md#a-deadline-settles)). A yes runs a script that rebases
each fix onto main and pushes. If main moved first, the fixes are dropped, and the next review
proposes them again.

**Issue next** is the smallest of the three: it writes a proposal for each open issue and runs
no task.

## Stopping it

Each process is a charter, so the ways to stop one are the ways to stop any charter:

- Suspend it, and lift the suspension later ([control](control.md)). With `-r`, every charter
  under the address is suspended:

    ```sh
    verse flow suspend -charter repos.verse.drift -why "a quiet week"
    verse flow suspend -charter repos.verse -r -why "a quiet week"
    ```

- Remove it from `.verse/charters/` on main. The change is asked about like any other, and a yes
  uninstalls it.
