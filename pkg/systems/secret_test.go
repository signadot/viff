package systems

import (
	"strings"
	"testing"

	"github.com/signadot/tony-format/go-tony/ir"
)

// exec is a secret.exec step's fields as the runner fills them: the block's secret and
// variable, the command spelled, where, stdin always said (false here, as a plain run's), and
// the plain variables given, an object.
func exec(t *testing.T, name, as, argv, dir, env string) *ir.Node {
	t.Helper()
	return execIn(t, name, as, argv, dir, env, "false")
}

// execIn is exec with stdin as given, "true" for an interactive run.
func execIn(t *testing.T, name, as, argv, dir, env, stdin string) *ir.Node {
	t.Helper()
	fields := FieldsDoc("secret.exec", map[string]string{"name": name, "as": as, "argv": argv, "dir": dir, "stdin": stdin})
	var kvs []ir.KeyVal
	for i, k := range fields.Fields {
		kvs = append(kvs, ir.KeyVal{Key: k, Val: fields.Values[i]})
	}
	return ir.FromKeyVals(append(kvs, ir.KeyVal{Key: ir.FromString("env"), Val: pat(t, env)}))
}

// VARIABLES ARE A FORM OF THEIR OWN, and a pattern on them is an object of names with a pattern on
// each value: a key that is no variable's name, a value that is no pattern over text, and a
// pattern that reads the whole as text are refused.
func TestAPatternOverVariablesIsChecked(t *testing.T) {
	f, _ := FieldOf("secret.exec", "env")
	if f.Form != FormEnv || f.Default != "{}" {
		t.Fatalf("env: %+v", f)
	}
	for text, want := range map[string]string{
		`null`:                 "",
		`{}`:                   "",
		`{NODE_OPTIONS: null}`: "",
		`{NPM_CONFIG_REGISTRY: !regexp "^https://"}`: "",
		`{A: x, B: !glob "y*"}`:                      "",
		`!or [{A: null}, {B: null}]`:                 "",
		`{"1st": x}`:                                 "a key is a variable's name",
		`{"A B": x}`:                                 "a key is a variable's name",
		`{A: [x, y]}`:                                "A: a list is no value",
		`{A: "x*"}`:                                  "A: \"x*\" holds a `*`",
		`{A: !gloob "x"}`:                            "!gloob is no operator",
		`!glob "A*"`:                                 "!glob matches text, and the field is variables by name",
		`!regexp "^A"`:                               "!regexp matches text, and the field is variables by name",
		`x`:                                          "the field is variables by name",
		`[A, B]`:                                     "the field is variables by name",
	} {
		err := f.Accepts(pat(t, text))
		if want == "" && err != nil || want != "" && (err == nil || !strings.Contains(err.Error(), want)) {
			t.Errorf("%s: %v, want %q", text, err, want)
		}
	}
	as, _ := FieldOf("secret.exec", "as")
	if as.Form != FormVar {
		t.Fatalf("as: %+v", as)
	}
	for text, want := range map[string]string{
		`NPM_TOKEN`:         "",
		`!glob "NPM_*"`:     "",
		`!regexp "_TOKEN$"`: "",
		`"1ST"`:             "a variable's name",
		`"NPM TOKEN"`:       "a variable's name",
		`""`:                "never empty",
	} {
		err := as.Accepts(pat(t, text))
		if want == "" && err != nil || want != "" && (err == nil || !strings.Contains(err.Error(), want)) {
			t.Errorf("as %s: %v, want %q", text, err, want)
		}
	}
}

// VARIABLES READ TWO WAYS, as an address does. Within, the step's variables lie among those the
// pattern names, each matching, and one it does not name puts the step outside: what a shape, a
// ceiling and an allow row mean. Touches, the step gives each the pattern names, matching: what
// a deny or an ask row means, and what tony's own object match is.
func TestVariablesReadWithinAndTouching(t *testing.T) {
	for _, c := range []struct {
		pattern, value  string
		within, touches bool
	}{
		{`null`, `{A: x}`, true, true},
		{`{}`, `{}`, true, true},
		{`{}`, `{A: x}`, false, true},
		{`{A: null}`, `{}`, true, false},
		{`{A: null}`, `{A: x}`, true, true},
		{`{A: null}`, `{A: x, B: y}`, false, true},
		{`{A: x}`, `{A: y}`, false, false},
		{`{A: !regexp "^https://"}`, `{A: "https://registry.npmjs.org/"}`, true, true},
		{`{A: !regexp "^https://"}`, `{A: "http://evil"}`, false, false},
		{`{A: null, B: null}`, `{A: x}`, true, false},
		{`{LD_PRELOAD: null}`, `{NODE_OPTIONS: x}`, false, false},
		{`{LD_PRELOAD: null}`, `{NODE_OPTIONS: x, LD_PRELOAD: /tmp/x.so}`, false, true},
		{`!or [{A: null}, {B: null}]`, `{B: x}`, true, true},
		{`!not {A: null}`, `{B: x}`, true, true},
		{`!not {A: null}`, `{A: x}`, false, false},
		{`{A: null}`, `x`, false, false},
	} {
		p, v := pat(t, c.pattern), pat(t, c.value)
		if got := MatchFieldAs(FormEnv, Within, p, v); got != c.within {
			t.Errorf("%s within %s: %v, want %v", c.value, c.pattern, got, c.within)
		}
		if got := MatchFieldAs(FormEnv, Touches, p, v); got != c.touches {
			t.Errorf("%s touches %s: %v, want %v", c.value, c.pattern, got, c.touches)
		}
	}
	// One pattern over variables is within another where every name it gives, the other gives,
	// with its pattern within the other's; every two meet, since a step giving none is within both.
	for _, c := range []struct {
		outer, inner string
		within       bool
	}{
		{`null`, `{A: null}`, true},
		{`{A: null}`, `null`, false},
		{`{}`, `{}`, true},
		{`{A: null, B: null}`, `{A: x}`, true},
		{`{A: null}`, `{A: x, B: y}`, false},
		{`{A: !regexp "^https://"}`, `{A: "https://x"}`, true},
		{`{A: !regexp "^https://"}`, `{A: null}`, false},
		{`{A: !regexp "^https://"}`, `{A: !glob "https://*"}`, true},
	} {
		outer, inner := pat(t, c.outer), pat(t, c.inner)
		if got := FieldContains(FormEnv, outer, inner); got != c.within {
			t.Errorf("%s within %s: %v, want %v", c.inner, c.outer, got, c.within)
		}
		if !FieldMeets(FormEnv, outer, inner) {
			t.Errorf("%s and %s do not meet", c.inner, c.outer)
		}
	}
}

// A SHAPE THAT SAYS NOTHING OF `env` GIVES NONE: the field's default, `{}`, is what a shape of
// the environment and a shape of the ceiling mean by leaving it out, so plain variables are
// given one by one; `env: null` is any. A row's match is not read so: a row names what it names.
func TestAShapeGivesNoVariableUnlessItNamesIt(t *testing.T) {
	e, err := readEnv(t, `systems: {secret: [
	  {name: npm-token, as: NPM_TOKEN, exec: [{argv: "npm publish --ignore-scripts"}, {argv: !regexp "^npm view ", env: {NODE_OPTIONS: null}}]},
	  {name: any-token, as: T, exec: {argv: !regexp "^x ", env: null}}]}`)
	if err != nil {
		t.Fatal(err)
	}
	if err := e.Check(); err != nil {
		t.Fatal(err)
	}
	step := func(name, as, argv, env string) *ir.Node { return exec(t, name, as, argv, "", env) }
	for _, c := range []struct {
		name, as, argv, env string
		allowed             bool
	}{
		{"npm-token", "NPM_TOKEN", "npm publish --ignore-scripts", `{}`, true},
		{"npm-token", "NPM_TOKEN", "npm publish --ignore-scripts", `{NODE_OPTIONS: x}`, false},
		{"npm-token", "NPM_TOKEN", "npm view left-pad", `{NODE_OPTIONS: x}`, true},
		{"npm-token", "NPM_TOKEN", "npm view left-pad", `{NODE_OPTIONS: x, LD_PRELOAD: y}`, false},
		{"npm-token", "NPM_TOKEN", "npm view left-pad", `{}`, true},
		{"npm-token", "OTHER", "npm publish --ignore-scripts", `{}`, false},
		{"other", "NPM_TOKEN", "npm publish --ignore-scripts", `{}`, false},
		{"any-token", "T", "x y", `{LD_PRELOAD: y, A: b}`, true},
	} {
		if got := e.Allows("secret.exec", step(c.name, c.as, c.argv, c.env)); got != c.allowed {
			why, _ := e.Refuses("secret.exec", step(c.name, c.as, c.argv, c.env))
			t.Errorf("%s %s %q %s: allowed %v, want %v (%s)", c.name, c.as, c.argv, c.env, got, c.allowed, why)
		}
	}
	// The refusal names the shapes as written, and the block by its secret.
	why, refused := e.Refuses("secret.exec", step("npm-token", "NPM_TOKEN", "npm publish --ignore-scripts", `{NODE_OPTIONS: x}`))
	if !refused || !strings.Contains(why, "`secret` for npm-token") || !strings.Contains(why, "env={NODE_OPTIONS: x}") {
		t.Errorf("the refusal: %s", why)
	}
	// A ceiling that says nothing of `env` holds a shape that says nothing, and not one that gives
	// a variable or any; one that names variables holds a shape naming some of them, within.
	c := readCeiling(t, `{name: npm-token, as: NPM_TOKEN, exec: [{argv: !regexp "^npm publish "}, {argv: !regexp "^npm view ", env: {NODE_OPTIONS: null, NPM_CONFIG_REGISTRY: !regexp "^https://registry\\.npmjs\\.org/"}}]}`)
	if err := c.Check("secret"); err != nil {
		t.Fatal(err)
	}
	for text, want := range map[string]string{
		`{name: npm-token, as: NPM_TOKEN, exec: {argv: "npm publish --ignore-scripts"}}`:                                          "",
		`{name: npm-token, as: NPM_TOKEN, exec: {argv: "npm publish --ignore-scripts", env: {}}}`:                                 "",
		`{name: npm-token, as: NPM_TOKEN, exec: {argv: "npm publish --ignore-scripts", env: {NODE_OPTIONS: null}}}`:               "within no shape",
		`{name: npm-token, as: NPM_TOKEN, exec: {argv: "npm publish --ignore-scripts", env: null}}`:                               "within no shape",
		`{name: npm-token, as: NPM_TOKEN, exec: {argv: "npm view x", env: {NODE_OPTIONS: null}}}`:                                 "",
		`{name: npm-token, as: NPM_TOKEN, exec: {argv: "npm view x", env: {NPM_CONFIG_REGISTRY: "https://registry.npmjs.org/"}}}`: "",
		`{name: npm-token, as: NPM_TOKEN, exec: {argv: "npm view x", env: {NPM_CONFIG_REGISTRY: "https://evil.example/"}}}`:       "within no shape",
		`{name: npm-token, as: NPM_TOKEN, exec: {argv: "npm view x", env: {NODE_OPTIONS: null, LD_PRELOAD: null}}}`:               "within no shape",
		`{name: npm-token, as: OTHER, exec: {argv: "npm view x"}}`:                                                                "as OTHER is beyond NPM_TOKEN",
		`{name: other, as: NPM_TOKEN, exec: {argv: "npm view x"}}`:                                                                "name other is beyond npm-token",
	} {
		e, err := readEnv(t, `systems: {secret: `+text+`}`)
		if err != nil {
			t.Fatal(err)
		}
		if err := e.Check(); err != nil {
			t.Fatalf("%s: %v", text, err)
		}
		err = c.Contains("secret", e.Systems["secret"][0])
		if want == "" && err != nil || want != "" && (err == nil || !strings.Contains(err.Error(), want)) {
			t.Errorf("%s: %v, want %q", text, err, want)
		}
	}
	// A row reaches a shape that says nothing of env with a match naming a variable: the row names
	// what it names, and a step giving no variable meets both.
	if !e.Reaches("secret", []string{"exec"}, map[string]*ir.Node{"env": pat(t, `{LD_PRELOAD: null}`)}) {
		t.Error("a row on LD_PRELOAD does not reach the environment")
	}
	// The environment is checked: `as` a variable's name, `name` lower case, required both.
	for text, want := range map[string]string{
		`{name: npm-token, as: "npm token", exec: {}}`:      "as: \"npm token\": the field is a variable's name",
		`{name: NPM, as: T, exec: {}}`:                      "name: \"NPM\": the field is lowercase",
		`{name: npm-token, exec: {}}`:                       "as: required",
		`{as: T, exec: {}}`:                                 "name: required",
		`{name: npm-token, as: T, exec: {env: {"a b": x}}}`: "exec.env: a b: a key is a variable's name",
	} {
		e, err := readEnv(t, `systems: {secret: `+text+`}`)
		if err == nil {
			err = e.Check()
		}
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: %v, want %q", text, err, want)
		}
	}
}

// A SHAPE THAT SAYS NOTHING OF `stdin` GIVES NO INPUT: the field's default, false, is what a
// shape of the environment and of the ceiling mean by leaving it out, so an interactive run is
// admitted one shape at a time; `stdin: null` admits either kind, and `stdin: true` only the
// interactive, since a shape names what it names.
func TestAShapeGivesNoStdinUnlessItNamesIt(t *testing.T) {
	f, ok := FieldOf("secret.exec", "stdin")
	if !ok || f.Form != FormBool || f.Default != "false" {
		t.Fatalf("stdin: %+v", f)
	}
	for text, want := range map[string]string{
		`true`:         "",
		`false`:        "",
		`null`:         "",
		`"true"`:       "write true, unquoted",
		`!glob "tru*"`: "!glob matches text, and the field is true or false",
		`x`:            "the field is true or false",
	} {
		err := f.Accepts(pat(t, text))
		if want == "" && err != nil || want != "" && (err == nil || !strings.Contains(err.Error(), want)) {
			t.Errorf("stdin %s: %v, want %q", text, err, want)
		}
	}
	e, err := readEnv(t, `systems: {secret: [
	  {name: npm-token, as: NPM_TOKEN, exec: [{argv: "npm whoami"}]},
	  {name: gpg-key, as: G, exec: {argv: !regexp "^gpg ", stdin: true, env: null}}]}`)
	if err != nil {
		t.Fatal(err)
	}
	if err := e.Check(); err != nil {
		t.Fatal(err)
	}
	step := func(name, as, argv, stdin string) *ir.Node { return execIn(t, name, as, argv, "", `{}`, stdin) }
	for _, c := range []struct {
		name, as, argv, stdin string
		allowed               bool
	}{
		{"npm-token", "NPM_TOKEN", "npm whoami", "false", true},
		{"npm-token", "NPM_TOKEN", "npm whoami", "true", false},
		{"gpg-key", "G", "gpg --decrypt", "true", true},
		{"gpg-key", "G", "gpg --decrypt", "false", false},
	} {
		if got := e.Allows("secret.exec", step(c.name, c.as, c.argv, c.stdin)); got != c.allowed {
			why, _ := e.Refuses("secret.exec", step(c.name, c.as, c.argv, c.stdin))
			t.Errorf("%s %q stdin=%s: allowed %v, want %v (%s)", c.name, c.argv, c.stdin, got, c.allowed, why)
		}
	}
	// A ceiling that says nothing of stdin holds a shape that says nothing, and not one that
	// opens it; one that says `stdin: null` holds every spelling.
	closed := readCeiling(t, `{name: npm-token, as: NPM_TOKEN, exec: {argv: !regexp "^npm "}}`)
	if err := closed.Check("secret"); err != nil {
		t.Fatal(err)
	}
	open := readCeiling(t, `{name: npm-token, as: NPM_TOKEN, exec: {argv: !regexp "^npm ", stdin: null, env: null}}`)
	if err := open.Check("secret"); err != nil {
		t.Fatal(err)
	}
	for text, want := range map[string]string{
		`{name: npm-token, as: NPM_TOKEN, exec: {argv: "npm whoami"}}`:               "",
		`{name: npm-token, as: NPM_TOKEN, exec: {argv: "npm whoami", stdin: false}}`: "",
		`{name: npm-token, as: NPM_TOKEN, exec: {argv: "npm whoami", stdin: true}}`:  "within no shape",
		`{name: npm-token, as: NPM_TOKEN, exec: {argv: "npm whoami", stdin: null}}`:  "within no shape",
	} {
		e, err := readEnv(t, `systems: {secret: `+text+`}`)
		if err != nil {
			t.Fatal(err)
		}
		if err := e.Check(); err != nil {
			t.Fatalf("%s: %v", text, err)
		}
		err = closed.Contains("secret", e.Systems["secret"][0])
		if want == "" && err != nil || want != "" && (err == nil || !strings.Contains(err.Error(), want)) {
			t.Errorf("the closed ceiling, %s: %v, want %q", text, err, want)
		}
		if err := open.Contains("secret", e.Systems["secret"][0]); err != nil {
			t.Errorf("the open ceiling, %s: %v", text, err)
		}
	}
}
