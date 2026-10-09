# coderunner: a codex driver — every command and patch codex proposes is a verse action, gated and narrated

## The problem

The coderunner runs a foreign coding agent as one opaque invocation per turn (`agents:` in its config, command.go). Verse gates the invocation and nothing inside it, and the read plane is silent until the program exits (issue 1qdmzhac). A charter that chooses a foreign agent today buys one gate and then silence.

## The shape

Codex has a client protocol, `codex app-server`: JSON-RPC over stdio, one object per line. A client starts a thread and a turn, and codex then *asks the client* before it runs a command or applies a patch (`item/commandExecution/requestApproval`, `item/fileChange/requestApproval`), asks it questions the model raises (`item/tool/requestUserInput`), and streams what it does (`item/started`, `item/completed`, `item/agentMessage/delta`, `turn/completed`). A running turn takes more input (`turn/steer`) and can be interrupted (`turn/interrupt`).

That inverts the opaque-program shape exactly where verse needs it. A new builtin driver, `codex`, beside `llm` and `calls`:

- **Each approval request codex sends is the driver's next proposed action.** `Driver.Next` waits on codex's event stream and returns a `ToolUse` per request, with derived effects: `exec` on the command for a command execution; `write`/`create`/`delete` on each path for a patch; `question` for `requestUserInput`. The loop's supervisor gates it like any other action: the vector decides, the question goes to the docket, `verse question` and `attach` answer it, a grant bounds it. Nothing new is added to the gate model.
- **`Execute` answers codex.** Proceed → `accept`; the executor then waits for the item to complete and returns its output and exit code, so the loop's exec frame carries the real result. Blocked → `decline` and codex re-plans, told why through `turn/steer`.
- **Approval policy `untrusted`** so codex asks about every command it does not know to be read-only and every patch. Under `vector: auto` verse answers yes at once, and the run is unattended but fully narrated and on the record. Reads codex runs without asking are codex's own trusted set; the doc says so. This is the part that is attested, not enforced.
- **Narration.** Agent messages become `say` frames at item completion, not per delta; commands codex ran without asking become frames too, so `attach` shows the work. The read plane stays bounded by `retain`.
- **Steering.** `Fold` during a turn is `turn/steer`; between turns it is the next `turn/start`. Answer-contract repair and the converse hand-over use the same path.
- **Cancel** interrupts the turn and ends the process. The driver's `Result` is the last agent message of the last turn.
- **Config.** A `codex:` section in the runner's config: the binary, the model, the sandbox mode, the name of the variable holding the API key (resolved by the parent and sent to the task process, never into the task's shell), and extra `-c` overrides. An environment or spawn selects `driver: codex`.

Codex's own sandbox (seatbelt / landlock) runs inside the coderunner's containment; the two compose.

## Tests

A fake app-server (a Go test helper speaking the same JSON-RPC over stdio) drives the driver through: a gated command approved, a patch declined and the decline reaching codex, a user-input question answered from the docket, a steer landing as `turn/steer` mid-turn, a turn end as the run's result, cancel as interrupt. The real binary (codex-cli 0.162.0) was probed for the wire format (initialize and thread/start answered; a turn needs an API key) and the protocol schema was generated from it.

## Branch

`verse/codex`, in `.worktrees/codex`, from main 3f2f5fd5.