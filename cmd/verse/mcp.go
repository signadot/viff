package main

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/scott-cotton/cli"

	"github.com/signadot/verse/api"
	"github.com/signadot/verse/client"
	"github.com/signadot/verse/codec"
)

// verse mcp: the user face of verse as an MCP server (a5mvads7, v27eah17). The host of a person's
// agent starts it, it talks on stdin and stdout, and it acts as whoever `verse` acts as here: the
// config, the login, VERSE_USER. No tool names an identity, so nothing an agent reads can make it
// act as somebody else.
//
// A TOOL IS A CALL OF THE API, as the command it mirrors makes it: the request and the rendering
// of its answer are functions the command and the tool share (stateQuery, renderDoc), and what is
// the command line's alone — flags, the package state one invocation keeps — stays in the
// command. The API is the contract; the command line changes more, and with less notice.

type mcpConfig struct {
	*cli.Command
	// ReadOnly serves the tools that read and nothing else, for an agent that reads what others
	// wrote: what it reads cannot talk it into a write the server does not offer.
	ReadOnly bool `cli:"name=read-only desc='serve only the tools that read: no write, run, ask, flow or charter change'"`
}

func mcpCmd() *cli.Command {
	cfg := &mcpConfig{}
	opts, err := cli.StructOpts(cfg)
	if err != nil {
		panic(err)
	}
	return cli.NewCommandAt(&cfg.Command, "mcp").
		WithSynopsis("mcp — serve this verse to an agent's host over MCP on stdin/stdout, as you").
		WithOpts(opts...).WithRun(cfg.run)
}

func (cfg *mcpConfig) run(cc *cli.Context, args []string) error {
	args, err := cfg.Parse(cc, args)
	if err != nil {
		return err
	}
	if len(args) > 0 {
		return fmt.Errorf("%w: mcp takes no arguments; its host starts it as `verse mcp`", cli.ErrUsage)
	}
	out, err := outFmt()
	if err != nil {
		return err
	}
	me, err := principal("")
	if err != nil {
		return err
	}
	return newMCPServer(cc.Go, mcpClient(me, lockedBearer), out, me, cfg.ReadOnly).Run(cc.Go, &mcp.StdioTransport{})
}

// mcpClient is the client the server calls the verse through, built once from the flags and the
// config as this invocation resolved them: the CLI's, with who it acts as fixed when the server
// starts rather than asked of the config on each request, and bearer, the login's, proving it.
func mcpClient(me string, bearer client.BearerFunc) *client.Client {
	c := apiClient()
	c.HTTP = &http.Client{Transport: client.Transport{
		Version: api.Version(), Principal: client.Static(me), Bearer: bearer,
		Warn: func(msg string) { skewWarned.Do(func() { fmt.Fprintf(warnTo, "verse: %s\n", msg) }) },
	}}
	return c
}

// mcpServer is what every tool has: the verse it calls, the format it answers in, and who it acts
// as. ctx is the server's life, which the wait's watch lives for.
type mcpServer struct {
	ctx context.Context
	c   *client.Client
	out codec.Format
	me  string
	// readOnly is -read-only: a tool not annotated readOnly() is not registered.
	readOnly bool

	// The wait: one watch, opened by the first verse_wait and followed for the server's life.
	waitOnce sync.Once
	log      *waitLog
}

// mcpIn is what a tool's documents are read as: tony, as each schema says, whatever format the
// config reads the command line's in.
const mcpIn = codec.Tony

// mcpAnswerMax is the most a tool answers, in bytes. An answer is never cut in the middle of a
// record (a5mvads7 rule 3): past this it is not answered at all, and the tool says how to narrow
// it. A var so a test can make it small.
var mcpAnswerMax = 64 << 10

// newMCPServer is the server with every tool registered, or with readOnly every tool that reads,
// calling the verse through c as me, for as long as ctx lasts.
func newMCPServer(ctx context.Context, c *client.Client, out codec.Format, me string, readOnly bool) *mcp.Server {
	m := &mcpServer{ctx: ctx, c: c, out: out, me: me, readOnly: readOnly, log: newWaitLog()}
	s := mcp.NewServer(&mcp.Implementation{Name: "verse", Version: api.Version()}, nil)
	m.addDataTools(s)
	m.addQuestionTools(s)
	m.addAskTools(s)
	m.addWaitTool(s)
	m.addTaskTools(s)
	m.addCharterTools(s)
	m.addPrompts(s)
	return s
}

// nobody is the refusal of a tool that acts as somebody, where nobody is configured.
func (m *mcpServer) nobody() error {
	if m.me != "" {
		return nil
	}
	return fmt.Errorf("nobody is configured, so this server acts as nobody: set `user:` in the " +
		"verse config, log in with `verse account login`, or start it with VERSE_USER set")
}

// answer is a tool's answer from what a renderer wrote: the text, or the refusal to answer more
// than mcpAnswerMax.
func answer(buf *bytes.Buffer) (*mcp.CallToolResult, any, error) {
	if buf.Len() > mcpAnswerMax {
		return nil, nil, fmt.Errorf("the answer is %d bytes, more than the %d this server answers, and an "+
			"answer is not cut: narrow it (a deeper address, if, trim) or ask for the paths", buf.Len(), mcpAnswerMax)
	}
	return &mcp.CallToolResult{Content: []mcp.Content{
		&mcp.TextContent{Text: strings.TrimRight(buf.String(), "\n")},
	}}, nil, nil
}

// get answers a read as the command that makes it prints it.
func (m *mcpServer) get(ctx context.Context, what, path string, q url.Values, f filters) (*mcp.CallToolResult, any, error) {
	resp, err := m.c.With(ctx).Get(path, q)
	if err != nil {
		return nil, nil, fmt.Errorf("%s: %w", what, err)
	}
	var buf bytes.Buffer
	if _, err := renderBody(&buf, resp, f, m.out, false); err != nil {
		return nil, nil, err
	}
	return answer(&buf)
}

// addTool registers a tool, unless the server reads only and the tool is not annotated readOnly():
// the annotation a host is shown is what decides, so the two cannot disagree.
func addTool[In, Out any](m *mcpServer, s *mcp.Server, t *mcp.Tool, h mcp.ToolHandlerFor[In, Out]) {
	if m.readOnly && (t.Annotations == nil || !t.Annotations.ReadOnlyHint) {
		return
	}
	mcp.AddTool(s, t, h)
}

// readOnly is the annotation every reading tool carries, so a host can allow the reads by kind.
func readOnly() *mcp.ToolAnnotations { return &mcp.ToolAnnotations{ReadOnlyHint: true} }

// writes is the annotation of a tool that writes: destructive when it takes something away.
func writes(destructive bool) *mcp.ToolAnnotations {
	return &mcp.ToolAnnotations{DestructiveHint: &destructive}
}

// sentence is a tool's answer when the command says a sentence.
func sentence(line string) *mcp.CallToolResult {
	return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: line}}}
}

// filtersOf reads a listing's if and trim, as --if and --trim are read.
func filtersOf(keep, trim string) (filters, error) {
	var f filters
	var err error
	if f.keep, err = parseFilter("if", keep, mcpIn); err != nil {
		return f, err
	}
	f.project, err = parseFilter("trim", trim, mcpIn)
	return f, err
}
