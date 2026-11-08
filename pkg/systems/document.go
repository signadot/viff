package systems

import (
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"path"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"

	"github.com/signadot/tony-format/go-tony/gomap"
	"github.com/signadot/tony-format/go-tony/ir"
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
	// Values are the only values the field takes, when it lists them: a literal on it is one of
	// them, and an operator matches one.
	Values []string `tony:"field=values,omitzero"`
	// Required is whether every shape of an environment's block gives it (`null` for any). A row's
	// match may leave it out, as any field.
	Required bool `tony:"field=required,omitzero"`
	// Default is the pattern a shape that leaves the field out is read with, in tony (`{}`), where
	// the field has one: what an environment's or a ceiling's shape means by saying nothing of
	// it, where saying nothing would otherwise be any. A row's match is not read so: it names what
	// it names. Empty is none, and a required field has none.
	Default string `tony:"field=default,omitzero"`

	// defaulted is Default as the pattern it is, put in once at load: filled reads it on every
	// act's check of every system, where parsing Default there would be once per shape per check.
	defaulted *ir.Node
}

// The forms a field's value takes: how a step carries it, and what a pattern on it is checked
// against (Accepts).
const (
	FormBool   = "bool"   // true or false
	FormInt    = "int"    // a whole number: 443
	FormRef    = "ref"    // a full ref: refs/heads/main
	FormBranch = "branch" // a branch's name, not its ref: main
	FormUpper  = "upper"  // uppercase: ENG, ENG-42
	FormLower  = "lower"  // lowercase: deploys
	FormRepo   = "repo"   // host/owner/name, lowercase: github.com/acme/app
	FormText   = "text"   // free text, a command: `/` is a character like any other
	FormFile   = "file"   // a file's path in the workspace, `/` between its directories: db/x.sql
	FormPath   = "path"   // an address in the store, its names in order: [play, issues, issue]
	FormDoc    = "doc"    // structure, held as given: an ask's `on`
	FormVar    = "var"    // a variable's name, as a shell spells one: NPM_TOKEN
	FormEnv    = "env"    // variables by name, each with a value: {NODE_OPTIONS: --max-old-space-size=4096}
)

// forms is every form, in the order a refusal lists them.
var forms = []string{FormBool, FormInt, FormRef, FormBranch, FormUpper, FormLower, FormRepo, FormText, FormFile, FormPath, FormDoc, FormVar, FormEnv}

// Accepts says why a pattern on the field can never match a value of its form, or one of its
// values where it lists them, or nil where it can. A literal is a value of the form: a string of
// a text form, true or false of a bool, a number of an int, an address of a path. A string that
// holds a `*` on a field no value of which does is refused as the literal it is, naming the
// pattern it was meant as. An operator is checked as tony reads it, and where it is one that
// matches text, against the form's spelling.
func (f Field) Accepts(pattern *ir.Node) error {
	pattern = spelled(f.Form, ir.Uncomment(pattern))
	if isAny(pattern) {
		return nil
	}
	if t := unknownOp(pattern); t != "" {
		return fmt.Errorf("%s is no operator of tony's, and it would match the value under it as a literal", t)
	}
	if op, branches := combinator(pattern); op != "" {
		for i, b := range branches {
			if err := f.Accepts(b); err != nil {
				if op == "not" {
					return fmt.Errorf("!not: %w", err)
				}
				return fmt.Errorf("!%s[%d]: %w", op, i, err)
			}
		}
		return nil
	}
	if _, err := safeMatch(sampleOf(f.Form), pattern); err != nil {
		return err
	}
	if f.Form == FormPath {
		segs, err := pathPattern(pattern)
		if err != nil {
			return err
		}
		// A segment's operator is tried against a name once, as the whole pattern was against a
		// sample: tony reads a list pattern element by element only against a list of its length,
		// so a regexp that does not compile in a segment went unread above.
		for i, seg := range segs {
			if seg.Tag == "" {
				continue
			}
			if _, err := safeMatch(ir.FromString(""), seg); err != nil {
				return fmt.Errorf("segment %d: %w", i, err)
			}
		}
		return nil
	}
	if pattern.Tag == "" {
		return f.acceptsValue(pattern)
	}
	switch op, text := operand(pattern); op {
	case "glob":
		return f.acceptsText("!glob", text, func(v string) bool { ok, _ := path.Match(text, v); return ok })
	case "regexp":
		re, err := regexp.Compile(text)
		if err != nil {
			return err
		}
		return f.acceptsText("!regexp", text, re.MatchString)
	}
	return nil
}

// sampleOf is a value of form, which a pattern is evaluated against once to find what tony
// refuses in it: an operator it has not, a regexp that does not compile, a name no `!let` binds.
func sampleOf(form string) *ir.Node {
	switch form {
	case FormBool:
		return ir.FromBool(false)
	case FormInt:
		return ir.FromInt(0)
	case FormPath:
		return ir.FromSlice(nil)
	case FormDoc, FormEnv:
		return ir.FromKeyVals(nil)
	}
	return ir.FromString("")
}

// acceptsValue is why an untagged pattern, a literal, is no value of the field, or nil.
func (f Field) acceptsValue(n *ir.Node) error {
	switch f.Form {
	case FormDoc:
		return nil
	case FormEnv:
		return acceptsEnv(n)
	case FormBool:
		if n.Type == ir.BoolType {
			return nil
		}
		if n.Type == ir.StringType && (n.String == "true" || n.String == "false") {
			return fmt.Errorf("%q is a string, and the field is true or false: write %s, unquoted", n.String, n.String)
		}
		return errors.New("the field is true or false")
	case FormInt:
		if n.Type == ir.NumberType && n.Int64 != nil {
			return nil
		}
		if n.Type == ir.StringType {
			if _, err := strconv.ParseInt(n.String, 10, 64); err == nil {
				return fmt.Errorf("%q is a string, and the field is a number: write %s, unquoted", n.String, n.String)
			}
		}
		return errors.New("the field is a whole number")
	}
	switch n.Type {
	case ir.StringType:
	case ir.ArrayType:
		return errors.New("a list is no value of the field: a pattern is one value, and alternatives are written `!or [a, b]`")
	default:
		return errors.New("the field is text: a string")
	}
	v := n.String
	if len(f.Values) > 0 && !slices.Contains(f.Values, v) {
		if v == "*" {
			return fmt.Errorf("%q: the field is one of %s; any of them is `null`", v, strings.Join(f.Values, ", "))
		}
		return fmt.Errorf("%q: the field is one of %s", v, strings.Join(f.Values, ", "))
	}
	if strings.Contains(v, "*") {
		if v == "*" || strings.Trim(v, "*") == "" {
			return fmt.Errorf("%q is a literal no value of the field is: any value is `null`", v)
		}
		if f.Form == FormText || f.Form == "" {
			// Text may hold a `*`, but a literal with one reads as a pattern while matching only
			// itself, so a `*` is written in a regexp either way.
			return fmt.Errorf("%q holds a `*`, which a literal does not take: the pattern it means is `!regexp %q`, "+
				"and a `*` of the text itself is `\\*` in a regexp", v, starRegexp(v))
		}
		if before, _, ok := strings.Cut(v, "**"); ok {
			if before != "" && v == before+"**" {
				return fmt.Errorf("%q is a literal no value of the field is: every value beginning %s is `!regexp %q`",
					v, before, "^"+regexp.QuoteMeta(before))
			}
			return fmt.Errorf("%q is a literal no value of the field is: a pattern is written `!glob` or `!regexp`, "+
				"and a glob's `*` stops at a `/`", v)
		}
		return fmt.Errorf("%q is a literal no value of the field is: a pattern is written `!glob %q`", v, v)
	}
	return checkText(f.Form, v)
}

// acceptsText is why an operator over text (`!glob`, `!regexp`) can match no value of the field,
// or nil: it matches text, so not a bool, a number or an address; on a field that lists its
// values, it matches one; and a glob is spelled as the form's values are, its `*` within a `/`.
func (f Field) acceptsText(op, text string, matches func(string) bool) error {
	switch f.Form {
	case FormBool, FormInt, FormPath, FormDoc, FormEnv:
		return fmt.Errorf("%s matches text, and the field is %s", op, formWords(f.Form))
	}
	if len(f.Values) > 0 {
		if !slices.ContainsFunc(f.Values, matches) {
			return fmt.Errorf("%s %q matches none of the field's values, %s", op, text, strings.Join(f.Values, ", "))
		}
		return nil
	}
	if op != "!glob" {
		return nil
	}
	if _, err := path.Match(text, ""); err != nil {
		return fmt.Errorf("!glob %q: %v", text, err)
	}
	if f.Form == "" || f.Form == FormText {
		return nil
	}
	if before, _, ok := strings.Cut(text, "**"); ok {
		if before != "" && text == before+"**" {
			return fmt.Errorf("!glob %q: a glob's `*` stops at a `/`, so `**` is `*`: every value beginning %s is `!regexp %q`",
				text, before, "^"+regexp.QuoteMeta(before))
		}
		return fmt.Errorf("!glob %q: a glob's `*` stops at a `/`, so `**` is `*`: a pattern across them is a `!regexp`", text)
	}
	switch f.Form {
	case FormRef:
		switch {
		case !strings.HasPrefix(text, "refs/") && !strings.HasPrefix(text, "*"):
			return fmt.Errorf("!glob %q: the field is a full ref (refs/heads/%s)", text, text)
		case strings.Count(text, "/") < 2:
			return fmt.Errorf("!glob %q: the field is a full ref (refs/heads/main), and `*` stops at a `/`: "+
				"`refs/heads/*` is every branch, `!regexp \"^refs/\"` every ref", text)
		}
		return nil
	case FormRepo:
		if strings.Count(text, "/") < 2 {
			return fmt.Errorf("!glob %q: the field is a repository as `host/owner/name` (github.com/acme/app), "+
				"and `*` stops at a `/`: `github.com/acme/*` is an owner's", text)
		}
	}
	return checkText(f.Form, strings.ReplaceAll(text, "*", ""))
}

// formWords is a form as a refusal names it.
func formWords(form string) string {
	switch form {
	case FormBool:
		return "true or false"
	case FormInt:
		return "a number"
	case FormPath:
		return "an address (a list of segments, or play.issues.*)"
	case FormDoc:
		return "structure, matched as tony matches it"
	case FormEnv:
		return "variables by name, an object whose keys are their names and whose values are patterns on theirs"
	}
	return "text"
}

// acceptsEnv is why an untagged pattern is no pattern over variables (the form env), or nil: an
// object, each key a variable's name, each value a pattern over text (`null` for any).
func acceptsEnv(n *ir.Node) error {
	if n.Type != ir.ObjectType {
		return errors.New("the field is " + formWords(FormEnv))
	}
	for i, k := range n.Fields {
		k = ir.Uncomment(k)
		if k == nil || k.Type != ir.StringType || !varName.MatchString(k.String) {
			return fmt.Errorf("%s: a key is a variable's name: letters, digits and `_`, not starting with a digit", Words(k))
		}
		if err := (Field{Form: FormText}).Accepts(n.Values[i]); err != nil {
			return fmt.Errorf("%s: %w", k.String, err)
		}
	}
	return nil
}

// checkText is why a string is no value of a text form's spelling, or nil.
func checkText(form, v string) error {
	if v == "" && (form == FormRef || form == FormRepo || form == FormVar) {
		return errors.New("an empty value: the field is never empty")
	}
	switch form {
	case FormVar:
		if !varName.MatchString(v) {
			return fmt.Errorf("%q: the field is a variable's name: letters, digits and `_`, not starting with a digit", v)
		}
	case FormRepo:
		if v != "" && strings.Count(v, "/") < 2 {
			return fmt.Errorf("%q: the field is a repository as `host/owner/name` (github.com/acme/app)", v)
		}
		if v != strings.ToLower(v) {
			return fmt.Errorf("%q: the field is lowercase (%s)", v, strings.ToLower(v))
		}
	case FormRef:
		if v != "" && (!strings.HasPrefix(v, "refs/") || strings.Count(v, "/") < 2) {
			return fmt.Errorf("%q: the field is a full ref (refs/heads/%s)", v, strings.TrimPrefix(v, "refs/"))
		}
	case FormBranch:
		if strings.HasPrefix(v, "refs/") {
			return fmt.Errorf("%q: the field is a branch's name (%s), not its ref", v,
				strings.TrimPrefix(strings.TrimPrefix(v, "refs/"), "heads/"))
		}
	case FormUpper:
		if v != strings.ToUpper(v) {
			return fmt.Errorf("%q: the field is uppercase (%s)", v, strings.ToUpper(v))
		}
	case FormLower:
		if v != strings.ToLower(v) || strings.HasPrefix(v, "#") {
			return fmt.Errorf("%q: the field is lowercase, without a `#` (%s)", v,
				strings.TrimPrefix(strings.ToLower(v), "#"))
		}
	}
	return nil
}

// Field is the attribute as a field of a step, which a shape and a row match like any other.
func (a Attribute) Field() Field {
	return Field{Name: a.Name, Description: a.Description, Form: a.Form, Values: a.Values}
}

// Value is a value a runner works out for the field, as text, in the form's type: a bool's
// `true` true, an int's `443` 443, an address's `play.issues` its names, a repository in its
// canonical spelling. Text that does not read as its form stays text, which no pattern of the
// form matches.
func (f Field) Value(s string) *ir.Node {
	switch f.Form {
	case FormBool:
		if b, err := strconv.ParseBool(s); err == nil && (s == "true" || s == "false") {
			return ir.FromBool(b)
		}
	case FormInt:
		if n, err := strconv.ParseInt(s, 10, 64); err == nil {
			return ir.FromInt(n)
		}
	case FormPath:
		if addr, err := Address(s); err == nil {
			return addr
		}
	case FormRepo:
		return ir.FromString(Canonical(f.Form, s))
	}
	return ir.FromString(s)
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
		for vi := range d.Verbs {
			fields := d.Verbs[vi].Fields
			for fi := range fields {
				fields[fi].defaulted, _ = fields[fi].defaultPattern() // check held it to parsing
			}
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
			if err := a.Field().Accepts(a.Field().Value(a.Default)); err != nil || strings.Contains(a.Default, "*") {
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
			if f.Default != "" {
				if f.Required {
					return fmt.Errorf("%s.%s: field %s is required and has a default; it is one or the other", d.Name, v.Name, f.Name)
				}
				p, err := f.defaultPattern()
				if err == nil {
					err = f.Accepts(p)
				}
				if err != nil {
					return fmt.Errorf("%s.%s: field %s: the default %q is no pattern of its: %w", d.Name, v.Name, f.Name, f.Default, err)
				}
			}
		}
	}
	return nil
}

// defaultPattern is the field's default as the pattern it is, nil where it has none: tony, read
// as a shape's value is.
func (f Field) defaultPattern() (*ir.Node, error) {
	if f.Default == "" {
		return nil, nil
	}
	n, err := parse.Parse([]byte(f.Default))
	if err != nil {
		return nil, err
	}
	return Plain(ir.Uncomment(n)), nil
}

// filled is a shape with the document's default put in for each field of the verb it leaves out
// that has one: what an environment's or a ceiling's shape means by saying nothing of such a
// field. A shape of a step no document describes is as given, and so is one that leaves out no
// such field.
func filled(step string, s Shape) Shape {
	system, verb, ok := Split(step)
	if !ok {
		return s
	}
	d, ok := Get(system)
	if !ok {
		return s
	}
	v, ok := d.Verb(verb)
	if !ok {
		return s
	}
	var out Shape
	for _, f := range v.Fields {
		if f.defaulted == nil {
			continue
		}
		if _, named := s[f.Name]; named {
			continue
		}
		if out == nil {
			out = maps.Clone(s)
			if out == nil {
				out = Shape{}
			}
		}
		out[f.Name] = f.defaulted
	}
	if out == nil {
		return s
	}
	return out
}

// checkValues is whether a field's listed values are each one of its form, and not a pattern.
func checkValues(form string, values []string) error {
	for _, val := range values {
		if err := checkText(form, val); err != nil || val == "" || strings.Contains(val, "*") {
			return fmt.Errorf("the value %q is not one of its form", val)
		}
	}
	return nil
}

func checkForm(form string) error {
	switch form {
	case "":
		return errors.New("a field has a form, which says what its value is and how a pattern on it is checked")
	}
	if slices.Contains(forms, form) {
		return nil
	}
	return fmt.Errorf("the form %q is not one of %s", form, strings.Join(forms, ", "))
}

// starRegexp is the regexp a text with `*`s in it was meant as: each `*` any run, anchored at an
// end without one (`git push*` is `^git push`).
func starRegexp(v string) string {
	parts := strings.Split(v, "*")
	for i, p := range parts {
		parts[i] = regexp.QuoteMeta(p)
	}
	re := strings.Join(parts, ".*")
	re = strings.TrimSuffix(strings.TrimPrefix(re, ".*"), ".*")
	if !strings.HasPrefix(v, "*") {
		re = "^" + re
	}
	if !strings.HasSuffix(v, "*") {
		re += "$"
	}
	return re
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

// FieldOf is the field a step carries by name: its block's attribute or its verb's field. False
// for a step no document describes, or a name it does not carry.
func FieldOf(step, name string) (Field, bool) {
	system, verb, ok := Split(step)
	if !ok {
		return Field{}, false
	}
	d, ok := Get(system)
	if !ok {
		return Field{}, false
	}
	fields, _ := d.StepFields(verb)
	for _, f := range fields {
		if f.Name == name {
			return f, true
		}
	}
	return Field{}, false
}

// FormOf is the form of a field a step carries, empty (any text) where the document gives none.
func FormOf(step, name string) string {
	f, _ := FieldOf(step, name)
	return f.Form
}

// FieldsDoc is a step's fields as a runner works them out, as text, each in its form's type
// (Field.Value): in the order the document gives them, and a field it does not describe after,
// as text.
func FieldsDoc(step string, fields map[string]string) *ir.Node {
	var kvs []ir.KeyVal
	seen := map[string]bool{}
	if system, verb, ok := Split(step); ok {
		if d, ok := Get(system); ok {
			described, _ := d.StepFields(verb)
			for _, f := range described {
				if v, given := fields[f.Name]; given {
					kvs = append(kvs, ir.KeyVal{Key: ir.FromString(f.Name), Val: f.Value(v)})
					seen[f.Name] = true
				}
			}
		}
	}
	for _, k := range slices.Sorted(maps.Keys(fields)) {
		if !seen[k] {
			kvs = append(kvs, ir.KeyVal{Key: ir.FromString(k), Val: ir.FromString(fields[k])})
		}
	}
	return ir.FromKeyVals(kvs)
}
