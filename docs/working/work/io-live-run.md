# The first live run of both io halves: what it found

A local verse against the real Slack workspace and the real Linear team, 2026-09-23, exercising
the in half, the out half and the gate charter by hand. Everything below was found by running it,
and none of it by the tests — which is the note's point. Four are fixed, three are open.

The run: `iod slack` and `iod linear` from the worktree, both with `-out`, registered with
`verse io add … --out <place> --requesters 'user:*'`; the gate charter installed at baseline; one
rule (`ship-ask`) posing a question with a `pose` and two options that say what they mean.

## Fixed

**1. `verse io add … --out <place>` refused every out half an operator typed** (`2d7db7fd`). The
"out places are inside what this io claims" check ran on the POSTED body, and an operator names
no claim — it comes from probing the io, after the request is read. So the check saw an empty
claim and refused, including for `deploy/repopulate.sh`'s own `add_io` line. `Entity.Check` now
stops at the fields and `OutHalf.CheckClaimed` runs after the probe. Every existing test passed
`owns` explicitly and so never went through that door; the regression test uses the git face.

**2. The out half acted as `agent:-io`** (`cd28961c`). It read its principal from `-root`, which
is normally unset, while the install seeds grants for `agent:<connector>-io`. Every status it
wrote would have been refused. It takes the connector's own root now.

**3. The gate charter DM'd people about questions that do not stand** (`2b7a16bb`). The charter
selects as the system and sees orphan records; the io reads each question as `agent:slack-io` and
cannot. What arrived in somebody's DMs was *"No question `verse.trigger.…~43e712da` stands"*, once
per pass, about nothing they could act on.

The rule taken: never CREATE a message about a question that does not stand — refuse the
delivery, leave the error on the status, retry — while a REDRAW keeps that line, since there
somebody was asked and what became of the question is the news. It replaces the opposite rule,
also mine, which posted first and redrew once the question appeared. Waiting costs one pass when
a question is a moment late; posting costs a permanently wrong message when it never becomes
readable, and the io cannot tell those apart at the moment it decides.

**4. `to` was ignored after the first delivery** (`d159a13e`). `Deliver` resolves the target only
when the status carries no `ts` (no `comment`, for Linear); afterwards the status decides where
the text goes. So changing `to` on a delivered input neither moved the message nor failed — it
silently kept editing the old one. Proved by accident with `to: "user:scott@…"`, not an address at
all, which updated the existing DM.

Of the three defensible answers — refuse, post anew at the new target, or write down that the
target is fixed at first delivery — the refusal is the one taken, in the FRAMEWORK rather than in
each connector, because it is the contract's rule. The status records `to` on a delivery, which is
what lets the check compare without resolving the address again; a delivery that never happened
records none, so a typo is still fixed by editing it.

## Open — each filed, with what it has to decide in the issue

**5. An orphaned question cannot be answered or removed** (`rbrgs0tm`). A question whose engine awaiter is gone
— a restart, a rule that no longer fires — stays pending on the docket forever:

- `verse gate <id> -answer deny -as <them>` → `no such question`, because `answerQuestion` finds
  no awaiter (`posed`/`raised`), although the record stands and is readable by its answerer;
- `verse d verse.gate.question.<id> -rm` → refused, *"is verse's own … written by the verbs that
  own it (… gate approve)"* — and `gate approve` is the verb that 404s.

The only thing that clears one is a restart, which withdraws orphans at startup. Since **3** it no
longer reaches anybody, so what is left is a docket that cannot be emptied and a read per pass.

**6. The CLI reports an unreadable node as an absent one** (`65xaf72n`). `verse d verse gate question '<id>'`
says *"nothing stands at …"* for a question that does stand and that its own answerer can read.
Reading `verse.gate` itself says `refused: … holds nothing`, so the two answers disagree about the
same tree, and "nothing stands" sends a reader looking for the wrong problem.

**7. `--if` on `verse d` is a guard, not a selector** (`rb6nvzjp`), and reads like one. The answer is one item,
so `-if '{name: verse-dev}'` over a collection of channels prints `{}` rather than the channel
that has that name. Documented in `cmd/verse/filter.go`, and still the first thing an operator
tries. That comment also names a `state` command that no longer exists.

## Not defects

The Linear io first came up on `:9811`, where an unrelated `runner` process already listened on
`*:9811` over IPv6, so `localhost` reached that instead and the probe read "not a source". Moved
to `:9821`. Environmental, but worth a minute of anyone's time to recognise.
