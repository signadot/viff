package api

import (
	"errors"
	"net/url"
	"strconv"
	"strings"

	"github.com/signadot/tony-format/go-tony/ir"
	"github.com/signadot/tony-format/go-tony/ir/kpath"

	"github.com/signadot/verse/codec"
	"github.com/signadot/verse/entity"
)

// THE VERSE AS `verse data` READS AND WRITES IT, and as verse_data, verse_data_put and
// verse_data_rm and the verse system's verse-data-get, -put and -rm do: one grammar for all, so a
// task reads and writes as its owner would. An error is a FieldError: which of the words it is
// about, and whether it is a misuse of them or a value that does not read.

// FieldError is a data grammar error about one of the words a caller gave. Field names it as
// verse mcp's tools do (at, if, if_at, trim, value, create, update, expect, grants), for each
// caller to say in its own spelling: the CLI by its flag, a tool by its parameter. Misuse is a
// misuse of the words, an address that is no address or two that contradict each other, rather
// than a value that does not read.
type FieldError struct {
	Field  string
	Misuse bool
	Err    error
}

func (e *FieldError) Error() string { return e.Field + ": " + e.Err.Error() }
func (e *FieldError) Unwrap() error { return e.Err }

// misuse is a FieldError for a misuse of field.
func misuse(field string, err error) error { return &FieldError{Field: field, Misuse: true, Err: err} }

// unread is a FieldError for a value of field that does not read.
func unread(field string, err error) error { return &FieldError{Field: field, Err: err} }

// StateQuery is a read of the verse at the kinded path at, as GET /state takes it: the server's
// selector (pattern, in the format in, rooted at patternAt), and for a listing the paths alone,
// depth levels deep (entity.Unbounded for all). It answers the path parsed too, which is what the
// answer is walked by.
func StateQuery(at, pattern, patternAt string, listing bool, depth int, in codec.Format) (url.Values, *kpath.KPath, error) {
	kp, err := entity.ParseAt(at)
	if err != nil {
		return nil, nil, misuse("at", err)
	}
	q := url.Values{}
	if at != "" {
		q.Set("at", at)
	}
	if err := FilterQuery(q, pattern, patternAt, in); err != nil {
		return nil, nil, err
	}
	if listing {
		// ASKED OF THE SERVER, which asks the store: the addresses come back without the
		// nodes, so a listing of a wide container ships names rather than everything they
		// stand for. A read with a predicate still answers the document and is walked by the
		// reader, since `if` picks per node the path reached.
		q.Set("paths", "1")
		if depth != entity.Unbounded {
			q.Set("depth", strconv.Itoa(depth))
		}
	}
	return q, kp, nil
}

// FilterQuery puts the server's selector on q: pattern, read in the format in, rooted at the
// dot-separated field path at.
//
// The pattern is parsed here and re-rendered as canonical tony. Parsing first is what makes a typo
// a message about the pattern rather than a request that silently matches nothing; re-rendering
// is what lets `-i json` and `-i yaml` say the same predicate, since a GET has no body and so no
// Content-Type to declare a dialect in. Tags survive the round trip — `!glob "hotfix-*"` reaches
// the server as itself — which is the whole reason the canonical form is tony rather than JSON.
func FilterQuery(q url.Values, pattern, at string, in codec.Format) error {
	if at != "" && pattern == "" {
		return misuse("if_at", errors.New("roots the if pattern; pass a pattern to root"))
	}
	if pattern == "" {
		return nil
	}
	node, err := codec.ParseNode([]byte(pattern), in)
	if err != nil {
		return unread("if", err)
	}
	b, err := codec.NodeBytesWire(node, codec.Tony)
	if err != nil {
		return unread("if", err)
	}
	q.Set("if", string(b))
	// Segment per parameter, as a write's field path travels: the wire takes the address as a
	// list, so a field name is never re-split at the far end.
	for _, f := range fieldPath(at) {
		q.Add("if-at", f)
	}
	return nil
}

// fieldPath splits a dot-separated if-at into the segments the wire takes.
//
// It is if-at's alone: an ADDRESS is a kinded path (entity.TargetOf), which quotes a field name
// carrying a dot, while this stays the flat split because if-at says where to root a predicate
// inside one payload rather than naming a place in the verse. The wire takes the segments as a
// list either way, so nothing but this spelling is in the way if a dotted field name ever needs
// rooting.
func fieldPath(at string) []string {
	if at == "" {
		return nil
	}
	return strings.Split(at, ".")
}

// DataWrite is a write as `data --put` takes it, each part as typed: the payload, the conditions
// on it, and what a node it creates holds out.
type DataWrite struct {
	Payload        string
	Create, Update bool
	Expect, Grants string
}

// Body is the request the write sends at segs (POST /entity), the payload and the condition read
// in the format in.
func (p DataWrite) Body(segs []string, in codec.Format) (PutEntity, error) {
	if len(segs) == 0 {
		return PutEntity{}, misuse("at", errors.New("a write wants a path to write at (demo.pr.1, demo.pr.1.labels.urgent)"))
	}
	// FROM THE SEGMENTS, not from a string: argv already is the vector, and assembling it into
	// a path only to parse the path back is the round trip that could not be right for every
	// input. The access is set when a step reaches into an ARRAY, which an address of names
	// cannot spell — see entity.Path.Access.
	at, err := entity.TargetOf(segs)
	if err != nil {
		return PutEntity{}, misuse("at", err)
	}
	// A payload typed on a command line names no format of its own, so it is read as the
	// caller's input format. Valid JSON is also valid tony, so `{"stage":"open"}` reads
	// whichever it is.
	//
	// Parsed to a NODE rather than a map: a payload is any tony value, so `42`, `true` and
	// `[a, b]` are payloads too, and decoding into a map would reject them here even though
	// the store and the wire both accept them.
	payload, err := codec.ParseNode([]byte(p.Payload), in)
	if err != nil {
		return PutEntity{}, unread("value", err)
	}
	when, err := ConditionsOf(p.Create, p.Update, p.Expect, in)
	if err != nil {
		return PutEntity{}, err
	}
	// GRANTS ARE GIVEN AT A CREATION, so a write that says something stands is not one: what a
	// node that stands holds out is changed with `verse perms`.
	var grants entity.Payload
	if p.Grants != "" {
		if p.Update || p.Expect != "" {
			return PutEntity{}, misuse("grants", errors.New("a write with grants creates the node, and update "+
				"and expect are about one that stands; change what a node holds out with verse perms --grants"))
		}
		if grants, err = codec.ParseNode([]byte(p.Grants), codec.Tony); err != nil {
			return PutEntity{}, unread("grants", err)
		}
	}
	// THE WHOLE ADDRESS IS THE PLACE. `verse data verse-dev drift head abc --put '{…}'` writes
	// there, and so does `verse data demo.pr.1.labels.urgent --put true` — a write is a value
	// at a path, and where the entity "ends" is a question a write does not answer. The answer
	// comes back from the address written, so a put still shows the merge wherever the address
	// IS an entity, which is the ordinary case.
	return PutEntity{At: at.Ref.KPath(), Sub: at.Access, Payload: payload, When: when, Grants: grants}, nil
}

// ConditionsOf renders a write's guards as the wire's preconditions, the pattern read in the
// format in. create and update are opposite answers to the same question, so asking both is a
// contradiction rather than a pair of conditions that can never hold — said before anything is
// sent.
func ConditionsOf(create, update bool, expect string, in codec.Format) ([]Condition, error) {
	var out []Condition
	if create && update {
		return nil, misuse("create", errors.New("create and update are opposites; pass one"))
	}
	if create || update {
		exists := update
		out = append(out, Condition{Exists: &exists})
	}
	if expect != "" {
		pattern, err := codec.ParseNode([]byte(expect), in)
		if err != nil {
			return nil, unread("expect", err)
		}
		out = append(out, Condition{Pattern: pattern})
	}
	return out, nil
}

// RemoveQuery is the removal `data --rm` makes at segs, as DELETE /entity takes it, and the place
// it names.
func RemoveQuery(segs []string) (url.Values, entity.Path, error) {
	if len(segs) == 0 {
		return nil, entity.Path{}, misuse("at", errors.New("a removal wants a path (demo.pr.1, demo.pr.1.labels.urgent)"))
	}
	at, err := entity.TargetOf(segs)
	if err != nil {
		return nil, entity.Path{}, misuse("at", err)
	}
	q := url.Values{"at": {at.Ref.KPath()}}
	if at.Access != "" {
		q.Set("sub", at.Access)
	}
	return q, at, nil
}

// RemovedLine is the line a removal says once verse has made it: nothing stands there now, which is
// all there is to say, and is equally true of a path that held nothing to begin with.
//
// NAMED IN STEPS, like every other address `verse data` prints, so the line reads as the one
// somebody typed rather than as the kpath it was sent as. A path that is not a plain address — a
// glob, a reach into an array — is echoed as it stands, since it has no steps.
func RemovedLine(at entity.Path) string {
	segs := at.Ref.Segs()
	if len(segs) == 0 {
		return "removed " + at.String()
	}
	// An access takes no space before it, as it takes no dot in a kpath: `demo pr 1 votes[0]`
	// is four steps, the last of them reaching into an array.
	return "removed " + entity.JoinSteps(segs) + at.Access
}

// ListedPaths is the addresses a listing answers, from either answer GET /state gives.
//
// A LISTING, where the store answered one: the addresses arrive as kinded paths, and an address
// is recovered by splitting each where it stops naming (LiteralPrefix) -- the names are the ref,
// the rest is the access, which is the same cut TargetOf makes on the way in.
//
// A DOCUMENT, where the read had to answer one: a path with a wildcard or a descent, or an `if`
// that picks per node, is answered over the node the read reached, and the walk over it is the
// listing (entity.Doc.Paths). It is the same set of addresses either way.
func ListedPaths(node *ir.Node, at *kpath.KPath, depth int) []entity.Path {
	var listed entity.Paths
	if err := codec.FromNode(node, &listed); err == nil && listed.Paths != nil {
		out := make([]entity.Path, 0, len(listed.Paths))
		for _, kp := range listed.Paths {
			out = append(out, pathOfKPath(kp))
		}
		return out
	}
	return (entity.Doc{Node: node}).Paths(at, depth)
}

// pathOfKPath reads one listed address: the names it spells, and the access below them.
func pathOfKPath(kp string) entity.Path {
	parsed, err := entity.ParseAt(kp)
	if err != nil {
		return entity.Path{Ref: entity.RefAt(kp)} // unreadable: echoed as it stands
	}
	names, rest := entity.LiteralPrefix(parsed)
	access := ""
	if rest != nil {
		access = rest.String()
	}
	return entity.Path{Ref: entity.RefAt(names...), Access: access}
}
