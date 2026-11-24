package entity

import (
	"errors"
	"fmt"

	"github.com/signadot/tony-format/go-tony/ir"
)

// SCRATCH is the store's second document: beside the log, with no history, no watches, no
// scopes and no preconditions, reads and writes only, flushed to disk behind the write (logd's
// `scratch` operation, tony-format r482sz5r). It is for a fact that is only ever about now — when
// an out half last passed, what the pass did — which in the document would be a commit per
// pass into a history nothing reads, replayed and snapshotted for as long as the cutoff keeps
// it. Nothing can watch scratch, and viff is designed so that nothing ever waits on it: what a
// consumer must be told about goes in the document, and scratch holds what is read when
// something else says to read it.
//
// A scratch address is spelled as the record's it belongs to, so the report of
// `mirror.iods.github` stands at `mirror.iods.github.out` in scratch as its config does in the
// document; the two namespaces are separate in logd, and the one spelling names the one thing
// in both. Permission is checked at that address by the attributed view, as a write or a read
// there would be, since docd knows no viff principals. Scratch has no scopes of its own, so a
// scoped store's scratch stands under a root of the scope's (scratchPath): a scoped record's
// report beside baseline's, never on it, and gone with the scope.
type Scratch interface {
	// ScratchWrite merges v into the scratch document at ref, as a patch merges into the
	// document; a nil v removes what stands there. Nothing is woken by it.
	ScratchWrite(ref Ref, v Payload) error
	// ScratchRead answers what stands at ref in scratch now, nil for nothing.
	ScratchRead(ref Ref) (*ir.Node, error)
}

// ErrNoScratch says the store a scratch operation reached has no scratch face: a view that
// wraps one in a way this package does not unwrap, or a store that is not logd's.
var ErrNoScratch = errors.New("the store has no scratch")

// ScratchOf is the scratch face of a store where it has one: the logd store's, and an attributed
// view's over it.
func ScratchOf(s Store) (Scratch, bool) {
	sc, ok := s.(Scratch)
	return sc, ok
}

// scratchPath is where ref stands in scratch: for the baseline store, under its root as a
// document write renders it (refPath); for a scoped store, under a root of the scope's own
// (scopeScratchRoot), since scratch is one document with no scopes and a scoped record's report
// would otherwise stand where baseline's does — a scoped pass overwriting baseline's counts, a
// scoped install clearing them, and the scope's own outliving it.
func (s *LogdStore) scratchPath(ref Ref) string {
	if s.cfg.Scope == "" {
		return s.refPath(ref)
	}
	path := scopeScratchRoot(s.root, s.cfg.Scope)
	for _, sg := range ref.Segs() {
		path += "." + seg(sg)
	}
	return path
}

// scopeScratchRoot is the one segment a scope's scratch stands under: `viff@sandbox` for the
// `sandbox` scope of a store rooted at `viff`. No entity renders there, since every entity
// renders under the root itself, and the scope's scratch goes as this one subtree when the
// scope does (DeleteScope).
func scopeScratchRoot(root, scope string) string { return seg(root + "@" + scope) }

// ScratchWrite is Scratch.ScratchWrite on the logd session, at the ref rendered under this
// store's root exactly as a document write to it would be, and under its scope's root for a
// scoped store (scratchPath).
func (s *LogdStore) ScratchWrite(ref Ref, v Payload) error {
	if err := CheckPatchable(v); err != nil {
		return fmt.Errorf("scratch write %s: %w", ref, err)
	}
	node := payloadToNode(v)
	if v == nil {
		node = deleteOp()
	}
	ctx, cancel := s.writeCtx()
	defer cancel()
	if _, err := s.sess.ScratchWrite(ctx, s.scratchPath(ref), node); err != nil {
		return fmt.Errorf("scratch write %s: %w", ref, err)
	}
	return nil
}

// ScratchRead is Scratch.ScratchRead on the logd session, under a read's budget.
func (s *LogdStore) ScratchRead(ref Ref) (*ir.Node, error) {
	ctx, cancel := s.reqCtx()
	defer cancel()
	n, err := s.sess.ScratchRead(ctx, s.scratchPath(ref))
	if err != nil {
		return nil, fmt.Errorf("scratch read %s: %w", ref, err)
	}
	return n, nil
}

// ScratchWrite checks the principal may write at ref, as a document write there is checked,
// and writes the underlying store's scratch. The system's views write unchecked, as they do
// the document.
func (a *attributed) ScratchWrite(ref Ref, v Payload) error {
	sc, ok := ScratchOf(a.Store)
	if !ok {
		return fmt.Errorf("scratch write %s: %w", ref, ErrNoScratch)
	}
	if !IsSystem(a.principal) {
		if _, refused := a.check(a.principal, ReadWrite, ref, "writing its scratch"); refused != nil {
			return refused
		}
	}
	return sc.ScratchWrite(ref, v)
}

// ScratchRead checks the principal may read at ref, and reads the underlying store's scratch.
func (a *attributed) ScratchRead(ref Ref) (*ir.Node, error) {
	sc, ok := ScratchOf(a.Store)
	if !ok {
		return nil, fmt.Errorf("scratch read %s: %w", ref, ErrNoScratch)
	}
	if !IsSystem(a.principal) {
		if _, refused := a.check(a.principal, Read, ref, "reading its scratch"); refused != nil {
			return nil, refused
		}
	}
	return sc.ScratchRead(ref)
}
