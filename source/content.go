package source

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"

	"github.com/signadot/tony-format/go-tony/ir"
	"github.com/signadot/tony-format/go-tony/system/libctl"
	logdapi "github.com/signadot/tony-format/go-tony/system/logd/api"
	"github.com/signadot/tony-format/go-tony/token"
)

// Content is a read-only libctl.Handler that serves file *bytes* on demand from the
// tree under root — the remote-access read path for federated files. connect mounts
// it at local:dir-content-<scope> (a nonce, so the baseline mount is collision-free
// across sessions). No content is ever written to the store: a client Matches a
// file's content path and the controller reads it live. This is the symmetric
// counterpart to the local:dir-status-<scope> metadata (see docs/content-federation.md).
type Content struct {
	root  string
	mount string // op-path prefix, e.g. "versemnt.local.dir-content-<scope>" (entity.MountKPath)
	max   int64  // cap a single content read
}

// maxContentBytes caps a single served file so one Match can't stream an unbounded
// file into a node; over it, the node carries size but no content (truncated). Set
// generous — it should hardly ever bite a real source tree — while still guarding
// against reading a giant binary into memory.
const maxContentBytes = 64 << 20

// NewContent serves the tree under root at the given mount kpath.
func NewContent(root, mount string) *Content {
	return &Content{root: root, mount: mount, max: maxContentBytes}
}

// Match returns {path, size, content} for the regular file at the requested content
// path, or {path, size, truncated} when the file is over the cap, or null for the
// subtree root, a missing/non-regular file, or a path that escapes the tree.
//
// THE PARAMS ARE DISCARDED, INCLUDING THE SCOPE, and that is safe HERE for a reason the
// next controller in this tree will not inherit. docd multiplexes many client sessions
// onto one controller connection, so a client's COW scope cannot ride the connection the
// way it rides a client's `hello`: docd sets `scope` on each ROUTED REQUEST instead, and a
// scope-aware controller is expected to honour that field. This one has no scope-dependent
// state to honour it with — connect mounts one Content per scope, at
// local:dir-content-<scope>, so the scope is already in the mount path and two scopes are
// two controllers over two trees. A controller serving one mount for several scopes would
// have to read MatchParams.Scope, and reading nothing would quietly serve the wrong tenant.
//
// (docd also rewrites the request id to its own on the way out, for the same multiplexing
// reason; libctl's runtime answers with the id it was given, so nothing here sees it.
// Both are documented at tony-format docs/logd/session.md#the-mount-protocol.)
func (c *Content) Match(_ context.Context, path string, _ *ir.Node, _ libctl.MatchParams) (*ir.Node, error) {
	rest, ok := strings.CutPrefix(path, c.mount+".")
	if !ok {
		return ir.Null(), nil // the mount root itself, or an unrelated path
	}
	rel := unkseg(rest)
	full, ok := confine(c.root, rel)
	if !ok {
		return ir.Null(), nil // path traversal / escape
	}
	info, err := os.Stat(full)
	if err != nil || !info.Mode().IsRegular() {
		return ir.Null(), nil
	}
	m := map[string]*ir.Node{
		"path": ir.FromString(rel),
		"size": ir.FromInt(info.Size()),
	}
	if info.Size() > c.max {
		m["truncated"] = ir.FromString("size-cap")
		return ir.FromMap(m), nil
	}
	b, err := os.ReadFile(full)
	if err != nil {
		return ir.Null(), nil
	}
	if !utf8.Valid(b) {
		// Non-UTF-8 (binary) content cannot go on the tony wire as a string — doing so
		// makes docd's tokenizer fail the read with "bad utf8", which resets this
		// controller's session and TOMBSTONES the whole content mount, so every
		// subsequent read_file in the session fails "file not found". Serve a marker
		// with the size instead (as with the over-cap `truncated` path); a validator
		// sees a binary file it cannot read as text, rather than poisoning the mount.
		m["binary"] = ir.FromString("non-utf8")
		return ir.FromMap(m), nil
	}
	m["content"] = ir.FromString(string(b))
	return ir.FromMap(m), nil
}

// Patch is declined: content is read-only and pulled on demand.
func (c *Content) Patch(context.Context, string, *ir.Node, libctl.PatchParams) (*logdapi.PatchResult, error) {
	return nil, libctl.ErrUnsupported
}

// Watch is a composition-safe no-op: content never changes through the store (it is
// pulled on demand), so this mount emits no events — but it must NOT decline the watch
// (ErrUnsupported), because any watch whose range overlaps this mount (e.g. an ancestor
// re-composing after a membership change) would then fail to (re)establish and die.
// Instead we "watch" by blocking until the caller cancels, emitting nothing.
func (c *Content) Watch(ctx context.Context, _ string, _ libctl.WatchParams, _ func(*logdapi.WatchEvent) error) error {
	<-ctx.Done()
	return ctx.Err()
}

// unkseg is the inverse of kseg: a file's relpath is one (possibly quoted) kpath
// segment, so unquote it back to the raw relpath.
func unkseg(s string) string {
	if len(s) >= 2 && (s[0] == '"' || s[0] == '\'') && s[len(s)-1] == s[0] {
		return token.QuotedToString([]byte(s))
	}
	return s
}

// confine resolves rel under root and rejects anything that escapes the tree.
func confine(root, rel string) (string, bool) {
	full := filepath.Join(root, filepath.FromSlash(rel))
	r, err := filepath.Rel(root, full)
	if err != nil || r == ".." || strings.HasPrefix(r, ".."+string(filepath.Separator)) {
		return "", false
	}
	return full, true
}
