package versed

import (
	_ "embed"
	"fmt"
	"os"

	"github.com/signadot/verse/codec"
	"github.com/signadot/verse/entity"
)

// baseCharter is the charter every verse stands on, in the binary: the standard schedules and
// the calendar (system.tony). Installed at every start, before the deployment's own.
//
//go:embed system.tony
var baseCharter []byte

// installSystemCharter applies the system's charter: the embedded base, then the file named by
// SystemCharter when the deployment names one. Their rules are the system's, their firings act
// as the system, and a question one of them poses is co-owned by the system alone. A file that
// cannot be read or installed is reported and the rest of the start goes on; a verse whose
// system charter is missing is a verse with fewer rules, not one that will not start.
func (s *Server) installSystemCharter() {
	s.installAsSystem("the base charter", baseCharter)
	if s.SystemCharter != "" {
		if data, err := os.ReadFile(s.SystemCharter); err != nil {
			fmt.Fprintf(os.Stderr, "verse: system charter: %v\n", err)
		} else {
			s.installAsSystem("system charter "+s.SystemCharter, data)
		}
	}
}

// installSystemCharters applies the charters handed to the server as text (SystemCharters), as
// the system, after the sources are restored: one that sends to an io's place is checked against
// the iods registered, and a text that cannot be installed is reported and the rest go on.
//
// ONE REFUSED FOR WANT OF ITS IOS OR RUNNERS WAITS FOR THEM. A verse started on an empty store (a
// wipe) has no iods or runners at start, and they are registered afterwards
// (deploy/repopulate.sh), so such a charter — one sending to an io, or spawning on a runner — is
// refused here and would stay missing until the next restart. It is kept, and tried again each
// time an io with an out half is registered or a runner's offers are written
// (installWaitingSystemCharters, offersChanged).
func (s *Server) installSystemCharters() {
	s.systemMu.Lock()
	defer s.systemMu.Unlock()
	s.systemWaiting = nil
	for _, c := range s.SystemCharters {
		if err := s.installAsSystem("system charter "+c.Name, c.Text); err != nil {
			s.systemWaiting = append(s.systemWaiting, c)
		}
	}
}

// installWaitingSystemCharters tries again each system charter refused so far, as an io's
// registration or a runner's offers may be what it was waiting for, and keeps those refused
// again.
func (s *Server) installWaitingSystemCharters() {
	s.systemMu.Lock()
	defer s.systemMu.Unlock()
	var still []SystemCharter
	for _, c := range s.systemWaiting {
		if err := s.installAsSystem("system charter "+c.Name, c.Text); err != nil {
			still = append(still, c)
			continue
		}
		fmt.Fprintf(os.Stderr, "verse: system charter %s: installed, once what it was waiting for stood\n", c.Name)
	}
	s.systemWaiting = still
}

// SystemCharter is one charter a deployment installs as the system beside the file it names: a
// text, and the name the log says it by.
type SystemCharter struct {
	Name string
	Text []byte
}

// installAsSystem installs one charter's text as the system, saying what it was when it cannot,
// and answering why.
func (s *Server) installAsSystem(what string, data []byte) error {
	docs, err := codec.ParseNodes(data, codec.Tony)
	if err != nil {
		fmt.Fprintf(os.Stderr, "verse: %s: %v\n", what, err)
		return err
	}
	release, err := s.lockCharter(s.ctxOrBackground())
	if err != nil {
		fmt.Fprintf(os.Stderr, "verse: %s: %v\n", what, err)
		return err
	}
	defer release()
	if _, _, err := s.installCharterAs(entity.As(s.Store, entity.SystemCharter), "", "", docs); err != nil {
		fmt.Fprintf(os.Stderr, "verse: %s: %v\n", what, err)
		return err
	}
	return nil
}
