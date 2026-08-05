package entity

import _ "embed"

// Schema is this package's generated tony signatures — the wire shapes it defines, as data.
// See the identical var in api, trigger, task, config and launch: a package owns the
// schema of the types it declares, and schema/ only indexes them.
//
//go:embed schema_gen.tony
var Schema []byte
