package source

// Federation naming for verse connect. A session's tree is federated under a nonce
// scope as two symmetric kind-slices of the `local` system: dir-status-<scope> (rich
// metadata, written to docd in COW scope <scope>) and dir-content-<scope> (file bytes
// served on demand by the content controller, nothing stored). Both connect (writer +
// content controller) and versed (the scoped engine's content reader) derive their
// kinds from these, so they stay in step. See docs/content-federation.md.
const (
	System        = "local"
	StatusPrefix  = "dir-status-"  // + <scope>
	ContentPrefix = "dir-content-" // + <scope>
)
