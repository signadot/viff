package systems

import (
	"cmp"
	"errors"
	"fmt"
	"maps"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/signadot/tony-format/go-tony/gomap"
	"github.com/signadot/tony-format/go-tony/ir"
	"github.com/signadot/verse/pkg/exprs"
)

// Environment is what a spawn's run can reach, and the variables its shell is given. It is
// fixed when the run starts: the runner sets the run up from it, its ceiling is checked against
// it, and a task's tools come from it. What happens to each step within it is the autonomy's.
type Environment struct {
	// Vars are variables set in the task's shell, by name, a sub-agent's included: how a
	// script gets data from the subject without it being spliced into the script's text.
	Vars map[string]string `tony:"field=vars,omitzero"`
	// Systems are the systems the run can reach, each a block or a list of blocks of the
	// system's attributes and verbs (Block). A system not here is not reachable at all.
	Systems Systems `tony:"field=systems,omitzero"`
}

// Systems is an environment's systems, by name: each a list of blocks. A system whose document
// has a unique attribute (git's `repo`) takes one block per value of it; any other takes one.
type Systems map[string][]Block

// Block is one block of a system: its attributes, and its verbs, each with the shapes of the
// steps the run may take. They are written side by side, `{repo: …, from: main, fetch: {},
// push: […]}`, and the document says which key is which: an attribute's value is a scalar, a
// verb's a shape or a list of shapes.
type Block struct {
	Attributes map[string]string
	Verbs      map[string][]Shape
}

// Shape is one allowed form of a verb's step: a field and the pattern its value must match (a
// tony match, pattern.go). A field the shape does not name may hold any value, so `{}` is any step
// of the verb.
type Shape map[string]*ir.Node

// Check is what an environment has to satisfy, against the systems' documents: variables named
// as a shell names them; systems that exist; blocks of each system's attributes and verbs, its
// required attributes given, its unique one distinct across blocks, each attribute a value of its
// form; shapes that name only their verb's fields, give its required ones, and spell patterns
// its fields could match; and a tracker's or a forge's repository among the `git` blocks'.
func (e Environment) Check() error {
	for _, name := range slices.Sorted(maps.Keys(e.Vars)) {
		if !varName.MatchString(name) {
			return fmt.Errorf("vars.%s: a variable's name is letters, digits and `_`, not starting with a digit", name)
		}
	}
	for _, system := range slices.Sorted(maps.Keys(e.Systems)) {
		d, ok := Get(system)
		if !ok {
			return fmt.Errorf("systems.%s: not a system (they are: %s)", system, strings.Join(names(), ", "))
		}
		blocks := e.Systems[system]
		unique, hasUnique := d.Unique()
		if len(blocks) > 1 && !hasUnique {
			return fmt.Errorf("systems.%s: one block; the system has no attribute that tells blocks apart", system)
		}
		seen := map[string]int{}
		for i, b := range blocks {
			at := blockAt(system, i, len(blocks))
			if err := b.check(d); err != nil {
				return fmt.Errorf("%s: %w", at, err)
			}
			if hasUnique {
				v := Canonical(unique.Form, b.Attributes[unique.Name])
				if j, dup := seen[v]; dup {
					return fmt.Errorf("%s: %s %s is %s's too; one block per %s", at, unique.Name, v, blockAt(system, j, len(blocks)), unique.Name)
				}
				seen[v] = i
			}
		}
	}
	return e.checkAcross()
}

// blockAt is where a block is, for a refusal: its system, and its place when there are several.
func blockAt(system string, i, n int) string {
	if n > 1 {
		return fmt.Sprintf("systems.%s[%d]", system, i)
	}
	return "systems." + system
}

// varName is a variable's name as a shell spells one.
var varName = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

func (b Block) check(d Document) error {
	for _, name := range slices.Sorted(maps.Keys(b.Attributes)) {
		a, ok := d.Attribute(name)
		if !ok {
			if _, isVerb := d.Verb(name); isVerb {
				return fmt.Errorf("%s: a verb takes a shape or a list of shapes (`%s: {}` for any)", name, name)
			}
			return fmt.Errorf("%s: %s has no attribute or verb %q (attributes: %s; verbs: %s)", name, d.Name, name,
				strings.Join(attributeNames(d), ", "), strings.Join(verbNames(d), ", "))
		}
		v := b.Attributes[name]
		if strings.Contains(v, "*") {
			return fmt.Errorf("%s: %q: an attribute is a value, not a pattern", name, v)
		}
		if err := checkText(a.Form, Canonical(a.Form, v)); err != nil {
			return fmt.Errorf("%s: %w", name, err)
		}
		if len(a.Values) > 0 && !slices.Contains(a.Values, v) {
			return fmt.Errorf("%s: %q: the attribute is one of %s", name, v, strings.Join(a.Values, ", "))
		}
	}
	for _, verb := range slices.Sorted(maps.Keys(b.Verbs)) {
		if _, isAttribute := d.Attribute(verb); isAttribute {
			return fmt.Errorf("%s: an attribute takes a value, not a shape (`%s: …`)", verb, verb)
		}
	}
	for _, a := range d.Attributes {
		if _, given := b.Attributes[a.Name]; a.Required && !given {
			return fmt.Errorf("%s: required", a.Name)
		}
	}
	for _, verb := range slices.Sorted(maps.Keys(b.Verbs)) {
		v, ok := d.Verb(verb)
		if !ok {
			return fmt.Errorf("%s: %s has no attribute or verb %q (attributes: %s; verbs: %s)", verb, d.Name, verb,
				strings.Join(attributeNames(d), ", "), strings.Join(verbNames(d), ", "))
		}
		shapes := b.Verbs[verb]
		if len(shapes) == 0 {
			return fmt.Errorf("%s: an empty list allows nothing; leave the verb out instead", verb)
		}
		for i, s := range shapes {
			if err := s.check(v); err != nil {
				if len(shapes) > 1 {
					return fmt.Errorf("%s[%d]%w", verb, i, err)
				}
				return fmt.Errorf("%s%w", verb, err)
			}
		}
	}
	return nil
}

func (s Shape) check(v Verb) error {
	for _, name := range slices.Sorted(maps.Keys(s)) {
		f, ok := fieldOf(v, name)
		if !ok {
			return fmt.Errorf(".%s: not a field of %s (it has: %s)", name, v.Name, strings.Join(fieldNames(v), ", "))
		}
		if Binds(s[name]) {
			return fmt.Errorf(".%s: %s: a shape is a pattern over what the task does and is not bound", name, Words(s[name]))
		}
		if err := f.Accepts(s[name]); err != nil {
			return fmt.Errorf(".%s: %w", name, err)
		}
	}
	for _, f := range v.Fields {
		if _, given := s[f.Name]; f.Required && !given {
			return fmt.Errorf(".%s: every shape of %s says it (`null` for any)", f.Name, v.Name)
		}
	}
	return nil
}

// checkAcross is the rules across blocks, which no one document can say. Two repositories never
// check out into one directory, which is a repository's name (`acme/app` and `other/app` would
// both be `app`). And a repository's own tracker, and its forge's pull requests and issues, are
// those of a repository the run checks out, so each such block's repo is a `git` block's.
func (e Environment) checkAcross() error {
	repos, dirs := map[string]bool{}, map[string]string{}
	git := e.Systems["git"]
	for i, b := range git {
		r := Canonical(FormRepo, b.Attributes["repo"])
		repos[r] = true
		dir := r[strings.LastIndex(r, "/")+1:]
		if other, dup := dirs[dir]; dup && other != r {
			return fmt.Errorf("%s: %s and %s would both check out as %s/", blockAt("git", i, len(git)), other, r, dir)
		}
		dirs[dir] = r
	}
	for _, system := range []string{"git-issue", "github", "github-issue"} {
		blocks := e.Systems[system]
		for i, b := range blocks {
			if r := Canonical(FormRepo, b.Attributes["repo"]); r != "" && !repos[r] {
				return fmt.Errorf("%s: %s is not checked out: it is no `git` block's repo", blockAt(system, i, len(blocks)), r)
			}
		}
	}
	return nil
}

// Allows is whether a step is within the environment: its system is here, a block of it is the
// step's (the one whose unique attribute the step carries, or the system's one block), the
// block gives the verb, and a shape of it matches the step's fields.
func (e Environment) Allows(step string, fields *ir.Node) bool {
	system, verb, ok := Split(step)
	if !ok {
		return false
	}
	d, ok := Get(system)
	if !ok {
		return false
	}
	unique, hasUnique := d.Unique()
	for _, b := range e.Systems[system] {
		if hasUnique && Canonical(unique.Form, FieldText(fields, unique.Name)) != Canonical(unique.Form, b.Attributes[unique.Name]) {
			continue
		}
		if b.differs(d, fields) != "" {
			continue
		}
		for _, s := range b.Given(system)[verb] {
			if s.Matches(step, fields) {
				return true
			}
		}
	}
	return false
}

// differs is an attribute the block gives that the step carries another value of, "" where
// there is none: the runner fills a step's attributes from the block it acts for, so these agree
// by construction, and a step whose do not is no step of the block's.
func (b Block) differs(d Document, fields *ir.Node) string {
	for _, a := range d.Attributes {
		v, given := b.Attributes[a.Name]
		if !given || stepValue(fields, a.Name) == nil {
			continue
		}
		if Canonical(a.Form, FieldText(fields, a.Name)) != Canonical(a.Form, v) {
			return a.Name
		}
	}
	return ""
}

// Refuses is why a step is outside the environment, in words an agent can correct its call by:
// the system or the block it lacks, or the verb the block does not give, naming what is there;
// or, for a step of the right block and verb that no shape matches, the shapes it does give. ok
// is false for a step the environment allows.
func (e Environment) Refuses(step string, fields *ir.Node) (why string, ok bool) {
	if e.Allows(step, fields) {
		return "", false
	}
	system, verb, valid := Split(step)
	if !valid {
		return fmt.Sprintf("%s is no step of a system", step), true
	}
	blocks := e.Systems[system]
	if len(blocks) == 0 {
		if e.reachesNothing() {
			return fmt.Sprintf("the environment gives no system, so nothing is within it: the spawn names the systems "+
				"its task works with under environment.systems (`%s`, with the verbs it may use)", system), true
		}
		return fmt.Sprintf("the environment gives no `%s` (it gives: %s)", system,
			strings.Join(slices.Sorted(maps.Keys(e.Systems)), ", ")), true
	}
	d, _ := Get(system)
	unique, hasUnique := d.Unique()
	var b *Block
	var named []string
	for i := range blocks {
		v := blocks[i].Attributes[unique.Name]
		named = append(named, v)
		if !hasUnique || Canonical(unique.Form, FieldText(fields, unique.Name)) == Canonical(unique.Form, v) {
			b = &blocks[i]
			break
		}
	}
	if b == nil {
		return fmt.Sprintf("the environment gives `%s` no block for %s %s (it has: %s)",
			system, unique.Name, FieldText(fields, unique.Name), strings.Join(named, ", ")), true
	}
	where := "`" + system + "`"
	if hasUnique {
		where = fmt.Sprintf("`%s` for %s", system, b.Attributes[unique.Name])
	}
	if name := b.differs(d, fields); name != "" {
		return fmt.Sprintf("the environment's %s has %s %s, and the step's is %s", where, name, b.Attributes[name], FieldText(fields, name)), true
	}
	given := b.Given(system)
	shapes, gives := given[verb]
	if !gives {
		return fmt.Sprintf("the environment's %s gives no `%s` (it gives: %s)", where, verb,
			cmp.Or(strings.Join(slices.Sorted(maps.Keys(given)), ", "), "no verb")), true
	}
	words := make([]string, len(shapes))
	for i, sh := range shapes {
		words[i] = sh.String()
	}
	return fmt.Sprintf("no shape of `%s` the environment's %s gives matches (%s): it gives %s",
		verb, where, stepWords(d, verb, fields), strings.Join(words, ", ")), true
}

// reachesNothing is whether the environment gives no system a step is of: none, or only the
// network, whose connections are not steps a doer calls.
func (e Environment) reachesNothing() bool {
	for name := range e.Systems {
		if name != "network" {
			return false
		}
	}
	return true
}

// String is a shape as a spawn writes it, its fields in order: `{ref: !glob refs/heads/*, own: true}`.
func (s Shape) String() string {
	fs := make([]string, 0, len(s))
	for _, k := range slices.Sorted(maps.Keys(s)) {
		fs = append(fs, k+": "+Words(s[k]))
	}
	return "{" + strings.Join(fs, ", ") + "}"
}

// stepWords is a step's fields as a refusal quotes them beside the shapes: the verb's own, since
// the block's attributes are no shape's to name (the refusal says which block it is).
func stepWords(d Document, verb string, fields *ir.Node) string {
	fields = ir.Uncomment(fields)
	if fields == nil || fields.Type != ir.ObjectType {
		return ""
	}
	fs := make([]string, 0, len(fields.Fields))
	for i, k := range fields.Fields {
		if _, isAttr := d.Attribute(k.String); isAttr {
			continue
		}
		fs = append(fs, k.String+"="+ValueText(FormOf(StepName(d.Name, verb), k.String), fields.Values[i]))
	}
	return strings.Join(fs, " ")
}

// Reaches is whether a row's match can match some step the environment allows: its system is
// here, a block of it whose attributes the row's params name it by, giving one of the verbs the
// row names (any, for nil), whose step carries every field the row names, with a shape each of whose patterns has
// a value in common with the row's (FieldMeets) where both name a field. A row that can match nothing is a mistake (it never fires), while a row
// wider than the environment is not: the environment bounds it. An attribute still to be bound at
// the firing (`$[…]`, Templated) could be anything, and is taken to match.
func (e Environment) Reaches(system string, verbs []string, params map[string]*ir.Node) bool {
	d, ok := Get(system)
	if !ok {
		return false
	}
	for _, b := range e.Blocks(system) {
		named := true
		for name, pattern := range params {
			if a, isAttr := d.Attribute(name); isAttr {
				v := b.Attributes[name]
				if exprs.Templated(v) {
					named = named && meetsWritten(a.Form, pattern, v)
				} else if !MatchField(a.Form, pattern, a.Field().Value(v)) {
					named = false
				}
			}
		}
		if !named {
			continue
		}
		given := b.Given(system)
		candidates := verbs
		if candidates == nil {
			candidates = slices.Collect(maps.Keys(given))
		}
		for _, v := range candidates {
			if !carries(d, v, params) {
				continue // its step has no such field, so no call of it is the row's
			}
			for _, sh := range given[v] {
				if sh.Meets(StepName(system, v), params) {
					return true
				}
			}
		}
	}
	return false
}

// THE EMPTY BLOCK. A block that names no verb gives every verb of its system, any call:
// `files: {}`, `git-issue: {repo: acme/app}`. Once one verb is written, only the verbs written are
// given. The network is apart: its reach is its mode, and a block of a mode alone gives no
// connection beyond it (its `tcp-client` shapes are what allow names).

// Given is the verbs a block gives of system, with their shapes: those it names, or, naming
// none, every verb of the system's document with one shape that names nothing (THE EMPTY BLOCK).
func (b Block) Given(system string) map[string][]Shape {
	return given(system, b.Verbs)
}

func given(system string, verbs map[string][]Shape) map[string][]Shape {
	if len(verbs) > 0 || system == "network" {
		return verbs
	}
	d, _ := Get(system)
	out := make(map[string][]Shape, len(d.Verbs))
	for _, v := range d.Verbs {
		out[v.Name] = []Shape{{}}
	}
	return out
}

// Gives is whether some block of a step's system gives its verb: whether the step can occur at
// all, so a doer is offered its tool. Which calls of it are within is Allows'.
func (e Environment) Gives(step string) bool {
	system, verb, ok := Split(step)
	if !ok {
		return false
	}
	for _, b := range e.Systems[system] {
		if _, gives := b.Given(system)[verb]; gives {
			return true
		}
	}
	return false
}

// carries is whether a verb's step has every field the row's params name.
func carries(d Document, verb string, params map[string]*ir.Node) bool {
	fields, ok := d.StepFields(verb)
	if !ok {
		return false
	}
	for name := range params {
		if !slices.ContainsFunc(fields, func(f Field) bool { return f.Name == name }) {
			return false
		}
	}
	return true
}

// Blocks is a system's blocks in the environment.
func (e Environment) Blocks(system string) []Block { return e.Systems[system] }

// Meets is whether a shape and a row's params can both hold of one step: each field both name
// has a value both patterns could match (FieldMeets). A field only one names is free, but for
// one the shape leaves out that has a default, which the shape means (filled).
func (s Shape) Meets(step string, params map[string]*ir.Node) bool {
	s = filled(step, s)
	for name, pattern := range params {
		if sp, ok := s[name]; ok && !FieldMeets(FormOf(step, name), sp, pattern) {
			return false
		}
	}
	return true
}

// Matches is whether a step's fields are of the shape, a field the shape leaves out that has a
// default read as the default (filled).
func (s Shape) Matches(step string, fields *ir.Node) bool {
	for name, pattern := range filled(step, s) {
		if !MatchField(FormOf(step, name), pattern, stepValue(fields, name)) {
			return false
		}
	}
	return true
}

// Canonical is a value as its form spells it once: a `repo` is `host/owner/name` in lower case,
// `owner/name` being github.com's.
func Canonical(form, v string) string {
	if form != FormRepo || v == "" || exprs.Templated(v) {
		return v
	}
	// `owner/name` is github.com's. A first segment with a dot is a host, which no owner's name
	// has: so a glob of a host's, `git.example.com/*`, is not read as an owner on github.com.
	if first, _, _ := strings.Cut(v, "/"); strings.Count(v, "/") == 1 && !strings.Contains(first, ".") {
		v = "github.com/" + v
	}
	return strings.ToLower(v)
}

func fieldOf(v Verb, name string) (Field, bool) {
	for _, f := range v.Fields {
		if f.Name == name {
			return f, true
		}
	}
	return Field{}, false
}

func fieldNames(v Verb) []string {
	out := make([]string, len(v.Fields))
	for i, f := range v.Fields {
		out[i] = f.Name
	}
	return out
}

func attributeNames(d Document) []string {
	out := make([]string, len(d.Attributes))
	for i, a := range d.Attributes {
		out[i] = a.Name
	}
	return out
}

func verbNames(d Document) []string {
	out := make([]string, len(d.Verbs))
	for i, v := range d.Verbs {
		out[i] = v.Name
	}
	return out
}

func names() []string {
	var out []string
	for _, d := range All() {
		out = append(out, d.Name)
	}
	return out
}

// FromTonyIR reads each system as a block, `{…}`, or a list of blocks. A unique attribute's value
// is read in its canonical spelling (Canonical), so `acme/app` and `GitHub.com/Acme/App` are one
// repository.
func (s *Systems) FromTonyIR(node *ir.Node, opts ...gomap.UnmapOption) error {
	node = ir.Uncomment(node)
	if node == nil || node.Type == ir.NullType {
		*s = nil
		return nil
	}
	if node.Type != ir.ObjectType {
		return errors.New("systems: an object of systems, `{git: […], slack: {…}}`")
	}
	out := Systems{}
	for i, key := range node.Fields {
		key = ir.Uncomment(key)
		if key == nil || key.Type != ir.StringType {
			return errors.New("systems: a key is a system's name")
		}
		system := key.String
		val := ir.Uncomment(node.Values[i])
		var items []*ir.Node
		switch {
		case val != nil && val.Type == ir.ArrayType:
			items = val.Values
		case val != nil && val.Type == ir.ObjectType:
			items = []*ir.Node{val}
		default:
			return fmt.Errorf("systems.%s: a block, `{…}`, or a list of blocks", system)
		}
		for j, item := range items {
			b, err := readBlock(system, item)
			if err != nil {
				if len(items) > 1 {
					return fmt.Errorf("systems.%s[%d]: %w", system, j, err)
				}
				return fmt.Errorf("systems.%s: %w", system, err)
			}
			out[system] = append(out[system], b)
		}
	}
	*s = out
	return nil
}

func readBlock(system string, node *ir.Node) (Block, error) {
	node = ir.Uncomment(node)
	if node == nil || node.Type != ir.ObjectType {
		return Block{}, errors.New("a block is an object of attributes and verbs")
	}
	d, _ := Get(system)
	b := Block{Attributes: map[string]string{}, Verbs: map[string][]Shape{}}
	for i, key := range node.Fields {
		key = ir.Uncomment(key)
		if key == nil || key.Type != ir.StringType {
			return Block{}, errors.New("a key is an attribute's or a verb's name")
		}
		name := key.String
		val := ir.Uncomment(node.Values[i])
		switch {
		case val != nil && val.Type == ir.ObjectType:
			s, err := readShape(val)
			if err != nil {
				return Block{}, fmt.Errorf("%s: %w", name, err)
			}
			b.Verbs[name] = []Shape{s}
		case val != nil && val.Type == ir.ArrayType:
			shapes := []Shape{} // empty, not nil: an empty list is said, and refused (Check)
			for j, item := range val.Values {
				item = ir.Uncomment(item)
				if item == nil || item.Type != ir.ObjectType {
					return Block{}, fmt.Errorf("%s[%d]: a shape is an object of fields", name, j)
				}
				s, err := readShape(item)
				if err != nil {
					return Block{}, fmt.Errorf("%s[%d]: %w", name, j, err)
				}
				shapes = append(shapes, s)
			}
			b.Verbs[name] = shapes
		default:
			v, err := scalar(val)
			if err != nil {
				return Block{}, fmt.Errorf("%s: %w", name, err)
			}
			if a, ok := d.Attribute(name); ok {
				v = Canonical(a.Form, v)
			}
			b.Attributes[name] = v
		}
	}
	return b, nil
}

func readShape(node *ir.Node) (Shape, error) {
	s := Shape{}
	for i, key := range node.Fields {
		key = ir.Uncomment(key)
		if key == nil || key.Type != ir.StringType {
			return nil, errors.New("a shape's key is a field's name")
		}
		s[key.String] = Plain(ir.Uncomment(node.Values[i]))
	}
	return s, nil
}

// scalar is a value as text: a string, or a boolean or a number as one would spell it.
func scalar(n *ir.Node) (string, error) {
	switch {
	case n == nil || n.Type == ir.NullType:
		return "", errors.New("null is no value; leave the key out")
	case n.Type == ir.StringType:
		return n.String, nil
	case n.Type == ir.BoolType:
		return strconv.FormatBool(n.Bool), nil
	case n.Type == ir.NumberType:
		switch {
		case n.Number != "":
			return n.Number, nil
		case n.Int64 != nil:
			return strconv.FormatInt(*n.Int64, 10), nil
		case n.Float64 != nil:
			return strconv.FormatFloat(*n.Float64, 'g', -1, 64), nil
		}
	}
	return "", errors.New("a value is a string, a boolean or a number")
}

// ToTonyIR writes each system as its document has it: a list of blocks for a system with a
// unique attribute, a block for any other.
func (s Systems) ToTonyIR(opts ...gomap.MapOption) (*ir.Node, error) {
	var kvs []ir.KeyVal
	for _, system := range slices.Sorted(maps.Keys(s)) {
		blocks := s[system]
		var val *ir.Node
		if d, ok := Get(system); ok && len(blocks) == 1 {
			if _, many := d.Unique(); !many {
				val = blocks[0].node()
			}
		}
		if val == nil {
			items := make([]*ir.Node, len(blocks))
			for i, b := range blocks {
				items[i] = b.node()
			}
			val = ir.FromSlice(items)
		}
		kvs = append(kvs, ir.KeyVal{Key: ir.FromString(system), Val: val})
	}
	return ir.FromKeyVals(kvs), nil
}

func (b Block) node() *ir.Node {
	var kvs []ir.KeyVal
	for _, name := range slices.Sorted(maps.Keys(b.Attributes)) {
		kvs = append(kvs, ir.KeyVal{Key: ir.FromString(name), Val: ir.FromString(b.Attributes[name])})
	}
	for _, verb := range slices.Sorted(maps.Keys(b.Verbs)) {
		shapes := b.Verbs[verb]
		var val *ir.Node
		if len(shapes) == 1 {
			val = shapes[0].node()
		} else {
			items := make([]*ir.Node, len(shapes))
			for i, s := range shapes {
				items[i] = s.node()
			}
			val = ir.FromSlice(items)
		}
		kvs = append(kvs, ir.KeyVal{Key: ir.FromString(verb), Val: val})
	}
	return ir.FromKeyVals(kvs)
}

func (s Shape) node() *ir.Node {
	var kvs []ir.KeyVal
	for _, name := range slices.Sorted(maps.Keys(s)) {
		v := s[name]
		if v == nil {
			v = ir.Null()
		}
		kvs = append(kvs, ir.KeyVal{Key: ir.FromString(name), Val: v.Clone()})
	}
	return ir.FromKeyVals(kvs)
}

// IsZero reports an environment that says nothing: no variables and no systems.
func (e Environment) IsZero() bool { return len(e.Vars) == 0 && len(e.Systems) == 0 }

// ToTonyIR writes `{vars, systems}`, each only when there is one.
func (e Environment) ToTonyIR(opts ...gomap.MapOption) (*ir.Node, error) {
	var kvs []ir.KeyVal
	if len(e.Vars) > 0 {
		var vars []ir.KeyVal
		for _, name := range slices.Sorted(maps.Keys(e.Vars)) {
			vars = append(vars, ir.KeyVal{Key: ir.FromString(name), Val: ir.FromString(e.Vars[name])})
		}
		kvs = append(kvs, ir.KeyVal{Key: ir.FromString("vars"), Val: ir.FromKeyVals(vars)})
	}
	if len(e.Systems) > 0 {
		sys, err := e.Systems.ToTonyIR(opts...)
		if err != nil {
			return nil, err
		}
		kvs = append(kvs, ir.KeyVal{Key: ir.FromString("systems"), Val: sys})
	}
	return ir.FromKeyVals(kvs), nil
}

// FromTonyIR reads `{vars, systems}`, strictly: a key that is neither is refused, since a
// misspelled `systems` dropped would leave the run reaching nothing it was meant to.
func (e *Environment) FromTonyIR(node *ir.Node, opts ...gomap.UnmapOption) error {
	node = ir.Uncomment(node)
	if node == nil || node.Type == ir.NullType {
		*e = Environment{}
		return nil
	}
	if node.Type != ir.ObjectType {
		return errors.New("environment: an object, `{vars: {…}, systems: {…}}`")
	}
	out := Environment{}
	for i, key := range node.Fields {
		key = ir.Uncomment(key)
		if key == nil || key.Type != ir.StringType {
			return errors.New("environment: a key is `vars` or `systems`")
		}
		val := node.Values[i]
		switch key.String {
		case "vars":
			v := ir.Uncomment(val)
			if v == nil || v.Type == ir.NullType {
				continue
			}
			if v.Type != ir.ObjectType {
				return errors.New("environment.vars: an object of variables")
			}
			out.Vars = map[string]string{}
			for j, k := range v.Fields {
				k = ir.Uncomment(k)
				if k == nil || k.Type != ir.StringType {
					return errors.New("environment.vars: a key is a variable's name")
				}
				s, err := scalar(ir.Uncomment(v.Values[j]))
				if err != nil {
					return fmt.Errorf("environment.vars.%s: %w", k.String, err)
				}
				out.Vars[k.String] = s
			}
		case "systems":
			if err := out.Systems.FromTonyIR(val, opts...); err != nil {
				return fmt.Errorf("environment.%w", err)
			}
		default:
			return fmt.Errorf("environment: %q is not `vars` or `systems`", key.String)
		}
	}
	*e = out
	return nil
}
