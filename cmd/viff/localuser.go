package main

import (
	"net"
	"os/user"

	"go.viff.dev/viff/pkg/entity"
)

// localHost is the host part of the local user's name: `user:<login>@localhost`.
const localHost = "localhost"

// localLogin is the name nobody's login stands in for: a system that cannot say who is logged
// in, or one whose login is not a name the grammar takes.
const localLogin = "local"

// login reads the operating system's name for whoever runs the CLI. A var so a test can say who.
var login = func() string {
	u, err := user.Current()
	if err != nil {
		return ""
	}
	return u.Username
}

// localUser is who a CLI with nobody configured acts as toward the viff at addr:
// `user:<login>@localhost` when addr is on this machine, and nobody ("") when it is not.
//
// A viff on this machine is the one a person has just started, and they have configured
// nothing yet: the first write they make should be theirs, under a name that says where it came
// from. A viff anywhere else is shared, and whoever writes to it says who they are.
//
// IT IS THE CLI'S DEFAULT AND NOT THE SERVER'S. viffd refuses a request that writes and names
// nobody whatever address it arrived from, so a program that is not this CLI still names
// somebody, and an empty principal never reaches the store.
//
// A loopback address does not prove a local viff — a port-forward to a shared one is loopback
// too — and the name says `@localhost` there as well, which is what it was typed from.
func localUser(addr string) string {
	if !loopback(addr) {
		return ""
	}
	for _, name := range []string{login(), localLogin} {
		if name == "" {
			continue
		}
		// A login the grammar refuses (a space, a `*`) is not guessed at: the stand-in is used.
		g, err := entity.ParsePrincipal(entity.UserKind + ":" + name + "@" + localHost)
		if err == nil && g.Kind == entity.UserKind {
			return g.String()
		}
	}
	return ""
}

// loopback reports whether addr, a `host:port` or a bare host, is this machine: `localhost`, an
// address in 127.0.0.0/8 or `::1`, or no host at all (`:9797`), which dials this machine.
func loopback(addr string) bool {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		host = addr
	}
	if host == "" || host == localHost {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}
