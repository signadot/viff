package io

import _ "embed"

//go:generate go tool tony-codegen -dir .

// Schema is this package's generated tony signatures: the registration an iod is installed as,
// and the rows and reports the iod routes answer with it. Folded into verse's schema
// (schema.go) with every other package's.
//
//go:embed schema_gen.tony
var Schema []byte
