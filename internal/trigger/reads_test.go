package trigger

import "testing"

// A READ MAY NAME VIFF'S OWN HALF, where a charter may not be installed: a read writes nothing.
// An empty address is still refused, since it reads the whole viff.
func TestAReadMayNameViffsOwnHalf(t *testing.T) {
	if _, err := ParseReads([]string{"mirror.gate.question"}); err != nil {
		t.Errorf("a read of viff's half was refused: %v", err)
	}
	if _, err := ParseAt("mirror.gate.question"); err == nil {
		t.Error("a charter could be installed in viff's half")
	}
	if _, err := ParseReads([]string{""}); err == nil {
		t.Error("an empty read was accepted")
	}
}
