# an ask about a network connection is asked

A row on `network` may only allow or deny (`task/autonomy.go:339`): "a connection can't wait for an answer", since the proxy decides it as it is opened and "the client on the other end gives up long before an answer could come" (8e94a494). Every other step's `ask` waits on the docket.

The premise is the client's timeout, not the connection's: curl's default connect timeout is 300s and git's HTTP is curl's, and a client that gives up retries, or the agent runs the command again.

What asking would take:

- the egress proxy, in the runner's process, holds the CONNECT and hands the `network.tcp-client` step to the task's supervisor, in the task's process, which poses it as any step's question (the docket, `verse question`, `attach`), and answers the proxy;
- the decision is kept per host and port for the rest of the run (or as the row says), so a client that gave up gets through on its retry and one host is asked about once;
- the write-time refusal of `then: ask` on a network row goes, and the default row's place in deciding a connection is revisited (it does not decide one: the issue on the default row and the network).

Not scheduled; filed so the refusal is a stated limit rather than a design.