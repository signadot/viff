package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/signadot/verse/codec"
	"github.com/signadot/verse/entity"
	"github.com/signadot/verse/entity/entitytest"
)

// ONE COMMAND for the motions, because a listing and a ballot are the same subject: what
// `verse motion` prints is what `--vote` answers. These are the shapes that one command has to
// hold apart — every motion, one of them, and the act on one — since they are told apart by
// how much was typed rather than by which word was.

// Bare, it is the docket. An empty one is an ANSWER rather than a failure to answer, so it
// says so and exits 0 — a script asking what is waiting is asking, not asserting.
func TestMotionBareIsTheDocket(t *testing.T) {
	serve(t)

	out, err := runCLI(t, "motion")
	if err != nil {
		t.Fatalf("motion: %v", err)
	}
	if !strings.Contains(out, "no motions") {
		t.Errorf("an empty docket does not say so: %q", out)
	}
}

// With an id, it is THAT motion — one document rather than an array of one, which is the
// distinction `data` draws at the same place: an address that names one thing answers with the
// thing. The filters then mean what they mean everywhere, since a document is one item.
func TestMotionByIDIsOneDocument(t *testing.T) {
	store := serve(t)
	whetherMotion(t, store, "m1")
	whetherMotion(t, store, "m2")

	out, err := runCLI(t, "motion", "m1")
	if err != nil {
		t.Fatalf("motion m1: %v", err)
	}
	if !strings.Contains(out, "id: m1") {
		t.Errorf("the motion asked for is not in the answer:\n%s", out)
	}
	if strings.Contains(out, "m2") {
		t.Errorf("naming one motion answered with another too:\n%s", out)
	}
	if strings.HasPrefix(strings.TrimSpace(out), "-") {
		t.Errorf("one motion came back as a list of one:\n%s", out)
	}
}

// An id that is not there is REFUSED, not answered with nothing. A listing that came back
// empty would read as "no motion is waiting on you", which is a different — and false —
// answer to the question that was asked.
func TestMotionByIDRefusesOneThatIsNotThere(t *testing.T) {
	serve(t)

	_, err := runCLI(t, "motion", "nosuch")
	if err == nil {
		t.Fatal("a motion that is not there was answered rather than refused")
	}
	if !strings.Contains(err.Error(), "nosuch") {
		t.Errorf("the refusal does not name what was asked for: %v", err)
	}
}

// --as IS THE BALLOT'S OTHER HALF, so alone it is a half-typed vote. A listing that ignored it
// would print the docket and look like it had cast one.
func TestMotionRefusesAsWithoutVote(t *testing.T) {
	store := serve(t)
	whetherMotion(t, store, "m1")

	_, err := runCLI(t, "motion", "m1", "--as", "alice")
	if err == nil {
		t.Fatal("--as with no --vote printed a motion; nothing was voted")
	}
	if !strings.Contains(err.Error(), "--vote") {
		t.Errorf("the refusal does not name the flag it goes with: %v", err)
	}
}

// A motion is named by ONE token, where `verse data` takes an address as steps. Somebody
// typing the spelling that works next door gets told, rather than answered about the first
// word of it.
func TestMotionRefusesASecondPositional(t *testing.T) {
	serve(t)

	if _, err := runCLI(t, "motion", "verse", "motion", "channel"); err == nil {
		t.Fatal("an address as steps was read as a motion id")
	}
}

// Every listing filters, this one included — and `--if` reads the elements of the array, which
// is what the docket answers with.
func TestMotionListingFilters(t *testing.T) {
	store := serve(t)
	whetherMotion(t, store, "m1")
	entitytest.Set(t, store, entity.VerseRef("motion", "channel", "m2"), entity.Obj{
		"axis": "who", "status": "open",
	}.Payload())

	out, err := runCLI(t, "motion", "--if", `{axis: who}`)
	if err != nil {
		t.Fatalf("motion --if: %v", err)
	}
	if strings.Contains(out, "m1") || !strings.Contains(out, "m2") {
		t.Errorf("--if did not narrow the docket to the who-axis:\n%s", out)
	}
}

// --watch is the same collection as it changes, so it asks at the same address the listing
// reads — narrowed to one motion when one is named. A watch that streamed the whole verse
// would be answering a question nobody asked, in a stream nobody can narrow afterwards.
func TestMotionWatchStreamsWhereTheMotionsLive(t *testing.T) {
	var asked string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		asked = r.URL.Query().Get("at")
		w.Header().Set(codec.HeaderFormat, "tony")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("data: {at: verse.motion.channel.m1, to: {status: closed}}\n"))
	}))
	defer srv.Close()

	defer func(addr string) { testAddr = addr }(testAddr)
	testAddr = strings.TrimPrefix(srv.URL, "http://")

	out, err := runCLI(t, "motion", "m1", "--watch")
	if err != nil {
		t.Fatalf("motion m1 --watch: %v", err)
	}
	if want := `verse.motion.channel.m1`; asked != want {
		t.Errorf("the watch asked at %q, want %q", asked, want)
	}
	if !strings.Contains(out, "status: closed") {
		t.Errorf("the frame did not reach the reader: %q", out)
	}

	if _, err := runCLI(t, "motion", "--watch"); err != nil {
		t.Fatalf("motion --watch: %v", err)
	}
	if want := "verse.motion.channel"; asked != want {
		t.Errorf("a bare watch asked at %q, want %q", asked, want)
	}
}

// A rejected watch must not read as a quiet verse, and it names the command that was typed:
// somebody who wrote `verse motion --watch` is not told about `watch`.
func TestMotionWatchCarriesTheRefusal(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "the store cannot replay from there", http.StatusBadRequest)
	}))
	defer srv.Close()

	defer func(addr string) { testAddr = addr }(testAddr)
	testAddr = strings.TrimPrefix(srv.URL, "http://")

	_, err := runCLI(t, "motion", "--watch")
	if err == nil {
		t.Fatal("a refused watch exited quietly, as though nothing had changed")
	}
	if !strings.Contains(err.Error(), "the store cannot replay from there") {
		t.Errorf("the refusal did not travel: %v", err)
	}
	if !strings.HasPrefix(err.Error(), "motion:") {
		t.Errorf("the refusal names another command: %v", err)
	}
}
