package versed

import (
	"testing"

	"github.com/signadot/verse/api"
	"github.com/signadot/verse/codec"
	"github.com/signadot/verse/entity"
	"github.com/signadot/verse/mirror"
)

// EVERY ASK A TEST LEAVES IS AN api.AskRecord, key for key. A typed view keeps only the keys its
// type names, so a key some writer adds and the type leaves out would vanish from GET /asks with
// nothing failing. The servers the versed tests stand up (testServer, askingServer, sayServer)
// run this when their test ends, so every ask any test poses, answers, carries, follows, orphans
// or suspends is held to the type: each stored record decodes, and every key it holds, at any
// depth, is still there when the decoded record is written back.
func checkAskRecords(t *testing.T, store entity.Reader) {
	t.Helper()
	for _, e := range entity.Below(store, mirror.IsAsk, 1, entity.VerseRoot, mirror.AskSystem) {
		var rec api.AskRecord
		if err := codec.FromNode(e.Payload, &rec); err != nil {
			t.Errorf("%s does not read as an api.AskRecord: %v", e.Ref, err)
			continue
		}
		back, err := codec.ToNode(rec)
		if err != nil {
			t.Errorf("%s does not write back: %v", e.Ref, err)
			continue
		}
		for _, lost := range lostKeys(e.Payload, back, "") {
			t.Errorf("%s holds %s, which api.AskRecord drops", e.Ref, lost)
		}
	}
}

// AN ASK RECORD SAYS WHICH KIND IT IS by whom it is from and to, and the five kinds are told
// apart in one place.
func TestAnAskRecordSaysItsKind(t *testing.T) {
	for want, rec := range map[string]api.AskRecord{
		api.AskKindSay:    {From: &api.AskFrom{Principal: "user:a@example.com"}, To: &api.AskTo{Task: "run-1"}},
		api.AskKindOnward: {From: &api.AskFrom{Principal: "via:verse.resolvers.r"}, Serves: &api.AskServes{ID: "land"}},
		api.AskKindPerson: {From: &api.AskFrom{Principal: "user:a@example.com"}, Gate: "ask"},
		api.AskKindTask:   {From: &api.AskFrom{Task: "run-1"}, Gate: "ask"},
		api.AskKindGate:   {From: &api.AskFrom{Rule: "land"}, Tool: "verse:trigger:land"},
	} {
		if got := rec.Kind(); got != want {
			t.Errorf("%+v is %q, want %q", rec, got, want)
		}
	}
}
