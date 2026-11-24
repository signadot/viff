# Why: Entities

The reasons behind [Entities](../reference/entities.md). They are for somebody changing viff; the page itself says what viff does.

## A positional argument is one step

A single word `w` has two legal readings: the one segment `w`, and `split(w, delim)`. If viff
chose either reading, the other would be impossible to say. A positional argument cannot be
both, so it is a step and nothing else.

A flag is different. It is a different *place* on the line, not a second reading of the same
word. So a whole address in one token is unambiguous where a flag marks it, for the same reason
that the positional argument is not. Viff declares which grammar applies, and does not guess.

The bracketed step clauses do not break this rule. What separates them from reading a delimiter
out of a word is the count: a delimiter changes how many segments a word is, and a bracketed
clause does not.

## The CLI and array writes

The third row of the table in [Reaching into an array](../reference/entities.md#reaching-into-an-array) makes the other
two rows safe to offer. An accepted patch past the end of an array would leave the log
unreadable from then on. It would be a bad write that succeeds and breaks every later read of
the whole store, with no repair. So the CLI can stay silent only because logd refuses such a
write at the patch.

## A write through an array

Without the refusal, the loss would be silent. `bo` is not a field of an array, and there is
nothing to merge into. So the array would be replaced by `{bo: yes}`. Both ballots would be
gone, and the command would exit 0. The answer echoes the value written, not the entity. So
nothing in the output would say that anything was lost.

## No history without a cursor

A replay of a long-lived store is many lines that nobody asked for with a flag. On a store that
cannot replay from the beginning, it would be a refusal, not a stream. So history is asked for
with a cursor, and a watch without one starts from now.
