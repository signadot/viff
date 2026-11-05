package api

import (
	"testing"
	"time"
)

// Labels are name=value pairs, comma-separated; a pair without its `=` is refused.
func TestAskLabelsAreNameValuePairs(t *testing.T) {
	got, err := LabelsOf("team=payments, kind=deploy")
	if err != nil || got["team"] != "payments" || got["kind"] != "deploy" || len(got) != 2 {
		t.Fatalf("LabelsOf = %v, %v", got, err)
	}
	for _, bad := range []string{"team", "=payments", "team=payments,"} {
		if _, err := LabelsOf(bad); err == nil {
			t.Errorf("label %q was read", bad)
		}
	}
}

// Whom an ask is put to is names in the grant grammar, spelled canonically; an empty one is
// refused rather than dropped.
func TestAskAnswerersAreGrantees(t *testing.T) {
	got, err := AnswerersOf("user:alice@example.com, group:leads")
	if err != nil || len(got) != 2 || got[0] != "user:alice@example.com" || got[1] != "group:leads" {
		t.Fatalf("AnswerersOf = %v, %v", got, err)
	}
	if _, err := AnswerersOf("user:alice@example.com,,group:leads"); err == nil {
		t.Error("an empty answerer was dropped, not refused")
	}
}

// A due is a date, or a duration from now made a date; one not in the future is refused.
func TestAskDueIsADate(t *testing.T) {
	now := time.Date(2026, 10, 28, 12, 0, 0, 0, time.UTC)
	if got, err := DueOf(" 48h ", now); err != nil || got != "2026-10-30T12:00:00Z" {
		t.Errorf("DueOf(48h) = %q, %v", got, err)
	}
	for _, bad := range []string{"-1h", "2020-01-01T00:00:00Z", "tomorrow"} {
		if _, err := DueOf(bad, now); err == nil {
			t.Errorf("due %q was read", bad)
		}
	}
}
