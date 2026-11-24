package main

import (
	"errors"
	"log/slog"
	"testing"

	"github.com/scott-cotton/cli"
)

// What /status publishes for a docd face is the address a CLIENT should dial, which is the
// advertised one when there is one and the dialled one otherwise.
func TestAdvertised(t *testing.T) {
	cases := []struct{ dialled, advertise, want string }{
		{"docd:7071", "", "docd:7071"},                          // the common case: one place
		{"docd:7071", "viff-docd:7071", "viff-docd:7071"},       // a cluster name means nothing outside
		{"127.0.0.1:41207", "viff-docd:7071", "viff-docd:7071"}, // …including an ephemeral one
		{"", "", ""}, // no face, nothing published
	}
	for _, c := range cases {
		if got := advertised(c.dialled, c.advertise); got != c.want {
			t.Errorf("advertised(%q, %q) = %q, want %q", c.dialled, c.advertise, got, c.want)
		}
	}
}

// An advertised address re-addresses a face that exists. Without the face there is nothing
// to re-address, and publishing one anyway would fail in another process — so it is refused
// from the flags alone, before anything is started.
func TestValidateAdvertiseNeedsItsFace(t *testing.T) {
	cases := map[string]struct {
		cfg     upConfig
		wantErr bool
	}{
		"client advertise with no docd": {
			cfg:     upConfig{DocdAdvertiseAddr: "viff-docd:7071"},
			wantErr: true,
		},
		"advertise with its face": {
			cfg: upConfig{DocdAddr: "docd:7071", DocdAdvertiseAddr: "viff-docd:7071"},
		},
		"no advertise — the default": {
			cfg: upConfig{DocdAddr: "docd:7071"},
		},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			err := c.cfg.validate()
			switch {
			case c.wantErr && err == nil:
				t.Fatal("validate() = nil, want a usage error")
			case c.wantErr && !errors.Is(err, cli.ErrUsage):
				t.Fatalf("validate() = %v, want a cli.ErrUsage", err)
			case !c.wantErr && err != nil:
				t.Fatalf("validate() = %v, want nil", err)
			}
		})
	}
}

// VIFF_LOG turns the engine's trace on, and says so when it is asked for something that is not
// a level. Every `tracef` in internal/trigger/ is wired to slog Debug and nothing turned Debug on, so the
// whole trace — which rule was evaluated at which commit, why a firing did not happen — was
// unreachable from a running viff. A rule that silently does not fire is the hardest thing a
// charter can do, and the tool for it was built and not connected.
func TestViffLogLevel(t *testing.T) {
	for _, tc := range []struct {
		in   string
		want bool // parses
	}{
		{"debug", true}, {"DEBUG", true}, {"info", true}, {"warn", true}, {"error", true},
		{"chatty", false}, {"", false},
	} {
		var l slog.Level
		err := l.UnmarshalText([]byte(tc.in))
		if (err == nil) != tc.want {
			t.Errorf("VIFF_LOG=%q parsed=%v, want %v", tc.in, err == nil, tc.want)
		}
	}
}

// Where its config names no issuer, viff up issues as its own address, with a host a runner
// beside it can dial where the address binds every interface.
func TestIssuerURLOf(t *testing.T) {
	for _, c := range []struct{ addr, want string }{
		{"localhost:9797", "http://localhost:9797"},
		{"127.0.0.1:9797", "http://127.0.0.1:9797"},
		{":9797", "http://localhost:9797"},
		{"0.0.0.0:9797", "http://localhost:9797"},
		{"[::]:9797", "http://localhost:9797"},
	} {
		if got := issuerURLOf(c.addr); got != c.want {
			t.Errorf("issuerURLOf(%q) = %q, want %q", c.addr, got, c.want)
		}
	}
}
