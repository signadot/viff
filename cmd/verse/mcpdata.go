package main

import (
	"bytes"
	"context"
	"fmt"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/signadot/verse/pkg/verbs"
)

// The data tools mirror `verse data`: the verse at an address. Each is a verb a runner's verse
// system offers too, defined once (verbs).

func (m *mcpServer) addDataTools(s *mcp.Server) {
	addVerb(m, s, verbs.DataGet, m.data)
	addVerb(m, s, verbs.DataPut, m.dataPut)
	addVerb(m, s, verbs.DataRm, m.dataRm)
}

func (m *mcpServer) dataPut(ctx context.Context, a verbs.Args) (*mcp.CallToolResult, any, error) {
	body, err := verbs.DataPutOf(a)
	if err != nil {
		return nil, nil, err
	}
	resp, err := m.c.With(ctx).Post("/entity", nil, body)
	if err != nil {
		return nil, nil, fmt.Errorf("data --put: %w", err)
	}
	var buf bytes.Buffer
	if err := renderBody(&buf, resp, filters{}, m.out, false); err != nil {
		return nil, nil, err
	}
	return answer(&buf)
}

func (m *mcpServer) dataRm(ctx context.Context, a verbs.Args) (*mcp.CallToolResult, any, error) {
	segs, err := verbs.Address(a, "at")
	if err != nil {
		return nil, nil, err
	}
	said, err := rmVia(m.c.With(ctx), segs)
	if err != nil {
		return nil, nil, err
	}
	return sentence(said), nil, nil
}

func (m *mcpServer) data(ctx context.Context, a verbs.Args) (*mcp.CallToolResult, any, error) {
	r, err := verbs.DataGetOf(a)
	if err != nil {
		return nil, nil, err
	}
	resp, err := m.c.With(ctx).Get("/state", r.Query)
	if err != nil {
		return nil, nil, err
	}
	var buf bytes.Buffer
	if r.Paths {
		err = renderPaths(&buf, resp, r.At, r.Depth, true)
	} else {
		err = renderDoc(&buf, resp, r.At, filters{project: r.Trim}, m.out, false)
	}
	if err != nil {
		return nil, nil, err
	}
	return answer(&buf)
}
