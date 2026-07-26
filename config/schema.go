package config

import _ "embed"

// Schema is this package's generated tony signatures — the wire shapes it defines,
// as data. It is embedded rather than read from disk so a verse binary can describe
// itself with no files beside it, and it lives here rather than in the aggregator
// because a package owns the schema of the types it declares.
//
//go:embed schema_gen.tony
var Schema []byte
