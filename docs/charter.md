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

## Ownership, work, and this charter.

This charter is owned by

judy@example.com, heidi@example.com, oscar@example.com, victor@example.com, trent@example.com, walter@example.com.

Work under this charter shapes verse and changes are justified against the current
state of verse.

## Adopting and changing this charter

- **Adoption is a team decision.** If gates can be answered in Slack by then, adopting this
  charter is the first gate. Otherwise the team adopts v1 by its usual means, the decision is
  recorded as a commit, moving the charter from docs/working to docs/
  and amendments are gated from then on.
- **The adoption gate is default-no.** It is adopted by its owners answering it; silence does
  not adopt it.
- **Changes follow the same path**, each with a justification.
