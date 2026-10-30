package main

import (
	"bytes"
	"context"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/signadot/tony-format/go-tony/ir"
	"github.com/signadot/verse/api"
)

// The question tools mirror `verse question`, the answerer's side: what is asked of you, and
// answering it. The ask tools mirror `verse ask`, the asker's: asking somebody something,
// changing it while it waits, taking it back.

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
		Name: "verse_question",
		Description: "What is asked of you, as `verse question` answers it: the questions waiting on " +
			"you, with all the whole docket, or with id one question as it stands.",
		Annotations: readOnly(),
	}, m.question)
	addTool(m, s, &mcp.Tool{
		Name: "verse_question_answer",
		Description: "Answer one question waiting on you, as you, choosing one of its options, as " +
			"`verse question <id> --answer <option>` does. A question that takes more than one voice " +
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

type askGetIn struct {
	NameOrID string `json:"name_or_id" jsonschema:"one of your asks, by the name you asked it under or its id"`
}

type askListIn struct{}

type askCreateIn struct {
	Name      string `json:"name" jsonschema:"what you call the ask; its id is derived from it and answered"`
	Question  string `json:"question" jsonschema:"what is asked, in the words the answerers read"`
	To        string `json:"to,omitempty" jsonschema:"who is asked: a name or a comma-separated list, in the grammar a grant uses (user:ana@example.com, ana@example.com, group:leads); absent, anyone may answer"`
	Due       string `json:"due,omitempty" jsonschema:"when it settles if nobody answers: an RFC 3339 date or a duration from now (48h)"`
	OnSilence string `json:"on_silence,omitempty" jsonschema:"with due: what it settles on if nobody answers by then, yes or no (the default); yes runs the then"`
	Then      string `json:"then,omitempty" jsonschema:"what follows a yes, on your authority, as a tony document of one key: {spawn: <a spawn, as a charter writes one>}, {answer: {ask: <id>, choice: <option>}} or {ask: {name: …, question: …}}"`
	Else      string `json:"else,omitempty" jsonschema:"what follows a no, or silence settling on no, on your authority; one key, as then takes"`
	About     string `json:"about,omitempty" jsonschema:"the path the ask is about, as a kinded path"`
	Keep      string `json:"keep,omitempty" jsonschema:"how long it stands once settled, as a duration (72h); absent, a week"`
	Label     string `json:"label,omitempty" jsonschema:"what the ask says about itself, name=value, comma-separated (team=payments,kind=deploy)"`
}

type askChangeIn struct {
	NameOrID string `json:"name_or_id" jsonschema:"the ask, by the name you asked it under or its id"`
	Due      string `json:"due,omitempty" jsonschema:"move when it settles: an RFC 3339 date or a duration from now; asks nothing again"`
	To       string `json:"to,omitempty" jsonschema:"change who is asked, as to takes it; asks again"`
	Then     string `json:"then,omitempty" jsonschema:"change what follows a yes, as then takes it; asks again"`
	Else     string `json:"else,omitempty" jsonschema:"change what follows a no, as else takes it; asks again"`
}

type askWithdrawIn struct {
	NameOrID string `json:"name_or_id" jsonschema:"the ask, by the name you asked it under or its id"`
}

func (m *mcpServer) addAskTools(s *mcp.Server) {
	addTool(m, s, &mcp.Tool{
		Name: "verse_ask_get",
		Description: "One of your asks, as `verse ask <name|id>` shows it: by the name you asked it under or " +
			"its id, whether it was answered, whom it waits on, and what its then and else did. " +
			"What waits on you is verse_question.",
		Annotations: readOnly(),
	}, m.askGet)
	addTool(m, s, &mcp.Tool{
		Name: "verse_ask_list",
		Description: "Your asks, as `verse ask` lists them: each with its id, question, who is asked, its " +
			"due and its status. verse_ask_get reads one of them. What waits on you is verse_question.",
		Annotations: readOnly(),
	}, m.askList)
	addTool(m, s, &mcp.Tool{
		Name: "verse_ask_create",
		Description: "Ask somebody something, as you, as `verse ask <name> -q <question>` does: " +
			"the question, who is asked, when it settles if nobody answers, and what follows a yes " +
			"or a no, which runs on your authority. Answers come back through verse_wait.",
		Annotations: writes(false),
	}, m.askCreate)
	addTool(m, s, &mcp.Tool{
		Name: "verse_ask_change",
		Description: "Change one of your asks while it waits, as `verse ask <name> --due/--to/--then/--else` " +
			"does: a new due date asks nothing again; new answerers or a new then or else asks it again.",
		Annotations: writes(false),
	}, m.askChange)
	addTool(m, s, &mcp.Tool{
		Name:        "verse_ask_withdraw",
		Description: "Take back one of your asks, as `verse ask <name> --withdraw` does: it is removed, and its question withdrawn.",
		Annotations: writes(true),
	}, m.askWithdraw)
}

func (m *mcpServer) askGet(ctx context.Context, _ *mcp.CallToolRequest, in askGetIn) (*mcp.CallToolResult, any, error) {
	if in.NameOrID == "" {
		return nil, nil, fmt.Errorf("name the ask, by its name or its id")
	}
	return m.askRead(askShown(m.c.With(ctx), in.NameOrID))
}

func (m *mcpServer) askList(ctx context.Context, _ *mcp.CallToolRequest, _ askListIn) (*mcp.CallToolResult, any, error) {
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

func (m *mcpServer) askCreate(ctx context.Context, _ *mcp.CallToolRequest, in askCreateIn) (*mcp.CallToolResult, any, error) {
	if err := m.nobody(); err != nil {
		return nil, nil, err
	}
	if strings.TrimSpace(in.Name) == "" {
		return nil, nil, fmt.Errorf("an ask needs a name")
	}
	spec := api.AskSpec{Name: in.Name, Question: in.Question, To: in.To, Due: in.Due, OnSilence: in.OnSilence,
		About: in.About, Keep: in.Keep, Label: in.Label}
	var err error
	if in.Then != "" {
		if spec.Then, err = thenDoc([]byte(in.Then), mcpIn); err != nil {
			return nil, nil, err
		}
	}
	if in.Else != "" {
		if spec.Else, err = thenDoc([]byte(in.Else), mcpIn); err != nil {
			return nil, nil, err
		}
	}
	body, err := newAsk(spec)
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
	if in.Due == "" && in.To == "" && in.Then == "" && in.Else == "" {
		return nil, nil, fmt.Errorf("a change says what changes: due, to, then or else")
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
	if in.Then != "" {
		if ch.Then, err = thenDoc([]byte(in.Then), mcpIn); err != nil {
			return nil, nil, err
		}
	}
	if in.Else != "" {
		if ch.Else, err = thenDoc([]byte(in.Else), mcpIn); err != nil {
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
