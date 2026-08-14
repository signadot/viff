package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"time"

	"github.com/scott-cotton/cli"

	"github.com/signadot/tony-format/go-tony/system/libctl"

	"github.com/signadot/tony-format/go-tony/ir"

	"github.com/signadot/verse/codec"
	"github.com/signadot/verse/entity"
	"github.com/signadot/verse/launch"
	"github.com/signadot/verse/source"
	"github.com/signadot/verse/trigger"
)

// connect federates the local working tree into the verse under a per-session scope.
// It (1) writes each file's rich metadata (fstat + content hash — the index, not the
// content) to docd as local:dir-status-<scope>:<path> entities in copy-on-write scope
// <scope>, and (2) installs a scoped trigger so the engine spawns a validator per
// file. File *content* is served on demand by a content controller (mounted at
// local:dir-content-<scope>) for remote validators — no contents are written to the
// store. It is its own process; run it in a second terminal.
type connectConfig struct {
	*cli.Command
	Interval string `cli:"name=interval default=2s desc='tree poll interval'"`
	Driver   string `cli:"name=driver desc='validator driver: scripted | llm (default: the runtime default)'"`
	// What the default prompt actually is, not what it once was: it reads the file at $path
	// and reports, because "validate $subject" hands an agent a ref it cannot read.
	Prompt string `cli:"name=prompt desc='per-file validator prompt (default: read the file at $path and report on it)'"`
}

func connectCmd() *cli.Command {
	cfg := &connectConfig{}
	opts, err := cli.StructOpts(cfg)
	if err != nil {
		panic(err)
	}
	return cli.NewCommandAt(&cfg.Command, "connect").
		WithSynopsis("connect [path] — federate the local tree into the verse (scoped)").
		WithOpts(opts...).
		WithRun(cfg.run)
}

func (cfg *connectConfig) run(cc *cli.Context, args []string) error {
	args, err := cfg.Parse(cc, args)
	if err != nil {
		return err
	}
	root := "."
	if len(args) >= 1 {
		root = args[0]
	}
	root, _ = filepath.Abs(root)
	// Parse the interval ONCE and carry the duration. It used to be validated here and
	// re-parsed in pollLoop discarding the error, so the two could only ever agree by
	// coincidence — and a value that slipped past this check would have ticked at 0.
	interval, err := time.ParseDuration(cfg.Interval)
	if err != nil {
		return fmt.Errorf("%w: connect: bad --interval %q: %v", cli.ErrUsage, cfg.Interval, err)
	}

	// One nonce scope per session (unless the user named one). Every client request
	// this task makes is folded into it via the global --scope, so its writes and
	// its trigger live in an isolated copy-on-write overlay.
	scope := g.Scope
	generated := false
	if scope == "" {
		if scope, err = genScope(); err != nil {
			return fmt.Errorf("connect: %w", err)
		}
		g.Scope = scope
		generated = true
	}
	statusKind := source.StatusPrefix + scope
	contentKind := source.ContentPrefix + scope

	// docd's faces (published by the running verse on /status): the client face for
	// scoped metadata writes, the mount face for the content controller.
	clientAddr, mountAddr, err := docdAddrs()
	if err != nil {
		return err
	}
	store, err := entity.OpenLogdStore(entity.LogdConfig{
		Addr:     clientAddr,
		ClientID: "connect-" + scope,
		Scope:    scope,
	})
	if err != nil {
		return fmt.Errorf("connect: open scoped store on docd %s: %w", clientAddr, err)
	}
	defer store.Close()

	ctx, stop := signal.NotifyContext(cc.Go, os.Interrupt)
	defer stop()

	// Serve file content on demand (remote read path) at the symmetric mount, so a
	// validator — including one running elsewhere — reads local:dir-content-<scope>:*
	// through docd rather than off a shared disk. No content is written to the store.
	// Start it and WAIT until it is serving before the trigger/metadata below, so a
	// validator that fires the instant a file lands can actually read it, rather than
	// racing an unmounted controller and reporting "file not found".
	contentMount := entity.MountKPath(source.FederationSystem, contentKind)
	go func() {
		err := libctl.RunController(ctx, &libctl.ControllerConfig{
			DocdAddr:   mountAddr,
			Controller: "content-" + scope,
			Path:       contentMount,
			Handler:    source.NewContent(root, contentMount),
		})
		if err != nil && ctx.Err() == nil {
			fmt.Fprintf(cc.Err, "connect: content controller: %v\n", err)
		}
	}()
	if err := waitContentReady(ctx, contentKind, root); err != nil {
		return fmt.Errorf("connect: content controller: %w", err)
	}

	// Now install the scoped trigger (its engine starts observing) and reflect
	// metadata below; a validator fires per file and can read content immediately.
	if err := cfg.installTrigger(scope, statusKind); err != nil {
		return err
	}

	fmt.Fprintf(cc.Out, "connect %s → verse, scope %q: %s:%s:* (metadata) + %s:%s:* (content on demand), validator per file. ctrl-c to stop.\n",
		root, scope, source.FederationSystem, statusKind, source.FederationSystem, contentKind)

	// A generated (nonce) scope is ephemeral to this session: drop it on exit so its
	// overlay + engine don't leak. A user-named scope is left for reuse.
	if generated {
		defer cfg.dropScope(cc, scope)
	}

	cfg.pollLoop(ctx, cc, store, root, statusKind, interval)
	fmt.Fprintln(cc.Out, "disconnected.")
	return nil
}

// waitContentReady blocks until the content controller is mounted and serving. It
// probes a regular file's content over the SAME path a validator uses — a BASELINE
// read (verse's storeContentReader reads via the baseline store; the content mount is
// baseline, made per-scope by the nonce in the ref, so a scoped read wouldn't reach
// it). This closes the race where a validator fires before the mount exists and
// reports "file not found".
func waitContentReady(ctx context.Context, contentKind, root string) error {
	var probe string
	for rel, meta := range source.WalkStatus(root) {
		if meta["type"] == "regular" {
			probe = rel
			break
		}
	}
	if probe == "" {
		return nil // no readable files anyway
	}
	// Baseline /entity (no ?scope) → versed's baseline store → the content mount.
	u := "http://" + g.Addr + "/entity?" + url.Values{
		"ref": {source.FederationSystem + ":" + contentKind + ":" + probe},
	}.Encode()
	deadline := time.After(30 * time.Second)
	for {
		if resp, err := verse.Get(u); err == nil {
			ready := resp.StatusCode == http.StatusOK
			resp.Body.Close()
			if ready {
				return nil
			}
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-deadline:
			return fmt.Errorf("%s not serving in time", contentKind)
		case <-time.After(100 * time.Millisecond):
		}
	}
}

// pollLoop reflects the tree into the scope until ctx is cancelled: on each tick it
// writes metadata for new/changed files and deletes vanished ones. Change detection
// is by size:mtime signature (the hash is carried in the written metadata).
func (cfg *connectConfig) pollLoop(ctx context.Context, cc *cli.Context, store *entity.LogdStore, root, statusKind string, interval time.Duration) {
	prev := map[string]string{} // relpath → size:mtime signature
	tick := func() {
		cur := source.WalkStatus(root)
		var created, changed, removed int
		for rel, meta := range cur {
			sig := fmt.Sprintf("%v:%v", meta["size"], meta["mtime"])
			if old, ok := prev[rel]; !ok || old != sig {
				if ok {
					changed++
				} else {
					created++
				}
				ref := entity.Ref{System: source.FederationSystem, Kind: statusKind, ID: rel}
				if _, _, err := store.Write(entity.Write{At: ref.At(), Value: entity.Obj(meta).Payload()}); err != nil {
					fmt.Fprintf(cc.Err, "connect: publishing %s: %v\n", ref, err)
				}
			}
			prev[rel] = sig
		}
		for rel := range prev {
			if _, ok := cur[rel]; !ok {
				removed++
				ref := entity.Ref{System: source.FederationSystem, Kind: statusKind, ID: rel}
				if _, _, err := store.Write(entity.Write{At: ref.At()}); err != nil { // nil value removes it
					fmt.Fprintf(cc.Err, "connect: removing %s: %v\n", ref, err)
				}
				delete(prev, rel)
			}
		}
		if created+changed+removed > 0 {
			fmt.Fprintf(cc.Out, "  +%d ~%d -%d  (%d files)\n", created, changed, removed, len(cur))
		}
	}

	tick() // reflect the initial tree at once
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			tick()
		}
	}
}

// installTrigger registers the per-file validator on the scope's engine: match any
// set on a dir-status entity → spawn a validator whose subject is that file.
func (cfg *connectConfig) installTrigger(scope, statusKind string) error {
	prompt := cfg.Prompt
	if prompt == "" {
		// $path is the file's relpath (a dir-status payload field) — a readable path,
		// not the ref, so an llm agent can read_file it and produce a real report. A
		// bare "validate $subject" hands it a ref it can't read, so it returns nothing.
		prompt = `Read the file at path "$path" and report any problems (or that it looks fine) in one sentence.`
	}
	def := trigger.Trigger{
		Name: "validate-" + scope,
		Condition: trigger.Condition{
			// Every federated file that has not been validated yet. A trigger is evaluated at
			// every commit touching what it reads, so the subject has to say what is still
			// OUTSTANDING rather than what exists — otherwise each file is validated again at
			// every save of any file, forever.
			//
			// `op: set` used to stand in for that: it made the rule fire on the change rather
			// than on the state. A match is asked only of state now, so the outstanding-ness
			// has to be written down, which is what `validated` is.
			Subject: trigger.Match{
				"system": source.FederationSystem, "kind": statusKind,
				// ABSENCE, built as a node rather than as a Go map, and both halves of
				// that are load-bearing.
				//
				// Absence is `!not {validated: null}`: `null` in a match VALUE position is
				// a wildcard, so `{validated: null}` is PRESENCE and there is no spelling
				// of "is not null" that also means "is not there". (Null-ness, when you
				// want it, is `!irtype null` / `!not.irtype null`.) This asked for
				// `{validated: !not null}` — "not any value" — which nothing satisfies, so
				// the rule selected no file and no file was ever validated.
				//
				// And a NODE, because the tag-keyed spelling of an operator (`{not: …}`) is
				// only recognised on a layout-tagged node — flow style, which is every JSON
				// object and is the reason the sugar exists. A Go map serialises to block
				// style, so this arrived as a literal object with a field named `not` and
				// matched nothing, silently. Match.ToTonyIR renders a predicate node "as
				// the node it is, operators and all", so a tag put here survives the wire.
				//
				// The "!" on the tag is load-bearing too: mergeop.SplitChild strips a tag's
				// first byte before looking the operator up, so `WithTag("not")` names "ot",
				// which is no operator, and the node is read as the literal object it is
				// wrapped around — PRESENCE, the exact inverse of this. The encoder prints
				// both spellings as `!not`, so a trip through the wire hides the difference
				// and only a Match used in-process shows it.
				"value": ir.FromKeyVals([]ir.KeyVal{
					{Key: ir.FromString("validated"), Val: ir.Null()},
				}).WithTag("!not"),
			},
		},
		Action: trigger.Action{
			// The same typed-it-yourself exemption `task run` gets, and for the same reason:
			// a scripted validator's prompt IS the shell command, and the person who typed
			// `connect --driver scripted --prompt …` authorized it for every file the trigger
			// will fire on. A charter that spawns scripted has typed nothing and is asked;
			// the llm driver keeps its own vector either way.
			Spawn: &launch.Spec{Driver: cfg.Driver, Prompt: prompt, Vector: scriptedVector(cfg.Driver)},
			// The latch, and it has to be a LITERAL. This wrote `validated: $hash` — the
			// dir-status entity's own content hash, so the record would say which version
			// was validated — and that reference is not reachable from here: a completion
			// hook is woken by a task finishing rather than by a write, so its occasion
			// carries the TASK event for references and nothing of the subject's payload
			// (Engine.applyHook). Every hook failed with "$hash is not present in the
			// payload that fired this action", which nothing surfaced because the rule it
			// hung off never selected a file in the first place.
			//
			// Recording which version was validated is still worth wanting; it needs either
			// a hook that can read its own subject or a latch written where the subject IS
			// in scope, and that is a design question rather than this fix.
			OnDone: &trigger.Action{Patch: entity.Obj{"validated": true}.Payload()},
		},
	}
	node, err := codec.ToNode(def)
	if err != nil {
		return fmt.Errorf("connect: install trigger: %w", err)
	}
	resp, err := httpPostNode("/triggers", node) // urlFor folds ?scope=<scope>
	if err != nil {
		return fmt.Errorf("connect: install trigger: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		msg, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("connect: install trigger: %s: %s", resp.Status, strings.TrimSpace(string(msg)))
	}
	return nil
}

// dropScope tears down the session's scope (stops its engine, deletes its overlay).
func (cfg *connectConfig) dropScope(cc *cli.Context, scope string) {
	resp, err := httpPost("/scope/delete", nil) // urlFor folds ?scope=<scope>
	if err == nil {
		resp.Body.Close()
	}
	fmt.Fprintf(cc.Out, "dropped scope %q\n", scope)
}

// genScope returns a short random nonce for a session scope.
func genScope() (string, error) {
	var b [6]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(b[:]), nil
}

// docdAddrs fetches docd's client and mount addresses from the running verse's GET
// /status. It errors clearly when the verse is unreachable or is not docd-backed.
func docdAddrs() (client, mount string, err error) {
	resp, err := httpGet("/status", nil)
	if err != nil {
		return "", "", fmt.Errorf("connect: cannot reach verse on %s (is `verse up` running?): %w", g.Addr, err)
	}
	var st struct {
		Docd struct {
			Client string `tony:"field=client"`
			Mount  string `tony:"field=mount"`
		} `tony:"field=docd"`
	}
	decodeClose(resp, &st)
	if st.Docd.Client == "" {
		return "", "", fmt.Errorf("connect: this verse published no docd address on /status — federation needs the docd faces (is it an older `verse up`?)")
	}
	return st.Docd.Client, st.Docd.Mount, nil
}

// disconnect ends a federation by dropping the --scope overlay (which also stops the
// scope's engine); without a scope it removes any federated local:* entities.
func disconnectCmd() *cli.Command {
	return leaf("disconnect", "disconnect — end federation: drop a --scope overlay, else remove local:* entities",
		func(cc *cli.Context, args []string) error {
			if g.Scope != "" {
				resp, err := httpPost("/scope/delete", nil) // urlFor folds in ?scope
				if err != nil {
					return fmt.Errorf("disconnect: %w", err)
				}
				defer resp.Body.Close()
				switch resp.StatusCode {
				case http.StatusNoContent:
					fmt.Fprintf(cc.Out, "dropped scope %q (overlay removed)\n", g.Scope)
					return nil
				case http.StatusNotImplemented:
					// a store without real scopes — fall back to local:* cleanup.
				default:
					msg, _ := io.ReadAll(resp.Body)
					return fmt.Errorf("disconnect: %s: %s", resp.Status, strings.TrimSpace(string(msg)))
				}
			}
			return removeLocalEntities(cc)
		})
}

// removeLocalEntities deletes every local:* entity in the current (scope) view.
func removeLocalEntities(cc *cli.Context) error {
	resp, err := httpGet("/entities", url.Values{"system": {source.FederationSystem}})
	if err != nil {
		return fmt.Errorf("disconnect: %w", err)
	}
	var ents []struct {
		Ref string `tony:"field=ref"`
	}
	decodeClose(resp, &ents)
	for _, e := range ents {
		if r, err := httpDelete("/entity", url.Values{"ref": {e.Ref}}); err == nil {
			r.Body.Close()
		}
	}
	fmt.Fprintf(cc.Out, "removed %d local entities\n", len(ents))
	return nil
}
