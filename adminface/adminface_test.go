package adminface

import (
	"net"
	"strconv"
	"strings"
	"testing"
)

// The derived default is the daemon's own port plus one, and the host it was given: a verse on
// localhost:9797 answers pprof on localhost:9798, and one bound to every interface stays bound
// to every interface. What is NOT derivable answers off rather than guessing at a port.
func TestDefaultIsThePortPlusOne(t *testing.T) {
	for _, c := range []struct{ serve, want string }{
		{"localhost:9797", "localhost:9798"},
		{":9797", ":9798"},
		{"0.0.0.0:9801", "0.0.0.0:9802"},
		{"127.0.0.1:0", "127.0.0.1:0"}, // ephemeral serve, ephemeral admin
		{"localhost:65535", Off},       // nothing above it
		{"localhost", Off},             // not host:port
		{"", Off},
	} {
		if got := Default(c.serve); got != c.want {
			t.Errorf("Default(%q) = %q, want %q", c.serve, got, c.want)
		}
	}
}

// A DERIVED PORT THAT IS TAKEN COSTS THE FACE, NOT THE DAEMON. Nobody asked for port+1, and
// two verses on one laptop is the ordinary case the derivation exists for — so losing the race
// for it must not be how a smoke run fails.
func TestADerivedAddressThatIsTakenIsNotFatal(t *testing.T) {
	held, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer held.Close()
	// Serve one BELOW the held port, so the derived default lands on it.
	port := held.Addr().(*net.TCPAddr).Port
	serve := net.JoinHostPort("127.0.0.1", strconv.Itoa(port-1))

	var logged []string
	srv, err := Start(Spec{Name: "test", Serve: serve, Log: func(f string, a ...any) {
		logged = append(logged, f)
	}})
	if err != nil {
		t.Fatalf("a derived address that would not bind failed the daemon: %v", err)
	}
	defer srv.Close()
	if srv.Addr() != "" {
		t.Errorf("admin face bound %q, but the port was held", srv.Addr())
	}
	if !anyHas(logged, "no admin face") {
		t.Errorf("nothing said the face was lost; logged %q", logged)
	}
}

// An address asked for BY NAME is a request, and a daemon that quietly did not honour it would
// be discovered by an operator curling a dead port during the incident this face is for.
func TestAnExplicitAddressThatIsTakenIsFatal(t *testing.T) {
	held, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer held.Close()
	srv, err := Start(Spec{Name: "test", Addr: held.Addr().String(), Serve: "127.0.0.1:0"})
	if srv != nil {
		defer srv.Close()
	}
	if err == nil {
		t.Fatal("an explicit --admin-addr that could not bind was accepted in silence")
	}
}

// `off` is a whole answer, and Start still hands back something to defer Close on.
func TestOffServesNothing(t *testing.T) {
	srv, err := Start(Spec{Name: "test", Addr: Off, Serve: "127.0.0.1:9797"})
	if err != nil {
		t.Fatal(err)
	}
	defer srv.Close()
	if srv.Addr() != "" {
		t.Errorf("admin face bound %q with --admin-addr off", srv.Addr())
	}
}

// The face is REACHABLE and reports what this process is, which is the whole reason it is not
// gops: an operator who knows the daemon's address knows this one, and gets stacks off it.
func TestTheFaceAnswersWithTheProcessFacts(t *testing.T) {
	srv, err := Start(Spec{Name: "verse up", Addr: "127.0.0.1:0", Serve: "127.0.0.1:9797"})
	if err != nil {
		t.Fatal(err)
	}
	defer srv.Close()
	if srv.Addr() == "" {
		t.Fatal("nothing bound")
	}
	if info := srv.Info(); info.Name != "verse up" {
		t.Errorf("index names %q, want the daemon", info.Name)
	}
}

func anyHas(ss []string, sub string) bool {
	for _, s := range ss {
		if strings.Contains(s, sub) {
			return true
		}
	}
	return false
}
