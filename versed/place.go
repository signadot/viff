package versed

import (
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"

	"github.com/signadot/verse/entity"
	verseio "github.com/signadot/verse/io"
	"github.com/signadot/verse/resolver"
	"github.com/signadot/verse/schedule"
	"github.com/signadot/verse/trigger"
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
// install has landed. The system takes no place, since it owns the root already; an install by
// nobody is refused, since what it installed would act as the system for want of a name.

// checkPlace refuses an install over a place the installer does not hold `owner` at.
func checkPlace(store entity.Store, installer string, ref entity.Ref, act string) (int, error) {
	if installer == "" {
		return http.StatusBadRequest, fmt.Errorf("%s: an install names who installs it", act)
	}
	if entity.IsSystem(installer) {
		return 0, nil
	}
	if err := entity.CheckTake(store, installer, ref, act); err != nil {
		return http.StatusForbidden, err
	}
	return 0, nil
}

// takePlace gives the place an owner record naming the installer, if it has none. A record that
// could not be written is said and not fatal: what was installed stands, and what it writes is
// recorded per entity, as it was, until the next install.
func takePlace(store entity.Store, installer string, ref entity.Ref, what string) {
	if installer == "" || entity.IsSystem(installer) {
		return
	}
	if err := entity.Claim(store, ref, installer); err != nil {
		fmt.Fprintf(os.Stderr, "verse: %s: taking %s for %s: %v\n", what, ref, installer, err)
	}
}

// errActsAsNobody is an install that landed and is not its installer's: see actsAsInstaller.
var errActsAsNobody = errors.New("installed as nobody's")

// actsAsInstaller is what every verb that makes a place that ACTS ends with — a rule, a
// schedule, a source, a resolver: the place acts as somebody, and where this install made it,
// as whoever installed it.
//
// A place acts as the owner on its own record and as nobody without one (entity.ActsAs). The
// record is written after the place, by the write that made it, and only for a writer who holds
// `owner` there: so an installer holding less — `read-write` beneath a registry, by a line
// somebody added — or a store that took the place and did not answer for the record, leaves a
// place that stands, lists as installed, and does nothing, for a reason nobody is told. Asked
// here, while somebody is present to hear it, and the caller takes the install back out.
//
// before is who the place acted as before this install, "" where it did not stand or stood as
// nobody's: an install over somebody's place, by a co-owner, leaves it theirs and is no fault.
func actsAsInstaller(store entity.Store, ref entity.Ref, installer, before string) error {
	now, err := entity.ActsAs(store, ref)
	if err != nil {
		return fmt.Errorf("reading whose %s is, after installing it: %w", ref, err)
	}
	same := now == installer || (entity.IsSystem(now) && entity.IsSystem(installer))
	switch {
	case now == "":
		return fmt.Errorf("%w: %s has no owner record, so it would act as nobody and do nothing: "+
			"%s holds less than `owner` there, or the store did not record it. Nothing was left "+
			"installed; install it again, as somebody who may create there", errActsAsNobody, ref, installer)
	case before == "" && !same:
		return fmt.Errorf("%w: %s was installed by %s and acts as %s, whose record stood at the "+
			"name first. Nothing was left installed", errActsAsNobody, ref, installer, now)
	}
	return nil
}

// takeBackOut removes a place an install has just made and actsAsInstaller refused. As the
// system, in the request's scope: the installer does not own it, which is the fault, so its
// removal is not theirs to make. A place that stood before the install is left standing — the
// install wrote over it and what it replaced is not kept here — and the refusal has said so.
func (s *Server) takeBackOut(r *http.Request, ref entity.Ref, stood bool) {
	if stood {
		return
	}
	store := s.Store
	if scope := r.URL.Query().Get("scope"); scope != "" {
		store = store.Scope(scope)
	}
	if _, _, err := store.Write(entity.Write{At: ref.At()}); err != nil {
		fmt.Fprintf(os.Stderr, "verse: taking %s back out after an install nobody owns: %v\n", ref, err)
	}
}

// actingAsNobody is every place that acts and has no owner record of its own — a rule, a
// schedule, a source, a resolver that stands, lists as installed, and holds nothing, so that
// whatever it does is refused. An install cannot leave one (actsAsInstaller); a store written by
// a build that could, or by hand, can hold them, and the fix for each is to remove it and
// install it again as somebody.
func actingAsNobody(store entity.Store) ([]entity.Ref, error) {
	var out []entity.Ref
	for _, reg := range []struct {
		at   []string
		pred func(entity.Ref) bool
	}{
		{[]string{entity.VerseRoot, trigger.CharterSystem, trigger.CharterKind}, nil},
		{[]string{entity.VerseRoot, schedule.System}, schedule.IsRecord},
		{[]string{entity.VerseRoot, verseio.System}, verseio.IsRegistration},
		{[]string{entity.VerseRoot, resolver.System}, resolver.IsResolver},
	} {
		ents, err := entity.BelowErr(store, reg.pred, 1, reg.at...)
		if err != nil {
			return nil, err
		}
		for _, e := range ents {
			who, err := entity.ActsAs(store, e.Ref)
			if err != nil {
				return nil, err
			}
			if who == "" {
				out = append(out, e.Ref)
			}
		}
	}
	return out, nil
}

// reportActingAsNobody says at start what actingAsNobody finds, once, where an operator reads
// what a start did. A read that fails says nothing: the places are no worse for not being
// listed, and the start has its own way of saying the store did not answer.
func (s *Server) reportActingAsNobody() {
	refs, err := actingAsNobody(s.Store)
	if err != nil {
		return
	}
	for _, ref := range refs {
		slog.Warn("a place that acts has no owner record, so it acts as nobody and what it does "+
			"is refused: remove it and install it again as somebody", "at", ref.String())
	}
}
