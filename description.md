# broker face: /broker/local serves the whole local runtime to anybody who can reach the port — remove it, or settle why not

`verse up --broker-addr <addr>` serves `Server.BrokerHandler()` on a sibling port, raw: no
principal check, no version check. `brokerFor` maps the FIXED nonce `local` to
`s.Taskd.Handler()` — the local runtime's whole control surface (task/taskd/daemon.go:140-149):
POST /tasks, /tasks/{id}/resolve, say, cancel, pause, resume, PUT /tasks/{id}/vector.

What was tested (throwaway probes against main ee9f5014, with `UseBroker`):

    POST /broker/local/tasks            no principal   -> 201 "id: sneaky"
    POST /broker/<minted nonce>/tasks                   -> 404
    POST /broker/<minted nonce>/tasks/x/resolve|cancel  -> 404
    PUT  /broker/<minted nonce>/tasks/x/vector          -> 404

So the MINTED path is fine: it serves the worker protocol and nothing else (claim, inbox,
report — broker.WorkerHandler), and the nonce is the capability.

The hole is beside it, on the same port, whatever the dial URL says: `/broker/local/` answers
wherever a broker face is served, and `local` is not a secret. A spawn through it makes a task
in the runtime with no check, no owner and no `verse.task.instance` record.

NOT tested, read only: `POST /broker/local/tasks/{id}/resolve` is registered to `d.resolve`, which
would answer a HOSTED task's gate at the runtime — around `answerQuestion` and everything on it
(who may answer, the posed-first rule, the tally, the record). This issue should not close until
that is either reproduced or shown not to work, with the probe's output here.

What triggers it: the documented use is a second verse bound to this one's runtime,

    verse up --runner http://<host>:<broker-addr>/broker/local

(cmd/verse/up.go:383, docs/task-control.md "The built-in local supervisor lives at the
well-known /broker/local"). That command, and the mount behind it, should probably be removed.
Nothing in the tree dials it: `localRunner` calls taskd in process, every test uses a minted
nonce, and local dev as documented (`verse connect`, dial-in workers) does not touch it.

Unknown: whether anybody runs that binding today. The broker face logs nothing and counts
nothing, so there is no way to tell from a running verse. Options, cheapest first:

  1. count hits on /broker/local/ on /status and warn once per remote address — breaks nobody,
     and answers the question;
  2. an off switch defaulting ON (laptops unchanged), set off in a shared deployment's profile —
     the only people it can break are those binding to that runtime, who are who (1) finds;
  3. make the checked route work for verse-to-verse and then delete the mount: the ops face's
     /tasks is the same runtime behind the door, the perms check and the mirror, and what it
     lacks is runner/http.go sending Verse-Principal (tested: the http runner against an ops
     face is refused `Verse-Principal is required` since ee9f5014). A rule's launch carries
     `via:`, which the door refuses, so what that runner sends needs deciding.

Rejected: serving it to loopback peers only — a `kubectl port-forward` arrives as loopback
inside the pod.

Before closing, settle and write down here:
  - is the resolve bypass real (probe output);
  - does anybody use `--runner …/broker/local` (counter, or asked);
  - what replaced it, or why it stays.