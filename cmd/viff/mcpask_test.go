package main

import (
	"strings"
	"testing"

	"go.viff.dev/viff/pkg/mirror"
)

// ASKING THROUGH THE TOOLS, END TO END: victor asks oscar, oscar sees it waiting and answers it,
// victor sees the answer; then a change and a withdrawal. Each person is their own server.
func TestTheAskToolsAskAnswerAndWithdraw(t *testing.T) {
	store := serveAsking(t)
	dan, dav := mcpSessionAs(t, victor), mcpSessionAs(t, oscar)

	out, isErr := callTool(t, dan, "viff_ask_create", map[string]any{
		"name": "land", "question": "Review viff/ask?", "to": "oscar@example.com", "due": "48h",
		"on": `[{match: {verdict: proceed}, then: {spawn: {prompt: "merge viff/ask"}}}]`,
	})
	if isErr || !strings.HasPrefix(out, "raised question ") || !strings.HasSuffix(out, ": Review viff/ask?") {
		t.Fatalf("the ask: error=%v %q", isErr, out)
	}
	id := strings.TrimSuffix(strings.TrimPrefix(out, "raised question "), ": Review viff/ask?")
	qid := questionOn(t, store, mirror.AskRef(id))

	if out, isErr := callTool(t, dav, "viff_question", nil); isErr || !strings.Contains(out, qid) {
		t.Fatalf("oscar's questions: error=%v\n%s", isErr, out)
	}
	if out, isErr := callTool(t, dan, "viff_question", nil); isErr || strings.Contains(out, qid) {
		t.Fatalf("victor's questions hold his own ask to oscar: error=%v\n%s", isErr, out)
	}
	if out, isErr := callTool(t, dav, "viff_question_answer", map[string]any{"id": qid, "option": "proceed"}); isErr || out != "answered "+qid {
		t.Fatalf("oscar's answer: error=%v %q", isErr, out)
	}
	waitFieldCLI(t, store, mirror.AskRef(id), "answer")
	if out, isErr := callTool(t, dan, "viff_ask_get", map[string]any{"name_or_id": " land "}); isErr || !strings.Contains(out, "answer: yes") {
		t.Fatalf("the answer is not on victor's ask: error=%v\n%s", isErr, out)
	}
	// Named nothing, an ask is raised under the name viff gives it, as `viff ask -q` raises it.
	if out, isErr := callTool(t, dan, "viff_ask_create", map[string]any{"question": "Coffee?"}); isErr || !strings.HasPrefix(out, "raised question run-") {
		t.Fatalf("an ask named nothing: error=%v %q", isErr, out)
	}

	out, _ = callTool(t, dan, "viff_ask_create", map[string]any{"name": "lunch", "question": "Lunch?", "to": "oscar@example.com"})
	second := strings.TrimSuffix(strings.TrimPrefix(out, "raised question "), ": Lunch?")
	questionOn(t, store, mirror.AskRef(second))
	if out, isErr := callTool(t, dan, "viff_ask_change", map[string]any{"name_or_id": "lunch", "due": "72h"}); isErr || !strings.HasPrefix(out, "moved "+second+" to ") {
		t.Fatalf("the change: error=%v %q", isErr, out)
	}
	if out, isErr := callTool(t, dan, "viff_ask_withdraw", map[string]any{"name_or_id": "lunch"}); isErr || out != "withdrew "+second {
		t.Fatalf("the withdrawal: error=%v %q", isErr, out)
	}
	if out, isErr := callTool(t, dan, "viff_ask_list", nil); isErr || strings.Contains(out, second) || !strings.Contains(out, id) {
		t.Fatalf("victor's asks after the withdrawal: error=%v\n%s", isErr, out)
	}
}

// A TOOL ANSWERS WHAT THE COMMAND SAYS: the listing of victor's asks is the CLI's, word for word.
func TestTheAskToolListsAsTheCLIDoes(t *testing.T) {
	store := serveAsking(t)
	id := askAs(t, victor, "land", "-q", "Review?", "--to", "oscar@example.com")
	// Posed first: posing rewrites the record, and the two reads must see one.
	questionOn(t, store, mirror.AskRef(id))
	as(t, victor)
	want, err := runCLI(t, "ask")
	if err != nil {
		t.Fatal(err)
	}
	if out, _ := callTool(t, mcpSessionAs(t, victor), "viff_ask_list", nil); out != strings.TrimRight(want, "\n") {
		t.Fatalf("the tool said\n%s\nthe CLI\n%s", out, want)
	}
}

// NOBODY CONFIGURED, NOTHING DONE: a write as nobody is refused by the server before it is sent.
func TestTheAskToolsRefuseNobody(t *testing.T) {
	serveAsking(t)
	out, isErr := callTool(t, mcpSessionAs(t, ""), "viff_ask_create", map[string]any{"name": "x", "question": "Hm?"})
	if !isErr || !strings.Contains(out, "nobody is configured") {
		t.Fatalf("error=%v %q", isErr, out)
	}
}
