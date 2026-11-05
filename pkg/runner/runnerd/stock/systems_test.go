package stock

import (
	"testing"

	"github.com/signadot/verse/pkg/systems"
)

// THE RUNNER'S CODE HONOURS EVERY VERB THE DOCUMENTS DESCRIBE: each tool and each step it has
// claimed its name from the documents, and the check a runner makes at start finds nothing
// documented that nothing here implements.
func TestTheRunnerImplementsTheDocumentedSystems(t *testing.T) {
	if err := systems.Check(); err != nil {
		t.Fatal(err)
	}
}
