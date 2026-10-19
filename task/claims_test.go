package task

import (
	"testing"

	"github.com/signadot/verse/systems"
)

// THE CODE CLAIMS WHAT THE DOCUMENTS DESCRIBE, FIELD FOR FIELD. The Step names above claim every
// verb with the fields the code fills, and systems.Check holds them to the documents: a param
// added to a document that nothing fills, or a field the code fills that no document names, is
// named here rather than by a row that never matches.
func TestTheCodeClaimsWhatTheDocumentsDescribe(t *testing.T) {
	if err := systems.Check(); err != nil {
		t.Fatal(err)
	}
}
