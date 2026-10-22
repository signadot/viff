package main

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// `verse c --check` SAYS WHAT AN INSTALL WOULD DO, AND DOES NONE OF IT: a tree that would install
// says what it adds and installs nothing; once installed, a tree that leaves a package out says
// the prune would take it; a tree outside the address is refused, as an answer and not an error;
// one file is read as a directory's file is.
func TestTheCharterCheckSaysWhatAnInstallWouldDo(t *testing.T) {
	serve(t)
	one := writeTree(t, map[string]string{"one.tony": onlyRule("sync.one")})
	out, err := runCLI(t, "charter", "--check", "sync", "-d", one)
	if err != nil || !strings.Contains(out, "verdict: installs") || !strings.Contains(out, "rules.") {
		t.Fatalf("a tree that would install: %v\n%s", err, out)
	}
	if _, err := runCLI(t, "charter", "sync", "one"); err == nil {
		t.Fatal("the check installed the package")
	}

	if out, err := runCLI(t, "charter", "-a", "sync", "-d", one, "--prune"); err != nil {
		t.Fatalf("install: %v\n%s", err, out)
	}
	two := writeTree(t, map[string]string{"two.tony": onlyRule("sync.two")})
	if out, err := runCLI(t, "charter", "--check", "sync", "-d", two); err != nil || !strings.Contains(out, "sync.one") ||
		!strings.Contains(out, "pruned") {
		t.Fatalf("a tree leaving a package out: %v\n%s", err, out)
	}

	if out, err := runCLI(t, "charter", "--check", "elsewhere", "-d", one); err != nil || !strings.Contains(out, "verdict: refused") {
		t.Fatalf("a tree outside the address: %v\n%s", err, out)
	}
	file := filepath.Join(t.TempDir(), "two.tony")
	if err := os.WriteFile(file, []byte(onlyRule("sync.two")), 0o644); err != nil {
		t.Fatal(err)
	}
	if out, err := runCLI(t, "charter", "--check", "sync", "-f", file); err != nil || !strings.Contains(out, "verdict: installs") {
		t.Fatalf("one file: %v\n%s", err, out)
	}
}

// A CHECK NAMES ITS ADDRESS, and does nothing else: no path, or an install or a removal beside
// it, is refused before anything is sent.
func TestTheCharterCheckIsAQuestionAtAnAddress(t *testing.T) {
	serve(t)
	dir := writeTree(t, map[string]string{"one.tony": onlyRule("sync.one")})
	for _, c := range []struct {
		args []string
		want string
	}{
		{[]string{"charter", "--check", "-d", dir}, "names the address"},
		{[]string{"charter", "--check"}, "names the address"}, // before any source is read
		{[]string{"charter", "--check", "-a", "sync", "-d", dir}, "does nothing"},
		{[]string{"charter", "--check", "-r", "sync"}, "does nothing"},
	} {
		if out, err := runCLI(t, c.args...); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%v: %v\n%s", c.args, err, out)
		}
	}
}

// AN INSTALL THAT CANNOT BE SENT SAYS WHICH COMMAND: `-a -d` and `-a`.
func TestAnUnsentInstallNamesItsCommand(t *testing.T) {
	ts := httptest.NewServer(http.NotFoundHandler())
	testAddr = strings.TrimPrefix(ts.URL, "http://")
	ts.Close()
	dir := writeTree(t, map[string]string{"one.tony": onlyRule("sync.one")})
	if _, err := runCLI(t, "charter", "-a", "-d", dir); err == nil || !strings.HasPrefix(err.Error(), "charter -a -d: ") {
		t.Errorf("-a -d = %v", err)
	}
	file := filepath.Join(dir, "one.tony")
	if _, err := runCLI(t, "charter", "-a", "-f", file); err == nil || !strings.HasPrefix(err.Error(), "charter -a: ") {
		t.Errorf("-a -f = %v", err)
	}
}
