package systems

import (
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"path"
	"sort"
	"strings"
	"sync"

	"github.com/signadot/tony-format/go-tony/gomap"
	"github.com/signadot/tony-format/go-tony/parse"
)

// Document describes one system: its name, what it is, and its verbs. It is what verse keeps
// at `verse.systems.<name>`, and what a policy row's match is checked against.
//
//tony:schemagen=verse-system,notag
type Document struct {
	// Name is the system's, as a tool, a step and a row spell it: `git`, `linear-issue`.
	Name string `tony:"field=name"`
	// Description says what the system is, for a person reading the store.
	Description string `tony:"field=description"`
	// Verbs are what a task may do to the system, each with its params.
	Verbs []Verb `tony:"field=verbs"`
}

// Verb is one act on a system: `push`, `create`. Its params are the fields a call of it carries,
// which a gate's match (and a policy row's) names as patterns.
//
//tony:schemagen=verse-system-verb,notag
type Verb struct {
	Name        string  `tony:"field=name"`
	Description string  `tony:"field=description"`
	Params      []Param `tony:"field=params,omitzero"`
}

// Param is one field of a verb's call: `repo`, `ref`, `path`.
//
//tony:schemagen=verse-system-param,notag
type Param struct {
	Name        string `tony:"field=name"`
	Description string `tony:"field=description"`
	// Form is how the runner spells the field's value, where it spells it one way: a pattern
	// written another way would never match, and a row written with it is refused (Accepts).
	// Empty is any text.
	Form string `tony:"field=form,omitzero"`
}

// The forms a param's value takes.
const (
	FormBool   = "bool"   // true or false
	FormRef    = "ref"    // a full ref: refs/heads/main
	FormBranch = "branch" // a branch's name, not its ref: main
	FormUpper  = "upper"  // uppercase: ENG, ENG-42
	FormLower  = "lower"  // lowercase: deploys
	FormRepo   = "repo"   // host/owner/name, lowercase: github.com/acme/app
)

// Accepts says why a pattern on the param can never match a value of its form, or nil when it
// can. A pattern is read as the text it is, `*` matching within a `/` segment and `**` across
// them: a pattern of only `*`s takes any value of a form without a `/`, and a value of a form
// that has some (a full ref) is matched by one that spells them, or by `**`.
func (p Param) Accepts(pattern string) error {
	if p.Form == "" {
		return nil
	}
	if pattern == "" && (p.Form == FormBool || p.Form == FormRef || p.Form == FormRepo) {
		return errors.New("an empty pattern: the field is never empty")
	}
	if p.Form == FormRepo && !strings.Contains(pattern, "**") && strings.Count(pattern, "/") < 2 {
		return fmt.Errorf("%q: the field is a repository as `host/owner/name` (github.com/acme/app), "+
			"and `*` does not cross its `/`: `github.com/acme/*` is an owner's, `**` every one", pattern)
	}
	if p.Form == FormRef {
		switch {
		case !strings.HasPrefix(pattern, "refs/") && !strings.HasPrefix(pattern, "*"):
			return fmt.Errorf("%q: the field is a full ref (refs/heads/%s)", pattern, pattern)
		case !strings.Contains(pattern, "**") && strings.Count(pattern, "/") < 2:
			return fmt.Errorf("%q: the field is a full ref (refs/heads/main), and `*` does not cross its `/`: "+
				"`refs/heads/*` is every branch, `**` every ref", pattern)
		}
		return nil
	}
	if strings.Trim(pattern, "*") == "" {
		return nil
	}
	switch p.Form {
	case FormBool:
		if pattern != "true" && pattern != "false" {
			return fmt.Errorf("%q: the field is true or false", pattern)
		}
	case FormBranch:
		if strings.HasPrefix(pattern, "refs/") {
			return fmt.Errorf("%q: the field is a branch's name (%s), not its ref", pattern,
				strings.TrimPrefix(strings.TrimPrefix(pattern, "refs/"), "heads/"))
		}
	case FormUpper:
		if pattern != strings.ToUpper(pattern) {
			return fmt.Errorf("%q: the field is uppercase (%s)", pattern, strings.ToUpper(pattern))
		}
	case FormRepo:
		if pattern != strings.ToLower(pattern) {
			return fmt.Errorf("%q: the field is lowercase (%s)", pattern, strings.ToLower(pattern))
		}
	case FormLower:
		if pattern != strings.ToLower(pattern) || strings.HasPrefix(pattern, "#") {
			return fmt.Errorf("%q: the field is lowercase, without a `#` (%s)", pattern,
				strings.TrimPrefix(strings.ToLower(pattern), "#"))
		}
	}
	return nil
}

//go:embed documents/*.tony
var documents embed.FS

var (
	loadOnce sync.Once
	loaded   []Document
	byName   map[string]*Document
	loadErr  error
)

// load reads the embedded documents once. A document that does not read, or that is named
// twice, is a build mistake, surfaced at first use.
func load() {
	byName = map[string]*Document{}
	entries, err := fs.ReadDir(documents, "documents")
	if err != nil {
		loadErr = err
		return
	}
	for _, e := range entries {
		data, err := documents.ReadFile(path.Join("documents", e.Name()))
		if err != nil {
			loadErr = err
			return
		}
		nodes, err := parse.ParseMulti(data)
		if err != nil {
			loadErr = fmt.Errorf("%s: %w", e.Name(), err)
			return
		}
		if len(nodes) != 1 {
			loadErr = fmt.Errorf("%s: one document per file, not %d", e.Name(), len(nodes))
			return
		}
		var d Document
		if err := gomap.FromTonyIR(nodes[0], &d); err != nil {
			loadErr = fmt.Errorf("%s: %w", e.Name(), err)
			return
		}
		if err := d.check(); err != nil {
			loadErr = fmt.Errorf("%s: %w", e.Name(), err)
			return
		}
		if strings.TrimSuffix(e.Name(), ".tony") != d.Name {
			loadErr = fmt.Errorf("%s describes %q: a document is named for its system", e.Name(), d.Name)
			return
		}
		if _, dup := byName[d.Name]; dup {
			loadErr = fmt.Errorf("%s: the system %s is described twice", e.Name(), d.Name)
			return
		}
		loaded = append(loaded, d)
		byName[d.Name] = &loaded[len(loaded)-1]
	}
	sort.Slice(loaded, func(i, j int) bool { return loaded[i].Name < loaded[j].Name })
	byName = map[string]*Document{}
	for i := range loaded {
		byName[loaded[i].Name] = &loaded[i]
	}
}

// check is what a document has to satisfy: a name, and verbs each named once with params each
// named once. Names are one or more lowercase words joined by `-`, so that `<system>-<verb>`
// and `<system>.<verb>` read back unambiguously.
func (d Document) check() error {
	switch {
	case !nameOK(d.Name):
		return fmt.Errorf("name %q: lowercase words joined by `-`", d.Name)
	case len(d.Verbs) == 0:
		return fmt.Errorf("%s: a system has verbs", d.Name)
	}
	verbs := map[string]bool{}
	for _, v := range d.Verbs {
		if !nameOK(v.Name) {
			return fmt.Errorf("%s: verb %q: lowercase words joined by `-`", d.Name, v.Name)
		}
		if verbs[v.Name] {
			return fmt.Errorf("%s: the verb %s is described twice", d.Name, v.Name)
		}
		verbs[v.Name] = true
		params := map[string]bool{}
		for _, p := range v.Params {
			if !nameOK(p.Name) {
				return fmt.Errorf("%s.%s: param %q: lowercase words joined by `-`", d.Name, v.Name, p.Name)
			}
			if params[p.Name] {
				return fmt.Errorf("%s.%s: the param %s is described twice", d.Name, v.Name, p.Name)
			}
			switch p.Form {
			case "", FormBool, FormRef, FormBranch, FormUpper, FormLower, FormRepo:
			default:
				return fmt.Errorf("%s.%s: param %s: the form %q is not one of %s, %s, %s, %s, %s, %s",
					d.Name, v.Name, p.Name, p.Form, FormBool, FormRef, FormBranch, FormUpper, FormLower, FormRepo)
			}
			params[p.Name] = true
		}
	}
	return nil
}

func nameOK(s string) bool {
	if s == "" {
		return false
	}
	for _, w := range strings.Split(s, "-") {
		if w == "" {
			return false
		}
		for _, r := range w {
			if (r < 'a' || r > 'z') && (r < '0' || r > '9') {
				return false
			}
		}
	}
	return true
}

func must() {
	loadOnce.Do(load)
	if loadErr != nil {
		panic("systems: " + loadErr.Error())
	}
}

// All is every system, by name.
func All() []Document {
	must()
	return loaded
}

// Get is the system named, if there is one.
func Get(name string) (Document, bool) {
	must()
	d, ok := byName[name]
	if !ok {
		return Document{}, false
	}
	return *d, true
}

// Verb is the verb of the system named, if the document has it.
func (d Document) Verb(name string) (Verb, bool) {
	for _, v := range d.Verbs {
		if v.Name == name {
			return v, true
		}
	}
	return Verb{}, false
}

// ParamNames is the verb's params, by name.
func (v Verb) ParamNames() []string {
	names := make([]string, len(v.Params))
	for i, p := range v.Params {
		names[i] = p.Name
	}
	return names
}

// StepName is the step a system's verb is: `<system>.<verb>`.
func StepName(system, verb string) string { return system + "." + verb }

// ToolName is the tool a system's verb is called by: `<system>-<verb>`.
func ToolName(system, verb string) string { return system + "-" + verb }

// SplitTool is the system and verb a tool's name is made of, `<system>-<verb>`: the longest
// system name the tool's begins with, and the verb of that system the rest names.
func SplitTool(tool string) (system, verb string, ok bool) {
	for _, d := range All() {
		if rest, found := strings.CutPrefix(tool, d.Name+"-"); found && len(d.Name) > len(system) {
			if _, has := d.Verb(rest); has {
				system, verb, ok = d.Name, rest, true
			}
		}
	}
	return system, verb, ok
}

// Split is the system and verb a step names, by its one `.`: the system's name is words joined
// by `-`, and so is the verb's, and neither holds a `.`.
func Split(step string) (system, verb string, ok bool) {
	system, verb, ok = strings.Cut(step, ".")
	return system, verb, ok && system != "" && verb != "" && !strings.Contains(verb, ".")
}

// Fields is the fields a step's call carries, the ones a gate's match may give: the verb's
// params. False for a step no document describes.
func Fields(step string) ([]string, bool) {
	system, verb, ok := Split(step)
	if !ok {
		return nil, false
	}
	d, ok := Get(system)
	if !ok {
		return nil, false
	}
	v, ok := d.Verb(verb)
	if !ok {
		return nil, false
	}
	return v.ParamNames(), true
}

// Steps is every step the documents describe, `<system>.<verb>`, sorted.
func Steps() []string {
	var out []string
	for _, d := range All() {
		for _, v := range d.Verbs {
			out = append(out, StepName(d.Name, v.Name))
		}
	}
	sort.Strings(out)
	return out
}
