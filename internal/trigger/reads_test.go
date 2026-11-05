package trigger

import "testing"

// A READ MAY NAME VERSE'S OWN HALF, where a charter may not be installed: a read writes nothing.
// An empty address is still refused, since it reads the whole verse.
func TestAReadMayNameVersesOwnHalf(t *testing.T) {
	if _, err := ParseReads([]string{"verse.gate.question"}); err != nil {
		t.Errorf("a read of verse's half was refused: %v", err)
	}
	if _, err := ParseAt("verse.gate.question"); err == nil {
		t.Error("a charter could be installed in verse's half")
	}
	if _, err := ParseReads([]string{""}); err == nil {
		t.Error("an empty read was accepted")
	}
}
