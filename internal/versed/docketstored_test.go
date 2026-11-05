package versed

import (
	"net/http"
	"testing"
	"time"

	"github.com/signadot/verse/pkg/codec"
	"github.com/signadot/verse/pkg/mirror"
)

// THE DOCKET IS THE QUESTION AS STORED, with nothing taken out. It used to leave out `status` and
// `posedAt`, which every pending question has, and a reader deciding by age reads `posedAt` there:
// the git iod's obsolete sweep found none, skipped every question, and settled nothing.
func TestTheDocketKeepsWhenAQuestionWasPosed(t *testing.T) {
	srv, ts := asking(t)
	const victor, oscar = "user:victor@example.com", "user:oscar@example.com"
	land := askID(t, ask(t, victor, ts.URL, `{name: land, question: "Merge?", to: [oscar@example.com]}`))
	posed(t, srv, mirror.AskRef(land))

	resp := getAs(t, oscar, ts.URL+"/asks?waiting=me")
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("the docket: %s", resp.Status)
	}
	var rows []map[string]any
	if err := codec.Decode(resp.Body, &rows, codec.Tony); err != nil {
		t.Fatal(err)
	}
	for _, row := range rows {
		if row["id"] != land {
			continue
		}
		at, _ := row["posedAt"].(string)
		if _, err := time.Parse(time.RFC3339Nano, at); err != nil {
			t.Errorf("the docket's row has posedAt %q, want when it was posed", at)
		}
		if row["status"] != mirror.QuestionPending {
			t.Errorf("the docket's row has status %v, want %s", row["status"], mirror.QuestionPending)
		}
		return
	}
	t.Fatalf("the docket does not list %s: %v", land, rows)
}
