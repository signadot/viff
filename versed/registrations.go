package versed

import (
	"github.com/signadot/verse/codec"
	"github.com/signadot/verse/entity"
)

// unreadableRegistration is a stored registration that does not decode as this verse reads it,
// with why.
type unreadableRegistration struct {
	Ref entity.Ref
	Err error
}

// readRegistrations reads every registration under a registry (`verse.runners`, `verse.iods`,
// `verse.resolvers`), at the subtree rather than over the whole document, and answers the ones
// that decode and the ones that do not. A listing shows the second kind as a row of its own, so a
// registration an older verse wrote can be seen and removed rather than vanishing.
func readRegistrations[T any](r entity.Reader, system string, is func(entity.Ref) bool) ([]T, []unreadableRegistration, error) {
	registry := []string{entity.VerseRoot, system}
	ents, err := entity.ListAtErr(r, registry, len(registry)+1, is)
	if err != nil {
		return nil, nil, err
	}
	var ok []T
	var bad []unreadableRegistration
	for _, ent := range ents {
		var v T
		if err := codec.FromNode(ent.Payload, &v); err != nil {
			bad = append(bad, unreadableRegistration{Ref: ent.Ref, Err: err})
			continue
		}
		ok = append(ok, v)
	}
	return ok, bad, nil
}
