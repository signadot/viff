package versed

import (
	"net/http"
	"slices"
	"testing"

	"github.com/signadot/verse/api"
	"github.com/signadot/verse/codec"
	"github.com/signadot/verse/mirror"
)

// GET /ASKS ANSWERS ITS TWO LISTINGS AS THEIR TYPES: the caller's own asks as api.AskRows, each
// with its id, and the docket as api.DocketRows, each saying which plane raised it.
func TestTheAskListingsAreTheirTypes(t *testing.T) {
	srv, ts := asking(t)
	const daniel, davido = "user:daniel@example.com", "user:oscar@example.com"
	land := askID(t, ask(t, daniel, ts.URL, `{name: land, question: "Merge?", to: [oscar@example.com]}`))
	posed(t, srv, mirror.AskRef(land))

	read := func(as, query string, into any) {
		t.Helper()
		resp := getAs(t, as, ts.URL+"/asks"+query)
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("GET /asks%s as %s: %s", query, as, resp.Status)
		}
		if err := codec.Decode(resp.Body, into, codec.Tony); err != nil {
			t.Fatalf("GET /asks%s does not read as its type: %v", query, err)
		}
	}

	var mine []api.AskRow
	read(daniel, "", &mine)
	if len(mine) != 1 || mine[0].ID != land || mine[0].From == nil || mine[0].From.Principal != daniel || mine[0].Kind() != api.AskKindPerson {
		t.Fatalf("daniel's asks = %+v, want %s, his, a person's ask", mine, land)
	}

	// One of daniel's own, by its id or its name; a name two share answers both, for the reader
	// to tell apart; and it is a narrowing of his own, never of a docket.
	tea1 := askID(t, ask(t, daniel, ts.URL, `{name: tea, question: "Tea?", to: [alice@example.com]}`))
	tea2 := askID(t, ask(t, daniel, ts.URL, `{name: tea, question: "Tea again?", to: [alice@example.com]}`))
	for query, want := range map[string][]string{
		"?ask=" + land: {land}, "?ask=land": {land}, "?ask=tea": {tea1, tea2}, "?ask=nothing": nil,
	} {
		var named []api.AskRow
		read(daniel, query, &named)
		var got []string
		for _, row := range named {
			got = append(got, row.ID)
		}
		slices.Sort(got)
		slices.Sort(want)
		if !slices.Equal(got, want) {
			t.Errorf("GET /asks%s = %v, want %v", query, got, want)
		}
	}
	if resp := getAs(t, davido, ts.URL+"/asks?ask=land&waiting=me"); resp.StatusCode != http.StatusBadRequest {
		t.Errorf("an ask named beside a docket = %s, want 400", resp.Status)
	}

	var docket []api.DocketRow
	read(davido, "?waiting=me", &docket)
	if len(docket) != 1 || docket[0].ID != land || docket[0].Where != "engine" || docket[0].PosedAt == "" {
		t.Fatalf("davido's docket = %+v, want %s, raised by the engine, with when it was posed", docket, land)
	}
}
