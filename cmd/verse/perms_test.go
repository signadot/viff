package main

import (
	"strings"
	"testing"
)

// A person takes a place of their own and holds nothing out beneath it: what they create
// there is theirs alone, refused to anybody else on a read as on a write, while what they
// created before, under the open default, stays readable.
func TestAPlaceOfOnesOwnIsNotAnothersToRead(t *testing.T) {
	serve(t)
	t.Setenv("VERSE_USER", "walter@example.com")
	if _, err := runCLI(t, "data", "spaces", "scott", "notes", "--put", `{todo: [buy milk]}`); err != nil {
		t.Fatalf("put notes: %v", err)
	}
	out, err := runCLI(t, "perms", "spaces", "scott", "--default", "{}", "--grants", "[]")
	if err != nil {
		t.Fatalf("perms: %v", err)
	}
	if !strings.Contains(out, "owner: walter@example.com") {
		t.Fatalf("the place is not scott's:\n%s", out)
	}
	if _, err := runCLI(t, "data", "spaces", "scott", "private", "--put", `{todo: [nothing]}`); err != nil {
		t.Fatalf("put private: %v", err)
	}
	if out, err := runCLI(t, "perms", "spaces", "scott"); err != nil || !strings.Contains(out, "default: {}") {
		t.Fatalf("perms does not read the record back: %v\n%s", err, out)
	}

	t.Setenv("VERSE_USER", "ana@example.com")
	if out, err := runCLI(t, "data", "spaces", "scott", "private"); err == nil {
		t.Fatalf("ana read scott's private node:\n%s", out)
	} else if !strings.Contains(err.Error(), "ana@example.com holds nothing at spaces.scott.private") {
		t.Fatalf("the refusal does not say why: %v", err)
	}
	if _, err := runCLI(t, "data", "spaces", "scott", "private", "--put", `{todo: [mine]}`); err == nil {
		t.Fatal("ana wrote scott's private node")
	}
	out, err = runCLI(t, "data", "spaces", "scott", "notes")
	if err != nil || !strings.Contains(out, "milk") {
		t.Fatalf("what scott created under the open default is no longer readable: %v\n%s", err, out)
	}
	// Nor is scott's place ana's to list: what she may read beneath it she reads by name.
	if out, err := runCLI(t, "data", "spaces", "scott"); err == nil {
		t.Fatalf("ana listed scott's space:\n%s", out)
	}
	// And the place is not ana's to reshape.
	if _, err := runCLI(t, "perms", "spaces", "scott", "--default", `{read: ["*"]}`); err == nil {
		t.Fatal("ana set the default on scott's place")
	}
	if _, err := runCLI(t, "perms", "spaces", "scott"); err == nil {
		t.Fatal("ana read the record of a place she may not read")
	}
	// What she may read, she may see the record of: who owns it and what it holds out.
	t.Setenv("VERSE_USER", "walter@example.com")
	if _, err := runCLI(t, "perms", "spaces", "scott", "notes", "--grant", `{read: [ana@example.com]}`); err != nil {
		t.Fatalf("grant: %v", err)
	}
	t.Setenv("VERSE_USER", "ana@example.com")
	out, err = runCLI(t, "perms", "spaces", "scott", "notes")
	if err != nil || !strings.Contains(out, "owner: walter@example.com") || !strings.Contains(out, "ana@example.com") {
		t.Fatalf("ana cannot see the record of a node she may read: %v\n%s", err, out)
	}
	if _, err := runCLI(t, "perms", "spaces", "scott", "notes", "--grant", `{read-write: [ana@example.com]}`); err == nil {
		t.Fatal("ana set the record of a node she may only read")
	}

	// Taking ana's read back leaves bob's standing: a revoke is one line's worth. `notes` was
	// created before the place was closed, so its record also holds out `read` to everyone,
	// and ana reads as everyone until that grantee is taken back too — `*` is a grantee like
	// any other.
	t.Setenv("VERSE_USER", "walter@example.com")
	if _, err := runCLI(t, "perms", "spaces", "scott", "notes", "--grant", `{read: [bob@example.com]}`); err != nil {
		t.Fatal(err)
	}
	out, err = runCLI(t, "perms", "spaces", "scott", "notes", "--revoke", `{read: [ana@example.com]}`)
	if err != nil || strings.Contains(out, "ana@example.com") || !strings.Contains(out, "bob@example.com") || !strings.Contains(out, "- *") {
		t.Fatalf("revoke: %v\n%s", err, out)
	}
	t.Setenv("VERSE_USER", "ana@example.com")
	if _, err := runCLI(t, "data", "spaces", "scott", "notes"); err != nil {
		t.Fatalf("ana no longer reads as everyone: %v", err)
	}
	t.Setenv("VERSE_USER", "walter@example.com")
	out, err = runCLI(t, "perms", "spaces", "scott", "notes", "--revoke", `{read: ["*"]}`)
	if err != nil || strings.Contains(out, "- *") || !strings.Contains(out, "bob@example.com") {
		t.Fatalf("revoke everyone: %v\n%s", err, out)
	}
	t.Setenv("VERSE_USER", "ana@example.com")
	if out, err := runCLI(t, "data", "spaces", "scott", "notes"); err == nil {
		t.Fatalf("ana still reads notes after her read was revoked:\n%s", out)
	}
	t.Setenv("VERSE_USER", "bob@example.com")
	if out, err := runCLI(t, "data", "spaces", "scott", "notes"); err != nil || !strings.Contains(out, "milk") {
		t.Fatalf("bob lost his read to ana's revoke: %v\n%s", err, out)
	}
}
