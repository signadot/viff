// Command worker is a verse DIAL-IN worker you can copy and change: it connects out to a
// broker, claims work, does it with a local `claude`, and reports back. Nothing has to be able
// to reach it, which is the whole point — a laptop, a pod in a private cluster and a person are
// all reachable by nobody, and all three can still be driven.
//
//	verse up --broker --broker-addr :9090          # in another terminal
//	go run ./docs/reference/examples/worker -verse localhost:9797 -as "$USER"
//	verse task run "summarise what changed in cmd/verse this week"
//
// It is a real program and it compiles with the tree, which is the point: a stub in a fenced
// block goes stale the first time a signature moves and nobody finds out. See
// ../../../task-control.md for the protocol, and runner/ for the OTHER shape — a runner verse
// dials, rather than one that dials verse.
//
// # Which shape to write
//
// A runner serves the task-control contract and verse reaches it over a URL. That is the right
// shape when something can accept a connection, and runner/ is the smallest one. It is the
// WRONG shape here, and runner/'s own doc comment says why: control reaching a runner must be
// applied synchronously, so a runner that queues control for something that has to dial in and
// collect it would have verse reporting a task cancelled while it kept running.
//
// A dial-in worker inverts every leg instead. verse pushes control and pulls reports; the
// worker pulls control and pushes reports; only the broker is reachable, and it is a relay
// rather than the record. That inversion is the broker's whole job, which is why this program
// talks to one and never to verse's task API except to find out what work exists.
//
// # What to change
//
// One function: doWork. It is handed the prompt and a way to narrate, and returns the result.
// Everything else is the protocol — claim, poll, report, fence — and is the same whatever the
// work is. As shipped doWork shells out to `claude -p`, so what you copy already works before
// you change anything.
//
// # What NOT to change
//
// The epoch. It rides every inbox poll and every report, and it is what makes a displaced
// worker STOP rather than carry on writing to a task somebody else now holds. A 409 is not a
// retryable error here — it means this worker is a stranger, and the only correct response is
// to let go.
package main

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/signadot/verse/api"
	"github.com/signadot/verse/codec"
	"github.com/signadot/verse/task"
)

func main() {
	verseAddr := flag.String("verse", "localhost:9797", "verse operator API, where unclaimed work is listed")
	identity := flag.String("as", "", "who this worker is (REQUIRED — a claim must be attributable)")
	agent := flag.String("agent", "claude", "the agent binary doWork shells out to")
	dir := flag.String("dir", ".", "working directory the agent runs in")
	poll := flag.Duration("poll", 3*time.Second, "how often to look for work, and to poll the inbox while working")
	once := flag.Bool("once", false, "take one task and exit, rather than staying up for more")
	flag.Parse()

	if *identity == "" {
		log.Fatal("worker: -as is required: a claim must be attributable")
	}
	w := &worker{
		verse:    "http://" + strings.TrimPrefix(*verseAddr, "http://"),
		identity: *identity,
		agent:    *agent,
		dir:      *dir,
		poll:     *poll,
		cl:       &http.Client{Timeout: 30 * time.Second},
	}
	log.Printf("worker %s: looking for work on %s", w.identity, w.verse)
	for {
		took, err := w.takeOne(context.Background())
		if err != nil {
			log.Printf("worker: %v", err)
		}
		// --once is one PASS, not one success. Exiting only on work taken would leave a
		// worker started against an empty verse polling forever, which is the opposite of
		// what a one-shot was asked for.
		if *once {
			if !took {
				log.Print("worker: nothing unclaimed to take")
			}
			return
		}
		if !took {
			time.Sleep(w.poll)
		}
	}
}

type worker struct {
	verse    string
	identity string
	agent    string
	dir      string
	poll     time.Duration
	cl       *http.Client
}

// takeOne finds one unclaimed brokered task, claims it, works it, and reports. It returns
// whether it took anything, so an idle worker can back off and a busy one can come straight
// back for more.
func (w *worker) takeOne(ctx context.Context) (bool, error) {
	t, err := w.findWork(ctx)
	if err != nil || t == nil {
		return false, err
	}
	// The dialURL comes off the task itself rather than from a flag. That is the point of the
	// broker minting it: a worker is TOLD where to dial, so the same binary works against a
	// verse it was never configured for, and an operator who moves the broker face moves it
	// for every worker at once.
	log.Printf("worker: claiming %s (%s)", t.ID, t.DialURL)
	c, err := w.claim(ctx, t)
	if err != nil {
		return false, fmt.Errorf("claim %s: %w", t.ID, err)
	}
	log.Printf("worker: claimed %s at epoch %d: %q", t.ID, c.Epoch, c.Spec.Prompt)
	return true, w.work(ctx, t, c)
}

// findWork asks verse what exists. A worker cannot discover this from the broker: the broker's
// worker face is per-task by design — a nonce plus a task id is the whole of what a worker is
// given — so there is no route on it that answers "what is there". Which is the security
// property, not a gap: handing somebody a dialURL grants them one task and tells them nothing
// about any other.
//
// So this leg reads the operator API, and a worker that should not have one is handed its task
// id instead (`-once` against a known id is a two-line change).
func (w *worker) findWork(ctx context.Context) (*task.Instance, error) {
	var tasks []task.Instance
	if err := w.call(ctx, http.MethodGet, w.verse+"/tasks", nil, &tasks); err != nil {
		return nil, fmt.Errorf("listing tasks: %w", err)
	}
	for i := range tasks {
		// Unclaimed AND brokered. A local or remote-taskd task never enters `unclaimed` and
		// carries no dialURL, so this is one condition read twice rather than two.
		if tasks[i].Status == task.Unclaimed && tasks[i].DialURL != "" {
			return &tasks[i], nil
		}
	}
	return nil, nil
}

// claimResp is what a claim answers: the spec to work from, the fencing token, and the two
// cursors to resume from.
type claimResp struct {
	Spec struct {
		Prompt string `tony:"field=prompt"`
		Driver string `tony:"field=driver"`
	} `tony:"field=spec"`
	Epoch   int64 `tony:"field=epoch"`
	CCursor int64 `tony:"field=cCursor"`
	RCursor int64 `tony:"field=rCursor"`
}

func (w *worker) claim(ctx context.Context, t *task.Instance) (*claimResp, error) {
	var out claimResp
	body := map[string]string{"identity": w.identity}
	err := w.call(ctx, http.MethodPost, t.DialURL+"/tasks/"+t.ID+"/claim", body, &out)
	return &out, err
}

// work runs the task and reports as it goes. The inbox is polled in parallel with the work
// rather than before it: control that arrives mid-run is the only kind worth having, and a
// worker that only looked at the start would be one an operator cannot stop.
func (w *worker) work(ctx context.Context, t *task.Instance, c *claimResp) error {
	ctx, stop := context.WithCancel(ctx)
	defer stop()

	rep := &reporter{w: w, base: t.DialURL, id: t.ID, epoch: c.Epoch}
	var cancelled bool
	var mu sync.Mutex

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		w.pollInbox(ctx, t, c, func(it api.InboxItem) {
			switch it.Kind {
			case "cancel":
				mu.Lock()
				cancelled = true
				mu.Unlock()
				log.Printf("worker: %s cancelled by %s", t.ID, it.By)
				stop() // kills the agent process through its context
			case "say":
				// A one-shot `claude -p` has no stdin to steer, so this is narrated rather
				// than applied — and narrating it is the honest half: an operator can see
				// their words reached the worker and did not reach the work. A driver that
				// holds a session open is where this becomes a real injection.
				rep.say(fmt.Sprintf("(heard from %s, but this driver is one-shot and cannot take it mid-run: %q)", it.By, it.Text))
			default:
				log.Printf("worker: %s: %s from %s", t.ID, it.Kind, it.By)
			}
		})
	}()

	result, workErr := doWork(ctx, w.agent, w.dir, c.Spec.Prompt, rep.say)
	stop()
	wg.Wait()

	mu.Lock()
	wasCancelled := cancelled
	mu.Unlock()

	// Report on a context that OUTLIVES the work. ctx is cancelled by now — by `stop` above,
	// and before that by the cancel this may be reporting — and reporting through it would
	// abandon the terminal at exactly the moment it matters most: a cancelled task would wind
	// down and never say so, leaving verse holding `running` for work that had stopped.
	ctx = context.WithoutCancel(ctx)

	switch {
	case wasCancelled:
		// The worker CONFIRMS a cancel rather than announcing one: verse asked, this says the
		// work actually wound down. Until it lands, the task is cancelling and not cancelled.
		return rep.terminal(ctx, task.Cancelled, "", "")
	case workErr != nil:
		return rep.terminal(ctx, task.Failed, "", workErr.Error())
	default:
		return rep.terminal(ctx, task.Done, result, "")
	}
}

// pollInbox pulls control until the context ends or the task goes terminal. `since` advances
// only past what has been handed to onItem, because the poll IS the acknowledgement: asking
// for what comes after N says this worker took everything up to N, and a gate answer riding at
// N becomes a decision on the strength of it.
func (w *worker) pollInbox(ctx context.Context, t *task.Instance, c *claimResp, onItem func(api.InboxItem)) {
	since := c.CCursor
	for {
		select {
		case <-ctx.Done():
			return
		case <-time.After(w.poll):
		}
		u := fmt.Sprintf("%s/tasks/%s/inbox?since=%d&epoch=%d",
			t.DialURL, url.PathEscape(t.ID), since, c.Epoch)
		var out struct {
			Items    []api.InboxItem `tony:"field=items"`
			Status   string          `tony:"field=status"`
			Terminal bool            `tony:"field=terminal"`
		}
		if err := w.call(ctx, http.MethodGet, u, nil, &out); err != nil {
			// Fenced: somebody else holds this task now. There is no recovering from being a
			// stranger, and retrying would be this worker insisting. Stop.
			if errors.Is(err, errFenced) {
				log.Printf("worker: %s: fenced — another worker holds it", t.ID)
				return
			}
			if ctx.Err() == nil {
				log.Printf("worker: inbox %s: %v", t.ID, err)
			}
			continue
		}
		for _, it := range out.Items {
			onItem(it)
			if it.Cursor > since {
				since = it.Cursor
			}
		}
		if out.Terminal {
			return
		}
	}
}

// reporter turns what the work says into events on the task's read plane, so
// `verse task attach <id>` shows it happening rather than only its result.
type reporter struct {
	w     *worker
	base  string
	id    string
	epoch int64
}

func (r *reporter) say(text string) {
	if strings.TrimSpace(text) == "" {
		return
	}
	ev := task.Frame{Kind: task.EvSay, Text: text}
	if err := r.post(context.Background(), []task.Frame{ev}, "", ""); err != nil {
		log.Printf("worker: reporting a say on %s: %v", r.id, err)
	}
}

// terminal reports the end of the work, and is the one report that must not be lost — so it
// carries a key. A worker whose response goes missing cannot know whether its terminal landed,
// and without a key its only options are to drop it or to send it twice; with one, the retry
// is answered with what the first attempt was answered with.
func (r *reporter) terminal(ctx context.Context, s task.Status, result, errText string) error {
	ev := task.Frame{Kind: task.EvStatus, Text: string(s), Err: errText}
	key := r.id + ":terminal"
	var err error
	for attempt := range 3 {
		if err = r.post(ctx, []task.Frame{ev}, result, key); err == nil {
			log.Printf("worker: %s → %s", r.id, s)
			return nil
		}
		if errors.Is(err, errFenced) {
			return fmt.Errorf("reporting %s on %s: fenced", s, r.id)
		}
		time.Sleep(time.Duration(attempt+1) * time.Second)
	}
	return fmt.Errorf("reporting %s on %s: %w", s, r.id, err)
}

func (r *reporter) post(ctx context.Context, events []task.Frame, result, key string) error {
	body := map[string]any{"events": events, "epoch": r.epoch}
	if result != "" {
		body["result"] = result
	}
	if key != "" {
		body["key"] = key
	}
	var out struct {
		RCursor int64 `tony:"field=rCursor"`
		Applied int   `tony:"field=applied"`
	}
	if err := r.w.call(ctx, http.MethodPost, r.base+"/tasks/"+r.id+"/report", body, &out); err != nil {
		return err
	}
	// `applied` is not always len(events): nothing follows a terminal, so a batch whose
	// terminal is not its last entry has the remainder dropped. Saying so is what lets a
	// worker compute a retry instead of assuming a 200 meant everything.
	if out.Applied < len(events) {
		log.Printf("worker: %s: %d of %d events applied", r.id, out.Applied, len(events))
	}
	return nil
}

// ── the one function to change ────────────────────────────────────────────────

// doWork does the task and returns its result. It is handed the prompt and a `say` it should
// narrate through, so an operator attached to the task watches the work rather than waiting
// for it.
//
// As shipped it shells out to `claude -p`, which is what makes this worker useful before it is
// changed: the agent already on the machine does the work, under whatever credentials and
// permissions that machine's owner gave it. Nothing about the credential crosses verse — which
// is the property that makes a laptop a reasonable place to run a task at all.
//
// ctx is cancelled when verse cancels the task, and killing the process is how that is
// honoured. A doWork that ignores it is a task an operator cannot stop.
func doWork(ctx context.Context, agent, dir, prompt string, say func(string)) (string, error) {
	cmd := exec.CommandContext(ctx, agent, "-p", prompt)
	cmd.Dir = dir
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return "", err
	}
	var stderr strings.Builder
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		return "", fmt.Errorf("starting %s: %w", agent, err)
	}

	// Narrate line by line and keep the whole thing: the lines are what an attached operator
	// sees as it happens, and the join is the result the task carries afterwards.
	var all strings.Builder
	sc := bufio.NewScanner(stdout)
	sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	for sc.Scan() {
		line := sc.Text()
		all.WriteString(line)
		all.WriteByte('\n')
		say(line)
	}
	scanErr := sc.Err()
	if err := cmd.Wait(); err != nil {
		if ctx.Err() != nil { // cancelled, not failed: the caller reports which
			return "", ctx.Err()
		}
		if msg := strings.TrimSpace(stderr.String()); msg != "" {
			return "", fmt.Errorf("%s: %w: %s", agent, err, msg)
		}
		return "", fmt.Errorf("%s: %w", agent, err)
	}
	if scanErr != nil && !errors.Is(scanErr, io.EOF) {
		return "", fmt.Errorf("reading %s output: %w", agent, scanErr)
	}
	return strings.TrimSpace(all.String()), nil
}

// ── the http leg ──────────────────────────────────────────────────────────────

// errFenced is a 409 from the broker: this worker's claim is no longer the current
// generation. It is deliberately its own error, because it is the one failure that must not be
// retried — everything else here may be.
var errFenced = errors.New("fenced: stale claim")

// call speaks the wire the broker and verse share: a tony request body, and a response decoded
// as whatever the server said it wrote.
//
// Tony in BOTH directions, and the outbound half is the one worth knowing about. verse reads
// JSON everywhere — valid JSON is valid tony — but the codec will not EMIT it, in a client as
// much as in a server, so `codec.Marshal(v, codec.JSON)` fails here rather than at the far end.
// The refusal is the same one an operator meets at `-o json`, and it reads like a reply from
// the server it never reached.
func (w *worker) call(ctx context.Context, method, u string, body, out any) error {
	var rdr io.Reader
	if body != nil {
		b, err := codec.Marshal(body, codec.Tony)
		if err != nil {
			return fmt.Errorf("encoding the request: %w", err)
		}
		rdr = strings.NewReader(string(b))
	}
	req, err := http.NewRequestWithContext(ctx, method, u, rdr)
	if err != nil {
		return err
	}
	if body != nil {
		req.Header.Set("Content-Type", codec.MediaTony)
	}
	// Say what will be accepted. Absent an Accept the server renders its default, which is
	// tony and is what this wants — but a worker that asked for JSON would be refused on the
	// reply having been understood perfectly, so naming it is worth the one header.
	req.Header.Set("Accept", codec.MediaTony)
	resp, err := w.cl.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}
	if resp.StatusCode == http.StatusConflict {
		return errFenced
	}
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated {
		return fmt.Errorf("%s %s: %s: %s", method, u, resp.Status, strings.TrimSpace(string(raw)))
	}
	if out == nil {
		return nil
	}
	f, ok := codec.FormatForMedia(resp.Header.Get("Content-Type"))
	if !ok {
		f = codec.Default()
	}
	return codec.Unmarshal(raw, out, f)
}
