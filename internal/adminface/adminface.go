// Package adminface wires a viff daemon's out-of-band face: the gops agent, and go-tony's
// admin listener — net/http/pprof, goroutine stacks, and a short index of what this process is
// and where it listens — on a listener that is off the data path.
//
// IT EXISTS BECAUSE THE STACKS WORTH READING are the ones a working process would never have
// produced. When staging sat at 0/1 for half an hour with reads timing out, the question was
// what viffd's goroutines were blocked on, and there was no way to ask it: the API face was
// answering the probe by wedging on the store, and gops binds an ephemeral port recorded in a
// per-pid file under the invoking user's config dir — fine on a laptop, unusable in a
// container nobody execs into as that user. go-tony's daemons have had this face since
// v0.0.120 (system/admin) and viff's had none of it.
//
// THE DEFAULT ADDRESS IS DERIVED from the daemon's own, one port up: a viff serving on :9797
// has its admin face on :9798, a sourced on :9801 on :9802. `o` gives each of its daemons a
// fixed default instead, and viff cannot copy that: `viff up` runs several times on one
// laptop — a smoke run beside a dev server, two worktrees — and each run already carries a
// distinct listen address, so deriving from it is what keeps two viffs off one port. The
// failure is not hypothetical; a fixed default is exactly why the `o` that viff spawns is
// handed an ephemeral --admin-addr (cmd/viff/docd.go).
//
// A DERIVED ADDRESS THAT WILL NOT BIND IS A WARNING; AN EXPLICIT ONE IS FATAL. Nobody asked
// for port+1, so losing it should not stop a daemon from serving what it is for. But
// `--admin-addr <x>` is a request, and a daemon that quietly did not honour it would be found
// out during the incident this face exists for, by somebody curling a port with nothing on it.
package adminface

import (
	"log/slog"
	"net"
	"strconv"

	"github.com/google/gops/agent"
	"github.com/signadot/tony-format/go-tony/system/admin"
)

// Off is the address value that asks for no admin listener at all.
const Off = admin.Off

// Addr is a named listener address, reported by the index so an operator can read a daemon's
// ports off the daemon rather than off lsof.
type Addr = admin.Addr

// Spec says which daemon this is and where its face goes.
type Spec struct {
	// Name identifies the process in the index, e.g. "viff up".
	Name string
	// Addr is the --admin-addr flag as given. Empty derives from Serve; `off` disables.
	Addr string
	// Serve is the daemon's own listen address, which the derived default comes from.
	Serve string
	// Log receives the startup line and the warning when a derived address will not bind.
	// nil discards, which is what tests want.
	Log func(format string, args ...any)
}

// Default is the admin address for a daemon serving on addr: the same host, one port up.
//
// A daemon on an ephemeral port takes an ephemeral admin port — there is no meaning to :0 + 1,
// and the address is still reported by whoever started it. An address that is not host:port
// has no derivation at all, and answers off rather than guessing.
func Default(addr string) string {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return Off
	}
	n, err := strconv.Atoi(port)
	if err != nil || n < 0 || n+1 > 65535 {
		return Off
	}
	if n == 0 {
		return net.JoinHostPort(host, "0")
	}
	return net.JoinHostPort(host, strconv.Itoa(n+1))
}

// Start brings up the gops agent and the admin listener.
//
// It answers a server in every case — disabled, bound, or a derived address that was taken —
// so a caller can defer Close and read Addr() without checking for nil first; Addr() is empty
// unless something is listening. The error is returned only for an address that was asked for
// by name, per the package comment.
func Start(spec Spec) (*admin.Server, error) {
	logf := spec.Log
	if logf == nil {
		logf = func(string, ...any) {}
	}
	// gops as well as the admin face, the way o starts both: where its per-pid file IS
	// readable it is the fastest way in, and it costs a goroutine.
	if err := agent.Listen(agent.Options{}); err != nil {
		logf("gops agent failed: %v", err)
	}
	addr, explicit := spec.Addr, spec.Addr != ""
	if !explicit {
		addr = Default(spec.Serve)
	}
	srv := admin.New(&admin.Spec{Addr: addr, Name: spec.Name, Log: slog.Default()})
	if err := srv.Start(); err != nil {
		if explicit {
			return srv, err
		}
		logf("no admin face: %v", err)
		return srv, nil
	}
	if a := srv.Addr(); a != "" {
		logf("admin face on %s (%s/debug/pprof/)", a, srv.URL())
	}
	return srv, nil
}
