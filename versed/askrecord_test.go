package versed

import (
	"testing"

	"github.com/signadot/tony-format/go-tony/ir"

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

// lostKeys is every key path of was that is absent from is: an object's keys, and the keys of
// the objects in its lists, at any depth. A null in was is no value, so it is not looked for;
// an empty string, a zero or false is a value, and a type that drops one fails here.
func lostKeys(was, is *ir.Node, path string) []string {
	was, is = ir.Uncomment(was), ir.Uncomment(is)
	if was == nil || was.Type == ir.NullType {
		return nil
	}
	var lost []string
	switch was.Type {
	case ir.ObjectType:
		have := map[string]*ir.Node{}
		if is != nil && is.Type == ir.ObjectType {
			have = ir.ToMap(is)
		}
		for k, v := range ir.ToMap(was) {
			if v = ir.Uncomment(v); v == nil || v.Type == ir.NullType {
				continue
			}
			got, ok := have[k]
			if !ok {
				lost = append(lost, path+"."+k)
				continue
			}
			lost = append(lost, lostKeys(v, got, path+"."+k)...)
		}
	case ir.ArrayType:
		if is == nil || is.Type != ir.ArrayType {
			return nil // a list read from another shape (one name as a list of one) keeps no keys
		}
		for i, v := range was.Values {
			if i < len(is.Values) {
				lost = append(lost, lostKeys(v, is.Values[i], path+"[]")...)
			}
		}
	}
	return lost
}

// AN ASK RECORD SAYS WHICH KIND IT IS by the keys it carries, and the five kinds are told apart
// in one place.
func TestAnAskRecordSaysItsKind(t *testing.T) {
	for want, rec := range map[string]api.AskRecord{
		api.AskKindSay:    {Asker: "user:a@example.com", SaidTo: "run-1"},
		api.AskKindOnward: {Asker: "via:verse.resolvers.r", Serves: &api.AskServes{ID: "land"}},
		api.AskKindPerson: {Asker: "user:a@example.com", Gate: "ask"},
		api.AskKindTask:   {Task: "run-1", Gate: "ask"},
		api.AskKindGate:   {Tool: "verse:trigger:land"},
	} {
		if got := rec.Kind(); got != want {
			t.Errorf("%+v is %q, want %q", rec, got, want)
		}
	}
}

// AN ASK WRITTEN BEFORE ITS SHAPES WERE ONE still reads: one answerer as a string is a list of
// one, and a carry's sentence in `status` is its note.
func TestAnOlderAskRecordStillReads(t *testing.T) {
	var rec api.AskRecord
	if err := codec.Unmarshal([]byte(`{answerers: user:ana@example.com, carried: {at: "2026-10-01T00:00:00Z", status: "nothing: the clock settled on no option"}}`), &rec, codec.Tony); err != nil {
		t.Fatal(err)
	}
	if len(rec.Answerers) != 1 || rec.Answerers[0] != "user:ana@example.com" {
		t.Errorf("answerers = %v, want the one name as a list", rec.Answerers)
	}
	if rec.Carried == nil || rec.Carried.Status != 0 || rec.Carried.Note == "" {
		t.Errorf("carried = %+v, want the sentence as its note and no status", rec.Carried)
	}
}
