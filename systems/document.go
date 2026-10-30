package systems

import (
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"path"
	"slices"
	"sort"
	"strings"
	"sync"

	"github.com/signadot/tony-format/go-tony/gomap"
	"github.com/signadot/tony-format/go-tony/parse"
)

// Document describes one system: what a block of it in a spawn's environment holds, and its
// verbs. It is what verse keeps at `verse.systems.<name>`, and the vocabulary a spawn's
// environment, a runner's ceiling and an autonomy row are written in and checked against.
//
// It describes steps, never tools. A tool and a verb are one to one by name (`git-push`,
// `git.push`), but the step the runner builds from a call carries more than the call: its
// block's attributes, and fields the runner works out itself (git push's `exists` and `own`).
// What a tool takes is the runner's.
//
//tony:schemagen=verse-system,notag
type Document struct {
	// Name is the system's, as a tool, a step and a row spell it: `git`, `linear-issue`.
	Name string `tony:"field=name"`
	// Description says what the system is, for a person reading the store.
	Description string `tony:"field=description"`
	// Attributes are what a block of the system in a spawn's environment holds, beside its
	// verbs (git's `repo` and `from`). Every step of the system carries its block's. A system
	// with none is a block of verbs only.
	Attributes []Attribute `tony:"field=attributes,omitzero"`
	// Verbs are what a task may do to the system, each with the fields of its step.
	Verbs []Verb `tony:"field=verbs"`
}

// Attribute is one field of a block, written by the spawn's author: `repo`, `from`.
//
//tony:schemagen=verse-system-attribute,notag
type Attribute struct {
	Name        string `tony:"field=name"`
	Description string `tony:"field=description"`
	// Form is how the value is spelled (Field.Form).
	Form string `tony:"field=form,omitzero"`
	// Values are the only values it takes, when it lists them (Field.Values).
	Values []string `tony:"field=values,omitzero"`
	// Required is whether every block gives it.
	Required bool `tony:"field=required,omitzero"`
	// Default is its value when a block doesn't give it; empty is none.
	Default string `tony:"field=default,omitzero"`
	// Unique is whether no two blocks of an environment share a value, compared in the form's
	// canonical spelling. A system with a unique attribute takes a list of blocks, one without
	// takes a single block; at most one attribute is unique.
	Unique bool `tony:"field=unique,omitzero"`
}

// Verb is one act on a system: `push`, `create`. Its fields, with its block's attributes, are
// what its step carries, which an environment's shapes and a row's match name as patterns.
//
//tony:schemagen=verse-system-verb,notag
type Verb struct {
	Name        string  `tony:"field=name"`
	Description string  `tony:"field=description"`
	Fields      []Field `tony:"field=fields,omitzero"`
}

// Field is one field of a verb's step: `ref`, `path`. The runner sets it when it builds the
// step, from the call or from what it works out itself.
//
//tony:schemagen=verse-system-field,notag
type Field struct {
	Name        string `tony:"field=name"`
	Description string `tony:"field=description"`
	// Form is how the runner spells the field's value, where it spells it one way: a pattern
	// written another way would never match, and a row written with it is refused (Accepts).
	// Empty is any text.
	Form string `tony:"field=form,omitzero"`
	// Values are the only values the field takes, when it lists them: a pattern on it is one of
	// them, or `*`.
	Values []string `tony:"field=values,omitzero"`
	// Required is whether every shape of an environment's block gives it (`*` allowed). A row's
	// match may leave it out, as any field.
	Required bool `tony:"field=required,omitzero"`
}

// The forms a param's value takes.
const (
	FormBool    = "bool"    // true or false
	FormRef     = "ref"     // a full ref: refs/heads/main
	FormBranch  = "branch"  // a branch's name, not its ref: main
	FormUpper   = "upper"   // uppercase: ENG, ENG-42
	FormLower   = "lower"   // lowercase: deploys
	FormRepo    = "repo"    // host/owner/name, lowercase: github.com/acme/app
	FormText    = "text"    // free text, a command: `/` is a character, and `*` matches across it
	FormAddress = "address" // a dotted address in the store: play.issues.issue, `.` separating its segments
)

// Accepts says why a pattern on the field can never match a value of its form, or one of its
// values when it lists them, or nil when it can. A pattern is read as the text it is, `*` matching
// within a segment (between `/`s, or `.`s in an address) and `**` across them: a pattern of only
// `*`s takes any value of a form without a separator, and a value of a form that has some (a full
// ref) is matched by one that spells them, or by `**`.
func (p Field) Accepts(pattern string) error {
	if len(p.Values) > 0 && pattern != "*" && !slices.Contains(p.Values, pattern) {
		return fmt.Errorf("%q: the field is one of %s, or `*`", pattern, strings.Join(p.Values, ", "))
	}
	return accepts(p.Form, pattern)
}

// Field is the attribute as a field of a step, which a shape and a row match like any other.
func (a Attribute) Field() Field {
	return Field{Name: a.Name, Description: a.Description, Form: a.Form, Values: a.Values}
}

func accepts(form, pattern string) error {
	if form == "" {
		return nil
	}
	if pattern == "" && (form == FormBool || form == FormRef || form == FormRepo) {
		return errors.New("an empty pattern: the field is never empty")
	}
	if form == FormRepo && !strings.Contains(pattern, "**") && strings.Count(pattern, "/") < 2 {
		return fmt.Errorf("%q: the field is a repository as `host/owner/name` (github.com/acme/app), "+
			"and `*` does not cross its `/`: `github.com/acme/*` is an owner's, `**` every one", pattern)
	}
	if form == FormRef {
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
	switch form {
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

// check is what a document has to satisfy: a name; attributes named once, at most one of them
// unique, with a default of their form; verbs named once, with fields named once, values of
// their form; and every attribute, verb and field name distinct, so that a key of a block, a
// shape or a row reads one way. Names are one or more lowercase words joined by `-`, so that
// `<system>-<verb>` and `<system>.<verb>` read back unambiguously.
func (d Document) check() error {
	switch {
	case !nameOK(d.Name):
		return fmt.Errorf("name %q: lowercase words joined by `-`", d.Name)
	case len(d.Verbs) == 0:
		return fmt.Errorf("%s: a system has verbs", d.Name)
	}
	names := map[string]string{} // name -> what it names, across the document
	claim := func(name, what string) error {
		if !nameOK(name) {
			return fmt.Errorf("%s: %s %q: lowercase words joined by `-`", d.Name, what, name)
		}
		if was, dup := names[name]; dup {
			return fmt.Errorf("%s: %q names both %s and %s; a name means one thing in a document", d.Name, name, was, what)
		}
		names[name] = what
		return nil
	}
	unique := ""
	for _, a := range d.Attributes {
		if err := claim(a.Name, "an attribute"); err != nil {
			return err
		}
		if err := checkForm(a.Form); err != nil {
			return fmt.Errorf("%s: attribute %s: %w", d.Name, a.Name, err)
		}
		if a.Unique {
			if unique != "" {
				return fmt.Errorf("%s: attributes %s and %s are both unique; at most one is", d.Name, unique, a.Name)
			}
			unique = a.Name
		}
		if a.Unique && a.Default != "" {
			return fmt.Errorf("%s: attribute %s is unique and has a default; blocks that leave it out would share it", d.Name, a.Name)
		}
		if a.Required && a.Default != "" {
			return fmt.Errorf("%s: attribute %s is required and has a default; it is one or the other", d.Name, a.Name)
		}
		if err := checkValues(a.Form, a.Values); err != nil {
			return fmt.Errorf("%s: attribute %s: %w", d.Name, a.Name, err)
		}
		if a.Default != "" {
			if err := a.Field().Accepts(a.Default); err != nil || strings.Contains(a.Default, "*") {
				return fmt.Errorf("%s: attribute %s: the default %q is not one of its values", d.Name, a.Name, a.Default)
			}
		}
	}
	for _, v := range d.Verbs {
		if err := claim(v.Name, "a verb"); err != nil {
			return err
		}
	}
	for _, v := range d.Verbs {
		fields := map[string]bool{}
		for _, f := range v.Fields {
			if fields[f.Name] {
				return fmt.Errorf("%s.%s: the field %s is described twice", d.Name, v.Name, f.Name)
			}
			fields[f.Name] = true
			if was, ok := names[f.Name]; ok && was != "a field" {
				return fmt.Errorf("%s.%s: the field %q is also %s; a name means one thing in a document", d.Name, v.Name, f.Name, was)
			}
			if !nameOK(f.Name) {
				return fmt.Errorf("%s.%s: field %q: lowercase words joined by `-`", d.Name, v.Name, f.Name)
			}
			names[f.Name] = "a field"
			if err := checkForm(f.Form); err != nil {
				return fmt.Errorf("%s.%s: field %s: %w", d.Name, v.Name, f.Name, err)
			}
			if err := checkValues(f.Form, f.Values); err != nil {
				return fmt.Errorf("%s.%s: field %s: %w", d.Name, v.Name, f.Name, err)
			}
		}
	}
	return nil
}

// checkValues is whether a field's listed values are each one of its form, and not a pattern.
func checkValues(form string, values []string) error {
	for _, val := range values {
		if err := accepts(form, val); err != nil || val == "" || strings.Contains(val, "*") {
			return fmt.Errorf("the value %q is not one of its form", val)
		}
	}
	return nil
}

func checkForm(form string) error {
	switch form {
	case "", FormBool, FormRef, FormBranch, FormUpper, FormLower, FormRepo, FormText, FormAddress:
		return nil
	}
	return fmt.Errorf("the form %q is not one of %s, %s, %s, %s, %s, %s, %s, %s",
		form, FormBool, FormRef, FormBranch, FormUpper, FormLower, FormRepo, FormText, FormAddress)
}

// Unique is the document's unique attribute, if it has one.
func (d Document) Unique() (Attribute, bool) {
	for _, a := range d.Attributes {
		if a.Unique {
			return a, true
		}
	}
	return Attribute{}, false
}

// Attribute is the attribute named, if the document has it.
func (d Document) Attribute(name string) (Attribute, bool) {
	for _, a := range d.Attributes {
		if a.Name == name {
			return a, true
		}
	}
	return Attribute{}, false
}

// StepFields is what a step of the verb carries: its block's attributes, then the verb's own
// fields. False when the document has no such verb.
func (d Document) StepFields(verb string) ([]Field, bool) {
	v, ok := d.Verb(verb)
	if !ok {
		return nil, false
	}
	out := make([]Field, 0, len(d.Attributes)+len(v.Fields))
	for _, a := range d.Attributes {
		out = append(out, a.Field())
	}
	return append(out, v.Fields...), true
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

// Fields is the names of the fields a step carries, the ones a row's match may give: its
// block's attributes and its verb's fields. False for a step no document describes.
func Fields(step string) ([]string, bool) {
	system, verb, ok := Split(step)
	if !ok {
		return nil, false
	}
	d, ok := Get(system)
	if !ok {
		return nil, false
	}
	fields, ok := d.StepFields(verb)
	if !ok {
		return nil, false
	}
	names := make([]string, len(fields))
	for i, f := range fields {
		names[i] = f.Name
	}
	return names, true
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
