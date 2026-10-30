package main

import (
	"context"
	"crypto/rsa"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"sync"
	"syscall"
	"time"

	"github.com/scott-cotton/cli"

	"github.com/signadot/verse/adminface"
	"github.com/signadot/verse/codec"
	"github.com/signadot/verse/config"
	"github.com/signadot/verse/entity"
	"github.com/signadot/verse/runner"
	"github.com/signadot/verse/runner/runnerd"
	"github.com/signadot/verse/runner/runnerd/stock"
	verseagent "github.com/signadot/verse/runner/runnerd/verse"
	"github.com/signadot/verse/system/repocharters"
	"github.com/signadot/verse/task"
	"github.com/signadot/verse/versed"
)

type upConfig struct {
	*cli.Command

	// The store is docd (durable): the store client connects to docd's client face.
	//
	// docd resolution (see docd.go): pass a running docd, or a data dir to bring
	// logd+docd up on, or nothing for a fresh temp data dir.
	DocdAddr string `cli:"name=docd-addr desc='connect to a running docd (client face); if set, verse spawns nothing'"`
	// What to PUBLISH on /status, when that is not what versed dials. A client reads
	// docd's address off /status and connects to it itself, from where IT is — and
	// where it is may not be where versed is. In a cluster versed reaches docd by a
	// Service name that resolves nowhere else, so a laptop given that name has been told
	// something true and useless.
	DocdAdvertiseAddr string `cli:"name=docd-advertise-addr desc='publish this client-face address on /status instead of --docd-addr (for clients that cannot reach it)'"`
	Data              string `cli:"name=data desc='data dir; verse ups logd+docd here via o sys up, and keeps the local runner here. default: ~/.verse/<addr> ($VERSE_HOME/<addr>)'"`
	LogdConfig        string `cli:"name=logd-config desc='logd configuration file (tony) for the spawned daemons, replacing the one verse writes (see docd.go)'"`
	OBin              string `cli:"name=o-bin desc='path to the o binary for spawning daemons; default: go run the pinned go-tony cmd/o'"`
	// LocalRunner false is a verse whose own runner hosts nothing: every spawn goes to a registered
	// runner. Unset is the config's `localRunner`, true by default. A string, since unset is not
	// false and the flag library takes a bool by value alone.
	LocalRunner string `cli:"name=local-runner desc='true or false: whether verse up starts a local runner beside itself (default: the config, true); false routes every spawn to a registered runner'"`
	// RunnerdBin is the runnerd the local runner is, where verse's own resolution should not pick.
	RunnerdBin string `cli:"name=runnerd-bin desc='path to the runnerd the local runner runs; default: beside verse, else ~/.verse/bin/<version>, else PATH'"`
	// AdminAddr is the out-of-band face: pprof and goroutine stacks, off the data path.
	// Empty derives it from --addr (see adminface); `off` disables it.
	AdminAddr string `cli:"name=admin-addr desc='admin/pprof listen address, or off to disable (default: the --addr port + 1)'"`
	// ReseedPerms writes the deployment's top — the root record, verse's half and the admin
	// group — OVER what stands, for the one start that applies an autonomy that changed. Without
	// it the seed writes them once, since after that they are where an operator's own edits
	// live and nothing can tell an edit from a config that moved.
	ReseedPerms bool `cli:"name=reseed-perms desc='write the root, verse and admin-group records from config OVER what stands (a policy change; the seed is once otherwise)'"`
}

// validate rejects contradictory flag combinations BEFORE anything is started.
//
// These checks used to sit further down run, past the point where docd had been brought up
// and a store opened — so an impossible `verse up` spent forty seconds compiling and
// launching daemons, then refused the command it could have refused immediately. A flag
// combination that cannot work is knowable from the flags alone; nothing should have
// happened yet when we say so.
func (cfg *upConfig) validate() error {
	// Refuse the docd knobs that only LOOK like they were honored. --data names a dir to
	// bring daemons up in, so with --docd-addr (which spawns nothing) it is dead. Dropped in
	// silence, it reads as "verse ignored where I told it to put my data".
	if cfg.DocdAddr != "" && cfg.Data != "" {
		return fmt.Errorf("%w: verse up: --data is meaningless with --docd-addr (nothing is spawned to use it)", cli.ErrUsage)
	}
	if cfg.DocdAddr != "" && cfg.LogdConfig != "" {
		return fmt.Errorf("%w: verse up: --logd-config configures the logd verse spawns, and --docd-addr spawns none", cli.ErrUsage)
	}
	// An advertised address is a claim about a face that exists, and needs the face it
	// re-addresses. Without --docd-addr the face is an ephemeral port on a docd verse
	// spawned itself, and publishing some other address for it is a promise nothing
	// keeps — the failure would land on whoever believed /status, a process away.
	if cfg.DocdAddr == "" && cfg.DocdAdvertiseAddr != "" {
		return fmt.Errorf("%w: verse up: --docd-advertise-addr needs --docd-addr (it re-addresses the docd being connected to, and verse spawns its own on ephemeral ports)", cli.ErrUsage)
	}
	if cfg.LocalRunner != "" {
		if _, err := strconv.ParseBool(cfg.LocalRunner); err != nil {
			return fmt.Errorf("%w: verse up: --local-runner=%q: true or false", cli.ErrUsage, cfg.LocalRunner)
		}
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
	adminAddr := cfg.AdminAddr
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

	// The local runner: the verse agent when a model credential is configured (so agents are real
	// out of the box), and scripts either way; what a task's shell inherits from it, which holds
	// the model credentials. Resolved before anything starts, so a verse that could not start its
	// runner says so first.
	shellEnv := appCfg.ShellEnv
	if len(shellEnv) == 0 {
		shellEnv = task.DefaultShellEnv
	}
	runnerCfg, runnerSecrets := localRunnerOffers(appCfg.LLM, shellEnv)
	hostsLocally := appCfg.HostsLocally()
	if on, err := strconv.ParseBool(cfg.LocalRunner); err == nil {
		hostsLocally = on // validate refused anything but unset, true or false
	}
	var runnerdBin string
	if hostsLocally {
		if runnerdBin, err = resolveRunnerd(cfg.RunnerdBin, cc.Err); err != nil {
			return err
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
	// The permission policy this deployment set, read and REFUSED HERE rather than seeded
	// wrong: a verse holding out something nobody asked for is what these settings exist to
	// prevent, and the seed runs inside the constructor below.
	perms, err := permsPolicy(appCfg.Perms)
	if err != nil {
		return err
	}
	// The flag, or the env a cluster sets without editing a pod's args
	// (`kubectl set env deploy/versed VERSE_RESEED_PERMS=true`, watched, then unset). It
	// overwrites an operator's live edits of the top, the admin group's membership included,
	// which is why it is one start's worth and not a setting in the config file.
	perms.Reseed = cfg.ReseedPerms
	if v := os.Getenv("VERSE_RESEED_PERMS"); v != "" {
		// A BOOL, read as one: `=0` and `=false` are what somebody writes to turn a thing OFF,
		// and a test for non-empty would have them rewrite the top over an operator's live
		// edits at every start.
		on, err := strconv.ParseBool(v)
		if err != nil {
			return fmt.Errorf("VERSE_RESEED_PERMS=%q: true or false", v)
		}
		perms.Reseed = perms.Reseed || on
	}
	// No runner in this process: every task runs on a registered runner, the local one among them.
	srv := versed.NewWithStore(store, versed.WithPerms(perms))
	// NO POLICY, NO SERVING. The root's record is what admits anybody at all now, so a server
	// that could not write it would refuse every request from every person — which reads as a
	// verse that lost its data, and is worth failing loudly at the start instead.
	if err := srv.SeedError(); err != nil {
		return fmt.Errorf("verse up: the permission seed did not land, so this verse would "+
			"admit nobody: %w", err)
	}
	// Publish docd's client face on /status — as a CLIENT should reach it, which is not
	// always how versed did (--docd-advertise-addr). storeKind below stays the dialled
	// address: it describes what this process connected to.
	srv.DocdClient = advertised(d.clientAddr, cfg.DocdAdvertiseAddr)
	storeKind := fmt.Sprintf("docd[client=%s]", d.clientAddr)

	// What the admin face now knows: this process's own health (answered without touching
	// the store, so it holds when the store is what is wrong) and every address verse
	// listens on or dials — which is the thing gops cannot do and the reason the face is
	// worth a port.
	adm.SetReport(func() map[string]any {
		h := srv.Health()
		return map[string]any{"ok": h.OK, "started": h.Started, "head": h.Head}
	})
	adm.SetAddrs( // its own goes without saying: the index prepends it
		adminface.Addr{Name: "api", Addr: g.Addr},
		adminface.Addr{Name: "docd-client", Addr: d.clientAddr},
	)

	srv.AdminAddr = adm.Addr()    // published on /status: a derived port is knowable, not guessed
	srv.Addr = g.Addr             // this verse's operator API listen address, for /status discovery
	srv.LLM = appCfg.LLM.Status() // secrets-safe agent-creds summary, for /status
	srv.SystemCharter = appCfg.SystemCharter
	// The charters verse ships at the system level, rendered for this deployment's repositories.
	// Refused at startup where one does not render: a reconciler believed to be watching a
	// repository and not installed is the worse verse.
	for _, rc := range appCfg.RepoCharters {
		text, err := repocharters.Render(repocharters.Params{
			Repo: rc.Repo, GitHub: rc.GitHub, Branch: rc.Branch, InstallAt: rc.At, Tree: rc.Tree, Answerers: rc.Answerers,
		})
		if err != nil {
			return fmt.Errorf("repoCharters: %w", err)
		}
		srv.SystemCharters = append(srv.SystemCharters, versed.SystemCharter{Name: "repo-charters " + rc.Repo, Text: text})
	}
	// How people are authenticated at the door. Refused at startup where it cannot be served, as
	// the pace below is: a verse believed to verify its callers and not doing so is the worse one.
	if err := srv.SetAuth(authOf(appCfg.Auth)); err != nil {
		return err
	}
	// And this verse as an issuer of its own tokens, which is what it proves itself to a
	// registered runner with: every runner is held to its registration, so every verse issues.
	// A key that does not read is a verse that does not start, rather than one whose runners and
	// tasks find out at their first call. Where the config names no issuer, verse up is one at its
	// own address, with a key it keeps in its data dir, or for this process alone where it has
	// none (--docd-addr).
	issuerURL, issuerKey := appCfg.Auth.Issuer.URL, "config"
	if iss := appCfg.Auth.Issuer; iss.URL != "" {
		key, err := versed.ReadIssuerKey(iss.Key)
		if err != nil {
			return fmt.Errorf("auth.issuer.key: %w", err)
		}
		if err := srv.SetIssuer(iss.URL, key, "config"); err != nil {
			return fmt.Errorf("auth.issuer: %w", err)
		}
	} else {
		var key *rsa.PrivateKey
		var err error
		from := "memory"
		if d.dataDir != "" {
			key, from, err = versed.IssuerKeyIn(d.dataDir)
		} else {
			key, err = versed.NewIssuerKey()
		}
		if err != nil {
			return fmt.Errorf("verse up: the issuer's key: %w", err)
		}
		issuerURL, issuerKey = issuerURLOf(g.Addr), from
		if err := srv.SetIssuer(issuerURL, key, from); err != nil {
			return fmt.Errorf("verse up: issuer: %w", err)
		}
	}
	// The cycle throttle's pace. Refused at startup rather than defaulted, because a server
	// running at a rate nobody asked for is exactly what the setting exists to prevent.
	if d, set, err := appCfg.TripPace(); err != nil {
		return err
	} else if set {
		srv.SetTripPace(d)
	}
	// Where a spawn runs is verse's to say (versed route.go): the agent a prompt naming none is
	// for, and whether this verse's own runner is among the runners it routes to.
	srv.DefaultAgent = appCfg.DefaultAgent

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
	// THE LOCAL RUNNER, once this verse serves: the runner fetches this verse's keys to take its
	// calls, and is asked to prove itself, so it is started (or found) behind the listener. A
	// runner that cannot be had stops the verse, as a store that cannot be opened does.
	var lrMu sync.Mutex
	var lr *localRunner
	var lrErr error
	if hostsLocally {
		dir := d.dataDir
		if dir == "" {
			if dir, err = defaultDataDir(g.Addr); err != nil {
				return err
			}
		}
		go func() {
			got, err := startLocalRunner(ctx, srv, runnerdBin, dir, runnerd.Verse{URL: issuerURLOf(g.Addr), Issuer: issuerURL}, runnerCfg, runnerSecrets, cc.Err)
			lrMu.Lock()
			lr, lrErr = got, err
			lrMu.Unlock()
			if err != nil {
				stop()
			}
		}()
	}
	go func() {
		<-ctx.Done()
		lrMu.Lock()
		running := lr
		lrMu.Unlock()
		if running != nil {
			running.stop() // before the listener closes: the runner checks the stop against this verse's keys
		}
		_ = httpSrv.Shutdown(context.Background())
	}()

	runs := "local runner: scripts only"
	if appCfg.LLM.Available() {
		runs = "local runner: the verse agent, and scripts"
	}
	if !hostsLocally {
		runs = "no local runner: every spawn goes to a registered runner"
	}
	fmt.Fprintf(cc.Out, "verse up on %s (engine + store[%s]; %s). ctrl-c to stop.\n", g.Addr, storeKind, runs)
	// A runner holds versed's tokens to this name exactly: one whose `verse.url` spells the address
	// another way (127.0.0.1 for localhost) refuses them unless its `verse.issuer` says this.
	fmt.Fprintf(cc.Out, "issuer: %s (key: %s); a runner's verse.url spells it so, or its verse.issuer does\n", issuerURL, issuerKey)

	if err := httpSrv.Serve(ln); err != nil && err != http.ErrServerClosed {
		return err
	}
	lrMu.Lock()
	defer lrMu.Unlock()
	return lrErr
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

// permsPolicy reads the deployment's permission settings: the root's default, and the admins
// the seed writes into the `admin` group and the root holds `owner` out to.
//
// Every value is parsed HERE, at startup, and a bad one is an error rather than an autonomy nobody
// meant. An admin that does not parse is the whole point of refusing: a grantee the grammar
// cannot read admits nobody, so a verse seeded with one would stand with a top no living person
// can create beneath.
func permsPolicy(c config.PermsConfig) (versed.Perms, error) {
	var p versed.Perms
	if c.ChildGrants != nil {
		levels, _, err := entity.ParseLevels(entity.Obj{entity.ChildGrantsField: c.ChildGrants}.Payload(), entity.ChildGrantsField)
		if err != nil {
			return p, fmt.Errorf("perms.%w", err)
		}
		if levels == nil {
			levels = entity.Levels{}
		}
		p.ChildGrants = levels
	}
	for _, a := range c.Admins {
		g, err := entity.ParseGrantee(a)
		if err != nil {
			return p, fmt.Errorf("perms.admins: %w", err)
		}
		p.Admins = append(p.Admins, g.String())
	}
	return p, nil
}

// authOf is the config's `auth.principal` and `auth.iod` as the server takes them.
func authOf(c config.AuthConfig) versed.Auth {
	a := versed.Auth{Mode: c.Principal.Mode}
	if len(c.Principal.Providers) > 0 {
		a.Providers = map[string]versed.AuthProvider{}
	}
	for name, p := range c.Principal.Providers {
		a.Providers[name] = versed.AuthProvider{
			Issuer: p.Issuer, ClientID: p.ClientID, Domains: p.Domains, IOs: p.IOs,
			Acts: p.Acts,
		}
	}
	a.Iod = versed.AuthIod{Issuer: c.Iod.Issuer, Audience: c.Iod.Audience, CA: c.Iod.CA}
	if len(c.Iod.Accounts) > 0 {
		a.Iod.Accounts = map[string]versed.IodAccount{}
	}
	for sub, acct := range c.Iod.Accounts {
		a.Iod.Accounts[sub] = versed.IodAccount{Principal: acct.Principal, IO: acct.IO}
	}
	for _, st := range c.Iod.Static {
		a.Iod.Static = append(a.Iod.Static, versed.IodStatic{File: st.File, Principal: st.Principal, IO: st.IO})
	}
	return a
}

// localRunnerOffers is what the local runner `verse up` starts offers: scripts, and the
// verse agent where there is a model credential, which a launch may route by a virtual key it was
// let spend (versed's `verse.portkey/virtual-key` annotation). Its operator is the person running
// it, so what it stops inside a run is open to them, said as open ceilings: a ceiling written
// nowhere is closed.
func localRunnerOffers(llm config.LLMConfig, shellEnv []string) (stock.Config, stock.Secrets) {
	c := stock.Open()
	c.Script.ShellEnv = shellEnv
	if llm.APIKey != "" {
		verse := stock.Agent{
			ShellEnv: shellEnv,
			Annotations: map[string]stock.Annotation{
				verseagent.AnnotationVirtualKey: {Type: "string", Default: llm.VirtualKey},
			},
			Systems: stock.OpenInside(stock.AgentName),
		}
		if llm.Model != "" {
			verse.Settings = map[string]stock.Setting{runner.SettingModel: {Default: llm.Model}}
		}
		c.Agents = map[string]stock.Agent{stock.AgentName: verse}
	}
	return c, stock.Secrets{PortkeyAPIKey: llm.APIKey, PortkeyBaseURL: llm.BaseURL}
}

// issuerURLOf is the issuer url of a verse up listening at addr: its own address, with a host a
// runner beside it can dial where addr binds every interface (`:9797`, `0.0.0.0:9797`).
func issuerURLOf(addr string) string {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return "http://" + addr
	}
	if ip := net.ParseIP(host); host == "" || (ip != nil && ip.IsUnspecified()) {
		host = "localhost"
	}
	return "http://" + net.JoinHostPort(host, port)
}
