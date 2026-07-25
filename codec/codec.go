// Package codec is the one place in verse that knows about serialization formats.
//
// verse speaks three dialects of one data model: tony, JSON and YAML are all parsed to
// and encoded from go-tony's ir.Node, so a document's *format* is a rendering choice, not
// a change of meaning. Everything that reads or writes a document — the HTTP wire, the
// CLI, charter files, config — goes through here rather than importing parse/encode/
// gomap directly, so that choice is made in one vocabulary and can be overridden in one
// place.
//
// Two things about go-tony shape this package, both pinned by upstream_test.go:
//
//   - There is NO format auto-detection. parse.Parse defaults to tony and the dialect is
//     always an explicit option. So every entry point here takes a Format, and callers get
//     it from a file suffix, a media type, a flag, or the configured default — never from
//     sniffing the bytes.
//   - JSON cannot carry tags AT ALL, and go-tony does not silently drop them: encoding a
//     tagged node as JSON is a hard error. Detag (detag.go) bridges that, once, over a
//     whole document.
package codec

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"sync/atomic"

	"github.com/signadot/tony-format/go-tony/encode"
	"github.com/signadot/tony-format/go-tony/format"
	"github.com/signadot/tony-format/go-tony/gomap"
	"github.com/signadot/tony-format/go-tony/ir"
	"github.com/signadot/tony-format/go-tony/parse"
)

// Format is go-tony's format, re-exported so a caller needs one import to name one thing.
type Format = format.Format

const (
	Tony = format.TonyFormat
	YAML = format.YAMLFormat
	JSON = format.JSONFormat
)

// defaultFormat is the format used when nothing more specific applies. Its zero value is
// Tony (format.TonyFormat is 0), so a process that never calls SetDefault is tony — which
// is the destination, not the starting point: until the flip, servers set it explicitly.
//
// Atomic because HTTP handlers read it concurrently. It is intended to be written ONCE,
// at startup, from config+env.
//
// SERVERS ONLY. A CLI command must not rely on it: config is loaded while the command
// tree is being built, but the cli library parses flags inside Run, so a global written
// before the tree exists is stale by the time -o is known. Client code resolves its
// format per invocation and passes it explicitly.
var defaultFormat atomic.Int32

// SetDefault sets the process-wide default format.
func SetDefault(f Format) { defaultFormat.Store(int32(f)) }

// Default reports the process-wide default format.
func Default() Format { return Format(defaultFormat.Load()) }

// Parse resolves a format name — "tony"/"t", "json"/"j", "yaml"/"y". An empty name is the
// configured default, so an unset flag falls through rather than erroring.
func Parse(s string) (Format, error) {
	if s == "" {
		return Default(), nil
	}
	f, err := format.ParseFormat(s)
	if err != nil {
		return Default(), fmt.Errorf("unknown format %q (want tony, json or yaml)", s)
	}
	return f, nil
}

// ── values ↔ bytes ───────────────────────────────────────────────────────────

// Marshal renders v in f, in the human-readable form (go-tony fixes the indent at 2).
func Marshal(v any, f Format) ([]byte, error) {
	node, err := ToNode(v)
	if err != nil {
		return nil, err
	}
	return NodeBytes(node, f)
}

// MarshalWire renders v in f, compact and on a single line. This is what a framed
// protocol wants: it is the encoding the docd/logd session wire already uses, and it is
// what makes an SSE `data:` frame one line.
func MarshalWire(v any, f Format) ([]byte, error) {
	node, err := ToNode(v)
	if err != nil {
		return nil, err
	}
	return NodeBytesWire(node, f)
}

// Unmarshal decodes data, read as f, into v.
func Unmarshal(data []byte, v any, f Format) error {
	node, err := ParseNode(data, f)
	if err != nil {
		return err
	}
	return FromNode(node, v)
}

// Encode writes v to w in f.
func Encode(w io.Writer, v any, f Format) error {
	node, err := ToNode(v)
	if err != nil {
		return err
	}
	return EncodeNode(w, node, f)
}

// Decode reads a whole document from r and decodes it into v.
func Decode(r io.Reader, v any, f Format) error {
	data, err := io.ReadAll(r)
	if err != nil {
		return err
	}
	return Unmarshal(data, v, f)
}

// ── values ↔ IR ──────────────────────────────────────────────────────────────

// ToNode converts a Go value to IR. An *ir.Node field passes through untouched, tags and
// comments intact — the json.RawMessage equivalent.
func ToNode(v any) (*ir.Node, error) { return gomap.ToTonyIR(v) }

// FromNode decodes IR into a Go value.
func FromNode(n *ir.Node, v any) error { return gomap.FromTonyIR(n, v) }

// ── IR ↔ bytes ───────────────────────────────────────────────────────────────

// ParseNode parses one document. Blank input yields a null node, absorbing two upstream
// edges a caller would otherwise have to know about: byte-empty input parses to a bare
// (nil, nil) — a nil node AND a nil error, which reads as success — while WHITESPACE-only
// input is a parse error, because indentation is significant in a YAML-family format and
// there is no document for it to be significant to. Neither is useful to a caller holding
// an empty HTTP body.
func ParseNode(data []byte, f Format) (*ir.Node, error) {
	n, err := ParseNodeOrNil(data, f)
	if err != nil {
		return nil, err
	}
	if n == nil {
		return ir.Null(), nil
	}
	return n, nil
}

// ParseNodeOrNil reports blank input as (nil, nil) — no document — for the callers that
// distinguish that from a document that says null. config's layer merge is one: a missing
// or empty file is an absent layer, not a layer asserting null over the one beneath it.
func ParseNodeOrNil(data []byte, f Format) (*ir.Node, error) {
	if len(bytes.TrimSpace(data)) == 0 {
		return nil, nil
	}
	return parse.Parse(data, parse.ParseFormat(f))
}

// ParseNodes parses a multi-document stream (`---` separated) — the idiomatic tony
// spelling of "several charters in one file".
func ParseNodes(data []byte, f Format) ([]*ir.Node, error) {
	return parse.ParseMulti(data, parse.ParseFormat(f))
}

// NodeBytes renders a node in f, human-readable.
func NodeBytes(n *ir.Node, f Format) ([]byte, error) {
	var buf bytes.Buffer
	if err := EncodeNode(&buf, n, f); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// NodeBytesWire renders a node in f, compact and single-line.
func NodeBytesWire(n *ir.Node, f Format) ([]byte, error) {
	var buf bytes.Buffer
	if err := EncodeNodeWire(&buf, n, f); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// EncodeNode writes a node to w in f.
func EncodeNode(w io.Writer, n *ir.Node, f Format) error { return encodeNode(w, n, f, false) }

// EncodeNodeWire writes a node to w in f, compact and single-line.
func EncodeNodeWire(w io.Writer, n *ir.Node, f Format) error { return encodeNode(w, n, f, true) }

func encodeNode(w io.Writer, n *ir.Node, f Format, wire bool) error {
	if n == nil {
		n = ir.Null()
	}
	// JSON has no tag syntax and go-tony refuses to guess: it returns an error rather
	// than dropping one. Rewriting to the tag-keyed form here is what lets a tagged
	// document — a charter — still be rendered as JSON.
	if f == JSON {
		n = Detag(n)
	}
	opts := []encode.EncodeOption{encode.EncodeFormat(f)}
	if wire {
		opts = append(opts, encode.EncodeWire(true))
	}
	return encode.Encode(n, w, opts...)
}

// Recompact re-renders one document from in to out, compact and single-line. Input it
// cannot parse is returned unchanged — this reads SSE frames off a live stream, where
// passing a surprising line through is better than swallowing it.
func Recompact(s string, in, out Format) string {
	n, err := ParseNodeOrNil([]byte(s), in)
	if err != nil || n == nil {
		return s
	}
	b, err := NodeBytesWire(n, out)
	if err != nil {
		return s
	}
	return string(b)
}

// ── helpers ──────────────────────────────────────────────────────────────────

// Unwrap returns the value a node carries, stepping past comment wrappers.
//
// A commented value in the IR is a CommentType node whose Values[0] is the real value,
// not a value with a comment attached. Anything that inspects a node's Type or Tag must
// unwrap first or it will mis-read every commented value — and a charter file is exactly
// where a human writes comments.
func Unwrap(n *ir.Node) *ir.Node {
	for n != nil && n.Type == ir.CommentType && len(n.Values) > 0 {
		n = n.Values[0]
	}
	return n
}

// AsInt64 reports v as an integer, whatever numeric Go type the decoder chose for it.
//
// Reading a number out of a decoded `any` cannot assume a Go type: encoding/json makes
// every number a float64, tony makes an integer an int64 and a fraction a float64, and a
// Go literal defaults to int. A bare v.(float64) is therefore wrong for two of the three.
//
// A non-integral float is not an integer and is reported as such, rather than silently
// truncating — a threshold of 2.5 is a malformed threshold, not 2.
func AsInt64(v any) (int64, bool) {
	switch n := v.(type) {
	case int:
		return int64(n), true
	case int8:
		return int64(n), true
	case int16:
		return int64(n), true
	case int32:
		return int64(n), true
	case int64:
		return n, true
	case uint:
		return uintToInt64(uint64(n))
	case uint8:
		return int64(n), true
	case uint16:
		return int64(n), true
	case uint32:
		return int64(n), true
	case uint64:
		return uintToInt64(n)
	case float32:
		return floatToInt64(float64(n))
	case float64:
		return floatToInt64(n)
	case json.Number:
		i, err := n.Int64()
		return i, err == nil
	}
	return 0, false
}

func uintToInt64(n uint64) (int64, bool) {
	if n > math.MaxInt64 {
		return 0, false
	}
	return int64(n), true
}

func floatToInt64(f float64) (int64, bool) {
	if math.IsNaN(f) || math.IsInf(f, 0) || f != math.Trunc(f) ||
		f < math.MinInt64 || f > math.MaxInt64 {
		return 0, false
	}
	return int64(f), true
}
