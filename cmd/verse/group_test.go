package main

import (
	"strings"
	"testing"
)

// `verse group` makes, shows, lists and removes a group.
func TestAGroupIsMadeShownAndRemoved(t *testing.T) {
	serve(t)
	t.Setenv("VERSE_USER", "walter@example.com")
	out, err := runCLI(t, "group", "add", "payments", "oscar@example.com", "ana@example.com")
	if err != nil || !strings.Contains(out, "verse.principal.group.payments") {
		t.Fatalf("add: %v\n%s", err, out)
	}
	out, err = runCLI(t, "group", "show", "payments")
	if err != nil || !strings.Contains(out, "ana@example.com") {
		t.Fatalf("show: %v\n%s", err, out)
	}
	out, err = runCLI(t, "group")
	if err != nil || !strings.Contains(out, "payments") {
		t.Fatalf("list: %v\n%s", err, out)
	}
	if _, err := runCLI(t, "group", "remove", "payments"); err != nil {
		t.Fatalf("remove: %v", err)
	}
	if _, err := runCLI(t, "group", "show", "payments"); err == nil {
		t.Fatal("the removed group still shows")
	}
}
