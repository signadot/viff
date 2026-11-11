package github

import (
	"encoding/json"
	"testing"
)

// A MERGED PULL REQUEST SAYS SO, read from the pulls listing as GitHub sends it: the listing has
// `merged_at` and no `merged`, so merged is read from the one and not only the other. A closed one
// that was not merged, and an open one, say false.
func TestAListedPullRequestThatWasMergedSaysMerged(t *testing.T) {
	for _, c := range []struct {
		name, raw string
		merged    bool
	}{
		{"merged, as the listing sends it", `{"number": 1, "state": "closed", "merged_at": "2026-11-06T12:55:11Z"}`, true},
		{"merged, as the single read sends it", `{"number": 1, "state": "closed", "merged": true, "merged_at": "2026-11-06T12:55:11Z"}`, true},
		{"closed, not merged", `{"number": 2, "state": "closed", "merged_at": null}`, false},
		{"open", `{"number": 3, "state": "open"}`, false},
	} {
		var p pull
		if err := json.Unmarshal([]byte(c.raw), &p); err != nil {
			t.Fatal(err)
		}
		if got := pullPayload("acme/app", p)["merged"]; got != c.merged {
			t.Errorf("%s: merged = %v, want %v", c.name, got, c.merged)
		}
	}
}
