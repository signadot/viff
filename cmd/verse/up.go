package main

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"syscall"
	"time"

	"github.com/scott-cotton/cli"

	"github.com/signadot/verse/controlpolicy"
	"github.com/signadot/verse/entity"
	"github.com/signadot/verse/launch"
	"github.com/signadot/verse/process"
	"github.com/signadot/verse/process/harness"
	"github.com/signadot/verse/trigger"
	"github.com/signadot/verse/versed"
)

type upConfig struct {
	*cli.Command
	Demo bool   `cli:"name=demo desc='seed a demo trigger (open→[ask:review]→spawn+reviewed) so the CLI loop is runnable'"`
	Root string `cli:"name=root desc='working dir an llm agent reads/acts in (e.g. . for local-dev federation); default: isolated temp dir'"`

	// Store backend. Default: docd (durable, controller-federated). The store client
	// connects to docd's client face; Sources mount at its controller face.
	Store string `cli:"name=store desc='force an alternate store: memory (no persistence, no daemons). default: docd'"`
	// docd resolution (see docd.go): pass a running docd, or a data dir to bring
	// logd+docd up on, or nothing for a fresh temp data dir.
	DocdAddr      string `cli:"name=docd-addr desc='connect to a running docd (client face); if set, verse spawns nothing'"`
	DocdMountAddr string `cli:"name=docd-mount-addr desc='the running docd controller/MOUNT face (with --docd-addr)'"`
	Data          string `cli:"name=data desc='data dir; verse ups logd+docd here via o sys up. default: a fresh (kept) temp dir'"`
	OBin          string `cli:"name=o-bin desc='path to the o binary for spawning daemons; default: go run the pinned go-tony cmd/o'"`
	// controlPolicy: where processes run. Default is the in-process local supervisor.
	Policy     string `cli:"name=policy desc='controlPolicy base URL to run processes on (default: local, in-process)'"`
	BrokerAddr string `cli:"name=broker-addr desc='sibling port to serve the broker face (/broker/<nonce>, worker/remote-facing); empty = off'"`
	Broker     bool   `cli:"name=broker desc='run processes on an in-process dial-in broker (requires --broker-addr; publishes each process dialURL from it)'"`
}

// upCmd starts the monolith: store + engine + process runtime + the versed HTTP
// surface, in one process.
func upCmd() *cli.Command {
	cfg := &upConfig{}
	opts, err := cli.StructOpts(cfg)
	if err != nil {
		panic(err)
	}
	return cli.NewCommandAt(&cfg.Command, "up").
		WithSynopsis("up — start the monolith (engine + store + proc runtime)").
		WithOpts(opts...).
		WithRun(cfg.run)
}

func (cfg *upConfig) run(cc *cli.Context, args []string) error {
	if _, err := cfg.Parse(cc, args); err != nil {
		return err
	}

	// Working directory an llm agent acts in: --root, else an isolated temp dir. A
	// federated read_file goes through the verse; --root is the non-federated fallback.
	root := cfg.Root
	if root == "" {
		t, err := os.MkdirTemp("", "verse-proc-*")
		if err != nil {
			return err
		}
		root = t
	} else if abs, err := filepath.Abs(root); err == nil {
		root = abs
	}
	// Per-spawn driver: the launch spec picks scripted or llm; an empty spec driver
	// uses the default. The default is llm when creds are configured (so agents are
	// real out of the box), else scripted (so the runtime is demoable with no creds).
	// An explicit --driver still wins, and an llm spawn with no creds fails *that*
	// process, not the engine.
	defaultDriver := "scripted"
	if appCfg.LLM.Available() {
		defaultDriver = "llm"
		fmt.Fprintf(cc.Err, "agents rooted at %s\n", root)
	}
	build := func(id, task, driver string, reg *process.Registry, content process.ContentReader) *process.Process {
		if driver == "" {
			driver = defaultDriver
		}
		switch driver {
		case "scripted":
			return versed.ScriptedBuilder(id, task, driver, reg, content)
		case "llm":
			if !appCfg.LLM.Available() {
				return process.Failed(id, reg, "llm driver unavailable: set llm.apiKey and llm.virtualKey in verse.tony, or PORTKEY_API_KEY/PORTKEY_VIRTUAL_KEY in the environment")
			}
			// content (non-nil for a scoped federation) routes read_file through the
			// verse; otherwise the agent reads the server-side --root tree.
			p, _ := harness.Build(harness.Config{
				ID: id, Task: task, Root: root, Resolver: reg, Log: cc.Err, Content: content,
				LLMBaseURL: appCfg.LLM.BaseURL, LLMKey: appCfg.LLM.APIKey,
				LLMVirtualKey: appCfg.LLM.VirtualKey, LLMModel: appCfg.LLM.Model,
			})
			return p
		default:
			return process.Failed(id, reg, "unknown driver "+driver+" (want scripted or llm)")
		}
	}

	var srv *versed.Server
	storeKind := "docd"
	switch cfg.Store {
	case "memory", "mem":
		srv = versed.New(build) // in-memory: no persistence, no daemons
		storeKind = "memory"
	case "":
		// docd-backed (default). Resolve docd's addresses — connecting to a running
		// one, or bringing logd+docd up via `o sys up` (see docd.go).
		d, err := resolveDocd(cfg, cc)
		if err != nil {
			return err
		}
		defer d.stop()
		store, err := openDocdStore(entity.LogdConfig{Addr: d.clientAddr, ClientID: "versed"}, 3*time.Minute, cc)
		if err != nil {
			return fmt.Errorf("connect to docd at %s: %w", d.clientAddr, err)
		}
		defer store.Close()
		srv = versed.NewWithStore(build, store)
		// Publish docd's faces on /status so `verse connect` (its own process) can find
		// the mount face and run the file controller against it.
		srv.DocdClient, srv.DocdMount = d.clientAddr, d.mountAddr
		storeKind = fmt.Sprintf("docd[client=%s mount=%s]", d.clientAddr, d.mountAddr)
	default:
		return fmt.Errorf("unknown --store %q: only 'memory', or omit for docd", cfg.Store)
	}
	srv.Addr = g.Addr             // this verse's operator API listen address, for /status discovery
	srv.LLM = appCfg.LLM.Status() // secrets-safe agent-creds summary, for /status
	if cfg.Demo {
		seedDemo(srv.Eng)
	}
	if cfg.Policy != "" && cfg.Broker {
		return fmt.Errorf("verse up: --policy and --broker are mutually exclusive (one run policy)")
	}
	if cfg.Policy != "" { // run processes on a remote controlPolicy instead of in-process
		srv.SetPolicy(controlpolicy.HTTP(cfg.Policy))
	}
	if cfg.Broker { // run processes on an in-process dial-in broker, hosted on the broker face
		if cfg.BrokerAddr == "" {
			return fmt.Errorf("verse up: --broker requires --broker-addr (the worker face to dial in on)")
		}
		face := brokerFace(cfg.BrokerAddr)
		srv.UseBroker(face)
		fmt.Fprintf(cc.Out, "verse up: dial-in broker policy; workers dial %s\n", face)
	}

	// Catch SIGTERM as well as SIGINT so `verse down` (which signals us) triggers the
	// same graceful shutdown — the deferred d.stop() reaps the spawned daemons.
	ctx, stop := signal.NotifyContext(cc.Go, os.Interrupt, syscall.SIGTERM)
	defer stop()

	srv.Start(ctx) // engine begins observing the store

	// Bind the address BEFORE recording our pid. The pidfile is NOT a lock and does not
	// prevent multiple ups: each `verse up` binds its own --addr and gets its own
	// verse-<addr>.pid; it only lets `verse down` find the instance on that address. A
	// second up on the *same* address fails here (port in use) and so never touches the
	// first's pidfile.
	ln, err := net.Listen("tcp", g.Addr)
	if err != nil {
		return fmt.Errorf("verse up: bind %s: %w", g.Addr, err)
	}
	pidPath := upPidPath(g.Addr)
	if werr := os.WriteFile(pidPath, []byte(strconv.Itoa(os.Getpid())), 0o644); werr == nil {
		defer removeOwnPidfile(pidPath) // only if still ours (guards the up-A→up-B handoff race)
	}

	httpSrv := &http.Server{Handler: srv.Handler()}
	go func() {
		<-ctx.Done()
		_ = httpSrv.Shutdown(context.Background())
	}()

	// Broker face on a SIBLING port (worker/remote-facing, kept off the operator API):
	// serves /broker/local — this verse's local supervisor — so a remote verse can bind
	// `--policy http://<host>:<broker-addr>/broker/local`.
	if cfg.BrokerAddr != "" {
		srv.BrokerAddr = cfg.BrokerAddr // publish the broker face address on /status
		brokerSrv := &http.Server{Addr: cfg.BrokerAddr, Handler: srv.BrokerHandler()}
		go func() {
			<-ctx.Done()
			_ = brokerSrv.Shutdown(context.Background())
		}()
		go func() {
			if err := brokerSrv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
				fmt.Fprintf(cc.Err, "verse up: broker face on %s: %v\n", cfg.BrokerAddr, err)
			}
		}()
	}

	fmt.Fprintf(cc.Out, "verse up on %s (engine + store[%s] + proc[default:%s])", g.Addr, storeKind, defaultDriver)
	if cfg.Demo {
		fmt.Fprint(cc.Out, " [--demo trigger seeded]")
	}
	fmt.Fprintln(cc.Out, ". ctrl-c to stop.")

	if err := httpSrv.Serve(ln); err != nil && err != http.ErrServerClosed {
		return err
	}
	return nil
}

// seedDemo installs one gated rule — now as a declarative Def, the same wire form
// `verse trigger add` uses — so the whole plumbing is exercisable from the CLI: put
// a demo:*:* entity with stage=open → an ASK gate posts (visible via `gate list`,
// answerable via `gate resolve`) → on approve, a review *process* is spawned on the
// runtime (visible via `process list` / `watch --system process`) and the subject
// advances to reviewed.
func seedDemo(eng *trigger.Engine) {
	// review: an opened demo:pr, once approved, moves to "reviewing" and a review
	// process is spawned (tagged with its flow subject).
	_ = eng.AddTrigger(trigger.Trigger{
		Name:    "review",
		Matches: []entity.Pattern{{System: "demo", Fields: map[string]any{"stage": "open"}}},
		Gates:   []trigger.Gate{{Name: "review", Mode: "ask"}},
		Action: trigger.Action{
			Set:   map[string]any{"stage": "reviewing"},
			Spawn: &launch.Spec{Driver: "scripted", Task: "review $subject"},
		},
	})
	// reviewed: when a review process completes, advance its flow's subject to
	// "reviewed" — the loop closing through the verse (opts into process:*).
	_ = eng.AddTrigger(trigger.Trigger{
		Name:    "reviewed",
		Matches: []entity.Pattern{{System: "process", Kind: "instance", Fields: map[string]any{"status": "completed"}}},
		// no gates: fires automatically on completion
		Action: trigger.Action{
			SetRef: "$flow",
			Set:    map[string]any{"stage": "reviewed"},
		},
	})
}

// openDocdStore opens the logd/docd store, retrying the first connect for up to `wait`.
// k8s brings versed and docd up concurrently, so versed usually loses the race; without this
// it would error out, get killed, and crash-loop until a restart happens to land after docd
// is ready. Retrying in-process turns that into a quiet wait. OpenLogdStore cleans up a
// failed attempt (cancels its context, closes any session), so retrying does not leak.
func openDocdStore(cfg entity.LogdConfig, wait time.Duration, cc *cli.Context) (*entity.LogdStore, error) {
	cfg.ConnectTimeout = 2 * time.Second // fail fast per attempt so we can poll every few sec
	deadline := time.Now().Add(wait)
	for attempt := 1; ; attempt++ {
		store, err := entity.OpenLogdStore(cfg)
		if err == nil {
			if attempt > 1 {
				fmt.Fprintf(cc.Err, "verse up: docd at %s ready (after %d attempts)\n", cfg.Addr, attempt)
			}
			return store, nil
		}
		if time.Now().After(deadline) {
			return nil, fmt.Errorf("not ready after %s: %w", wait, err)
		}
		// Log the first failure, then stay quiet while polling (~every 3s) to avoid spam.
		if attempt == 1 {
			fmt.Fprintf(cc.Err, "verse up: docd at %s not ready yet, waiting…: %v\n", cfg.Addr, err)
		}
		time.Sleep(1 * time.Second)
	}
}

// brokerFace turns a broker listen address into a URL a worker can dial. An empty or
// wildcard host (":9090", "0.0.0.0:9090", "[::]:9090") is advertised as localhost — right
// for dev; a real deployment sets --broker-addr to a routable host:port. Scheme is http.
func brokerFace(addr string) string {
	host, port, err := net.SplitHostPort(addr)
	if err != nil { // not host:port (e.g. bare ":9090" edge) — use as-is under http
		return "http://" + addr
	}
	switch host {
	case "", "0.0.0.0", "::", "[::]":
		host = "localhost"
	}
	return "http://" + net.JoinHostPort(host, port)
}
