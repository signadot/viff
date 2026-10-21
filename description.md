# cli: url.PathEscape on a path the client escapes again double-encodes the id

`client.Client.URL` builds `url.URL{Path: path}`, and `String()` escapes the path itself. A caller that first runs `url.PathEscape` on an id therefore sends it encoded twice.

## Observed

The id is sent through verse's client to a `ServeMux` route `GET /asks/{id}` with `"/asks/"+url.PathEscape(id)`. The server's `PathValue` is:

| sent | server sees |
|---|---|
| `plain` | `plain` |
| `a b` | `a%20b` |
| `50%` | `50%25` |
| `x@y.com` | `x@y.com` |

Without `url.PathEscape`, the client's own escaping delivers each id as sent. It also already escapes `?`: an id `x?since=0` goes out as `x%3Fsince=0`, not as a query.

## Where (main)

- `cmd/verse/ask.go`: 400 (PATCH ask), 425 (DELETE ask), 774 (`/served`)
- `cmd/verse/client.go`: 720 (GET question), 767 (POST answer)
- `cmd/verse/group.go`: 58, 81
- `cmd/verse/login.go`: 580, 606 (`/accounts/<name>`)
- `cmd/verse/schedule.go`: 47

`cmd/verse/mcpask.go` on verse/mcp does the same for `verse_question` with an id, matching the CLI.

## Reach

Latent for asks. An ask's id is derived from its name and slugged: `verse ask "a b" -q …` makes `a_b-e9a56460e06f`, so an ask id carries nothing that escaping changes. Not checked: whether group, account (provider) or schedule names can hold a space, `%` or similar. If one can, that command reaches the wrong name.

## Fix

Drop `url.PathEscape` at these call sites and let the client escape. A `/` inside an id is the one thing the client cannot carry in a segment. Where an id could hold one, refuse it, as the per-task MCP tools on verse/mcp do.