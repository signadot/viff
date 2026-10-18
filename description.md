# cli, versed: the iod verbs and addresses say io

The docs say iod (7daac51f on verse/cut): an iod has two halves, the five iods verse ships, `verse io add` installs one. The names in code still say io, so a reader learns one word and types another:

- the CLI verbs `verse io add|show|check|remove` and the listing `verse ios` (cmd/verse/io.go)
- the entity address of an installed one, `verse.io.<name>` (versed, mirror, the `verse io` output, docs/reference/io.md)
- the principal an iod acts as, `agent:<name>-io` (deploy/auth/*.tony `accounts`, the iods' `-as` defaults)
- the `auth.iod` block's `io:` key on an account (config.IodAccountConfig) and a provider's `ios` list (config, docs/reference/up.md)
- the `io/` packages and `docs/reference/examples/io`

Rename them to `verse iod …`, `verse iods`, `verse.iod.<name>`, `agent:<name>-iod`, `iod:` and `iods` (Tier contract: a deployed verse's registrations and auth config change spelling; the carry at the next wipe names them). The packages can stay `io/`: a Go path is not a word a user types.

Not for now: nothing is wrong, and a rename of addresses on staging is a deploy step. Do it in one commit when a wipe is due anyway, with deploy/carry.sh and the auth tony files updated beside it.