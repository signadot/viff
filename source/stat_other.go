//go:build !unix

package source

import "os"

// sysFields contributes no unix stat fields on non-unix platforms; the portable
// metadata (path/type/mode/size/mtime/hash) still applies.
func sysFields(os.FileInfo) map[string]int64 { return nil }
