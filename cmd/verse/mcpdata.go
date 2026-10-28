package main

import (
	"bytes"
	"context"
	"fmt"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/signadot/verse/api"
	"github.com/signadot/verse/entity"
)

// The data tools mirror `verse data`: the verse at an address.

type dataIn struct {
	At    string `json:"at,omitempty" jsonschema:"the address as a kinded path, as a document prints it: demo.pr.1, demo.pr.*.stage; empty is all of the verse you may read"`
	If    string `json:"if,omitempty" jsonschema:"only entities whose payload matches this tony pattern, chosen by the server before anything is sent: {stage: open}"`
	IfAt  string `json:"if_at,omitempty" jsonschema:"root the if pattern at this dot-separated field path inside the payload (labels)"`
	Trim  string `json:"trim,omitempty" jsonschema:"write only the parts of each entity this match document names, as a key set: {id status}"`
	Paths bool   `json:"paths,omitempty" jsonschema:"answer one kinded path per line instead of the document"`
	Depth *int   `json:"depth,omitempty" jsonschema:"with paths: walk at most this many levels beneath the address; 0 is the address alone"`
}

type dataPutIn struct {
	At     string `json:"at" jsonschema:"where to write, as a kinded path: demo.pr.1, or demo.pr.1.labels.urgent for one field; no wildcards"`
	Value  string `json:"value" jsonschema:"what to write there, a tony value: {stage: open}, 42, [a, b]. A document is merged into what stands"`
	Create bool   `json:"create,omitempty" jsonschema:"only if nothing stands there yet: the write loses a race rather than clobbering"`
	Update bool   `json:"update,omitempty" jsonschema:"only if something already stands there"`
	Expect string `json:"expect,omitempty" jsonschema:"only if what stands there matches this tony pattern"`
	Grants string `json:"grants,omitempty" jsonschema:"what the node this write creates holds out, as verse perms --grants takes it: [{read: [ana@example.com]}], or [] for nothing; refused where the node stands"`
}

type dataRmIn struct {
	At string `json:"at" jsonschema:"what to remove, as a kinded path: an entity (demo.pr.1) or one field of one (demo.pr.1.labels.urgent); no wildcards"`
}

func (m *mcpServer) addDataTools(s *mcp.Server) {
	addTool(m, s, &mcp.Tool{
		Name: "verse_data",
		Description: "Read the verse at an address, as `verse data` does: the document stored there, " +
			"or with paths the addresses beneath it. What is not in your view, because nothing stands " +
			"there or it is not yours to read, is answered alike. Where you may not read into the " +
			"address at all, the read is refused, naming the nearest owner on record, whom to ask, " +
			"whether or not anything stands there.",
		Annotations: readOnly(),
	}, m.data)
	addTool(m, s, &mcp.Tool{
		Name: "verse_data_put",
		Description: "Write a value at an address, as `verse data <address> --put` does, merged into " +
			"what stands there, on your authority; with create, update or expect, only on that condition. " +
			"Answers what stands there after the write.",
		Annotations: writes(false),
	}, m.dataPut)
	addTool(m, s, &mcp.Tool{
		Name: "verse_data_rm",
		Description: "Remove what stands at an address, an entity or one field of one, as " +
			"`verse data <address> --rm` does.",
		Annotations: writes(true),
	}, m.dataRm)
}

func (m *mcpServer) dataPut(ctx context.Context, _ *mcp.CallToolRequest, in dataPutIn) (*mcp.CallToolResult, any, error) {
	if err := m.nobody(); err != nil {
		return nil, nil, err
	}
	segs, err := addressOf(nil, in.At)
	if err != nil {
		return nil, nil, err
	}
	body, err := putBody(api.DataWrite{Payload: in.Value, Create: in.Create, Update: in.Update, Expect: in.Expect,
		Grants: in.Grants}, segs, mcpIn)
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

func (m *mcpServer) dataRm(ctx context.Context, _ *mcp.CallToolRequest, in dataRmIn) (*mcp.CallToolResult, any, error) {
	if err := m.nobody(); err != nil {
		return nil, nil, err
	}
	segs, err := addressOf(nil, in.At)
	if err != nil {
		return nil, nil, err
	}
	said, err := rmVia(m.c.With(ctx), segs)
	if err != nil {
		return nil, nil, err
	}
	return sentence(said), nil, nil
}

func (m *mcpServer) data(ctx context.Context, _ *mcp.CallToolRequest, in dataIn) (*mcp.CallToolResult, any, error) {
	depth := entity.Unbounded
	if in.Depth != nil {
		if !in.Paths || *in.Depth < 0 {
			return nil, nil, fmt.Errorf("depth bounds a listing, so it goes with paths, and is never negative")
		}
		depth = *in.Depth
	}
	q, kp, err := stateQuery(in.At, in.If, in.IfAt, in.Paths, depth, mcpIn)
	if err != nil {
		return nil, nil, err
	}
	var f filters
	if f.project, err = parseFilter("trim", in.Trim, mcpIn); err != nil {
		return nil, nil, err
	}
	resp, err := m.c.With(ctx).Get("/state", q)
	if err != nil {
		return nil, nil, err
	}
	var buf bytes.Buffer
	if in.Paths {
		err = renderPaths(&buf, resp, kp, depth, true)
	} else {
		err = renderDoc(&buf, resp, kp, f, m.out, false)
	}
	if err != nil {
		return nil, nil, err
	}
	return answer(&buf)
}
