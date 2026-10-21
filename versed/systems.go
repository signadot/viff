package versed

import (
	"fmt"

	"github.com/signadot/verse/entity"
	"github.com/signadot/verse/systems"
)

// systemsSystem is where the systems' documents live in verse's half of the tree:
// `verse.systems.<name>`.
const systemsSystem = "systems"

// systemRef is the address of one system's document.
func systemRef(name string) entity.Ref {
	return entity.VerseRef(systemsSystem, name)
}

// installSystems writes every system's document (package systems) at its address, as the
// system: the binary ships them, and the store holds them for whoever reads what a step or a
// tool may be named. A document there of a system the binary does not ship is removed, so
// the store says what this verse checks autonomies against and nothing it no longer does. One
// that cannot be written is reported, and the rest go in.
func (s *Server) installSystems() error {
	store := entity.As(s.Store, entity.SystemCharter)
	var failed error
	shipped := map[string]bool{}
	for _, d := range systems.All() {
		shipped[d.Name] = true
		if _, _, err := store.Write(entity.Write{At: systemRef(d.Name).At(), Value: entity.Val(d)}); err != nil {
			failed = fmt.Errorf("the %s document: %w", d.Name, err)
		}
	}
	// What the store lists under verse.systems is each document's fields, so a system is the
	// segment after it.
	under := systemRef("").Segs()
	stale := map[string]bool{}
	for _, e := range s.Store.List(func(r entity.Ref) bool { return r.Under(under...) }) {
		if segs := e.Ref.Segs(); len(segs) > len(under) && !shipped[segs[len(under)]] {
			stale[segs[len(under)]] = true
		}
	}
	for name := range stale {
		if _, _, err := store.Write(entity.Write{At: systemRef(name).At(), Value: entity.Removed()}); err != nil {
			failed = fmt.Errorf("removing the %s document: %w", name, err)
		}
	}
	return failed
}
