package versed

import (
	"errors"
	"fmt"
	"net/http"
	"os"
	"time"

	"github.com/signadot/tony-format/go-tony/ir"

	"github.com/signadot/verse/entity"
	"github.com/signadot/verse/trigger"
)

// A CHARTER STANDS FOR AS LONG AS ITS INSTALL SAYS: a fielded charter's `ttl:` is how long, and
// at the end of it the package goes as `verse charter -r <address>` takes it down — its rules,
// levels, schedules, outboxes and declaration, and nothing it wrote. Whoever installs a charter
// may give it one, a task carried by its runner included, and nobody has to: a charter written to
// outlive what installed it, and then to stop, is what it is for.
//
//	at: repos.r.watch
//	ttl: 72h
//	rules:
//	- …
//
// IT IS THE PACKAGE'S DECLARATION, beside `reads:` and `sends:`, kept on the same record
// (trigger.PackageRef) and replaced whole by each fielded install as they are: installing the
// file again renews it, and installing it with no `ttl:` leaves it standing until it is removed.
// The shorthand — bare rules at a path — declares nothing, so it leaves the expiry as it stands.
// Baseline has none, as it has no `reads:`: it is not removed whole. Nor has a scope's charter,
// which goes with its scope.
//
// The install writes `expires`, its own instant plus the ttl, where `verse charter` reads it
// back: the file says `ttl:`, which installs as it was written, and the instant is a note beside
// it, as a held rule is — state, not the author's text. The timer that acts on it is this
// process's, armed from a watch on the declarations, which opens by replaying what stands; so a
// restart arms what a previous process left, as the asks' expiries are (askttl.go).

const (
	// charterTTLField is how long a package stands, as its file wrote it (`72h`).
	charterTTLField = "ttl"
	// charterExpiresField is the instant it goes at, RFC 3339.
	charterExpiresField = "expires"
)

// charterTTL reads a fielded charter's `ttl:`: a duration, more than none.
func charterTTL(v *ir.Node) (time.Duration, error) {
	bad := errors.New("a charter's `ttl:` is how long it stands, a duration (`72h`, `30m`): " +
		"when it is over the charter is removed")
	if v == nil || v.Type != ir.StringType {
		return 0, bad
	}
	d, err := time.ParseDuration(v.String)
	if err != nil || d <= 0 {
		return 0, bad
	}
	return d, nil
}

// expiry writes, or takes away, the expiry of the package a fielded charter declares: its ttl
// from now, or none when the file gives none. The declaration record stands by now (imports),
// and its undo puts the whole of it back, expiry included.
func (tx *charterTx) expiry(g *charterGroup) (int, error) {
	if g.ttl > 0 && tx.scope != "" {
		return http.StatusBadRequest, errors.New("ttl: a scope's charter has none: it goes " +
			"with its scope, and a ttl is for a package in the verse")
	}
	confine, err := trigger.ParseAt(g.at)
	if err != nil {
		return http.StatusBadRequest, err
	}
	ref := trigger.PackageRef(confine.Stamp())
	if g.ttl > 0 {
		g.expires = tx.s.now().Add(g.ttl).UTC().Format(time.RFC3339)
		if _, _, err := tx.store.Write(entity.Write{At: entity.Path{Ref: ref}, Value: entity.Obj{
			charterTTLField:     g.ttlText,
			charterExpiresField: g.expires,
		}.Payload()}); err != nil {
			return http.StatusServiceUnavailable, fmt.Errorf("ttl: recording it: %w", err)
		}
		return http.StatusCreated, nil
	}
	decl, had, err := tx.s.packageDecl(g.at)
	if err != nil {
		return http.StatusServiceUnavailable, fmt.Errorf("ttl: reading what stands: %w", err)
	}
	if !had {
		// No declaration, nothing to take a ttl off: a scope's install, whose declaration is
		// the scope's.
		return http.StatusCreated, nil
	}
	for _, f := range []string{charterTTLField, charterExpiresField} {
		if entity.Get(decl, f) == nil {
			continue
		}
		if _, _, err := tx.store.Write(entity.Write{At: entity.Path{Ref: ref, Access: f}}); err != nil {
			return http.StatusServiceUnavailable, fmt.Errorf("ttl: taking the last one away: %w", err)
		}
	}
	return http.StatusCreated, nil
}

// armCharters is the startup phase: follow every package's declaration, the ones standing now
// included, and arm a timer for each expiry.
func (s *Server) armCharters() error {
	if err := s.awaiting(phasePackages); err != nil {
		return err
	}
	kind := entity.VerseRef(trigger.CharterSystem, trigger.PackageKind)
	ch, unsub, err := s.Store.WatchSubtree(len(kind.Segs())+1, entity.VerseRoot, trigger.CharterSystem, trigger.PackageKind)
	if err != nil {
		return entity.Unread(fmt.Errorf("the charters could not be followed, so no expiry was armed: %w", err))
	}
	ctx := s.ctxOrBackground()
	go func() {
		defer unsub()
		for {
			select {
			case <-ctx.Done():
				return
			case c, ok := <-ch:
				if !ok {
					return
				}
				for _, ev := range c.Changes {
					if !trigger.IsCharterPackage(ev.Ref) {
						continue
					}
					if ev.Op == entity.Delete || ev.To == nil {
						s.charterExpiries.drop(ev.Ref.Name())
						continue
					}
					s.expireCharterLater(ev.Ref, ev.To)
				}
			}
		}
	}()
	return nil
}

// expireCharterLater is one package's declaration as it stands: a timer at its expiry, or none.
func (s *Server) expireCharterLater(ref entity.Ref, p entity.Payload) {
	at := entity.Str(p, charterExpiresField)
	if at == "" {
		s.charterExpiries.drop(ref.Name())
		return
	}
	when, err := time.Parse(time.RFC3339, at)
	if err != nil {
		fmt.Fprintf(os.Stderr, "verse: charter %s: its expiry %q is not a date, so it stands\n", ref.Name(), at)
		return
	}
	s.charterExpiries.set(ref.Name(), s.at(when, func() { s.expireCharter(ref) }))
}

// expireCharter removes a package whose expiry has come, under the charter lock, as `verse
// charter -r` would, and only the package AT the address: a charter under it is one of its own.
// It reads the declaration again first, so a timer outliving what armed it acts on the charter as
// it stands: one installed again since, renewed or with no ttl, is not removed.
func (s *Server) expireCharter(ref entity.Ref) {
	release, err := s.lockCharter(s.ctxOrBackground())
	if err != nil {
		return // the server is stopping; the next start arms it again
	}
	defer release()
	ent, ok, err := s.Store.Get(ref)
	if err != nil || !ok {
		return
	}
	when, err := time.Parse(time.RFC3339, entity.Str(ent.Payload, charterExpiresField))
	if err != nil {
		return
	}
	if s.now().Before(when) {
		s.expireCharterLater(ref, ent.Payload)
		return
	}
	at := entity.Str(ent.Payload, trigger.AtField)
	if err := s.uninstallOne(entity.As(s.Store, entity.SystemDefault), at); err != nil {
		fmt.Fprintf(os.Stderr, "verse: charter %s: its ttl is over, and it was not removed: %v\n", at, err)
		return
	}
	fmt.Fprintf(os.Stderr, "verse: charter %s: its ttl is over, and it is removed\n", at)
}
