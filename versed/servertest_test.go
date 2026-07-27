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
	return NewWithStore(ScriptedBuilder, entitytest.Store(t))
}
