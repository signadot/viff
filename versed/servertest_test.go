package versed

import (
	"testing"

	"github.com/signadot/verse/entity"
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
	t.Cleanup(s.stopAllIOs)
	return s
}

// owns gives each place an owner record naming who, as the install, spawn or first write that
// made it would have left: a fixture that stands something up through the engine or the store
// directly acts as the system, which leaves no record, and a node with none is its nearest
// owner's — the root's, or verse's — so nobody the test goes on to act as would hold anything
// there. grants is what the record holds out; nil is the default above it.
func owns(t *testing.T, srv *Server, who string, grants []entity.Grant, places ...entity.Ref) {
	t.Helper()
	for _, at := range places {
		if err := entity.Establish(srv.Store, at, who, entity.Ref{}, grants); err != nil {
			t.Fatalf("recording %s as %s's: %v", at, who, err)
		}
	}
}

// testerOwns is owns for the principal an unattributed test request is made as.
func testerOwns(t *testing.T, srv *Server, places ...entity.Ref) {
	t.Helper()
	owns(t, srv, "user:"+tester, nil, places...)
}

// asTester is the store a fixture seeds the world through when the test goes on to act on it
// as the tester: what they wrote is theirs.
func asTester(srv *Server) entity.Store { return entity.As(srv.Store, "user:"+tester) }
