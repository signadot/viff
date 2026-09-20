package main

import (
	"strings"
	"testing"
)

// A RECORD GOVERNS ITS NODE WHEREVER THE READ IS ASKED: ana is refused scott's secret at its own
// address, and a read or a replayed watch of what holds it answers without it.
func TestARecordBeneathAReadIsLeftOutOfIt(t *testing.T) {
	serve(t)
	t.Setenv("VERSE_USER", "walter@example.com")
	if _, err := runCLI(t, "data", "demo", "pr", "1", "--put", `{open: yes, secret: {x: 1}}`); err != nil {
		t.Fatalf("put: %v", err)
	}
	if _, err := runCLI(t, "perms", "demo", "pr", "1", "secret", "--grants", "[]", "--child-grants", "{}"); err != nil {
		t.Fatalf("perms: %v", err)
	}
	if out, err := runCLI(t, "data", "demo", "pr", "1"); err != nil || !strings.Contains(out, "x: 1") {
		t.Fatalf("scott's own read lost the secret: %v\n%s", err, out)
	}
	t.Setenv("VERSE_USER", "ana@example.com")
	if _, err := runCLI(t, "data", "demo", "pr", "1", "secret"); err == nil {
		t.Fatal("ana read the secret at its own address")
	}
	for _, args := range [][]string{
		{"data", "demo", "pr", "1"},
		{"data", "demo", "pr"},
		{"data", "demo"},
		{"watch", "demo", "pr", "1", "--since", "0", "--until", `{to: {open: yes}}`},
	} {
		out, err := runCLI(t, args...)
		if err != nil {
			t.Fatalf("ana %q: %v", args, err)
		}
		if !strings.Contains(out, "open") || strings.Contains(out, "secret") {
			t.Errorf("ana %q answered %q, want the row without the secret", args, out)
		}
	}
}

// perms READS STEPS AS `verse data` DOES: the quoted clause is the name inside it, and an access
// or a `*` is refused, since a record is about one node.
func TestPermsReadsItsPathAsSteps(t *testing.T) {
	serve(t)
	t.Setenv("VERSE_USER", "walter@example.com")
	if _, err := runCLI(t, "data", "spaces", "scott", "notes", "--put", `{todo: [buy milk]}`); err != nil {
		t.Fatalf("put notes: %v", err)
	}
	if _, err := runCLI(t, "perms", "spaces", "scott", "--child-grants", "{}"); err != nil {
		t.Fatalf("perms: %v", err)
	}
	plain, err := runCLI(t, "perms", "spaces", "scott")
	if err != nil {
		t.Fatalf("perms spaces scott: %v", err)
	}
	quoted, err := runCLI(t, "perms", "spaces", `"scott"`)
	if err != nil || quoted != plain {
		t.Errorf(`perms spaces "scott" = (%q, %v), want the record perms spaces scott reads:
%s`, quoted, err, plain)
	}
	for _, steps := range [][]string{{"spaces", "*"}, {"spaces", "scott", "[0]"}} {
		if out, err := runCLI(t, append([]string{"perms"}, steps...)...); err == nil {
			t.Errorf("perms %q = %q, want a refusal: a record is about one node", steps, out)
		}
	}
}

// A person takes a place of their own and holds nothing out beneath it: what is in it is
// theirs alone, refused to anybody else on a read as on a write — what they had written there
// BEFORE closing it included, since it is theirs through the place and closing the place closed
// it. A row carries no copy of the default it was created under; the place its creation made
// does, which here is `spaces`, and that stays readable.
func TestAPlaceOfOnesOwnIsNotAnothersToRead(t *testing.T) {
	serve(t)
	t.Setenv("VERSE_USER", "walter@example.com")
	if _, err := runCLI(t, "data", "spaces", "scott", "notes", "--put", `{todo: [buy milk]}`); err != nil {
		t.Fatalf("put notes: %v", err)
	}
	out, err := runCLI(t, "perms", "spaces", "scott", "--child-grants", "{}", "--grants", "[]")
	if err != nil {
		t.Fatalf("perms: %v", err)
	}
	if !strings.Contains(out, "owner: user:walter@example.com") {
		t.Fatalf("the place is not scott's:\n%s", out)
	}
	if _, err := runCLI(t, "data", "spaces", "scott", "private", "--put", `{todo: [nothing]}`); err != nil {
		t.Fatalf("put private: %v", err)
	}
	if out, err := runCLI(t, "perms", "spaces", "scott"); err != nil || !strings.Contains(out, "child-grants: {}") {
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
	if out, err := runCLI(t, "data", "spaces", "scott", "notes"); err == nil {
		t.Fatalf("ana read a row inside the place scott closed:\n%s", out)
	}
	// Nor is scott's place ana's to list: what she may read beneath it she reads by name.
	if out, err := runCLI(t, "data", "spaces", "scott"); err == nil {
		t.Fatalf("ana listed scott's space:\n%s", out)
	}
	// And the place is not ana's to reshape.
	if _, err := runCLI(t, "perms", "spaces", "scott", "--child-grants", `{read: ["*"]}`); err == nil {
		t.Fatal("ana set the default on scott's place")
	}
	if _, err := runCLI(t, "perms", "spaces", "scott"); err == nil {
		t.Fatal("ana read the record of a place she may not read")
	}
	// A grantee the grammar does not read is refused where it is typed, whichever flag it rides.
	t.Setenv("VERSE_USER", "walter@example.com")
	for _, flag := range [][]string{{"--grant", `{read: [user]}`}, {"--revoke", `{read: [agent]}`},
		{"--child-grants", `{read: [coderunner]}`}, {"--grants", `[{read: [verse.principal.group.release]}]`}} {
		if _, err := runCLI(t, append([]string{"perms", "spaces", "scott", "notes"}, flag...)...); err == nil ||
			!strings.Contains(err.Error(), "a bare word names nothing") && !strings.Contains(err.Error(), "is no person") {
			t.Errorf("perms %s: %v, want the grammar's refusal", flag, err)
		}
	}
	// What she may read, she may see the record of: who owns it and what it holds out.
	if _, err := runCLI(t, "perms", "spaces", "scott", "notes", "--grant", `{read: [ana@example.com]}`); err != nil {
		t.Fatalf("grant: %v", err)
	}
	t.Setenv("VERSE_USER", "ana@example.com")
	out, err = runCLI(t, "perms", "spaces", "scott", "notes")
	if err != nil || !strings.Contains(out, "owner: user:walter@example.com") || !strings.Contains(out, "ana@example.com") {
		t.Fatalf("ana cannot see the record of a node she may read: %v\n%s", err, out)
	}
	if _, err := runCLI(t, "perms", "spaces", "scott", "notes", "--grant", `{read-write: [ana@example.com]}`); err == nil {
		t.Fatal("ana set the record of a node she may only read")
	}

	// Taking ana's read back leaves bob's standing: a revoke is one line's worth. The record
	// of `notes` was taken inside the space scott closed, so it holds out exactly what he
	// granted on it and nothing to everyone — there is no `*` for ana to go on reading as.
	t.Setenv("VERSE_USER", "walter@example.com")
	if _, err := runCLI(t, "perms", "spaces", "scott", "notes", "--grant", `{read: [bob@example.com]}`); err != nil {
		t.Fatal(err)
	}
	out, err = runCLI(t, "perms", "spaces", "scott", "notes", "--revoke", `{read: [ana@example.com]}`)
	if err != nil || strings.Contains(out, "ana@example.com") || !strings.Contains(out, "bob@example.com") || strings.Contains(out, "- *") {
		t.Fatalf("revoke: %v\n%s", err, out)
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

// Bare `verse perms` lists, as every collection does: the records of what the requester may
// read, and nothing about what they may not.
func TestBarePermsListsWhatOneMayRead(t *testing.T) {
	serve(t)
	t.Setenv("VERSE_USER", "walter@example.com")
	if _, err := runCLI(t, "data", "spaces", "scott", "notes", "--put", `{todo: [buy milk]}`); err != nil {
		t.Fatalf("put notes: %v", err)
	}
	if _, err := runCLI(t, "perms", "spaces", "scott", "--child-grants", "{}", "--grants", "[]"); err != nil {
		t.Fatalf("perms: %v", err)
	}
	if _, err := runCLI(t, "data", "spaces", "scott", "private", "--put", `{todo: [nothing]}`); err != nil {
		t.Fatalf("put private: %v", err)
	}
	out, err := runCLI(t, "perms")
	if err != nil {
		t.Fatalf("bare perms: %v", err)
	}
	// Two records, where ownership changed: `spaces`, which his first put brought into being,
	// and `spaces.scott`, which he took to close. The rows beneath have none, being owned
	// through the place above them, so they are not listed — there is nothing to list.
	for _, at := range []string{"at: spaces\n", "at: spaces.scott\n"} {
		if !strings.Contains(out, at) {
			t.Errorf("scott's listing lacks %q:\n%s", strings.TrimSpace(at), out)
		}
	}
	for _, row := range []string{"spaces.scott.notes", "spaces.scott.private"} {
		if strings.Contains(out, row) {
			t.Errorf("a node with no record of its own is listed (%s):\n%s", row, out)
		}
	}
	if _, err := runCLI(t, "perms", "list"); err == nil || !strings.Contains(err.Error(), "bare `verse perms` is the listing") {
		t.Errorf("`perms list` is not told what the listing is: %v", err)
	}
	if _, err := runCLI(t, "perms", "--grants", "[]"); err == nil {
		t.Error("a set with no path was not refused")
	}

	t.Setenv("VERSE_USER", "ana@example.com")
	out, err = runCLI(t, "perms")
	if err != nil {
		t.Fatalf("bare perms as ana: %v", err)
	}
	if !strings.Contains(out, "at: spaces\n") {
		t.Errorf("ana's listing lacks the place created under the open default:\n%s", out)
	}
	if strings.Contains(out, "spaces.scott") {
		t.Errorf("ana's listing carries what she may not read:\n%s", out)
	}
}
