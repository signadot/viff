package runnerd

import (
	"fmt"
	"reflect"
	"sort"
	"strings"

	"github.com/signadot/tony-format/go-tony/ir"

	"go.viff.dev/viff/pkg/codec"
)

// DefaultListen is the address a runner serves viff on when its configuration names none.
const DefaultListen = ":9802"

// Config is every runner's configuration. A runner's own keys sit beside these in the same
// file, and Load reads both.
//
// Secrets are never in the file: a runner reads them from its environment, which is where a
// deployment puts a Kubernetes Secret, or from a secret store the file names the directory of
// (the viff runner's `secrets:`, package secretstore), which is where a deployment mounts one.
type Config struct {
	// Listen is the address the runner serves viff on.
	Listen string `tony:"field=listen,omitzero"`
	// Viff is the one viff this runner serves, by the token viffd presents (viffauth.go).
	// Absent, the runner serves whoever reaches it.
	Viff *Viff `tony:"field=viff,omitzero"`
}

// Check fills in the defaults and validates what is set.
func (c *Config) Check() error {
	if c.Listen == "" {
		c.Listen = DefaultListen
	}
	if c.Viff != nil {
		if err := c.Viff.Check(); err != nil {
			return err
		}
	}
	return nil
}

// Load reads a runner's configuration file into Config and into own, a pointer to the runner's
// own configuration struct, whose keys sit beside Config's in the file. The file's format comes
// from its name.
//
// A key neither knows is refused, at any depth: the decoder drops what it doesn't recognise, so
// a misspelt key would otherwise be a setting silently not applied.
func Load(path string, own any) (Config, error) {
	var c Config
	node, _, err := codec.ReadFileNode(path)
	if err != nil {
		return c, err
	}
	node = ir.Uncomment(node)
	if node == nil || node.Type != ir.ObjectType {
		return c, fmt.Errorf("%s: want an object of settings", path)
	}
	types := []reflect.Type{reflect.TypeOf(c)}
	if own != nil {
		types = append(types, reflect.TypeOf(own).Elem())
	}
	if err := checkFields(node, "", types...); err != nil {
		return c, fmt.Errorf("%s: %w", path, err)
	}
	if err := codec.FromNode(node, &c); err != nil {
		return c, fmt.Errorf("%s: %w", path, err)
	}
	if own != nil {
		if err := codec.FromNode(node, own); err != nil {
			return c, fmt.Errorf("%s: %w", path, err)
		}
	}
	if err := c.Check(); err != nil {
		return c, fmt.Errorf("%s: %w", path, err)
	}
	return c, nil
}

// checkFields refuses a key of node that none of the struct types has a field for, and
// descends into the fields that are structs themselves, and into each value of a map of structs.
// A map's keys are its own to name.
func checkFields(node *ir.Node, at string, types ...reflect.Type) error {
	node = ir.Uncomment(node)
	if node == nil || node.Type != ir.ObjectType {
		return nil // not an object: the decoder's complaint to make, in its own terms
	}
	known := map[string]reflect.Type{}
	for _, t := range types {
		for t.Kind() == reflect.Pointer {
			t = t.Elem()
		}
		if t.Kind() != reflect.Struct {
			continue
		}
		for f := range t.Fields() {
			if name := fieldName(f); name != "" {
				known[name] = f.Type
			}
		}
	}
	for i, field := range node.Fields {
		name := field.String
		ft, ok := known[name]
		if !ok {
			want := make([]string, 0, len(known))
			for k := range known {
				want = append(want, k)
			}
			sort.Strings(want)
			return fmt.Errorf("no setting %q (want any of %s)", at+name, strings.Join(want, ", "))
		}
		if err := checkValue(node.Values[i], at+name, ft); err != nil {
			return err
		}
	}
	return nil
}

// checkValue checks one value of type t at the key at: a struct's fields, and a map's values,
// each at its own key.
func checkValue(node *ir.Node, at string, t reflect.Type) error {
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	switch t.Kind() {
	case reflect.Struct:
		return checkFields(node, at+".", t)
	case reflect.Map:
		node = ir.Uncomment(node)
		if node == nil || node.Type != ir.ObjectType {
			return nil
		}
		for i, field := range node.Fields {
			if err := checkValue(node.Values[i], at+"."+field.String, t.Elem()); err != nil {
				return err
			}
		}
	}
	return nil
}

// fieldName is the key a struct field is read from: its tony `field=` name.
func fieldName(f reflect.StructField) string {
	for part := range strings.SplitSeq(f.Tag.Get("tony"), ",") {
		if name, ok := strings.CutPrefix(part, "field="); ok {
			return name
		}
	}
	return ""
}
