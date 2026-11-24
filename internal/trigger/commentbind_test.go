package trigger

import (
	"testing"

	"github.com/signadot/tony-format/go-tony/ir"

	"go.viff.dev/viff/pkg/codec"
	"go.viff.dev/viff/pkg/entity"
	"go.viff.dev/viff/pkg/launch"
	"go.viff.dev/viff/pkg/systems"
)

// A PAYLOAD THAT STARTS WITH A COMMENT BINDS ITS FIELDS, as one without does.
//
// A record put from a tony file with a header comment is stored with it, and its root is a
// Comment node wrapping the object. entity.IsObj sees through the wrapper, so the condition
// matched and the rule fired; the binding read the root with ir.ToMap, which does not, so no
// field was bound.
func TestACommentedPayloadBindsItsFields(t *testing.T) {
	payload, err := codec.ParseNode([]byte("# a comment\nrepo: signadot/viff\nhandoff: {summary: done}\n"), codec.Tony)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if payload.Type != ir.CommentType || !entity.IsObj(payload) {
		t.Fatalf("the payload is a %s (IsObj %v): the case this test is about is a comment wrapping an object",
			payload.Type, entity.IsObj(payload))
	}
	spec := launch.Spec{
		Prompt: "flat=$[repo] deep=$[subject.value.handoff.summary]",
		Environment: systems.Environment{Systems: systems.Systems{
			"git": {{Attributes: map[string]string{"repo": "$[subject.value.repo]"}}},
		}},
	}
	b := bindingFor(payload, entity.RefAt("demo", "work", "1"), payload)
	got := bound(t, spec, b)
	if got.Prompt != "flat=signadot/viff deep=done" {
		t.Errorf("prompt = %q", got.Prompt)
	}
	if repo := got.Environment.Systems["git"][0].Attributes["repo"]; repo != "signadot/viff" {
		t.Errorf("environment.systems.git: repo = %q", repo)
	}
}
