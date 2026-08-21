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
		{"docd:7071", "", "docd:7071"},                            // the common case: one place
		{"docd:7071", "verse-docd:7071", "verse-docd:7071"},       // a cluster name means nothing outside
		{"127.0.0.1:41207", "verse-docd:7071", "verse-docd:7071"}, // …including an ephemeral one
		{"", "", ""}, // no mount face, nothing published
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
			cfg:     upConfig{DocdAdvertiseAddr: "verse-docd:7071"},
			wantErr: true,
		},
		"mount advertise with no mount face": {
			cfg:     upConfig{DocdAddr: "docd:7071", DocdMountAdvertiseAddr: "verse-docd-mount:7072"},
			wantErr: true,
		},
		"both, each with its face": {
			cfg: upConfig{
				DocdAddr: "docd:7071", DocdMountAddr: "docd-mount:7072",
				DocdAdvertiseAddr: "verse-docd:7071", DocdMountAdvertiseAddr: "verse-docd-mount:7072",
			},
		},
		"neither — the default": {
			cfg: upConfig{DocdAddr: "docd:7071", DocdMountAddr: "docd-mount:7072"},
		},
		"broker advertise with no broker face": {
			cfg:     upConfig{BrokerAdvertiseAddr: "verse-broker:9090"},
			wantErr: true,
		},
		// The face is what --broker-addr serves, so hosting one is enough to re-address it.
		// A verse can host a broker for somebody else's worker without running tasks on one.
		"broker advertise with a face but no --broker": {
			cfg: upConfig{BrokerAddr: ":9090", BrokerAdvertiseAddr: "verse-broker:9090"},
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

// brokerFace advertises a dialable URL: wildcard/empty hosts become localhost (dev), an
// explicit host is preserved (deployment), and the scheme is http.
func TestBrokerFace(t *testing.T) {
	cases := map[string]string{
		":9090":           "http://localhost:9090",
		"0.0.0.0:9090":    "http://localhost:9090",
		"[::]:9090":       "http://localhost:9090",
		"verse.test:9090": "http://verse.test:9090",
		"127.0.0.1:9090":  "http://127.0.0.1:9090",
	}
	for in, want := range cases {
		if got := brokerFace(in); got != want {
			t.Errorf("brokerFace(%q) = %q, want %q", in, got, want)
		}
	}
}

// The face a worker is told to dial is built from the ADVERTISED address, which is the whole
// point of having one: a pod binds a wildcard port and is reached by a name it cannot bind, so
// the published URL has to come from somewhere other than the listen address. Without the
// advertised one the same deployment publishes localhost, which every worker resolves and none
// of them reaches.
func TestBrokerFaceUsesTheAdvertisedAddress(t *testing.T) {
	cases := map[string]struct {
		cfg  upConfig
		want string
	}{
		"a cluster: bound to a wildcard, reached by a tailnet name": {
			cfg:  upConfig{BrokerAddr: ":9090", BrokerAdvertiseAddr: "verse-broker:9090"},
			want: "http://verse-broker:9090",
		},
		"dev: nothing advertised, so where it bound is where to dial": {
			cfg:  upConfig{BrokerAddr: ":9090"},
			want: "http://localhost:9090",
		},
		"the advertised port may differ too — a proxy in front of the bind": {
			cfg:  upConfig{BrokerAddr: ":9090", BrokerAdvertiseAddr: "verse-broker:443"},
			want: "http://verse-broker:443",
		},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			got := brokerFace(advertised(c.cfg.BrokerAddr, c.cfg.BrokerAdvertiseAddr))
			if got != c.want {
				t.Errorf("broker face = %q, want %q", got, c.want)
			}
		})
	}
}

// VERSE_LOG turns the engine's trace on, and says so when it is asked for something that is not
// a level. Every `tracef` in trigger/ is wired to slog Debug and nothing turned Debug on, so the
// whole trace — which rule was evaluated at which commit, why a firing did not happen — was
// unreachable from a running verse. A rule that silently does not fire is the hardest thing a
// charter can do, and the tool for it was built and not connected.
func TestVerseLogLevel(t *testing.T) {
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
			t.Errorf("VERSE_LOG=%q parsed=%v, want %v", tc.in, err == nil, tc.want)
		}
	}
}
