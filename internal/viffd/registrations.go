package viffd

import (
	"fmt"
	"net/http"

	"go.viff.dev/viff/pkg/codec"
	"go.viff.dev/viff/pkg/entity"
)

// unreadableRegistration is a stored registration that does not decode as this viff reads it,
// with why.
type unreadableRegistration struct {
	Ref entity.Ref
	Err error
}

// readRegistrations reads every registration under a registry (`mirror.runners`, `mirror.iods`,
// `mirror.resolvers`), at the subtree rather than over the whole document, and answers the ones
// that decode and the ones that do not. A listing shows the second kind as a row of its own, so a
// registration an older viff wrote can be seen and removed rather than vanishing.
func readRegistrations[T any](r entity.Reader, system string, is func(entity.Ref) bool) ([]T, []unreadableRegistration, error) {
	registry := []string{entity.ViffRoot, system}
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

// probeParam reads `?probe=`, which says whether to ask a runner or an io what it is now. It is
// `true` or `false`, and absent is dflt: an install probes, a listing does not. Anything else is
// refused, so a typo never quietly picks the default.
func probeParam(w http.ResponseWriter, r *http.Request, dflt bool) (probe, ok bool) {
	switch v := r.URL.Query().Get("probe"); v {
	case "":
		return dflt, true
	case "true":
		return true, true
	case "false":
		return false, true
	default:
		http.Error(w, fmt.Sprintf("probe is true or false, not %q", v), http.StatusBadRequest)
		return false, false
	}
}
