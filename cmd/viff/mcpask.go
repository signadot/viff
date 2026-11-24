package main

import (
	"bytes"
	"context"
	"fmt"
	"net/url"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/signadot/tony-format/go-tony/ir"
	"go.viff.dev/viff/pkg/verbs"
)

// The question tools mirror `viff question`, the answerer's side: what is asked of you, and
// answering it. The ask tools mirror `viff ask`, the asker's: asking somebody something,
// changing it while it waits, taking it back. Raising, reading back and listing are verbs a
// runner's viff system offers too, defined once (verbs).

type questionIn struct {
	ID     string `json:"id,omitempty" jsonschema:"one question by its id: its options, ballots, deadline and outcome; without it, the questions waiting on you"`
	All    bool   `json:"all,omitempty" jsonschema:"every pending ask you can read, whoever raised it and whoever it waits on: the whole docket"`
	Served bool   `json:"served,omitempty" jsonschema:"list the asks a resolver serves for you too, each saying which"`
	If     string `json:"if,omitempty" jsonschema:"keep only the questions matching this tony pattern"`
	Trim   string `json:"trim,omitempty" jsonschema:"write only the parts of each question this match document names, as a key set: {id pose}"`
}

type answerIn struct {
	ID     string `json:"id" jsonschema:"the question's id"`
	Option string `json:"option" jsonschema:"the option to choose, by id: proceed or deny on a yes-or-no question"`
	Say    string `json:"say,omitempty" jsonschema:"words to say to whoever asked, with the answer"`
	Force  bool   `json:"force,omitempty" jsonschema:"answer yourself an ask a resolver you granted serves for you"`
}

func (m *mcpServer) addQuestionTools(s *mcp.Server) {
	addTool(m, s, &mcp.Tool{
		Name: "viff_question",
		Description: "What is asked of you, as `viff question` answers it: the questions waiting on " +
			"you, with all the whole docket, or with id one question as it stands.",
		Annotations: readOnly(),
	}, m.question)
	addTool(m, s, &mcp.Tool{
		Name: "viff_question_answer",
		Description: "Answer one question waiting on you, as you, choosing one of its options, as " +
			"`viff question <id> --answer <option>` does. A question that takes more than one voice " +
			"records the answer and says where the tally stands.",
		Annotations: writes(false),
	}, m.answer)
}

func (m *mcpServer) question(ctx context.Context, _ *mcp.CallToolRequest, in questionIn) (*mcp.CallToolResult, any, error) {
	switch {
	case in.ID != "" && (in.All || in.Served):
		return nil, nil, fmt.Errorf("all and served are about every question, and take no id")
	case in.All && in.Served:
		return nil, nil, fmt.Errorf("all lists every pending ask, served ones included; served widens " +
			"the questions waiting on you, so pass one")
	}
	f, err := filtersOf(in.If, in.Trim)
	if err != nil {
		return nil, nil, err
	}
	path, q := "/asks", questionQuery(in.All, in.Served)
	if in.ID != "" {
		path, q = "/asks/"+url.PathEscape(in.ID), nil
	}
	return m.get(ctx, "question", path, q, f)
}

func (m *mcpServer) answer(ctx context.Context, _ *mcp.CallToolRequest, in answerIn) (*mcp.CallToolResult, any, error) {
	if err := m.nobody(); err != nil {
		return nil, nil, err
	}
	said, err := answerVia(m.c.With(ctx), in.ID, in.Option, m.me, in.Say, in.Force, "answered")
	if err != nil {
		return nil, nil, err
	}
	return sentence(said), nil, nil
}

type askChangeIn struct {
	NameOrID string `json:"name_or_id" jsonschema:"the ask, by the name you asked it under or its id"`
	Due      string `json:"due,omitempty" jsonschema:"move when it settles: an RFC 3339 date or a duration from now; asks nothing again"`
	To       string `json:"to,omitempty" jsonschema:"change who is asked, as to takes it; asks again"`
	On       string `json:"on,omitempty" jsonschema:"change what follows its answer, rows as on takes them, in place of the rows it had; asks again"`
}

type askWithdrawIn struct {
	NameOrID string `json:"name_or_id" jsonschema:"the ask, by the name you asked it under or its id"`
}

func (m *mcpServer) addAskTools(s *mcp.Server) {
	addVerb(m, s, verbs.AskGet, m.askGet)
	addVerb(m, s, verbs.AskList, m.askList)
	addVerb(m, s, verbs.AskCreate, m.askCreate)
	addTool(m, s, &mcp.Tool{
		Name: "viff_ask_change",
		Description: "Change one of your asks while it waits, as `viff ask <name> --due/--to/--on` " +
			"does: a new due date asks nothing again; new answerers or a new on asks it again.",
		Annotations: writes(false),
	}, m.askChange)
	addTool(m, s, &mcp.Tool{
		Name:        "viff_ask_withdraw",
		Description: "Take back one of your asks, as `viff ask <name> --withdraw` does: it is removed, and its question withdrawn.",
		Annotations: writes(true),
	}, m.askWithdraw)
}

func (m *mcpServer) askGet(ctx context.Context, a verbs.Args) (*mcp.CallToolResult, any, error) {
	return m.askRead(askShown(m.c.With(ctx), a.Word("name_or_id")))
}

func (m *mcpServer) askList(ctx context.Context, _ verbs.Args) (*mcp.CallToolResult, any, error) {
	return m.askRead(askList(m.c.With(ctx), m.me))
}

// askRead answers what an ask read said: a sentence, or the document.
func (m *mcpServer) askRead(node *ir.Node, line string, err error) (*mcp.CallToolResult, any, error) {
	if err != nil {
		return nil, nil, err
	}
	if node == nil {
		return sentence(line), nil, nil
	}
	var buf bytes.Buffer
	if err := renderIR(&buf, node, filters{}, m.out, false); err != nil {
		return nil, nil, err
	}
	return answer(&buf)
}

func (m *mcpServer) askCreate(ctx context.Context, a verbs.Args) (*mcp.CallToolResult, any, error) {
	body, err := verbs.AskOf(a, time.Now())
	if err != nil {
		return nil, nil, err
	}
	said, err := createAsk(m.c.With(ctx), body)
	if err != nil {
		return nil, nil, err
	}
	return sentence(said), nil, nil
}

func (m *mcpServer) askChange(ctx context.Context, _ *mcp.CallToolRequest, in askChangeIn) (*mcp.CallToolResult, any, error) {
	if err := m.nobody(); err != nil {
		return nil, nil, err
	}
	if in.Due == "" && in.To == "" && in.On == "" {
		return nil, nil, fmt.Errorf("a change says what changes: due, to or on")
	}
	var ch askEdit
	var err error
	if in.Due != "" {
		if ch.Due, err = dateOf(in.Due, time.Now()); err != nil {
			return nil, nil, err
		}
	}
	if in.To != "" {
		if ch.To, err = answerersOf(in.To); err != nil {
			return nil, nil, err
		}
	}
	if in.On != "" {
		if ch.On, err = onDoc([]byte(in.On), mcpIn); err != nil {
			return nil, nil, err
		}
	}
	said, err := changeAsk(m.c.With(ctx), in.NameOrID, ch)
	if err != nil {
		return nil, nil, err
	}
	return sentence(said), nil, nil
}

func (m *mcpServer) askWithdraw(ctx context.Context, _ *mcp.CallToolRequest, in askWithdrawIn) (*mcp.CallToolResult, any, error) {
	if err := m.nobody(); err != nil {
		return nil, nil, err
	}
	said, err := withdrawAskVia(m.c.With(ctx), in.NameOrID)
	if err != nil {
		return nil, nil, err
	}
	return sentence(said), nil, nil
}
