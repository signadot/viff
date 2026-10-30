# Routing: one failed handshake takes a runner out until `verse runner check`

Follow-up from the #59 review (smaller points).

A failed handshake at a spawn records the registration unverified (`recordUnverified`), and `candidates` then skips every registration that is not Current. Nothing on the spawn path asks it again. runnerd's hello runs only at its own start, and verse's probe only at verse's start. Before #59, `offersOf` re-probed an undescribed registration when a spawn named it.

On staging there is one runner per doer, so a single unverified handshake (a token-rotation glitch, a slow restart) stops that doer until an operator runs `verse runner check`.

A probe showed it: runner `a` failed once and `b` took the spawn. Then `a` was healthy again and `b` failed. The next spawn was refused, and `a` was never asked.