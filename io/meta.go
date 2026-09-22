package io

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	stdio "io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// WalkStatus returns relpath → status metadata for every non-directory entry under
// root (files, symlinks, devices, fifos, sockets), skipping dotfiles and .git. This
// is what `verse connect` writes into its scope as local:dir-status-<scope>:<relpath>
// entities — the index (fstat + content hash), never the content itself. WalkDir does
// not follow symlinks, so metadata is the lstat.
func WalkStatus(root string) map[string]map[string]any {
	out := map[string]map[string]any{}
	_ = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		name := d.Name()
		if d.IsDir() {
			if p != root && (name == ".git" || strings.HasPrefix(name, ".")) {
				return fs.SkipDir
			}
			return nil
		}
		if strings.HasPrefix(name, ".") {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return nil
		}
		rel, err := filepath.Rel(root, p)
		if err != nil {
			return nil
		}
		rel = filepath.ToSlash(rel)
		out[rel] = fileMeta(rel, p, info)
		return nil
	})
	return out
}

// fileMeta is the rich status entity for one entry: full lstat metadata (no atime —
// hashing reads would perturb it), the entry type, a symlink's target, and a content
// hash for a regular file (a fingerprint, not the content — index, not warehouse).
// uid/gid/nlink/ino/dev/rdev come from sysFields (empty on non-unix).
func fileMeta(rel, full string, info os.FileInfo) map[string]any {
	m := map[string]any{
		"path":  rel,
		"type":  fileType(info.Mode()),
		"mode":  fmt.Sprintf("%04o", info.Mode().Perm()),
		"size":  info.Size(),
		"mtime": info.ModTime().Unix(),
	}
	for k, v := range sysFields(info) {
		m[k] = v
	}
	switch {
	case info.Mode()&fs.ModeSymlink != 0:
		if tgt, err := os.Readlink(full); err == nil {
			m["target"] = tgt
		}
	case info.Mode().IsRegular():
		if h := hashFile(full, info.Size()); h != "" {
			m["hash"] = h
		}
	}
	return m
}

// fileType names the entry kind from its mode bits.
func fileType(mode fs.FileMode) string {
	switch {
	case mode&fs.ModeSymlink != 0:
		return "symlink"
	case mode.IsDir():
		return "dir"
	case mode&fs.ModeDevice != 0:
		if mode&fs.ModeCharDevice != 0 {
			return "chardev"
		}
		return "blockdev"
	case mode&fs.ModeNamedPipe != 0:
		return "fifo"
	case mode&fs.ModeSocket != 0:
		return "socket"
	default:
		return "regular"
	}
}

// maxHashBytes caps content hashing so a huge file can't stall a poll; over it, the
// entity carries no hash (its size/mtime still track changes).
const maxHashBytes = 64 << 20

// hashFile returns "sha256:<hex>" for a regular file, or "" if it is over the cap or
// cannot be read. Hashing reads content but is a fingerprint, not the content itself.
func hashFile(full string, size int64) string {
	if size > maxHashBytes {
		return ""
	}
	fh, err := os.Open(full)
	if err != nil {
		return ""
	}
	defer fh.Close()
	h := sha256.New()
	if _, err := stdio.Copy(h, fh); err != nil {
		return ""
	}
	return "sha256:" + hex.EncodeToString(h.Sum(nil))
}
