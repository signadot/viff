# Why: Tutorial

The reasons behind [Tutorial](../tutorial/README.md). They are for somebody changing viff; the page itself says what viff does.

## Why a script you type runs unasked

A gate asks about what you have not already authorized. A script is one action and no step,
and a spawn's `script` runs as written: an argument about **who addressed the request**. When
you write a shell command, you authorized that command. What the script then calls
(`viff-call-tool git-push`, say) is a step, which the spawn's environment and autonomy decide.
An agent asks before its steps because the agent *chose* them.

## Why a spawning rule must latch

A rule with no latch is considered at every commit that touches what it reads. If that rule
spawns, it spawns again and again, and every task costs money. So viff refuses it at install.

## Why derive exists

A rule that acts only on its `subject` has firings that viff can check. But without `derive`,
a charter has one way to bring an entity into being at another address: spawn an agent and let
the agent write. That puts the write outside anything that analysis at install can see. `derive`
states that write in the charter, where install can check it against the strata.

## Why there is no par

A `seq` gives order. Parallel work needs no construct of its own, because independent rules that
match the same condition already run in parallel.
