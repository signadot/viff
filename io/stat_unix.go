//go:build unix

package io

import (
	"os"
	"syscall"
)

// sysFields returns the unix stat(2) identity/ownership fields for a file entity —
// uid/gid/nlink and ino/dev (identity; ino+dev detects hard links) and rdev (a
// device node's major/minor). Times other than mtime are omitted deliberately:
// atime is perturbed by our own hashing reads. Field types vary across unix
// (e.g. Dev is int32 on darwin, uint64 on linux), so each is widened to int64.
func sysFields(info os.FileInfo) map[string]int64 {
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return nil
	}
	return map[string]int64{
		"uid":   int64(st.Uid),
		"gid":   int64(st.Gid),
		"nlink": int64(st.Nlink),
		"ino":   int64(st.Ino),
		"dev":   int64(st.Dev),
		"rdev":  int64(st.Rdev),
	}
}
