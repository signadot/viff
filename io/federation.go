package io

// Federation naming for verse connect. A session's tree is federated under a nonce
// scope as two symmetric kind-slices of the `local` root: dir-status-<scope> (rich
// metadata, written to docd in COW scope <scope>) and dir-content-<scope> (file bytes
// served on demand by the content controller, nothing stored). Both connect (writer +
// content controller) and versed (the scoped engine's content reader) derive their
// kinds from these, so they stay in step. See docs/content-federation.md.
const (
	// FederationRoot is the root `verse connect` federates a working tree into. It is
	// named for what it is rather than being the source package's `System`, because the
	// registry of external sources is `verse.sources` and wants
	// that name — see registration.go.
	FederationRoot = "local"
	StatusPrefix   = "dir-status-"  // + <scope>
	ContentPrefix  = "dir-content-" // + <scope>
)
