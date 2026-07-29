package main

import (
	"strings"
	"testing"
)

// `verse schema` prints the ROOT — the top-level paths and the shapes served there — because
// that is the question someone typing it has: what is a verse. The flat set of thirty-odd
// signature names is at GET /schema for anything resolving the graph; it is not an answer to
// read.
func TestSchemaPrintsTheRoot(t *testing.T) {
	serve(t)

	out, err := runCLI(t, "schema")
	if err != nil {
		t.Fatalf("verse schema: %v", err)
	}
	if !strings.Contains(out, "name: verse-root") {
		t.Errorf("verse schema did not print the root signature:\n%s", out)
	}
	for _, want := range []string{"entities: .[array(verse-entity)]", "status: .[verse-status]"} {
		if !strings.Contains(out, want) {
			t.Errorf("verse schema is missing %q:\n%s", want, out)
		}
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
