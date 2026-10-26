package versed

import (
	"github.com/signadot/verse/api"
	"github.com/signadot/verse/codec"
	"github.com/signadot/verse/entity"
)

// askRowAt is the ask stored at e as GET /asks lists it, with its id from its address. One that
// does not read as an api.AskRecord is answered as its id and why, so it can be seen.
func askRowAt(e entity.Entity) api.AskRow {
	var rec api.AskRecord
	if err := codec.FromNode(e.Payload, &rec); err != nil {
		return api.AskRow{AskRecord: api.AskRecord{ID: e.Ref.Name()}, Unreadable: err.Error()}
	}
	rec.ID = e.Ref.Name()
	return api.AskRow{AskRecord: rec}
}
