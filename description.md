# the default row does not decide a network connection

On `verse/environment` (#33), the task's proxy decides each connection under `network: {mode: allow}` as the `network.tcp-client` step: outside the environment's `tcp-client` shapes it is refused, within them the autonomy decides, its default row included, and an `ask` refuses it since a connection can't wait for an answer (`runner/runnerd/network.go:31`). A row on the network that asks is refused where it is written (`task/autonomy.go:339`).

So under the default autonomy (`ask`), or any spawn whose last row is `{then: ask}`, an environment's `tcp-client` grant reaches nothing, and nothing says so where the spawn is written: every connection is a 403 and a count in the task's `refused`.

Decided (Scott, 2026-10-21): the default row does not decide `network.tcp-client`. The environment's `tcp-client` shapes are the reach, fixed at launch and within the runner's ceiling; a row on `network` allows or denies within them (and still may not ask); a connection no such row matches goes ahead, then the ceiling decides. A spawn whose default row denies still reaches the hosts its environment names; one that wants a host off says so with a row on `network`, or leaves the host out of its shapes.

Asking about a connection is a separate issue (the proxy holding the CONNECT while the task's supervisor asks).