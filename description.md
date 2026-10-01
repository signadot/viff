# question: should ask: and answer: be charter acts next to patch: and derive:?

Asked about `verse/ask-next`: should a rule's `action` take `ask:` and `answer:`, beside `patch:`, `derive:` and `spawn:`?

Nothing is decided. Below is a first reading of the branch and a suggested direction.

## What stands on ask-next

- **A rule's action** is `patch`, `derive`, `spawn`, `calls`, `seq` and the `onX` hooks (docs/reference/action.md). A rule acts `via:` its address, which resolves to its installer. No rule but the system's writes under `verse`, but a rule may select a subject there.
- **A rule already asks, through its gates.**
  - A `mode: ask` gate poses a question on the docket.
  - Its id comes from (rule, gate, subject), so a firing that comes back joins the open question rather than posting a copy. Once answered it is not asked again; a later firing asks under `<id>-2` (trigger/asking.go).
  - A gate carries answerers, `need`, options, deadlines and `onDenied`.
  - A gate on a rule with no action is refused, because nothing would read its answer.
- **A person's ask** (`verse ask`, versed/asks.go) is its asker's record. Its `then:` and `else:` may hold one more act of the asker's:
  - `{answer: {ask, choice, say, edit}}` answers one named ask, with the words its asker saw;
  - `{ask: {…}}` raises another ask (versed/askthen.go).
- **Automated answering is a resolver's job.**
  - A party grants `answer` at `verse.for.<you>`.
  - The resolver's `match.about` and `match.labels` scope what it answers.
  - Its answer is recorded `via:verse.resolvers.<name>`, and two resolvers never answer for one party on one ask.
  - The only driver built is `service`; `llm` and `script` are not.

## Suggested reading

**`ask:` probably not.**
- Gates already are a rule asking, with identity, joining and settle-once solved.
- What `then:` and `else:` give a person's ask, a rule gets from its action and `onDenied`.
- The one thing missing is an ask that does not block: a question whose answer other rules react to. A gate plus a `derive` of the decision may cover that.

**`answer:` is a real gap, but it probably should not be an act any rule can use.**
- It would read naturally: a rule selects `verse.ask.*` and says `answer: {choice: …}`. A firing that comes back would cast the same ballot again and be refused, so level-triggering is not the obstacle.
- The obstacle is **whose ballot it casts**. Through `via:`, any installed rule would cast its installer's ballot on every ask it matches. Those are asks the installer never saw, answered with words nobody consented to.
- That is a standing policy answering asks. On this branch, that is what a resolver's grant and match scope. `then: {answer}` is different: it names one ask and the edit its asker saw.

**A possible shape:**
- a `charter` driver for resolvers, beside the unbuilt `llm` and `script` drivers;
- the rules of that resolver's package may use `answer:`, recorded `via:` the resolver;
- the answer counts only where a party granted `answer` to that resolver, held to its match, with the existing overlap refusal.

`patch:` and `derive:` stay the convergent-state acts, gates stay a rule's way to ask, and resolvers stay the one place an automated answer gets its authority.

## Open

- Is a non-blocking rule ask needed? If so, is it a gate whose rule's action is only a `derive`, or something else?
- `answer:` as a resolver's charter driver, or a rule answering as `via:<rule>` under a grant made to that rule? The second would need the grant grammar to name a rule.
- Should an answer cast by rules say so on the ask (by whom, through which rule), as a resolver's answer does?