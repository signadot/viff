package delivery

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/signadot/tony-format/go-tony/ir"
	"github.com/signadot/verse/pkg/codec"
	"github.com/signadot/verse/pkg/runner/runnerd/mcpclient"
	"github.com/signadot/verse/pkg/systems"
	"github.com/signadot/verse/pkg/task"
)

// THE MCP SYSTEM IS THE TOOLS OF A REMOTE SERVER THE OPERATOR NAMES, called by the runner with the
// server's credential, which the task never holds: the task's process never reaches a server. A
// call is one act (`mcp.call`), decided at the runner by the environment, the autonomy and the
// ceiling as every act is, on the server, the tool and the arguments whole; the list of a server's
// tools (`mcp.list`) is what a call would be decided on, asked once for each server as the task is
// built, and decides nothing itself.
//
// WHAT IS SENT IS THE STEP. A call's arguments come as the request's body, a JSON object, and are
// read into the step's `arguments` as structure (ReadCall): the step the autonomy, the ceiling and
// an answer see is the call that is sent, since what goes to the server is that structure written
// back as JSON (mcpclient.Arguments), never the body's bytes. A `null` stays a `null`, in the step
// and in what is sent, and is never an absent argument: a server may read the two apart, and an
// approval of one would otherwise perform the other.
//
// Both are performed APART: a server that takes a minute does not hold a push, and neither reads
// the checkout. A call in flight when the task's process goes is given up, and whether the server
// performed it is then not known.
const (
	// OpMCPCall calls one tool of one server: the step `mcp.call` (task.StepMCPCall).
	OpMCPCall = "mcp.call"
	// OpMCPList lists one server's tools, those the environment and the ceiling admit some call
	// of, and the server's instructions. It asks about no step, and refuses a server the
	// environment gives no block for.
	OpMCPList = "mcp.list"
)

// The bounds: on a call's arguments, since the step is carried whole in the task's events, its
// snapshot and a question's record; on what a list passes on of a server's words (each
// description, each tool's schema, the instructions, and the list whole), so one server's list
// fits a frame; on a result, well under the frame. mcpGrace is what the table's deadline has past
// the client's own timeouts, so the client's fires first and says why.
const (
	ArgumentsMost    = 64 << 10
	InstructionsMost = 4 << 10
	DescriptionMost  = 1 << 10
	SchemaMost       = 16 << 10
	ListMost         = 512 << 10
	ResultMost       = 256 << 10
	mcpGrace         = 5 * time.Second
)

// noMCP is what the ops answer on a runner that calls no MCP server.
const noMCP = "this runner calls no MCP server for a task: its configuration names none (`mcp.servers`)"

// Call is an `mcp.call` as read from a request (ReadCall): the server's name, the tool's, and the
// arguments as structure, an object.
type Call struct {
	Server, Tool string
	Arguments    *ir.Node
}

// ReadCall reads an `mcp.call` from what a request gives, and says why it is refused before any
// row decides it, "" where it is not: the server and the tool named, the arguments an object
// within ArgumentsMost that JSON can carry (no tag, no comment, every key a string). The runner
// reads it as the request arrives (mcpCall), and the task's tool reads its step from the request
// it sends (StepOf), so a row decides one step on both sides, and a number spelled `1.0` is
// `1.0` on both.
func ReadCall(server, tool, body string) (Call, string) {
	if server == "" {
		return Call{}, "a call names the server"
	}
	if tool == "" {
		return Call{}, "a call names the tool"
	}
	if strings.IndexFunc(tool, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsControl(r) }) >= 0 {
		return Call{}, fmt.Sprintf("%q is no tool's name", tool)
	}
	if len(body) > ArgumentsMost {
		return Call{}, fmt.Sprintf("the arguments are %d bytes, over the %d a call carries", len(body), ArgumentsMost)
	}
	n, err := codec.ParseNode([]byte(body), codec.JSON)
	if err != nil {
		return Call{}, "the arguments do not read: " + err.Error()
	}
	// The layout's tags off (Plain), what is left that JSON cannot say is the author's: a
	// comment wraps what it is on, so it is looked for before the wrapping comes off.
	n = systems.Plain(n)
	if what := notJSON(n); what != "" {
		return Call{}, "the arguments hold " + what + ", which no call carries"
	}
	if n = ir.Uncomment(n); n == nil || n.Type != ir.ObjectType {
		return Call{}, "the arguments are an object (`{}` for none)"
	}
	return Call{Server: server, Tool: tool, Arguments: n}, ""
}

// notJSON is what n holds that JSON cannot say, "" where nothing: a tag (an operator, which a
// pattern has and a value does not), a comment, a key that is no string.
func notJSON(n *ir.Node) string {
	if n == nil {
		return ""
	}
	if n.Type == ir.CommentType || n.Comment != nil {
		return "a comment"
	}
	if n.Tag != "" {
		return "a tag (" + n.Tag + ")"
	}
	for _, k := range n.Fields {
		if k.Type != ir.StringType || k.Tag != "" {
			return "a key that is no string"
		}
	}
	for _, v := range n.Values {
		if what := notJSON(v); what != "" {
			return what
		}
	}
	return ""
}

// Step is the step the call is, as the autonomy, the ceiling and an answer see it: the server,
// the tool, and the arguments as given, which a shape and a row match open.
func (c Call) Step() task.Step {
	return task.NewStep(task.StepMCPCall, map[string]string{"server": c.Server, "tool": c.Tool}).With("arguments", c.Arguments)
}

// mcpCall answers an `mcp.call`: the request read into the one step, decided by the environment,
// the autonomy and the ceiling, and then the call, on the task's session with the server.
func (s *Server) mcpCall(ctx context.Context, tk Task, req Request) (asked, Reply) {
	c, why := ReadCall(req.Field("server"), req.Field("tool"), req.Body)
	if why != "" {
		return before(Reply{Refused: why})
	}
	return asking(c.Step(), func() Reply {
		if tk.mcp == nil {
			return Reply{Error: noMCP}
		}
		if _, calls := s.Services.MCP[c.Server]; !calls {
			// An environment naming a server the configuration does not: nothing was sent.
			return Reply{Error: fmt.Sprintf("this runner calls no server %s, so the call of %s was not made", c.Server, c.Tool)}
		}
		args, err := mcpclient.Arguments(c.Arguments)
		if err != nil {
			return s.bug(tk, "%s read arguments it cannot send: %v", req.Op, err)
		}
		res, err := tk.mcp.Call(ctx, c.Server, c.Tool, args)
		var refused *mcpclient.ServerError
		if err != nil {
			fmt.Fprintf(s.Log, "delivery: %s: mcp.call %s %s: %v\n", tk.ID, c.Server, c.Tool, err)
			if errors.As(err, &refused) {
				// The server answered, and did not perform the call as asked: a tool it has
				// not, arguments its schema refuses, a tool that asked for input the runner does
				// not give. The task re-plans on the server's word.
				return Reply{Refused: fmt.Sprintf("%s refused the call of %s: %s", c.Server, c.Tool, refused.Message)}
			}
			if errors.Is(err, context.DeadlineExceeded) {
				return Reply{Error: fmt.Sprintf("the call of %s at %s was given up at the server's timeout (%s); whether the server "+
					"performed it is not known, so read it back before doing it again", c.Tool, c.Server, s.mcpTimeout(c.Server))}
			}
			return Reply{Error: fmt.Sprintf("the call of %s at %s did not complete: %v; whether the server performed it is "+
				"not known, so read it back before doing it again", c.Tool, c.Server, err)}
		}
		fmt.Fprintf(s.Log, "delivery: %s: mcp.call %s %s\n", tk.ID, c.Server, c.Tool)
		return mcpResult(mcpclient.Read(res))
	})
}

// mcpTimeout is how long a call of server has: the server's `timeout`, or the client's default.
func (s *Server) mcpTimeout(server string) time.Duration {
	if d := s.Services.MCP[server].Timeout; d > 0 {
		return d
	}
	return mcpclient.Timeout
}

// mcpResult is a call's reply from what the server answered: the result's text, which is the
// server's word, an error of the server's marked as such and no error of the runner's; the text
// cut at ResultMost, saying so.
func mcpResult(r mcpclient.Result) Reply {
	text, cut := cutAt(r.Text, ResultMost)
	switch {
	case r.IsError && strings.TrimSpace(text) == "":
		text = "the server answered an error with no text"
	case r.IsError:
		text = "the server answered an error:" + withText(text)
	case strings.TrimSpace(text) == "":
		text = "the result has no text"
	}
	if cut {
		text += fmt.Sprintf("\n[cut: the result is %d bytes, and the runner passes on %d]", len(r.Text), ResultMost)
	}
	return Reply{OK: true, Out: text}
}

// withText is text after a sentence, on a line of its own, or nothing where there is none.
func withText(text string) string {
	if strings.TrimSpace(text) == "" {
		return ""
	}
	return "\n" + text
}

// cutAt is s within most bytes, at a rune's boundary, and whether it was cut.
func cutAt(s string, most int) (string, bool) {
	if len(s) <= most {
		return s, false
	}
	for most > 0 && !utf8.RuneStart(s[most]) {
		most--
	}
	return s[:most], true
}

// mcpList answers an `mcp.list`: the tools of one server the task's environment gives, less
// those no shape of the environment's or the ceiling's admits a call of, each with its
// description and its input schema, and the server's instructions, as a tony document the task's
// side reads (Out). A server the environment gives no block for is refused, so under an empty
// environment nothing is listed, as nothing could be called.
func (s *Server) mcpList(ctx context.Context, tk Task, req Request) (asked, Reply) {
	server := req.Field("server")
	if server == "" {
		return before(Reply{Refused: "a list names the server"})
	}
	env := tk.environment()
	if !env.Reaches("mcp", []string{"call"}, map[string]*ir.Node{"server": ir.FromString(server)}) {
		why, _ := env.Refuses(task.StepMCPCall, systems.FieldsDoc(task.StepMCPCall, map[string]string{"server": server}))
		return before(Reply{Refused: "refused: outside the environment: " +
			cmp.Or(why, "the environment gives `mcp` no block for server "+server+" with a `call`")})
	}
	if tk.mcp == nil {
		return before(Reply{Error: noMCP})
	}
	tools, instructions, err := tk.mcp.List(ctx, server)
	if err != nil {
		fmt.Fprintf(s.Log, "delivery: %s: mcp.list %s: %v\n", tk.ID, server, err)
		return before(Reply{Error: fmt.Sprintf("%s did not answer its list of tools: %v", server, err)})
	}
	var (
		listed  []*ir.Node
		size    int // what the tools listed come to, encoded
		leftOut int // tools admitted and not listed, past ListMost
	)
	for _, tl := range tools {
		if !admitted(env, s.Ceilings["mcp"], server, tl.Name) {
			continue
		}
		if leftOut > 0 {
			leftOut++
			continue
		}
		description, cut := cutAt(tl.Description, DescriptionMost)
		if cut {
			description += fmt.Sprintf(" [cut: the description is %d bytes, and the runner passes on %d]", len(tl.Description), DescriptionMost)
		}
		// A schema past its bound is replaced by the least one: the tool is still listed and
		// still called, with the server's own schema the judge of a call.
		input := tl.Input
		if n := encodedSize(input); n > SchemaMost {
			input = ir.FromKeyVals([]ir.KeyVal{{Key: ir.FromString("type"), Val: ir.FromString("object")}})
			description += fmt.Sprintf(" [cut: the schema is %d bytes, and the runner passes on %d; the arguments are as the server reads them]", n, SchemaMost)
		}
		entry := ir.FromKeyVals([]ir.KeyVal{
			{Key: ir.FromString("name"), Val: ir.FromString(tl.Name)},
			{Key: ir.FromString("description"), Val: ir.FromString(description)},
			{Key: ir.FromString("input"), Val: input},
		})
		if size += encodedSize(entry); size > ListMost {
			leftOut = 1
			continue
		}
		listed = append(listed, entry)
	}
	said, cut := cutAt(instructions, InstructionsMost)
	if cut {
		said += fmt.Sprintf("\n[cut: the instructions are %d bytes, and the runner passes on %d]", len(instructions), InstructionsMost)
	}
	kvs := []ir.KeyVal{
		{Key: ir.FromString("instructions"), Val: ir.FromString(said)},
		{Key: ir.FromString("tools"), Val: ir.FromSlice(listed)},
	}
	if leftOut > 0 {
		fmt.Fprintf(s.Log, "delivery: %s: mcp.list %s: %d tool(s) left out, the list over %d bytes\n", tk.ID, server, leftOut, ListMost)
		kvs = append(kvs, ir.KeyVal{Key: ir.FromString("cut"), Val: ir.FromString(fmt.Sprintf(
			"%d tool(s) the environment admits are left out: the list is over the %d bytes the runner passes on", leftOut, ListMost))})
	}
	return before(tonyOut(ir.FromKeyVals(kvs)))
}

// encodedSize is what n comes to in the answer, encoded; the frame's measure, near enough.
func encodedSize(n *ir.Node) int {
	raw, err := codec.NodeBytes(n, codec.Tony)
	if err != nil {
		return 0
	}
	return len(raw)
}

// admitted is whether some call of tool at server lies within the environment and within the
// ceiling: a block of each for the server, with a shape of `call` that a call of the tool could
// match, whatever its arguments. A tool left out is one no call of could pass; a tool listed is
// still decided call by call.
func admitted(env systems.Environment, ceiling systems.Ceiling, server, tool string) bool {
	params := map[string]*ir.Node{"server": ir.FromString(server), "tool": ir.FromString(tool)}
	if !env.Reaches("mcp", []string{"call"}, params) {
		return false
	}
	for _, b := range ceiling {
		if p, named := b.Attributes["server"]; named && !systems.MatchField(systems.FormOf(task.StepMCPCall, "server"), p, params["server"]) {
			continue
		}
		for _, sh := range (systems.Block{Verbs: b.Verbs}).Given("mcp")["call"] {
			if sh.Meets(task.StepMCPCall, params) {
				return true
			}
		}
	}
	return false
}
