package calltool

import (
	"fmt"

	"github.com/signadot/verse/codec"
)

// Env is the environment variable that names the socket, as the task process sets it for the
// programs it runs and as verse-call-tool reads it.
const Env = "VERSE_TOOLS"

// Tool is one tool as the shell sees it: what the model is told about it, less the code. A host
// offers its tools as these, with params as it has them; the server describes them (Describe).
// The params are JSON-schema properties, and `enum` on one is the vocabulary the server accepts
// — verse-call-tool shows it in a tool's usage and refuses a value outside it before the call
// crosses.
type Tool struct {
	Name        string         `tony:"field=name"`
	Description string         `tony:"field=description,omitzero"`
	Params      map[string]any `tony:"field=params,omitzero"`
	Required    []string       `tony:"field=required,omitzero"`
}

// Reserved is the one name a tool's parameter may not have. verse-call-tool takes the whole
// input as `-input`, and a parameter of that name would be the same flag twice; the list grows
// on the server with no change to the binary, so the server is where it is refused.
const Reserved = "input"

// Describe is a tool as the list reports it. The params are normalised through the codec, so
// the schema the server checks against is the schema the binary reads: a tool declares an enum
// as whatever Go slice it has to hand, and the check reads one shape. A schema the codec cannot
// carry, or a parameter named Reserved, is the error: such a tool cannot be listed, and is not
// described with a schema its check would then quietly skip. Server.Start refuses it.
func Describe(t Tool) (Tool, error) {
	if _, has := t.Params[Reserved]; has {
		return Tool{}, fmt.Errorf("calltool: %s has a parameter named %q, which verse-call-tool reserves for the whole input", t.Name, Reserved)
	}
	params, err := normalise(t.Params)
	if err != nil {
		return Tool{}, fmt.Errorf("calltool: the schema of %s cannot be carried: %w", t.Name, err)
	}
	return Tool{Name: t.Name, Description: t.Description, Params: params, Required: t.Required}, nil
}

// normalise is params as they are after a trip through the codec: every list a []any, every
// object a map[string]any, every integer an int64.
func normalise(params map[string]any) (map[string]any, error) {
	if len(params) == 0 {
		return nil, nil
	}
	node, err := codec.ToNode(params)
	if err != nil {
		return nil, err
	}
	var out map[string]any
	if err := codec.FromNode(node, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// Call is one call of a tool: the body of POST /call.
type Call struct {
	Tool  string         `tony:"field=tool"`
	Input map[string]any `tony:"field=input,omitzero"`
}

// Result is what a call came to, when it was performed or refused. A call that FAILED is not a
// Result: it is a status with the failure in words, which the client returns as an error.
type Result struct {
	// Out is what happened, in the words the model would read: where the work went, what the
	// person answered, which pull request it is.
	Out string `tony:"field=out,omitzero"`
	// Refused says the call was declined — by a gate, or by the far side about its own rules —
	// rather than failed. The caller re-plans on it rather than retrying it.
	Refused bool `tony:"field=refused,omitzero"`
}
