# verse up: the default data dir doesn't depend on how `--addr` is spelled

Follow-up from the #59 review (smaller points).

The default data dir, `~/.verse/<host>_<port>` (cmd/verse/localrunner.go), is keyed by how `--addr` is spelled:
- `localhost:9797` and `:9797` share one;
- `127.0.0.1:9797` and `0.0.0.0:9797` each get a separate, empty verse.

Switching spelling silently loses the store. Normalising loopback and unspecified hosts to one key would fix it.