package viffd

import "go.viff.dev/viff/pkg/entity"

// failingStore is a store whose reads fail — a reset session, a timeout. Everything else
// delegates, so only the read path is under test.
type failingStore struct {
	entity.Store
	err error
}

func (f failingStore) Get(ref entity.Ref) (entity.Entity, bool, error) {
	if f.err != nil {
		return entity.Entity{}, false, f.err
	}
	return f.Store.Get(ref)
}
