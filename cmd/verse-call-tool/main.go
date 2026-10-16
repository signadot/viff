// Command verse-call-tool calls one tool of the task whose workspace it runs in: `ask`, `push`,
// `fetch`, the pull-request tools, and whatever the task's runner offers. It is the shell's way
// to what the verse agent and codex have as tools of their own, for a coding agent or a script
// the environment runs as a program.
//
//	verse-call-tool                                 the tools on offer here, with their arguments
//	verse-call-tool <tool> -help                    one tool's arguments
//	verse-call-tool push -branch verse/x            one call; the flags are the tool's parameters
//	verse-call-tool ask -question "ship it?" -options yes -options no -within 30m
//	verse-call-tool <tool> -input '<document>'      the whole input as a tony or json document
//
// The result is stdout. Exit 0: it was done. Exit 3: it was refused — by a gate, or by the far
// side about its own rules — and stdout says why; a refusal is an answer to re-plan on, not an
// error to retry. Exit 1: it failed, and stderr says how. Exit 2: the command line was wrong.
//
// The binary knows no tool by name. It asks the task process what is on offer and turns each
// tool's parameters into flags: a string is `-name value`, a boolean `-name`, a list `-name v`
// repeated, a number `-name 3`, an object `-name '<document>'`. A tool the runner adds is a tool
// this command has the next time it runs. The task process answers on the socket $VERSE_TOOLS
// names, or the one `-socket` does.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"sort"
	"strconv"
	"strings"
	"syscall"

	"github.com/signadot/verse/codec"
	"github.com/signadot/verse/runner/runnerd/calltool"
)

const (
	exitOK      = 0
	exitFailed  = 1
	exitUsage   = 2
	exitRefused = 3
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	os.Exit(run(ctx, os.Args[1:], os.Stdin, os.Stdout, os.Stderr))
}

// run is the command: args are the arguments after the program's name, and the result is its
// exit status.
func run(ctx context.Context, args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("verse-call-tool", flag.ContinueOnError)
	fs.SetOutput(stderr)
	socket := fs.String("socket", os.Getenv(calltool.Env),
		"the socket the task process answers on (default $"+calltool.Env+")")
	fs.Usage = func() {
		fmt.Fprint(stderr, "usage: verse-call-tool [-socket <path>] [<tool> [-<param> <value>]... | <tool> -help]\n"+
			"with no tool, lists the tools on offer and their parameters\n\n")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return exitOK
		}
		return exitUsage
	}
	if *socket == "" {
		fmt.Fprintf(stderr, "verse-call-tool: no socket: $%s is unset and -socket was not given; "+
			"this command runs in a verse task's workspace, where the task process sets it\n", calltool.Env)
		return exitUsage
	}
	c := calltool.Dial(*socket)
	tools, err := c.Tools(ctx)
	if err != nil {
		fmt.Fprintf(stderr, "verse-call-tool: %v\n", err)
		return exitFailed
	}
	rest := fs.Args()
	if len(rest) == 0 {
		list(stdout, tools)
		return exitOK
	}
	name := rest[0]
	tool, ok := calltool.Find(tools, name)
	if !ok {
		fmt.Fprintf(stderr, "verse-call-tool: no tool %q here; the tools are: %s\n", name, strings.Join(calltool.Names(tools), ", "))
		return exitUsage
	}
	input, err := parseInput(tool, rest[1:], stdin, stderr)
	switch {
	case errors.Is(err, flag.ErrHelp):
		return exitOK
	case err != nil:
		fmt.Fprintf(stderr, "verse-call-tool %s: %v\n", name, err)
		return exitUsage
	}
	res, err := c.Call(ctx, name, input)
	if err != nil {
		fmt.Fprintf(stderr, "verse-call-tool: %v\n", err)
		return exitFailed
	}
	if res.Out != "" {
		fmt.Fprintln(stdout, res.Out)
	}
	if res.Refused {
		return exitRefused
	}
	return exitOK
}

// list is the tools on offer, each with its description and parameters.
func list(w io.Writer, tools []calltool.Tool) {
	if len(tools) == 0 {
		fmt.Fprintln(w, "no tools are on offer here")
		return
	}
	for i, t := range tools {
		if i > 0 {
			fmt.Fprintln(w)
		}
		fmt.Fprintf(w, "%s\n", t.Name)
		if t.Description != "" {
			fmt.Fprint(w, wrap(t.Description, "    ", 92))
		}
		for _, p := range params(t) {
			fmt.Fprintf(w, "    %s\n", p.line())
		}
	}
}

// param is one of a tool's parameters as a flag: what its schema says about it.
type param struct {
	name, typ, items, desc string
	enum                   []string
	required               bool
}

// params reads a tool's parameters from its schema, required ones first and then by name, so a
// listing reads the same way each time.
func params(t calltool.Tool) []param {
	var out []param
	for name, raw := range t.Params {
		p := param{name: name, typ: "string"}
		if m, ok := raw.(map[string]any); ok {
			if s, ok := m["type"].(string); ok && s != "" {
				p.typ = s
			}
			p.desc, _ = m["description"].(string)
			if items, ok := m["items"].(map[string]any); ok {
				p.items, _ = items["type"].(string)
			}
			if e, ok := m["enum"].([]any); ok {
				for _, v := range e {
					p.enum = append(p.enum, fmt.Sprint(v))
				}
			}
		}
		for _, r := range t.Required {
			if r == name {
				p.required = true
			}
		}
		out = append(out, p)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].required != out[j].required {
			return out[i].required
		}
		return out[i].name < out[j].name
	})
	return out
}

// line is the parameter as a listing shows it: the flag, then what it takes.
func (p param) line() string {
	if u := p.usage(); u != "" {
		return "-" + p.name + " " + u
	}
	return "-" + p.name
}

func (p param) usage() string {
	var b strings.Builder
	switch p.typ {
	case "boolean":
		b.WriteString("(or -" + p.name + "=false)")
	case "array":
		b.WriteString("<" + orString(p.items) + ">  (repeat for each)")
	case "object":
		b.WriteString("<document>")
	default:
		b.WriteString("<" + p.typ + ">")
	}
	if len(p.enum) > 0 {
		fmt.Fprintf(&b, "  one of: %s", strings.Join(p.enum, ", "))
	}
	if p.required {
		b.WriteString("  (required)")
	}
	if p.desc != "" {
		b.WriteString("\n" + strings.TrimRight(wrap(p.desc, "        ", 92), "\n"))
	}
	return b.String()
}

func orString(s string) string {
	if s == "" {
		return "string"
	}
	return s
}

// parseInput reads one tool's arguments as the call's input. Only what was given is sent: a
// parameter left unsaid is absent from the input, which is what a tool reads as "default".
// `-input` is the whole input as a document, with any flags given laid over it; `-input -` reads
// the document from stdin.
func parseInput(t calltool.Tool, args []string, stdin io.Reader, stderr io.Writer) (map[string]any, error) {
	ps := params(t)
	for _, p := range ps {
		// The server refuses such a tool (calltool.Describe); said here too, for a server
		// that did not, since defining the flag twice would end this command with a stack trace.
		if p.name == calltool.Reserved {
			return nil, fmt.Errorf("%s has a parameter named %q, which this command reserves for the whole input", t.Name, calltool.Reserved)
		}
	}
	fs := flag.NewFlagSet(t.Name, flag.ContinueOnError)
	fs.SetOutput(stderr)
	doc := fs.String(calltool.Reserved, "", "the whole input as a tony or json document; - reads it from stdin")
	values := map[string]flag.Value{}
	for _, p := range ps {
		v := p.value()
		values[p.name] = v
		fs.Var(v, p.name, p.usage())
	}
	fs.Usage = func() {
		fmt.Fprintf(stderr, "usage: verse-call-tool %s [-<param> <value>]...\n", t.Name)
		if t.Description != "" {
			fmt.Fprint(stderr, wrap(t.Description, "    ", 92))
		}
		fmt.Fprintln(stderr)
		for _, p := range ps {
			fmt.Fprintf(stderr, "  %s\n", p.line())
		}
		fmt.Fprintln(stderr, "  -input <document>  the whole input as a tony or json document; - reads it from stdin")
	}
	if err := fs.Parse(args); err != nil {
		return nil, err
	}
	if fs.NArg() > 0 {
		if len(ps) == 0 {
			return nil, fmt.Errorf("unexpected argument %q: %s takes no parameters", fs.Arg(0), t.Name)
		}
		return nil, fmt.Errorf("unexpected argument %q: a tool's parameters are flags, -<name> <value>; "+
			"a boolean is -<name> or -<name>=false", fs.Arg(0))
	}
	input := map[string]any{}
	if *doc != "" {
		text := *doc
		if text == "-" {
			b, err := io.ReadAll(stdin)
			if err != nil {
				return nil, fmt.Errorf("reading the input from stdin: %w", err)
			}
			text = string(b)
		}
		if err := codec.Unmarshal([]byte(text), &input, codec.Tony); err != nil {
			return nil, fmt.Errorf("-input: %w", err)
		}
		if input == nil {
			input = map[string]any{}
		}
	}
	var perr error
	fs.Visit(func(f *flag.Flag) {
		if f.Name == calltool.Reserved {
			return
		}
		v, err := values[f.Name].(paramValue).value()
		if err != nil {
			perr = fmt.Errorf("-%s: %w", f.Name, err)
			return
		}
		input[f.Name] = v
	})
	if perr != nil {
		return nil, perr
	}
	// The whole input, however it was given, against the tool's schema: what -input carried is
	// checked as the flags were.
	if err := calltool.Check(t, input); err != nil {
		return nil, err
	}
	return input, nil
}

// paramValue is a flag.Value that also yields the typed value the input carries.
type paramValue interface {
	flag.Value
	value() (any, error)
}

func (p param) value() paramValue {
	switch p.typ {
	case "boolean":
		return &boolValue{}
	case "array":
		return &listValue{items: p.items}
	case "integer":
		return &numberValue{integer: true}
	case "number":
		return &numberValue{}
	case "object":
		return &docValue{}
	default:
		return &stringValue{}
	}
}

type stringValue struct{ s string }

func (v *stringValue) String() string      { return v.s }
func (v *stringValue) Set(s string) error  { v.s = s; return nil }
func (v *stringValue) value() (any, error) { return v.s, nil }

type boolValue struct{ b bool }

func (v *boolValue) String() string      { return strconv.FormatBool(v.b) }
func (v *boolValue) IsBoolFlag() bool    { return true }
func (v *boolValue) value() (any, error) { return v.b, nil }
func (v *boolValue) Set(s string) error {
	b, err := strconv.ParseBool(s)
	if err != nil {
		return err
	}
	v.b = b
	return nil
}

type listValue struct {
	items string
	vs    []string
}

func (v *listValue) String() string     { return strings.Join(v.vs, ",") }
func (v *listValue) Set(s string) error { v.vs = append(v.vs, s); return nil }
func (v *listValue) value() (any, error) {
	out := make([]any, 0, len(v.vs))
	for _, s := range v.vs {
		switch v.items {
		case "integer":
			n, err := strconv.ParseInt(s, 10, 64)
			if err != nil {
				return nil, fmt.Errorf("%q is not an integer", s)
			}
			out = append(out, n)
		case "number":
			f, err := strconv.ParseFloat(s, 64)
			if err != nil {
				return nil, fmt.Errorf("%q is not a number", s)
			}
			out = append(out, f)
		case "boolean":
			b, err := strconv.ParseBool(s)
			if err != nil {
				return nil, fmt.Errorf("%q is not a boolean", s)
			}
			out = append(out, b)
		default:
			out = append(out, s)
		}
	}
	return out, nil
}

type numberValue struct {
	integer bool
	s       string
}

func (v *numberValue) String() string     { return v.s }
func (v *numberValue) Set(s string) error { v.s = s; return nil }
func (v *numberValue) value() (any, error) {
	if v.integer {
		n, err := strconv.ParseInt(v.s, 10, 64)
		if err != nil {
			return nil, fmt.Errorf("%q is not an integer", v.s)
		}
		return n, nil
	}
	f, err := strconv.ParseFloat(v.s, 64)
	if err != nil {
		return nil, fmt.Errorf("%q is not a number", v.s)
	}
	return f, nil
}

type docValue struct{ s string }

func (v *docValue) String() string     { return v.s }
func (v *docValue) Set(s string) error { v.s = s; return nil }
func (v *docValue) value() (any, error) {
	var out any
	if err := codec.Unmarshal([]byte(v.s), &out, codec.Tony); err != nil {
		return nil, err
	}
	return out, nil
}

// wrap lays text out in lines of at most width columns, each opening with indent, ending in a
// newline.
func wrap(text, indent string, width int) string {
	var b strings.Builder
	line := indent
	for _, word := range strings.Fields(text) {
		if len(line) > len(indent) && len(line)+1+len(word) > width {
			b.WriteString(line + "\n")
			line = indent
		}
		if len(line) > len(indent) {
			line += " "
		}
		line += word
	}
	if len(line) > len(indent) {
		b.WriteString(line + "\n")
	}
	return b.String()
}
