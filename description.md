# systems: Environment.Without keeps a block that does not give the verb

`Without(system, verb)` rebuilds every block from `Block.Given`. For `network`, Given answers the block's own verbs, which are nil for a mode-only block, so the block is dropped as "giving nothing else". The result is `{network: {mode: open}}.Without("network", "tcp-client")`, which reaches no network.

The only caller today subtracts `verse.charter-install`, so this is latent. The method is general, though, and its doc says a block giving other verbs still gives them.

Fix: a block that does not give the verb is kept as written. Only a block that does give it is rewritten, and it is dropped if nothing else is left.