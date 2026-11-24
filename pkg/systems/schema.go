package systems

import _ "embed"

// Schema is this package's generated tony signatures: the shape of a system's document, as
// data, for the viff instance schema to fold in.
//
//go:embed schema_gen.tony
var Schema []byte
