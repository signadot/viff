package codec

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/signadot/tony-format/go-tony/ir"
)

// suffixes maps a file extension to the dialect it names. ".yml" is here by hand:
// format.Suffix() only ever emits ".yaml", so the round trip through go-tony would not
// recognize a file people routinely write.
var suffixes = map[string]Format{
	".tony": Tony,
	".json": JSON,
	".yaml": YAML,
	".yml":  YAML,
}

// ForPath picks the format a file's name declares, falling back to the configured
// default for an unknown or absent suffix — so stdin and extensionless files follow
// config rather than being guessed at.
func ForPath(path string) Format {
	if f, ok := ForPathStrict(path); ok {
		return f
	}
	return Default()
}

// ForPathStrict reports the format a file's name declares, and whether it declared one
// at all. Callers that must not silently fall back — a config layer, where two files
// with different suffixes is a mistake worth naming — use this.
func ForPathStrict(path string) (Format, bool) {
	f, ok := suffixes[strings.ToLower(filepath.Ext(path))]
	if !ok {
		return Default(), false
	}
	return f, true
}

// Suffixes lists the recognized file extensions, in the order a probe should try them:
// tony first, since it is the native spelling.
func Suffixes() []string { return []string{".tony", ".json", ".yaml", ".yml"} }

// ReadFile reads and decodes a file, taking its format from its name.
func ReadFile(path string, v any) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	if err := Unmarshal(data, v, ForPath(path)); err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}
	return nil
}

// ReadFileNode reads a file as a document, returning the node and the format it was read
// as (so a caller can report or echo it).
func ReadFileNode(path string) (*ir.Node, Format, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, Default(), err
	}
	f := ForPath(path)
	n, err := ParseNode(data, f)
	if err != nil {
		return nil, f, fmt.Errorf("%s: %w", path, err)
	}
	return n, f, nil
}

// ReadSource reads a document from a file or, when path is "" or "-", from r. It returns
// the bytes and the format they should be read as: a named file's suffix decides, and
// anything else uses hint (a caller's explicit --ifmt, or the configured default).
//
// Keeping the two together is the point — every command that accepts "a file or stdin"
// needs both answers, and deciding them apart is how a suffix ends up ignored.
func ReadSource(r io.Reader, path string, hint Format) ([]byte, Format, error) {
	if path == "" || path == "-" {
		data, err := io.ReadAll(r)
		return data, hint, err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, hint, err
	}
	if f, ok := ForPathStrict(path); ok {
		return data, f, nil
	}
	return data, hint, nil
}
