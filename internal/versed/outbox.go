package versed

import (
	"fmt"
	"net/http"
	"os"
	"strings"

	"github.com/signadot/tony-format/go-tony/ir"

	"github.com/signadot/verse/internal/trigger"
	"github.com/signadot/verse/internal/trigger/charter"
	"github.com/signadot/verse/pkg/api"
	"github.com/signadot/verse/pkg/entity"
	verseio "github.com/signadot/verse/pkg/io"
)

// OUTBOXES: a package's places an io's out half reads (trigger.SendsField, git-issue
// 84a83gkwh12krst5phn0).
//
// A package writes only inside its address and an io's out half reads only inside its claim, so a
// package could not ask an io for anything. An outbox is the meeting place, declared by the
// package: the io reads what is asked there exactly as it reads its own out place, and writes each
// status beside its input — inside the package, where the package's rules can read it.
//
// Nothing about the engine changes. The package's writes stay in its address and its own strata;
// what changes is who may read a place the package already owned.

// ioFor is the registration whose out half reads place, if one does.
func ioFor(r entity.Reader, place string) (verseio.Entity, bool) {
	for _, e := range verseio.List(r) {
		if e.Out == nil {
			continue
		}
		for _, at := range e.Out.Places {
			if ref, err := entity.ParseAddr(at); err == nil && ref.KPath() == place {
				return e, true
			}
		}
	}
	return verseio.Entity{}, false
}

// setPackageSends installs a package's outboxes: each `to` is an out place a registered io reads,
// the record says what is declared, and each outbox is held out to the io that reads it — and no
// longer to one whose entry went.
func (s *Server) setPackageSends(store entity.Store, installer, at string, sends, was []trigger.Send) error {
	confine, err := trigger.ParseAt(at)
	if err != nil {
		return err
	}
	readers := make([]string, len(sends))
	for i, send := range sends {
		e, ok := ioFor(s.Store, send.To)
		if !ok {
			return fmt.Errorf("sends: %s is no io's out place — an outbox sends to a place a "+
				"registered io reads (`verse iod` lists them, with their `out.places`)", send.To)
		}
		readers[i] = e.Out.As
	}
	var field any = entity.Removed()
	if len(sends) > 0 {
		field = trigger.StampSends(sends)
	}
	if _, _, err := store.Write(entity.Write{
		At:    entity.Path{Ref: charter.PackageRef(confine.Stamp())},
		Value: entity.Obj{charter.AtField: confine.Stamp(), trigger.SendsField: field}.Payload(),
	}); err != nil {
		return fmt.Errorf("sends: recording the declaration: %w", err)
	}
	reader := map[string]string{}
	for i, send := range sends {
		reader[send.From] = readers[i]
		if err := holdOutbox(store, installer, send.From, readers[i]); err != nil {
			return fmt.Errorf("sends: %s: %w", send.From, err)
		}
	}
	// What an entry that went, or now sends to another io, held out to the io it sent to. An io
	// no longer registered took its grants back when it was removed (releaseOutboxesTo).
	for _, old := range was {
		e, ok := ioFor(s.Store, old.To)
		if ok && reader[old.From] != e.Out.As {
			releaseOutbox(store, old.From, e.Out.As)
		}
	}
	return nil
}

// holdOutbox holds an outbox out to the io that reads it, as seedOut holds out an io's own out
// place: the package's installer takes the place, and the io READS beneath it and OWNS each status
// it creates there, so nobody forges one. The package's rules ask as its installer, who owns the
// place and so reads and writes beneath it already, statuses included (owners above co-own).
func holdOutbox(store entity.Store, installer, from, as string) error {
	ref, err := entity.ParseAddr(from)
	if err != nil {
		return err
	}
	takePlace(store, installer, ref, "outbox")
	return grantOutbox(store, ref, as)
}

// grantOutbox writes an outbox's grant line for the io acting as `as`, BESIDE whatever else the
// place holds out — the installer's own lines are theirs — and in place of a line it held before,
// so holding again does not stack a second.
func grantOutbox(store entity.Store, ref entity.Ref, as string) error {
	if err := entity.Revoke(store, ref, outboxLevels(as)); err != nil {
		return err
	}
	return entity.AddGrant(store, ref, entity.Grant{At: entity.SegsOf(ref), Below: true, To: outboxLevels(as)})
}

// outboxLevels is what an outbox holds out to the io that reads it: read beneath, and owner of
// what it creates there.
func outboxLevels(as string) entity.Levels {
	return entity.Levels{entity.Read: {as}, entity.OwnerLevel: {as}}
}

// AN OUTBOX'S GRANT FOLLOWS BOTH SIDES: it stands while the package declares the outbox AND an io
// reads the place it sends to. So an io's removal takes back the grants of every outbox sending
// to its places, whatever the removal kept of its own (`--keep` keeps the io's place for a
// re-install, but an outbox is a package's arrangement with an io that is gone), and an io's
// install grants the outboxes already declared for its places — the package need not be
// installed again to be heard. Written as the system: the outboxes are other people's places,
// and this is the bookkeeping of a declaration they made, not an act of whoever moves the io.

// releaseOutboxesTo takes back what every outbox sending to one of places held out to the io
// acting as `as`.
func (s *Server) releaseOutboxesTo(places []string, as string) {
	sys := entity.As(s.Store, entity.SystemDefault)
	for _, place := range places {
		ref, err := entity.ParseAddr(place)
		if err != nil {
			continue
		}
		for _, send := range s.outboxesTo(ref.KPath()) {
			releaseOutbox(sys, send.From, as)
		}
	}
}

// grantOutboxesTo grants the io acting as `as` every outbox already declared for one of places.
func (s *Server) grantOutboxesTo(places []string, as string) {
	sys := entity.As(s.Store, entity.SystemDefault)
	for _, place := range places {
		ref, err := entity.ParseAddr(place)
		if err != nil {
			continue
		}
		for _, send := range s.outboxesTo(ref.KPath()) {
			from, err := entity.ParseAddr(send.From)
			if err != nil {
				continue
			}
			if err := grantOutbox(sys, from, as); err != nil {
				fmt.Fprintf(os.Stderr, "verse: outbox %s: not held out to %s: %v\n", send.From, as, err)
			}
		}
	}
}

// releaseOutbox takes back what holdOutbox held out to the io acting as `as`, for an entry or a
// package that went, and nothing else the place holds out (git-issue ne9fce9dh12ks1q4qsn0). Said
// on stderr rather than refused, as unseedOut does: the grant is visible to `verse perms` either
// way.
func releaseOutbox(store entity.Store, from, as string) {
	ref, err := entity.ParseAddr(from)
	if err != nil {
		return
	}
	if err := entity.Revoke(store, ref, outboxLevels(as)); err != nil {
		fmt.Fprintf(os.Stderr, "verse: outbox %s: its grant to %s still stands: %v\n", from, as, err)
	}
}

// packageSends is what a package's record says it sends, nil for none.
func (s *Server) packageSends(at string) ([]trigger.Send, error) {
	decl, had, err := s.packageDecl(at)
	if err != nil || !had {
		return nil, err
	}
	return trigger.SendsOf(decl), nil
}

// outboxesTo is every declared outbox sending to place, in package order.
func (s *Server) outboxesTo(place string) []trigger.Send {
	var out []trigger.Send
	for _, ent := range entity.Below(s.Store, charter.IsPackage, 2,
		entity.VerseRoot, charter.System) {
		for _, send := range trigger.SendsOf(ent.Payload) {
			if send.To == place {
				out = append(out, send)
			}
		}
	}
	return out
}

// listOutboxes is `GET /outboxes?to=<place>`: the outboxes an io's out half reads beside its own
// place. Asked by THAT io — the principal its registration says the out half acts as — or the
// system: an outbox is a package's arrangement with one io, and nobody else's to enumerate.
func (s *Server) listOutboxes(w http.ResponseWriter, r *http.Request) {
	to := r.URL.Query().Get("to")
	ref, err := entity.ParseAddr(to)
	if err != nil {
		http.Error(w, "to: "+err.Error(), http.StatusBadRequest)
		return
	}
	place := ref.KPath()
	e, ok := ioFor(s.Store, place)
	if !ok {
		http.Error(w, fmt.Sprintf("%s is no io's out place", place), http.StatusNotFound)
		return
	}
	if who := principalOf(r); who != e.Out.As && !entity.IsSystem(who) {
		http.Error(w, fmt.Sprintf("the outboxes sending to %s are for %s, the io that reads it, "+
			"to list", place, e.Out.As), http.StatusForbidden)
		return
	}
	out := make([]api.Outbox, 0)
	for _, send := range s.outboxesTo(place) {
		out = append(out, api.Outbox{From: send.From, To: send.To})
	}
	writeOut(w, r, http.StatusOK, out)
}

// relativeSends writes each `from` relative to the package's address, as a charter writes it, so
// a listing installs back as the declaration it came from.
func relativeSends(sends []trigger.Send, at string) []trigger.Send {
	out := make([]trigger.Send, len(sends))
	for i, send := range sends {
		out[i] = send
		if confine, err := trigger.ParseAt(at); err == nil {
			out[i].From = strings.TrimPrefix(send.From, confine.Stamp()+".")
		}
	}
	return out
}

// sendNodes renders outboxes as the charter's `sends:` list.
func sendNodes(sends []trigger.Send) *ir.Node {
	vals := make([]*ir.Node, len(sends))
	for i, send := range sends {
		vals[i] = ir.FromMap(map[string]*ir.Node{"from": ir.FromString(send.From), "to": ir.FromString(send.To)})
	}
	return ir.FromSlice(vals)
}
