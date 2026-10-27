package main

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A DEPLOYMENT HOLDS REGISTRATIONS TO ITS ADMIN, in the record and not in code: the ship's setup
// (deploy/repopulate.sh) replaces the rows under verse.runners with the admin as the one who
// creates beneath it and everyone reading. After it, another principal's add is refused, the
// admin's stands, and everyone still lists the registrations, as a spawn naming one must.
func TestRegistrationsCanBeHeldToTheAdmin(t *testing.T) {
	serve(t)
	t.Setenv("VERSE_USER", "admin@verse")
	if _, err := runCLI(t, "perms", "verse", "runners", "--grants", `[{child-owner: ["user:admin@verse"]}, {read: ["*"]}, {below: true, read: ["*"]}]`); err != nil {
		t.Fatalf("perms verse runners: %v", err)
	}
	t.Setenv("VERSE_USER", "ana@example.com")
	if out, err := runCLI(t, "runner", "add", "--no-probe", "anas", "http://127.0.0.1:1"); err == nil {
		t.Fatalf("ana registered a runner where only the admin may: %s", out)
	}
	t.Setenv("VERSE_USER", "admin@verse")
	if out, err := runCLI(t, "runner", "add", "--no-probe", "gpu", "http://127.0.0.1:1"); err != nil {
		t.Fatalf("the admin registers: %v\n%s", err, out)
	}
	t.Setenv("VERSE_USER", "ana@example.com")
	if out, err := runCLI(t, "runner"); err != nil || !strings.Contains(out, "gpu") {
		t.Fatalf("ana lists the registrations: %v\n%s", err, out)
	}
	if out, err := runCLI(t, "runner", "show", "gpu"); err != nil || !strings.Contains(out, "127.0.0.1:1") {
		t.Fatalf("ana reads a registration: %v\n%s", err, out)
	}

	// Registering again asks what the write does, read-write at a registration that stands: the
	// admin holds gpu's out to ana, and ana re-registers it; bob, holding nothing, may not.
	t.Setenv("VERSE_USER", "admin@verse")
	if out, err := runCLI(t, "perms", "verse", "runners", "gpu", "--grants", `[{read-write: ["user:ana@example.com"]}, {read: ["*"]}]`); err != nil {
		t.Fatalf("perms verse runners gpu: %v\n%s", err, out)
	}
	t.Setenv("VERSE_USER", "ana@example.com")
	if out, err := runCLI(t, "runner", "add", "--no-probe", "gpu", "http://127.0.0.1:2"); err != nil {
		t.Fatalf("ana, holding read-write, registers gpu again: %v\n%s", err, out)
	}
	t.Setenv("VERSE_USER", "bob@example.com")
	if out, err := runCLI(t, "runner", "add", "--no-probe", "gpu", "http://127.0.0.1:3"); err == nil {
		t.Fatalf("bob, holding nothing, registered gpu again: %s", out)
	}
}

// A STATIC TOKEN NEVER LEAVES THE MACHINE THAT HOLDS IT. `runner add -token-file` sends its hash
// as the subject, refuses a token too short for its hash to be published, and is one way or
// the other with -subject.
func TestARunnerIsAddedByItsStaticTokensHash(t *testing.T) {
	serve(t)
	dir := t.TempDir()
	tok := strings.Repeat("k", 40)
	file := filepath.Join(dir, "token")
	if err := os.WriteFile(file, []byte(tok+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256([]byte(tok))
	hash := "sha256:" + hex.EncodeToString(sum[:])
	// This verse issues no tokens of its own, so the add is refused, naming the subject it was
	// sent: the hash, and not the token.
	out, err := runCLI(t, "runner", "add", "--no-probe", "edge", "http://127.0.0.1:1", "--token-file", file)
	if err == nil || !strings.Contains(out+err.Error(), hash) || strings.Contains(out+err.Error(), tok) {
		t.Fatalf("add by a static token = %v\n%s, want the server naming %s and never the token", err, out, hash)
	}
	short := filepath.Join(dir, "short")
	if err := os.WriteFile(short, []byte("tiny"), 0o600); err != nil {
		t.Fatal(err)
	}
	if out, err := runCLI(t, "runner", "add", "--no-probe", "edge", "http://127.0.0.1:1", "--token-file", short); err == nil || !strings.Contains(out+err.Error(), "bytes at least") {
		t.Fatalf("a short static token = %v\n%s", err, out)
	}
	if out, err := runCLI(t, "runner", "add", "--no-probe", "edge", "http://127.0.0.1:1", "--token-file", file, "--subject", "system:serviceaccount:verse:edge"); err == nil || !strings.Contains(out+err.Error(), "give one") {
		t.Fatalf("-token-file beside -subject = %v\n%s", err, out)
	}
}
