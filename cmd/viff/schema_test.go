package main

import (
	"strings"
	"testing"
)

// `viff schema` prints the WHOLE schema: the root, and then the shapes its values name. It
// printed only the root for a while — eight references and none of the things referenced —
// which made the command that exists to show you the schema show you an index of it.
func TestSchemaPrintsTheWholeSchema(t *testing.T) {
	serve(t)

	out, err := runCLI(t, "schema")
	if err != nil {
		t.Fatalf("viff schema: %v", err)
	}
	// The root, first inside the define, since it is what the rest is read from.
	for _, want := range []string{"define:", "root:", "state: .[viff-state]", "status: .[viff-status]"} {
		if !strings.Contains(out, want) {
			t.Errorf("viff schema is missing %q:\n%s", want, out)
		}
	}
	// And the shapes those references name, rather than the names alone.
	if !strings.Contains(out, "viff-instance:") || !strings.Contains(out, "name: viff\n") {
		t.Errorf("viff schema printed the root without the definitions it names:\n%s", out)
	}
}

// The root is all references, so they have to be followable — otherwise it names shapes an
// operator has no way to look up, and printing it is a dead end.
func TestSchemaPrintsOneDefinitionByName(t *testing.T) {
	serve(t)

	out, err := runCLI(t, "schema", "viff-entity")
	if err != nil {
		t.Fatalf("viff schema viff-entity: %v", err)
	}
	if !strings.HasPrefix(strings.TrimSpace(out), "viff-entity:") || !strings.Contains(out, "at: .[string]") {
		t.Errorf("viff schema viff-entity printed something else:\n%s", out)
	}
}

// A name that does not resolve is an error, not an empty document: a signature that prints
// nothing reads as a type with no shape rather than as a typo.
func TestSchemaRejectsAnUnknownName(t *testing.T) {
	serve(t)

	if _, err := runCLI(t, "schema", "viff-nonesuch"); err == nil {
		t.Error("viff schema viff-nonesuch succeeded")
	}
}

// Two names is a mistake worth naming, since the plausible reading — print both — is not what
// it does, and silently printing the first would look like it had.
func TestSchemaTakesOneNameAtMost(t *testing.T) {
	serve(t)

	if _, err := runCLI(t, "schema", "viff-entity", "viff-event"); err == nil {
		t.Error("viff schema with two names succeeded")
	}
}
