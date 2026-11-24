package resolver

import _ "embed"

//go:generate go tool tony-codegen -dir .

// Schema is this package's generated tony signatures: a resolver as installed, and the rows the
// resolver routes answer with it. Folded into viff's schema (schema.go) with every other
// package's.
//
//go:embed schema_gen.tony
var Schema []byte
