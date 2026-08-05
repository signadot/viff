// Package codec is the one place in verse that knows about serialization formats.
//
// Everything that reads or writes a document — the HTTP wire, the CLI, charter files,
// config — goes through here rather than importing parse/encode/gomap directly, so that
// choice is made in one vocabulary and can be overridden in one place.
//
// THE READ SIDE AND THE WRITE SIDE ARE NOT SYMMETRIC, and that asymmetry is the design:
//
//	read:   tony, JSON (as tony), YAML
//	write:  tony, YAML
//
// JSON is an INPUT DECLARATION, never a rendering. A .json file, an
// `application/json` body and `-i json` all keep working — valid JSON is valid tony
// (pinned by TestUpstreamJSONParsesAsTony), so they are simply parsed by the tony parser
// and mean the same thing they always did. What is gone is asking verse to *emit* JSON:
// `-o json`, `VERSE_FORMAT=json` and `Accept: application/json` are refused, each with a
// sentence saying so.
//
// The reason is tags. verse's own grammar — charters, matches, formulas — is tagged, and
// JSON has no tag syntax. Rendering a tagged document as JSON therefore means rewriting
// it into a tag-keyed object (`!or [a, b]` → `{"or": ["a","b"]}`), which is unambiguous
// only where the surrounding keyspace is closed. Inside a match's `fields` — an open user
// document, where a payload key may legitimately BE "or" — it is not: the JSON rendering
// re-reads as something else. A format that can express three quarters of the model is
// worse than one that is honestly not offered, because the quarter it mangles it mangles
// silently. Users still write JSON; they just get tony back.
//
// Two facts about go-tony shape the rest, both pinned by upstream_test.go:
//
//   - There is NO format auto-detection. parse.Parse defaults to tony and the dialect is
//     always an explicit option. So every entry point here takes a Format, and callers get
//     it from a file suffix, a media type, a flag, or the configured default — never from
//     sniffing the bytes.
//   - Encoding a tagged node as JSON is a hard ERROR upstream, not a silent drop. That is
//     the backstop under encodeNode's own refusal: even if a JSON format reached the
//     encoder some other way, a charter could not quietly lose its meaning on the way out.
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

	// JSON names JSON SYNTAX ON INPUT — a .json suffix, an application/json body, `-i
	// json`. It is not a rendering: readAs parses it with the tony parser, and encoding
	// with it is an error (see encodeNode). Callers resolving a format to WRITE in use
	// ParseOutput, which never yields it.
	JSON = format.JSONFormat
)

// Writable reports whether f is a format verse renders. It is the one predicate behind
// every refusal, so "what can verse emit" has a single answer to change.
func Writable(f Format) bool { return f != JSON }

// ErrNotWritable explains the refusal, in the terms the person hitting it needs: what to
// ask for instead, and the reassurance that their INPUT is unaffected.
const ErrNotWritable = "verse does not render json (tony and yaml are the output formats); " +
	"json input is still accepted everywhere, since valid json is valid tony"

// Baseline is verse's representation when nothing overrides it — no config, no env, no
// flag, no header.
//
// It is tony, and it is now the only rendering a fresh install can produce (YAML being the
// other, if asked). The JSON rendering that this line used to be able to fall back to is
// gone — see the package doc — so reverting the flip means choosing YAML or restoring the
// JSON encoder, not flipping one constant.
var Baseline = Tony

// defaultFormat holds Format+1 so that zero means UNSET — format.TonyFormat is 0, so a
// plain value could not distinguish "nobody called SetDefault" from "somebody chose tony",
// and every test that never touches it would silently get tony.
//
// Atomic because HTTP handlers read it concurrently. Written ONCE, at startup, from
// config+env.
//
// SERVERS ONLY. A CLI command must not rely on it: config is loaded while the command tree
// is being built, but the cli library parses flags inside Run, so a global written before
// the tree exists is stale by the time -o is known. Client code resolves its format per
// invocation and passes it explicitly.
var defaultFormat atomic.Int32

// SetDefault sets the task-wide default format.
func SetDefault(f Format) { defaultFormat.Store(int32(f) + 1) }

// Default reports the task-wide default format, or the Baseline if none was set.
func Default() Format {
	if v := defaultFormat.Load(); v != 0 {
		return Format(v - 1)
	}
	return Baseline
}

// Parse resolves the name of a format to READ — "tony"/"t", "json"/"j", "yaml"/"y". An
// empty name is the configured default, so an unset flag falls through rather than
// erroring.
//
// "json" is accepted here and nowhere on the write side: it declares what the bytes look
// like, which is a fact about them, not a request for a rendering.
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

// ParseOutput resolves the name of a format to WRITE. It is Parse plus the one refusal:
// "json" names a dialect verse reads but does not render.
//
// Every knob that picks an output format goes through here — `-o`, VERSE_FORMAT,
// VERSE_OUTPUT_FORMAT, VERSE_WIRE_FORMAT, `?format=` on a response — so the refusal
// happens where a person can still act on it, rather than deep inside the encoder with a
// half-written body.
func ParseOutput(s string) (Format, error) {
	f, err := Parse(s)
	if err != nil {
		return f, err
	}
	if !Writable(f) {
		return Default(), fmt.Errorf("%s", ErrNotWritable)
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
// comments intact: the json.RawMessage equivalent.
//
// `omitzero`, `omit` and `-` are honored by gomap itself as of go-tony v0.0.89. Until then
// they were silently ignored on the reflection path, and this package carried its own
// ~130-line pruning pass to compensate; that is gone, and upstream's behaviour is pinned by
// TestUpstreamHonorsOmitzero so its removal is noticed rather than discovered.
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
	return parse.Parse(data, parse.ParseFormat(readAs(f)))
}

// ParseNodes parses a multi-document stream (`---` separated) — the idiomatic tony
// spelling of "several charters in one file".
func ParseNodes(data []byte, f Format) ([]*ir.Node, error) {
	return parse.ParseMulti(data, parse.ParseFormat(readAs(f)))
}

// readAs is the parser a declared format is READ with. It is the identity except for
// JSON, which is read by the tony parser — because valid JSON is valid tony (pinned by
// TestUpstreamJSONParsesAsTony), so there is nothing the JSON parser can do here that the
// tony parser cannot.
//
// This is not a shortcut. Going through one parser means one grammar, and a document
// declared JSON gains the two things the JSON dialect withholds:
//
//   - TAGS. `{"id": !glob "hotfix-*"}` is not JSON, but it is what a JSON-shaped charter
//     has to write to reach an operator. We are no longer checking that input IS JSON —
//     only that it is tony, which JSON already is.
//   - LEADING WHITESPACE. `  {"a": 1}` is rejected by go-tony's JSON parser
//     ("imbalanced document: trailing material") and accepted by its tony parser. An
//     indented body from a shell heredoc is not a thing verse should refuse.
//
// YAML keeps its own parser: a YAML document is NOT tony (nor the reverse), so the same
// argument does not run.
func readAs(f Format) Format {
	if f == JSON {
		return Tony
	}
	return f
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

// EncodeNodeColor writes a node to w in f with ANSI colour, the way `o view` renders.
//
// Colour is a property of the DESTINATION, not of the document, so this is a separate
// entry point rather than a flag on the others: only a caller that knows it is writing to
// a terminal should reach for it. Piped output must stay clean — `verse … | jq` and
// `verse … | o v` both break on escape codes.
func EncodeNodeColor(w io.Writer, n *ir.Node, f Format) error {
	if n == nil {
		n = ir.Null()
	}
	if !Writable(f) {
		return fmt.Errorf("%s", ErrNotWritable)
	}
	return encode.Encode(n, w, encode.EncodeFormat(f), encode.EncodeColors(encode.NewColors()))
}

func encodeNode(w io.Writer, n *ir.Node, f Format, wire bool) error {
	if n == nil {
		n = ir.Null()
	}
	// THE LAST GATE. Every knob that names an output format refuses json before it gets
	// here (ParseOutput, Negotiate), so reaching this line means a format arrived by a
	// path nobody audited — a config file decoded straight to format.Format, a Go caller
	// passing the constant. Refusing here rather than encoding means such a path cannot
	// quietly produce a document whose tags have been flattened into ordinary keys.
	if !Writable(f) {
		return fmt.Errorf("%s", ErrNotWritable)
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
