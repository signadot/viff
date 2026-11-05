package schema

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// The curated set must equal the set of types declaring `codec=custom`, in BOTH directions.
//
// This exists because of a specific failure the precedence rule cannot prevent by itself.
// "A curated signature always wins" is right when a curated entry is there because generation
// is wrong — but nothing enforces that it still is. Abstracted: with `cur` (a curated entry
// exists), `genOK` (generation is now correct) and `curOK` (the curated entry is still
// correct), the rule assumes cur ⇒ curOK ∨ ¬genOK, and the negation cur ∧ genOK ∧ ¬curOK is
// satisfiable — codegen improves, the generated signature becomes right, and a stale curated
// entry goes on shadowing it. That is not hypothetical: go-tony fixed three separate defects
// in one week, and verse-entity and verse-event were retired BY HAND both times.
//
// `codec=custom` is the declaration that a type's wire shape is not derivable from its Go
// shape, which is exactly the condition under which a curated signature is warranted. So the
// two sets should coincide, and either direction of drift is a real problem:
//
//   - curated but no longer custom → codegen can derive it; the curated entry is now the
//     stale one, and the SAT model above has been realized.
//   - custom but not curated → its generated signature is unchecked, and for a struct that
//     means a confident description of fields the codec never emits.
func TestCuratedSetEqualsCustomCodecTypes(t *testing.T) {
	custom, err := customCodecSignatures("../..")
	if err != nil {
		t.Fatalf("scan sources: %v", err)
	}
	if len(custom) == 0 {
		t.Fatal("found no codec=custom declarations — the scan is broken, not the invariant")
	}
	cur, err := Curated()
	if err != nil {
		t.Fatalf("curated: %v", err)
	}
	curSet := map[string]bool{}
	for _, n := range cur {
		curSet[n] = true
	}
	for _, n := range custom {
		if !curSet[n] {
			t.Errorf("%s declares codec=custom but has no curated signature — codegen is "+
				"describing its Go shape, which codec=custom says is not its wire shape", n)
		}
		delete(curSet, n)
	}
	for n := range curSet {
		t.Errorf("%s is curated but no longer declares codec=custom — if codegen can derive "+
			"it now, delete the curated entry so the generated one is served", n)
	}
}

// The upgrade that retired two curated entries: entity.Entity and Event are GENERATED now,
// because go-tony v0.0.93 consults TextMarshaler when typing a struct field, so `ref` is
// described as the string its generated code has always emitted rather than as an int.
//
// TestCuratedSetEqualsCustomCodecTypes would catch a curated entry reappearing here only if
// nobody also declared codec=custom on the type. This names the two by hand because they are
// the cases where that already happened once.
func TestEntitySignaturesAreGeneratedNotCurated(t *testing.T) {
	cur, err := Curated()
	if err != nil {
		t.Fatalf("curated: %v", err)
	}
	for _, n := range cur {
		if n == "verse-entity" || n == "verse-event" {
			t.Errorf("%s is curated again — if codegen regressed, say so here rather than "+
				"letting a hand-written signature quietly stand in for a generated one", n)
		}
	}
	for _, n := range []string{"verse-entity", "verse-event"} {
		if _, ok, _ := Get(n); !ok {
			t.Errorf("%s is not published at all", n)
		}
	}
}

var customCodecRe = regexp.MustCompile(`//tony:schemagen=([a-z0-9-]+)[^\n]*\bcodec=custom\b`)

// customCodecSignatures returns the signature names declared with codec=custom under root.
func customCodecSignatures(root string) ([]string, error) {
	var out []string
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			// Worktrees hold whole copies of this repo; scanning them would count every
			// declaration several times over and report drift that does not exist here.
			if name := d.Name(); name == ".worktrees" || name == ".claude" || name == ".git" || name == "docs" {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		src, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		for _, m := range customCodecRe.FindAllStringSubmatch(string(src), -1) {
			out = append(out, m[1])
		}
		return nil
	})
	sort.Strings(out)
	return out, err
}
