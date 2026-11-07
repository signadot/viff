// Package secretstore is the runner's secret store: a directory the deployment mounts, one file
// to a secret, the file's name the secret's name.
//
// THE STORE HOLDS WHAT A TASK NEVER HOLDS. The runner acts with a secret, for a task, and the
// task's process has neither the value nor the directory: no sandbox binds it
// (runnerd.CheckSecretStore). What the task's process does hold, its model's key, is not here;
// it is sent on the process's stdin (stock.Secrets).
//
// A VALUE IS READ AT EACH USE, so a secret that is rotated is the next act's with no restart. A
// Kubernetes Secret volume changes its files by moving one link, and a read then finds the old
// value whole or the new one.
package secretstore

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// maxValue is the most a secret's file may hold. A token is tens of bytes and a key in PEM a few
// thousand; a file past this is no secret, and is not read into a header.
const maxValue = 64 << 10

// Store is the secrets under one directory.
type Store struct{ dir string }

// Open is the store under dir. It reads nothing: a directory that is not there is found at the
// first Get, as a secret that is not there is.
func Open(dir string) *Store { return &Store{dir: dir} }

// Dir is the directory the store reads.
func (s *Store) Dir() string { return s.dir }

// CheckName refuses what is no secret's name. A NAME IS A FILE'S NAME AND NOTHING ELSE: lower
// case letters, digits, `-`, `_` and `.`, not starting with a `.`. So a name reaches no other
// directory, and none of the names a Kubernetes Secret volume keeps for itself (`..data`).
func CheckName(name string) error {
	if name == "" {
		return fmt.Errorf("a secret's name is not empty")
	}
	if name[0] == '.' {
		return fmt.Errorf("%q is no secret's name: a name does not start with a `.`", name)
	}
	for _, r := range name {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '-', r == '_', r == '.':
		default:
			return fmt.Errorf("%q is no secret's name: a name is lower case letters, digits, `-`, `_` and `.`", name)
		}
	}
	return nil
}

// Get is the secret's value, read now. The newline the file ends with is not the value's: a file
// written by hand ends with one, and a header cannot carry it.
//
// A SECRET IS THERE WHERE ITS FILE READS AND IS NOT EMPTY. An empty file is an absent secret, as
// an empty variable is one that is not set, and so is a file the runner cannot read. The error
// names the secret and says which, and never holds any of a value.
func (s *Store) Get(name string) (string, error) {
	if s == nil || s.dir == "" {
		return "", fmt.Errorf("the secret %s: this runner has no secret store (`secrets.dir`)", name)
	}
	if err := CheckName(name); err != nil {
		return "", err
	}
	f, err := os.Open(filepath.Join(s.dir, name))
	if err != nil {
		return "", fmt.Errorf("the secret %s: %w", name, absent(err))
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, maxValue+1))
	if err != nil {
		// A read's error names the path and what the system said of it, and nothing read.
		return "", fmt.Errorf("the secret %s: %w", name, absent(err))
	}
	if len(b) > maxValue {
		return "", fmt.Errorf("the secret %s: its file is over %d bytes, which is no secret's value", name, maxValue)
	}
	v := strings.TrimSuffix(strings.TrimSuffix(string(b), "\n"), "\r")
	if v == "" {
		return "", fmt.Errorf("the secret %s: its file is empty", name)
	}
	return v, nil
}

// Has is whether Get would give a value.
func (s *Store) Has(name string) bool {
	_, err := s.Get(name)
	return err == nil
}

// absent is a file's error as the store says it: what the system said, without the path, which
// the message has already as the secret's name.
func absent(err error) error {
	var pe *fs.PathError
	if errors.As(err, &pe) {
		if errors.Is(pe.Err, fs.ErrNotExist) {
			return errors.New("it is not in the store")
		}
		return fmt.Errorf("it does not read (%w)", pe.Err)
	}
	return fmt.Errorf("it does not read (%w)", err)
}
