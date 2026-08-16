package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/signadot/verse/entity"
	"github.com/signadot/verse/entity/entitytest"
)

// whetherMotion is what the engine writes when a quorum gate opens: the ballot that counts is
// the option's `choice`, not its id, which is the gap a voter falls into.
func whetherMotion(t *testing.T, s entity.Store, id string) {
	t.Helper()
	entitytest.Set(t, s, entity.Ref{System: "motion", Kind: "channel", ID: id}, entity.Obj{
		"axis": "whether", "status": "open", "gate": "approvals",
		"options": []any{
			entity.Obj{"id": "proceed", "choice": "approve", "need": int64(2)},
			entity.Obj{"id": "deny", "choice": "reject"},
		},
	}.Payload())
}

// A ballot no tally could ever count must be refused rather than stored. `y` is the word
// `gate approve` and the interactive prompt both teach, so it is exactly what a voter types —
// and written verbatim it landed in the vote log, counted nothing, and left the motion open
// with nothing on the docket to say why.
func TestVoteRefusesABallotNoTallyCouldCount(t *testing.T) {
	store := serve(t)
	whetherMotion(t, store, "m1")

	_, err := runCLI(t, "vote", "m1", "y", "--as", "alice")
	if err == nil {
		t.Fatal("vote m1 y was accepted; a ballot that cannot be tallied is worse than one refused")
	}
	if !strings.Contains(err.Error(), "approve") {
		t.Errorf("error %q does not say what the motion takes — a refusal that does not name "+
			"the alternative sends the voter back to the docket to guess", err)
	}
	if p := payloadOf(t, store, entity.Ref{System: "vote", Kind: "m1", ID: "alice"}); p != nil {
		t.Errorf("a refused ballot was still written: %v", p)
	}
}

// The word the docket tells a voter to type is the word that works.
func TestVoteAcceptsWhatTheMotionNames(t *testing.T) {
	store := serve(t)
	whetherMotion(t, store, "m1")

	if _, err := runCLI(t, "vote", "m1", "approve", "--as", "alice"); err != nil {
		t.Fatalf("vote m1 approve: %v", err)
	}
	p := payloadOf(t, store, entity.Ref{System: "vote", Kind: "m1", ID: "alice"})
	if p == nil || p["choice"] != "approve" {
		t.Errorf("ballot = %v, want choice approve", p)
	}
}

// A select motion is picked by option id, so those must pass — the check reads the motion
// rather than a fixed vocabulary, which is the whole reason it can serve both axes.
func TestVoteAcceptsASelectOption(t *testing.T) {
	store := serve(t)
	entitytest.Set(t, store, entity.Ref{System: "motion", Kind: "channel", ID: "m2"}, entity.Obj{
		"axis": "who", "status": "open",
		"options": []any{
			entity.Obj{"id": "gpt", "choice": "gpt", "need": int64(2)},
			entity.Obj{"id": "claude", "choice": "claude", "need": int64(2)},
		},
	}.Payload())

	if _, err := runCLI(t, "vote", "m2", "claude", "--as", "bob"); err != nil {
		t.Fatalf("vote m2 claude: %v", err)
	}
	if _, err := runCLI(t, "vote", "m2", "y", "--as", "sam"); err == nil {
		t.Error("vote m2 y was accepted; no option of a who-motion is named y")
	}
}

// A motion the client cannot read is not grounds to refuse. The check exists to catch the
// certainly-wrong case, not to become a second authority on what the server will accept — so
// an unknown motion still reaches it, exactly as before.
func TestVoteOnAnUnknownMotionIsTheServersCall(t *testing.T) {
	store := serve(t)

	if _, err := runCLI(t, "vote", "nosuch", "approve", "--as", "alice"); err != nil {
		t.Fatalf("vote on an unknown motion was refused client-side: %v", err)
	}
	if p := payloadOf(t, store, entity.Ref{System: "vote", Kind: "nosuch", ID: "alice"}); p == nil {
		t.Error("the ballot did not reach the store")
	}
}

// A shell hands an unset variable through as an empty string, so `verse vote "$m" y` with $m
// unset used to write vote::alice — a ballot belonging to no motion, which no tally reads and
// nothing reports. It is the same defect as an uncountable choice, one argument to the left.
func TestVoteRefusesAnEmptyMotionID(t *testing.T) {
	store := serve(t)

	if _, err := runCLI(t, "vote", "", "approve", "--as", "alice"); err == nil {
		t.Fatal("an empty motion id was accepted; the ballot would belong to nothing")
	}
	if p := payloadOf(t, store, entity.Ref{System: "vote", Kind: "", ID: "alice"}); p != nil {
		t.Errorf("a ballot was written for no motion: %v", p)
	}
}

// choices has to name the same words the docket does, or the listing tells a voter to type
// something the check then rejects.
func TestChoicesMirrorsTheDocketListing(t *testing.T) {
	for _, tc := range []struct {
		name string
		opts any
		want string
	}{
		{"whether tallies the choice, not the id",
			[]any{
				map[string]any{"id": "proceed", "choice": "approve"},
				map[string]any{"id": "deny", "choice": "reject"},
			}, "approve reject"},
		{"select is picked by id",
			[]any{
				map[string]any{"id": "gpt", "choice": "gpt"},
				map[string]any{"id": "claude", "choice": "claude"},
			}, "gpt claude"},
		{"an option with no choice falls back to its id",
			[]any{map[string]any{"id": "proceed"}}, "proceed"},
		{"no options is not an opinion", "not a list", ""},
	} {
		if got := strings.Join(choices(tc.opts), " "); got != tc.want {
			t.Errorf("%s: choices = %q, want %q", tc.name, got, tc.want)
		}
	}
}

// A refused ballot says so. This printed "voted" on any answer the server gave, including a
// refusal — so a vote that was never recorded reported itself cast, and the only way to notice
// was that the tally never moved.
func TestVoteReportsARefusal(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/entity":
			if r.Method == http.MethodPost {
				http.Error(w, "the store said no", http.StatusBadRequest)
				return
			}
			w.WriteHeader(http.StatusNotFound) // checkChoice: unreadable motion is not its call
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	defer func(addr string) { testAddr = addr }(testAddr)
	testAddr = strings.TrimPrefix(srv.URL, "http://")
	out, err := runCLI(t, "vote", "m1", "approve", "--as", "scott")
	if err == nil {
		t.Fatalf("a refused ballot reported success: %q", out)
	}
	if !strings.Contains(err.Error(), "the store said no") {
		t.Errorf("the error does not carry the refusal: %v", err)
	}
	if strings.Contains(out, "voted") {
		t.Errorf("it printed success anyway: %q", out)
	}
}
