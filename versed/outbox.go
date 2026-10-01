package versed

import (
	"fmt"
	"net/http"
	"os"
	"strings"

	"github.com/signadot/tony-format/go-tony/ir"

	"github.com/signadot/verse/entity"
	verseio "github.com/signadot/verse/io"
	"github.com/signadot/verse/trigger"
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
				"registered io reads (`verse io` lists them, with their `out.places`)", send.To)
		}
		readers[i] = e.Out.As
	}
	var field any = entity.Removed()
	if len(sends) > 0 {
		field = trigger.StampSends(sends)
	}
	if _, _, err := store.Write(entity.Write{
		At:    entity.Path{Ref: trigger.PackageRef(confine.Stamp())},
		Value: entity.Obj{trigger.AtField: confine.Stamp(), trigger.SendsField: field}.Payload(),
	}); err != nil {
		return fmt.Errorf("sends: recording the declaration: %w", err)
	}
	kept := map[string]bool{}
	for i, send := range sends {
		kept[send.From] = true
		if err := holdOutbox(store, installer, send.From, readers[i]); err != nil {
			return fmt.Errorf("sends: %s: %w", send.From, err)
		}
	}
	for _, old := range was {
		if !kept[old.From] {
			releaseOutbox(store, old.From)
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
	grants := []entity.Grant{{At: entity.SegsOf(ref), Below: true, To: map[entity.Level][]string{
		entity.Read:       {as},
		entity.OwnerLevel: {as},
	}}}
	return entity.SetGrants(store, ref, grants)
}

// releaseOutbox takes back what holdOutbox held out, for an entry or a package that went. Said on
// stderr rather than refused, as unseedOut does: the grant is visible to `verse perms` either way.
func releaseOutbox(store entity.Store, from string) {
	ref, err := entity.ParseAddr(from)
	if err != nil {
		return
	}
	if err := entity.SetGrants(store, ref, nil); err != nil {
		fmt.Fprintf(os.Stderr, "verse: outbox %s: its grants still stand: %v\n", from, err)
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
	for _, ent := range entity.Below(s.Store, trigger.IsCharterPackage, 2,
		entity.VerseRoot, trigger.CharterSystem) {
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
	out := make([]map[string]any, 0)
	for _, send := range s.outboxesTo(place) {
		out = append(out, map[string]any{"from": send.From, "to": send.To})
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
