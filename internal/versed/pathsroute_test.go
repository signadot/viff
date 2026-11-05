package versed

import (
	"io"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/signadot/verse/pkg/codec"
	"github.com/signadot/verse/pkg/entity"
)

// A LISTING IS ANSWERED AS ADDRESSES, not as the document they stand in. That is the whole point
// of asking the store for it: the reply carries the names, so listing a wide container does not
// ship what every name stands for.
//
// The addresses are kinded paths, one per line when a command prints them, and each is one a read
// takes -- `--kpaths` prints what arrived, and `--paths` renders the same address as steps.
func TestStateAnswersALitingAsAddresses(t *testing.T) {
	srv := askingServer(t)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	pr(t, srv, "1", "payments")
	pr(t, srv, "2", "billing")

	body := func(url string) string {
		resp := getAs(t, "", url)
		out, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode != 200 {
			t.Fatalf("%s: %s: %s", url, resp.Status, out)
		}
		return string(out)
	}

	listed := body(ts.URL + "/state?at=change.pr&paths=1")
	for _, want := range []string{`change.pr."1"`, `change.pr."2"`} {
		if !strings.Contains(listed, want) {
			t.Errorf("the listing does not name %s: %s", want, listed)
		}
	}
	// It is a listing, not the document: the nodes the addresses stand for are not in it.
	if strings.Contains(listed, "payments") {
		t.Errorf("the listing shipped what the addresses stand for: %s", listed)
	}
	// And it is the listing wire type, which is what a client reads it back as.
	node, err := codec.ParseNode([]byte(listed), codec.Tony)
	if err != nil {
		t.Fatalf("parse the reply: %v (%s)", err, listed)
	}
	var out entity.Paths
	if err := codec.FromNode(node, &out); err != nil || len(out.Paths) == 0 {
		t.Fatalf("the reply is not a listing: %v (%s)", err, listed)
	}

	// --depth bounds it BENEATH the address, so the records are named and their fields are not.
	bounded := body(ts.URL + "/state?at=change.pr&paths=1&depth=1")
	if !strings.Contains(bounded, `change.pr."1"`) || strings.Contains(bounded, `change.pr."1".area`) {
		t.Errorf("--depth 1 answered %s", bounded)
	}

	// A path nothing stands at is not an empty listing.
	resp := getAs(t, "", ts.URL+"/state?at=nosuch&paths=1")
	resp.Body.Close()
	if resp.StatusCode != 404 {
		t.Errorf("a listing of nothing answered %s, want 404", resp.Status)
	}

	// A FILTERED LISTING IS THE STORE'S TOO. `--if` is asked of each node the path REACHED — the
	// store matches its pattern against each member of a set — and the listing is what it kept
	// and what is beneath that, as addresses: the document is not read to find out.
	kept := body(ts.URL + `/state?at=change.pr.*&paths=1&if=%7Barea%3A%20payments%7D`)
	if !strings.Contains(kept, `change.pr."1"`) || !strings.Contains(kept, `change.pr."1".area`) {
		t.Errorf("the filtered listing does not name what the filter kept, and what is beneath it: %s", kept)
	}
	if strings.Contains(kept, `change.pr."2"`) {
		t.Errorf("the filtered listing names a node the filter did not keep: %s", kept)
	}
	if strings.Contains(kept, "payments") {
		t.Errorf("a listing answered values, so it read the document: %s", kept)
	}
	// A filter that keeps nothing is an answer about the filter, and a path that reaches nothing
	// is a wrong address: the first is an empty listing and the second is a 404.
	if none := body(ts.URL + `/state?at=change.pr.*&paths=1&if=%7Barea%3A%20nowhere%7D`); strings.Contains(none, "change.pr") {
		t.Errorf("a filter that keeps nothing listed %s", none)
	}
	resp = getAs(t, "", ts.URL+`/state?at=nosuch.*&paths=1&if=%7Barea%3A%20payments%7D`)
	resp.Body.Close()
	if resp.StatusCode != 404 {
		t.Errorf("a filtered listing over a path that reaches nothing answered %s, want 404", resp.Status)
	}
	// And the depth is the store's, counted from what the path reached: 0 is those nodes alone.
	alone := body(ts.URL + `/state?at=change.pr.*&paths=1&depth=0`)
	if !strings.Contains(alone, `change.pr."2"`) || strings.Contains(alone, `change.pr."2".area`) {
		t.Errorf("depth 0 over a wildcard is not the reached nodes alone: %s", alone)
	}
}
