# console

A read-and-steer face for a running verse: the docket, the charter, a subject's flow, and the
tasks in flight — in the verse's own vocabulary, one tab per noun the CLI names.

```sh
go build -o console ./docs/reference/examples/console
./console -verse localhost:9797 -as user:you@example.com
# console on http://localhost:9899 → verse localhost:9797
```

`-verse` and `-as` fall back to `VERSE_ADDR` and `VERSE_USER`, so a shell already set up for the
CLI needs no second spelling. An explicit flag beats the environment.

## What it is, and what it is not

**It is a separate program**, beside the coderunner rather than inside versed. Whether verse
should serve a UI of its own is a decision about verse's surface, and this does not take it:
nothing in `versed/` changes to run this.

**It proxies rather than letting the page dial verse.** versed sets no CORS headers, so a page
served from any other origin cannot read its responses — the browser refuses before verse is
asked. One origin removes that, and a proxy is the smallest thing that does it: `/v/<route>`
here is `<route>` there.

**The page parses yaml, because verse renders no JSON.** `application/json` alone is a 406, and
that is a decision in `codec` rather than an omission. So the proxy asks for `application/yaml`
and hands the response through *unchanged* — nothing here re-renders a response, which is what
keeps a route that grows a field from needing this program edited. The gate inspector prints
every field the route returned, not a list written here; `askedAgain` showed up that way.

**Writes name a principal.** `-as` rides as `Verse-Principal` on everything that is not a GET.
Without it the reads still work and the write buttons are disabled, because verse refuses an
unattributable answer and a console that hid that would be lying about the model.

## The views

| tab | route | what it shows |
|---|---|---|
| `gate` | `GET /gates` | every question waiting on a person: `mode`, `where`, `waitingOn`, `options` with their `need`, and `spawns` — the work a yes will start, named before it exists. Answers through `POST /gate/resolve`, `/gate/say`, `/gate/escalate` |
| `flow` | `GET /flows`, `GET /flow/tree?subject=` | a subject's chain tree as a canvas. Steers through `POST /flow/{say,suspend,resume,cancel}` |
| `charter` | `GET /charter`, `/charter/{name}` | the installed rules, read back from the verse |
| `task` | `GET /tasks` | every task, whatever runs it |
| `entity` | `GET /state?at=` | the store, read as one document at a kinded path |
| `schedule`, `io`, `runner`, `resolver` | their `GET` routes | the registries |

## The canvas, and why it is coloured that way

`verse flow show` assembles a subject from three places — the **charter** (what may yet
happen), the **mirror** (what already did), and the live **registry** (what is running). The
canvas makes that the grammar instead of a footnote: every node is drawn by where it came from.

- **registry** — solid, green, pulsing. `status: running`.
- **mirror** — solid, quiet. A terminal status.
- **charter** — dashed. `status: pending`: a hook that has not fired, so no task and no entity
  stand behind it yet.

That third one is not a device invented for the drawing. `GET /flow/tree` returns
`demo-review.onDone` with `status: pending` while `demo-review` runs, and the dashed node is
that row. A flow is not stored, and the canvas is meant to look assembled rather than saved.

## Watching

The page holds three `GET /watch` streams, and a change lands on screen in about a frame.

**Per-subtree, and no root watch** — the engine's own rule, for the engine's own reason. A
watch at the root carries the whole verse on every commit, which `docs/convergence.md` measures
as a cost that grows with the store. This page cares about three places, so each stream names
one: `verse.gate.question`, `verse.task.instance`, `verse.trigger.rule`. Each carries
`wait-if-absent=true`, because on a verse where nothing has happened yet those paths hold
nothing and the default answer to a watch on an empty path is a refusal.

**The stream says *when*; the routes say *what*.** A frame carries the changed document, but
what this page shows — which gates are still pending, which tasks are in flight — is a derived
view. Rebuilding those here would be a second copy of the server's own reading, wrong the first
time either changed. So an event is a doorbell and the summary routes are re-read, debounced by
250ms so a firing's burst of writes costs one refresh.

A watch with no `since` means **from now**, not from the beginning — so the page does a full
read before opening the streams, and a 15s interval stays underneath them as a floor: a stream
that drops and never reconnects, or a change in a place none of the three names, still lands.

`?watch=off` falls back to polling alone. A headless browser advances its virtual clock only
when no request is outstanding and a watch is outstanding for ever, so a screenshot or a DOM
dump of this page hangs until the streams are the thing turned off.

The proxy carries the stream because it sets no overall client timeout — an overall deadline is
a stream that dies at the deadline — and bounds a silent verse with `ResponseHeaderTimeout`
instead, which covers the wait for headers and leaves the body alone. It flushes each chunk;
a buffered SSE stream reaches the page whenever the buffer happens to fill, which for a watch
carrying one small frame a minute is never.

**It flushes the headers before reading a byte**, which is a separate thing and was a bug here
first. `net/http` holds the response until the handler fills its buffer or flushes, so a watch
on a subtree where nothing is happening is a watch whose `200` and `Content-Type` never leave
the proxy: the page's `EventSource` sits in `connecting` and `onopen` never fires. A busy verse
hides this completely — the first frame arrives at once and flushes the headers past on its way
— so it showed up only when the console was first pointed at a quiet one.

## Reading only what is on screen

Every refresh used to read all nine routes, on the 15s floor and again on every watch event,
whichever tab was open. Against staging that is **857KB a pass**, and almost all of it is one
route: `/tasks` is **785KB over 81 entries**, because a task carries its prompt and its whole
`result`, and an LLM task's result is kilobytes of JSON. The page dragged three quarters of a
megabyte over the network and ran js-yaml across it to draw a tab showing none of it.

Now `/status`, `/gates` and `/flows` are always read — about **2KB together** — and they are
what the masthead, the docket badge and the subject rail need. Everything else is read when its
own view is on screen, so a tab nobody has opened costs nothing and its badge shows no count
rather than a `0` the page has no evidence for.

`render()` also skips a repaint when nothing moved, keyed on `storeRev` — which advances on
every commit, so it stands in for "the verse changed" without stringifying a payload to find
out. A full `innerHTML` rebuild throws away scroll position and any selected text, and on a
15s timer that is a page that fights the reader.

**This is worth knowing about `/tasks` generally**, not just here: any client listing tasks on a
busy verse pays that cost, and there is no way to ask for less. A summary projection — or a
`?fields=` — would help every reader of that route, and is a decision for whoever owns the API
rather than something a console should work around.

## What it does not do yet

- **No scopes.** Every read is baseline; `-scope` has no spelling here.
- **No perms view.** `GET /perms` is served and unread by this page.
- **js-yaml comes from cdnjs**, so a first load wants the network. Vendoring it beside
  `index.html` and embedding it is a two-line change if that matters.
