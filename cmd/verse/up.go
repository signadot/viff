package main

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"syscall"
	"time"

	"github.com/scott-cotton/cli"

	"github.com/signadot/verse/adminface"
	"github.com/signadot/verse/codec"
	"github.com/signadot/verse/entity"
	"github.com/signadot/verse/runner"
	"github.com/signadot/verse/task"
	"github.com/signadot/verse/task/harness"
	"github.com/signadot/verse/versed"
)

type upConfig struct {
	*cli.Command
	Root string `cli:"name=root desc='working dir an llm agent reads/acts in (e.g. . for local-dev federation); default: isolated temp dir'"`

	// The store is docd (durable, controller-federated): the store client connects to
	// docd's client face, and Sources mount at its controller face.
	//
	// docd resolution (see docd.go): pass a running docd, or a data dir to bring
	// logd+docd up on, or nothing for a fresh temp data dir.
	DocdAddr      string `cli:"name=docd-addr desc='connect to a running docd (client face); if set, verse spawns nothing'"`
	DocdMountAddr string `cli:"name=docd-mount-addr desc='the running docd controller/MOUNT face (with --docd-addr)'"`
	// What to PUBLISH on /status, when that is not what versed dials. A client reads
	// docd's addresses off /status and connects to them itself, from where IT is — and
	// where it is may not be where versed is. In a cluster versed reaches docd by a
	// Service name that resolves nowhere else, so a laptop given that name has been told
	// something true and useless.
	DocdAdvertiseAddr      string `cli:"name=docd-advertise-addr desc='publish this client-face address on /status instead of --docd-addr (for clients that cannot reach it)'"`
	DocdMountAdvertiseAddr string `cli:"name=docd-mount-advertise-addr desc='publish this MOUNT-face address on /status instead of --docd-mount-addr'"`
	Data                   string `cli:"name=data desc='data dir; verse ups logd+docd here via o sys up. default: a fresh (kept) temp dir'"`
	LogdConfig             string `cli:"name=logd-config desc='logd configuration file (tony) for the spawned daemons, replacing the one verse writes (see docd.go)'"`
	OBin                   string `cli:"name=o-bin desc='path to the o binary for spawning daemons; default: go run the pinned go-tony cmd/o'"`
	// runner: where tasks run. Default is the in-process local supervisor.
	Runner     string `cli:"name=runner desc='task-control base URL to run tasks on (default: local, in-process)'"`
	BrokerAddr string `cli:"name=broker-addr desc='sibling port to serve the broker face (/broker/<nonce>, worker/remote-facing); empty = off'"`
	Broker     bool   `cli:"name=broker desc='serve an in-process dial-in broker, nameable as --runner broker (requires --broker-addr; publishes each task dialURL from it)'"`
	// What to PUBLISH as each task's dialURL, when that is not where the broker LISTENS —
	// the same split docd's two advertise flags exist for, arriving here for the same reason.
	// A worker reads dialURL off the task and connects to it from where IT is; a process that
	// binds a wildcard port inside a cluster cannot also bind the name that reaches it from
	// outside, so the two addresses are different strings and neither can be derived from the
	// other. Without this the broker advertises where it bound, which for `:9090` is
	// `localhost:9090` — an address every worker has and none of them wants.
	BrokerAdvertiseAddr string `cli:"name=broker-advertise-addr desc='publish this address in each task dialURL instead of --broker-addr (for workers that cannot reach it)'"`
	// AdminAddr is the out-of-band face: pprof and goroutine stacks, off the data path.
	// Empty derives it from --addr (see adminface); `off` disables it.
	AdminAddr string `cli:"name=admin-addr desc='admin/pprof listen address, or off to disable (default: the --addr port + 1)'"`
}

// validate rejects contradictory flag combinations BEFORE anything is started.
//
// These checks used to sit further down run, past the point where docd had been brought up
// and a store opened — so `verse up --runner … --broker` spent forty seconds compiling and
// launching daemons, then refused the command it could have refused immediately. A flag
// combination that cannot work is knowable from the flags alone; nothing should have
// happened yet when we say so.
func (cfg *upConfig) validate() error {
	// Refuse the docd knobs that only LOOK like they were honored. --data names a dir to
	// bring daemons up in, so with --docd-addr (which spawns nothing) it is dead; and
	// --docd-mount-addr names a face of the docd being connected to, so without --docd-addr
	// there is no docd it can be about. Both used to be dropped in silence, which reads as
	// "verse ignored where I told it to put my data".
	if cfg.DocdAddr != "" && cfg.Data != "" {
		return fmt.Errorf("%w: verse up: --data is meaningless with --docd-addr (nothing is spawned to use it)", cli.ErrUsage)
	}
	if cfg.DocdAddr != "" && cfg.LogdConfig != "" {
		return fmt.Errorf("%w: verse up: --logd-config configures the logd verse spawns, and --docd-addr spawns none", cli.ErrUsage)
	}
	if cfg.DocdAddr == "" && cfg.DocdMountAddr != "" {
		return fmt.Errorf("%w: verse up: --docd-mount-addr needs --docd-addr (it is the mount face of the docd being connected to)", cli.ErrUsage)
	}
	// An advertised address is a claim about a face that exists; each needs the face it
	// re-addresses. Without --docd-addr the faces are ephemeral ports on a docd verse
	// spawned itself, and publishing some other address for them is a promise nothing
	// keeps — the failure would land on whoever believed /status, a process away.
	if cfg.DocdAddr == "" && cfg.DocdAdvertiseAddr != "" {
		return fmt.Errorf("%w: verse up: --docd-advertise-addr needs --docd-addr (it re-addresses the docd being connected to, and verse spawns its own on ephemeral ports)", cli.ErrUsage)
	}
	if cfg.DocdMountAddr == "" && cfg.DocdMountAdvertiseAddr != "" {
		return fmt.Errorf("%w: verse up: --docd-mount-advertise-addr needs --docd-mount-addr (there is no mount face to re-address)", cli.ErrUsage)
	}
	// --runner and --broker compose rather than compete: one says where a spawn naming nobody
	// runs, the other serves a place a spawn may NAME. A verse can send its unnamed work to a
	// remote taskd and still put a named task in front of a person.
	if cfg.Broker && cfg.BrokerAddr == "" {
		return fmt.Errorf("%w: verse up: --broker requires --broker-addr (the worker face to dial in on)", cli.ErrUsage)
	}
	// The same rule the docd advertise flags are held to: re-addressing a face needs the face.
	// The broker one is checked against --broker-addr rather than --broker, because the face is
	// what --broker-addr serves and a hosted nonce is reachable across it whether or not this
	// verse also runs tasks on one.
	if cfg.BrokerAddr == "" && cfg.BrokerAdvertiseAddr != "" {
		return fmt.Errorf("%w: verse up: --broker-advertise-addr needs --broker-addr (there is no broker face to re-address)", cli.ErrUsage)
	}
	return nil
}

// upCmd starts the monolith: store + engine + task runtime + the versed HTTP
// surface, in one process.
func upCmd() *cli.Command {
	cfg := &upConfig{}
	opts, err := cli.StructOpts(cfg)
	if err != nil {
		panic(err)
	}
	return cli.NewCommandAt(&cfg.Command, "up").
		WithSynopsis("up — start the monolith (engine + store + task runtime)").
		WithOpts(opts...).
		WithRun(cfg.run)
}

func (cfg *upConfig) run(cc *cli.Context, args []string) error {
	if _, err := cfg.Parse(cc, args); err != nil {
		return err
	}
	if err := cfg.validate(); err != nil {
		return err
	}

	// THE ENGINE'S TRACE, off unless asked for. Every `tracef` in trigger/ — which rule was
	// evaluated at which commit, why a firing did not happen, what a read resolved to — is
	// wired to slog Debug, and nothing anywhere turned Debug on, so all of it was unreachable
	// from a running verse. A rule that silently does not fire is the hardest thing a charter
	// can do, and the tool for it was built and not connected.
	//
	// An env var rather than a flag: it is a property of the RUN, wanted mid-investigation and
	// often on a verse somebody else started, and `VERSE_LOG=debug verse up` needs no charter,
	// no restart argument and no config edit.
	if lvl := os.Getenv("VERSE_LOG"); lvl != "" {
		var l slog.Level
		if err := l.UnmarshalText([]byte(lvl)); err != nil {
			return fmt.Errorf("VERSE_LOG=%q: want debug, info, warn or error", lvl)
		}
		slog.SetLogLoggerLevel(l)
	}

	// THE ADMIN FACE COMES UP FIRST — before docd is resolved, which on a cold laptop is a
	// `go install` of o, and before a store is opened, which is a three-minute wait by
	// design. Everything below this line can wedge, and this is the channel that has to
	// answer when it does. It reports the addresses and this process's own health once
	// there is a server to ask; until then it still has the stacks, which is the point.
	adminAddr := adminAddrFor(cfg.AdminAddr, g.Addr, cfg.BrokerAddr)
	if adminAddr == adminface.Off && cfg.AdminAddr == "" {
		fmt.Fprintf(cc.Err, "verse up: no admin face: it derives to %s, which --broker-addr "+
			"asked for; pass --admin-addr <addr> to put it somewhere else\n", cfg.BrokerAddr)
	}
	adm, err := adminface.Start(adminface.Spec{
		Name:  "verse up",
		Addr:  adminAddr,
		Serve: g.Addr,
		Log:   func(f string, a ...any) { fmt.Fprintf(cc.Err, "verse up: "+f+"\n", a...) },
	})
	if err != nil {
		return fmt.Errorf("verse up: %w", err)
	}
	defer adm.Close()

	// The SERVER's default response format, from config/env (VERSE_WIRE_FORMAT, then
	// VERSE_FORMAT). Set once here, which is the only place it is safe to: a request
	// handler reads it concurrently, and unlike the client's -o there is no per-invocation
	// flag to be stale against.
	codec.SetDefault(appCfg.Format.ResolveWire())

	// Working directory an llm agent acts in: --root, else an isolated temp dir. A
	// federated read_file goes through the verse; --root is the non-federated fallback.
	root := cfg.Root
	if root == "" {
		t, err := os.MkdirTemp("", "verse-task-*")
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
	// task, not the engine.
	defaultDriver := "scripted"
	if appCfg.LLM.Available() {
		defaultDriver = "llm"
		fmt.Fprintf(cc.Err, "agents rooted at %s\n", root)
	}
	build := func(l task.Launch, reg *task.Registry, content task.ContentReader) *task.Task {
		driver := l.Driver
		if driver == "" {
			driver = defaultDriver
		}
		switch driver {
		case "scripted":
			// A contract the driver cannot enforce fails the spawn — there is no model to
			// ask again, so honouring the field would mean dropping it, and a dropped
			// contract is worse than none. The refusal lives in NewCallsTaskFrom now, so
			// both runtimes state it once and neither can forget it.
			return versed.NewScriptedTask(l, root, reg)
		case "llm":
			if !appCfg.LLM.Available() {
				return task.NewFailed(l.ID, reg, "llm driver unavailable: set llm.apiKey and llm.virtualKey in verse.tony, or PORTKEY_API_KEY/PORTKEY_VIRTUAL_KEY in the environment")
			}
			// content (non-nil for a scoped federation) routes read_file through the
			// verse; otherwise the agent reads the server-side --root tree.
			//
			// Frame/Tools/Vector come from the spawn. The vector is handed to Build rather
			// than left for the daemon to install afterwards because the DEFAULT FRAME is
			// derived from it: describing each tool as it will actually behave only works
			// if the vector is known while the frame is being written. The daemon installs
			// it again after Build, which is idempotent and is also what bounds the
			// scripted driver, so neither path depends on a builder remembering.
			p, _ := harness.Build(harness.Config{
				ID: l.ID, Prompt: l.Prompt, Root: root, Resolver: reg, Log: cc.Err, Content: content,
				Frame: l.Frame, Tools: l.Tools, Answer: l.Answer, Vector: l.Vector,
				LLMBaseURL: appCfg.LLM.BaseURL, LLMKey: appCfg.LLM.APIKey,
				LLMVirtualKey: appCfg.LLM.VirtualKey, LLMModel: appCfg.LLM.Model,
			})
			return p
		case task.DriverCalls:
			// A list of calls the charter wrote, with the harness's own executor and NO model —
			// which is why it is here rather than under the llm case: `calls` needs the four
			// tools and none of the credentials, so a verse with no keys configured can still
			// run the mechanical work. `scripted` above is the same driver with a
			// one-shell-command list built for it.
			if err := task.CheckCalls(l.Calls); err != nil {
				return task.NewFailed(l.ID, reg, task.DriverCalls+": "+err.Error())
			}
			return harness.Calls(harness.Config{
				ID: l.ID, Root: root, Resolver: reg, Log: cc.Err, Content: content,
				Tools: l.Tools, Answer: l.Answer, Vector: l.Vector,
			}, l.Calls)
		default:
			return task.NewFailed(l.ID, reg,
				"unknown driver "+driver+" (want scripted, calls or llm)")
		}
	}

	// Resolve docd's addresses — connecting to a running one, or bringing logd+docd up
	// via `o sys up` (see docd.go) — and open the store on its client face.
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
	srv := versed.NewWithStore(build, store)
	// Publish docd's faces on /status so `verse connect` (its own process) can find
	// the mount face and run the file controller against it — as the CLIENT should reach
	// them, which is not always how versed did (--docd-advertise-addr). storeKind below
	// stays the dialled pair: it describes what this process connected to.
	srv.DocdClient = advertised(d.clientAddr, cfg.DocdAdvertiseAddr)
	srv.DocdMount = advertised(d.mountAddr, cfg.DocdMountAdvertiseAddr)
	storeKind := fmt.Sprintf("docd[client=%s mount=%s]", d.clientAddr, d.mountAddr)

	// What the admin face now knows: this process's own health (answered without touching
	// the store, so it holds when the store is what is wrong) and every address verse
	// listens on or dials — which is the thing gops cannot do and the reason the face is
	// worth a port. The broker's is added below, where it is decided.
	adm.SetReport(srv.Health)
	adm.SetAddrs( // its own goes without saying: the index prepends it
		adminface.Addr{Name: "api", Addr: g.Addr},
		adminface.Addr{Name: "docd-client", Addr: d.clientAddr},
		adminface.Addr{Name: "docd-mount", Addr: d.mountAddr},
	)

	srv.AdminAddr = adm.Addr()    // published on /status: a derived port is knowable, not guessed
	srv.Addr = g.Addr             // this verse's operator API listen address, for /status discovery
	srv.LLM = appCfg.LLM.Status() // secrets-safe agent-creds summary, for /status
	srv.SystemCharter = appCfg.SystemCharter
	// The cycle throttle's pace. Refused at startup rather than defaulted, because a server
	// running at a rate nobody asked for is exactly what the setting exists to prevent.
	if d, set, err := appCfg.TripPace(); err != nil {
		return err
	} else if set {
		srv.SetTripPace(d)
	}
	// The root's default: what a node created with no default above it holds out. Refused at
	// startup for the reason the pace is, since a verse holding out something nobody asked for
	// is what the setting exists to prevent.
	if appCfg.Perms.Default != nil {
		levels, _, err := entity.ParseLevels(entity.Obj{"default": appCfg.Perms.Default}.Payload(), "perms.default")
		if err != nil {
			return err
		}
		if levels == nil {
			levels = entity.Levels{}
		}
		entity.PolicyOf(srv.Store).SetRootDefault(levels)
	}
	if cfg.Runner != "" { // run tasks on a remote runner instead of in-process
		srv.SetRunner(runner.HTTP(cfg.Runner))
	}
	if cfg.Broker { // serve an in-process dial-in broker, beside the default runner
		face := brokerFace(advertised(cfg.BrokerAddr, cfg.BrokerAdvertiseAddr))
		srv.UseBroker(face)
		fmt.Fprintf(cc.Out, "verse up: dial-in broker served as `broker` "+
			"(--runner broker); workers dial %s\n", face)
	}

	// Catch SIGTERM as well as SIGINT so `verse down` (which signals us) triggers the
	// same graceful shutdown — the deferred d.stop() reaps the spawned daemons.
	ctx, stop := signal.NotifyContext(cc.Go, os.Interrupt, syscall.SIGTERM)
	defer stop()

	srv.StartServing(ctx) // engine restores behind the listener; /healthz says which

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
	// `--runner http://<host>:<broker-addr>/broker/local`.
	if cfg.BrokerAddr != "" {
		srv.BrokerAddr = cfg.BrokerAddr // publish the broker face address on /status
		adm.SetAddrs(
			adminface.Addr{Name: "api", Addr: g.Addr},
			adminface.Addr{Name: "broker", Addr: cfg.BrokerAddr},
			adminface.Addr{Name: "docd-client", Addr: d.clientAddr},
			adminface.Addr{Name: "docd-mount", Addr: d.mountAddr},
		)
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

	fmt.Fprintf(cc.Out, "verse up on %s (engine + store[%s] + task[default:%s]). ctrl-c to stop.\n", g.Addr, storeKind, defaultDriver)

	if err := httpSrv.Serve(ln); err != nil && err != http.ErrServerClosed {
		return err
	}
	return nil
}

// adminAddrFor is the admin face's address: what was asked for, else the derived default —
// unless that default is a port somebody asked for BY NAME.
//
// A DERIVED ADDRESS YIELDS TO AN EXPLICIT ONE. The default is the API port plus one, and
// `--broker-addr` is often exactly the next port along: the admin face starts first (before
// docd, deliberately) so it won the bind, and the broker face — which was named outright — lost
// it and was silently not served, leaving every spawn with a dialURL nothing answered.
//
// It is the same rule adminface already states from the other side, where a derived address
// that will not bind is a warning and an explicit one is fatal. Applied here, before the race,
// rather than discovered afterwards in a log line nobody reads.
func adminAddrFor(explicit, api, broker string) string {
	if explicit != "" {
		return explicit
	}
	if broker != "" && adminface.Default(api) == broker {
		return adminface.Off
	}
	return ""
}

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

// advertised picks what /status publishes for one of docd's faces: the address a client
// was told to use, when it was given, else the one versed dialled. They differ whenever
// versed and the client are not in the same place to dial from — a cluster Service name is
// the whole reason this exists — and the dialled address stays the sane default, because
// most of the time there is only one place.
func advertised(dialled, advertise string) string {
	if advertise != "" {
		return advertise
	}
	return dialled
}

// brokerFace turns a broker address into a URL a worker can dial. An empty or wildcard host
// (":9090", "0.0.0.0:9090", "[::]:9090") becomes localhost — right for dev, where the worker
// is on the same machine. Scheme is http.
//
// It is given the ADVERTISED address, so a deployment whose bind address is not reachable
// says so with --broker-advertise-addr rather than by binding somewhere it cannot. A pod
// binds a wildcard and is reached by a Service name; asking one string to be both is what
// makes localhost the published answer.
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
