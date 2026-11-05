package versed

import (
	"log/slog"
	"sort"
	"strings"
	"sync"

	"github.com/signadot/tony-format/go-tony/ir"

	"github.com/signadot/verse/pkg/api"
	"github.com/signadot/verse/pkg/codec"
	"github.com/signadot/verse/pkg/entity"
)

// askRowAt is the ask stored at e as GET /asks lists it, with its id from its address. One that
// does not read as an api.AskRecord is answered as its id and why, so it can be seen.
//
// A KEY THE TYPE DOES NOT NAME is dropped from the row, and said in the log, once per record and
// keys, so a writer that drifts from api.AskRecord shows in a line rather than nowhere. The check
// in versed's tests is what keeps the two equal; this is for a writer they do not reach.
func askRowAt(e entity.Entity) api.AskRow {
	var rec api.AskRecord
	if err := codec.FromNode(e.Payload, &rec); err != nil {
		return api.AskRow{AskRecord: api.AskRecord{ID: e.Ref.Name()}, Unreadable: err.Error()}
	}
	if back, err := codec.ToNode(rec); err == nil {
		if lost := lostKeys(e.Payload, back, ""); len(lost) > 0 {
			sort.Strings(lost)
			key := e.Ref.String() + " " + strings.Join(lost, ",")
			if _, said := droppedAskKeys.LoadOrStore(key, true); !said {
				slog.Warn("an ask holds keys api.AskRecord does not name, and GET /asks drops them",
					"ask", e.Ref.String(), "keys", lost)
			}
		}
	}
	rec.ID = e.Ref.Name()
	return api.AskRow{AskRecord: rec}
}

// droppedAskKeys is which records' dropped keys askRowAt has said, so each is said once.
var droppedAskKeys sync.Map

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
