package versed

import (
	"testing"

	"github.com/signadot/verse/entity/entitytest"
)

// testServer is a server over a store of its own — the assembly `up` performs, minus
// the daemons it would spawn, so an HTTP-surface test gets the real store the product
// runs on without knowing how to stand one up.
func testServer(t *testing.T) *Server {
	t.Helper()
	s := NewWithStore(ScriptedBuilder, entitytest.Store(t))
	// A source installed in a test runs a reflector under the server's context, which for a
	// server nobody started is Background — so without this it outlives the store it writes to
	// and goes on polling into a closed session, in whichever test runs next.
	t.Cleanup(s.stopAllSources)
	return s
}
