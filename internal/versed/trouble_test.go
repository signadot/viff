package versed

import (
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/signadot/verse/pkg/runner/runnerd/runnerdtest"

	"github.com/signadot/verse/pkg/api"
	"github.com/signadot/verse/pkg/codec"
	"github.com/signadot/verse/pkg/entity"
	"github.com/signadot/verse/pkg/entity/entitytest"
)

// A rule whose action fails is marked in the charter's listing, where its author looks, and
// counted in the status: the place in the rule, what it was about, and why.
func TestARuleInTroubleIsMarkedInTheCharterAndCountedInTheStatus(t *testing.T) {
	srv := serverWith(t, runnerdtest.Scripted, entitytest.Store(t))
	srv.Start(t.Context())
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	status := func() api.Status {
		t.Helper()
		resp := getAs(t, tester, ts.URL+"/status")
		defer resp.Body.Close()
		var st api.Status
		if err := codec.Decode(resp.Body, &st, codec.Tony); err != nil {
			t.Fatalf("decoding the status: %v", err)
		}
		return st
	}
	if st := status(); st.Firings != nil {
		t.Fatalf("a verse where nothing has fired says firings: %+v", st.Firings)
	}

	installFile(t, ts.URL, `{
		name: copies,
		condition: {subject: {path: "demo.pr.*", value: {stage: open}}},
		action: {patch: {stage: seen, reviewer: .[assignee]}},
	}`)
	resp := putAs(t, tester, ts.URL, "demo.pr.1", entity.Obj{"stage": "open"}.Payload())
	resp.Body.Close()

	var listing string
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if listing = charterListing(t, ts.URL); strings.Contains(listing, "trouble:") {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	for _, want := range []string{"trouble:", "kind: failed", "chain: copies", "assignee", "count: 1"} {
		if !strings.Contains(listing, want) {
			t.Errorf("the charter's listing does not say %q:\n%s", want, listing)
		}
	}
	if !strings.Contains(listing, `demo.pr.`) {
		t.Errorf("the charter's listing does not say what the firing was about:\n%s", listing)
	}
	if st := status(); st.Firings == nil || st.Firings.Failed != 1 || st.Firings.Unread != 0 {
		t.Errorf("status firings = %+v, want one failed", st.Firings)
	}
}
