# verse charter (draft v1)

Status: a proposal for the team to decide. Nothing here is in force until it is adopted (see
[Adopting and changing this charter](#adopting-and-changing-this-charter)). 

## Statement of purpose

The purposes of verse are:

1. To govern SDLC work by people and agents across systems it does not own, so that the work
is organised, observable, gated, steerable, and controllable.
2. To govern that work whoever does it: verse's own agent loop, where gates are enforced by
   construction, or the agents, tools and people already in use. And to state what each one's
   gates guarantee in a fashion which best serves 1.
3. To achieve 1 and 2 in a fashion which maximises the return value of where it seeks human attention
and which scales to a massive rate of change.
4. To contain no work outside this charter, and to change that only through changing the charter.
5. To act so as to be something others can freely build on and use, initially via internal use.
6. To take on work and people's or organisation's time and resources, only in order to carry out the first five.

## What verse is

A small runtime for governing SDLC work across systems we do not own.

- It reflects those systems (git, GitHub, Linear, Slack) into a shared, observable entity store.
- **Charters** of rules react to changes in that store.
- The work goes to **doers**: verse's own agent loop, external agents such as Claude Code,
  scripts, dial-in workers, or people.
- Every reaction passes a **gate**: auto, ask, or park.
- Running work can be watched and steered: paused, resumed, cancelled, suspended.
- Every write names who made it, and there is a permissions model over both data and control.
- Rules can also run on a schedule.

It has run on staging since mid-August 2026.  It is a result of
- Blueprint — SDLC as Code
- On Representations of Agentic SDLC
- Blueprint — Core Model
- Discussions of the above and verse itself.

## Ownership and this charter.

This charter is owned by

judy@example.com, heidi@example.com, oscar@example.com, victor@example.com, trent@example.com, walter@example.com.

## Work under this charter.

Work under this charter refers to work that does not aim to change the charter
itself and rather is in service of it. Such work has distinct owners.  Ownership
of work under this charter is claimed in `docs/working/owners.tony`, where every
charter owner can see it, and is always stated with a deadline for engaging a
pending item in the work. When the deadline expires or does not exist in the
file, the ownership is void.   The default deadline is 1 month and it may not
exceed 3 months.  An owner is told before the deadline arrives, and renews the
ownership by engaging with the pending item or by naming a new deadline.

All charter owners may claim unowned work, and should minimize overlap between
the acts and content of the work.

Work under this charter shapes verse and changes are justified against the current
state of verse.

An owner of work seeks review to ensure the work reflects the group's intention,
and interested parties should direct review and feedback of work to whatever
is prescribed by the work's owner.  An owner should strive to make review
as widely accessible as possible.

## How we work together

1. Make verse gates work through slack.  This is a prerequisite for 2.
2. Apply gates for charter adoption and the repo design process (proposed details
   below).
3. Each owner decides how to organise building, code reviews, bug fixes, everything
not mentioned in point 2 above.  `main` is kept fast forward only.

### Adopting and changing this charter

- **Adoption is a team decision.** If gates can be answered in Slack by then, adopting this
  charter is the first gate. Otherwise the team adopts v1 by its usual means, the decision is
  recorded as a commit, moving the charter from docs/working to docs/
  and amendments are gated from then on.
- **The adoption gate is default-no.** It is adopted by its owners answering it; silence does
  not adopt it.
- **Changes follow the same path**, each with a justification.


### Design

- **Design docs live in the repo.** A proposal goes in `docs/working/`, then becomes a plan, and
  finally `docs/` describes what is true.
- **A design review is a note beside the doc, or edits to it.** Asking for one is a gate over the doc.
  Review may occur out of repo, but needs to be reflected into the repo by this mechanism in any event.
- **The owner of the design work sets that gate's default**, and so says what silence does.

### Deployment

- Deploying a shared instance is work under this charter, so it has an owner and that owner
  decides how it proceeds.
- Deployment of shared instance `I` gets an annotated git tag `deploy/<I>.N`. 
  The annotation for the tag mentions the components which are deployed.
- Deployment tags the main branch in normal operation, but may tag off main
  to work around a deployment issue.  If it tags off-main, the tag annotation names the
  nearest main commit.

What is deployed should reflect the group, and that is a matter of interpretation this charter
leaves open. What it does not leave open is who acts on the interpretation:

- **An owner says what of their work reflects the group and how.** Saying so is an act they perform,
  not a property anyone derives from the work, and they say it where the charter owners can see
  it.
- **The deployment owner deploys what has been communicated to them as reflecting the group**,
  and reads that communication as they find it.
- **On a conflict, the default is not to deploy.** The deployment owner leaves contested work
  out and reaches out to the owners concerned. Non-deployment is cheap and reversible;
  deploying work somebody says does not reflect the group is neither, and the tag's annotation
  would then say the group deployed it.

## Success criteria

- **Gates:** a gate raised by any doer reaches its answerer, can be answered where it lands,
  and says what guarantee it carries.
- **Charter is adopted** 
- **Control+Steering:** pause, resume and cancel reach any doer at its next boundary, and say 
what boundaries are and how long they are expected to be unresponsive.
- **Internal Usage** internal usage exists and either works smoothly or has concrete work to be done
  which would make it work smoothly.

