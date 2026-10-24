package main

import (
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
}
