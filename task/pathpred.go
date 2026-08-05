package task

import (
	"path/filepath"
	"strings"
)

// PathPred is a file-matching predicate, lifted from signadot/42's
// enforced-planning model (internal/skill.Subject + planner.matchSubjects). In
// 42 a Subject selects which CI skills apply to a changeset; here a PathPred
// selects which gate applies to the files an action is about to touch. (Named
// PathPred here, not Subject, to avoid colliding with the flow subject a launch
// is about — launch.Spec.Subject.)
//
//tony:schemagen=verse-path-pred,notag
type PathPred struct {
	Name string `tony:"field=name,omitzero"` // pattern or identifier
	Type string `tony:"field=type,omitzero"` // glob | extension | directory | any
}

// MatchFiles returns the subset of files the predicate matches.
func (s PathPred) MatchFiles(files []string) []string {
	var matched []string
	switch s.Type {
	case "glob", "file_pattern", "":
		for _, f := range files {
			if matchGlob(s.Name, f) {
				matched = append(matched, f)
			}
		}
	case "extension":
		ext := s.Name
		if !strings.HasPrefix(ext, ".") {
			ext = "." + ext
		}
		for _, f := range files {
			if filepath.Ext(f) == ext {
				matched = append(matched, f)
			}
		}
	case "directory", "path":
		dir := s.Name
		if !strings.HasSuffix(dir, "/") {
			dir += "/"
		}
		for _, f := range files {
			if strings.HasPrefix(f, dir) {
				matched = append(matched, f)
			}
		}
	case "any", "all":
		if len(files) > 0 {
			matched = append(matched, files...)
		}
	}
	return matched
}

// matchGlob matches a glob against a path. It extends filepath.Match (single
// path segment) with crude "**" support (spans separators) and a basename
// fallback so that "*.go" matches "app/server.go".
func matchGlob(pattern, path string) bool {
	if strings.Contains(pattern, "**") {
		segs := strings.Split(pattern, "**")
		pos := 0
		for i, seg := range segs {
			if seg == "" {
				continue
			}
			k := strings.Index(path[pos:], seg)
			if k < 0 {
				return false
			}
			if i == 0 && k != 0 { // first literal segment anchors at start
				return false
			}
			pos += k + len(seg)
		}
		// last literal segment anchors at end unless the pattern ends with "**"
		if !strings.HasSuffix(pattern, "**") && pos != len(path) {
			return false
		}
		return true
	}
	if ok, err := filepath.Match(pattern, path); err == nil && ok {
		return true
	}
	ok, _ := filepath.Match(pattern, filepath.Base(path))
	return ok
}
