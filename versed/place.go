package versed

import (
	"fmt"
	"net/http"
	"os"

	"github.com/signadot/verse/entity"
)

// A PLACE IS TAKEN BY WHAT IS INSTALLED OVER IT. A charter package writes beneath its address
// and a source reflects beneath the nodes its claims name, and either acts as its record, which
// resolves to whoever installed it. A record is written only where ownership changes, so with
// nothing recorded at the place, every entity written beneath it got a record of its own naming
// the installer. Taking the place at install writes that one record where it says the most, and
// everything beneath is owned through it.
//
// The same two steps for both: the installer must hold `owner` at the place before anything
// installs, so a place somebody else owns is refused with the check's words — which is also what
// makes two sources sharing a node share an installer — and the record is written once the
// install has landed. The system and an unattributed request take no place: the system owns the
// root already, and nobody's name is no name to record.

// checkPlace refuses an install over a place the installer does not hold `owner` at.
func checkPlace(store entity.Store, installer string, ref entity.Ref, act string) (int, error) {
	if installer == "" || installer == entity.SystemPrincipal {
		return 0, nil
	}
	if err := entity.Check(store, installer, entity.OwnerLevel, ref, act); err != nil {
		return http.StatusForbidden, err
	}
	return 0, nil
}

// takePlace gives the place an owner record naming the installer, if it has none. A record that
// could not be written is said and not fatal: what was installed stands, and what it writes is
// recorded per entity, as it was, until the next install.
func takePlace(store entity.Store, installer string, ref entity.Ref, what string) {
	if installer == "" || installer == entity.SystemPrincipal {
		return
	}
	if err := entity.Claim(store, ref, installer); err != nil {
		fmt.Fprintf(os.Stderr, "verse: %s: taking %s for %s: %v\n", what, ref, installer, err)
	}
}
