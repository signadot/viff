package main

import (
	"strings"
	"testing"
)

// `verse schema` prints the WHOLE schema: the root, and then the shapes its values name. It
// printed only the root for a while — eight references and none of the things referenced —
// which made the command that exists to show you the schema show you an index of it.
func TestSchemaPrintsTheWholeSchema(t *testing.T) {
	serve(t)

	out, err := runCLI(t, "schema")
	if err != nil {
		t.Fatalf("verse schema: %v", err)
	}
	// The root, first, since it is what the rest is read from.
	for _, want := range []string{"root:", "entities: .[array(verse-entity)]", "status: .[verse-status]"} {
		if !strings.Contains(out, want) {
			t.Errorf("verse schema is missing %q:\n%s", want, out)
		}
	}
	// And the shapes those references name, rather than the names alone.
	if !strings.Contains(out, "signatures:") || !strings.Contains(out, "name: verse-instance") {
		t.Errorf("verse schema printed the root without the signatures it names:\n%s", out)
	}
}

// The root is all references, so they have to be followable — otherwise it names shapes an
// operator has no way to look up, and printing it is a dead end.
func TestSchemaPrintsOneSignatureByName(t *testing.T) {
	serve(t)

	out, err := runCLI(t, "schema", "verse-entity")
	if err != nil {
		t.Fatalf("verse schema verse-entity: %v", err)
	}
	if !strings.Contains(out, "name: verse-entity") {
		t.Errorf("verse schema verse-entity printed something else:\n%s", out)
	}
}

// A name that does not resolve is an error, not an empty document: a signature that prints
// nothing reads as a type with no shape rather than as a typo.
func TestSchemaRejectsAnUnknownName(t *testing.T) {
	serve(t)

	if _, err := runCLI(t, "schema", "verse-nonesuch"); err == nil {
		t.Error("verse schema verse-nonesuch succeeded")
	}
}

// Two names is a mistake worth naming, since the plausible reading — print both — is not what
// it does, and silently printing the first would look like it had.
func TestSchemaTakesOneNameAtMost(t *testing.T) {
	serve(t)

	if _, err := runCLI(t, "schema", "verse-entity", "verse-event"); err == nil {
		t.Error("verse schema with two names succeeded")
	}
}
