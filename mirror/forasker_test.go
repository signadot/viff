package mirror

import (
	"reflect"
	"testing"

	"github.com/signadot/verse/entity"
	"github.com/signadot/verse/task"
)

// WHOM A RELAYED TASK'S QUESTION ASKS: a gate naming its answerers keeps them, then a frame's
// own, then the launch's default for a question naming nobody (Meta.Answerers), and otherwise
// the docket. $principal is put as whom the task acts for, and dropped where that is nobody.
func TestARelayedQuestionAsksWhomItNames(t *testing.T) {
	const daniel, ani = "user:daniel@example.com", "user:ani@example.com"
	me := []string{task.PrincipalAnswerer}
	named := []task.Gate{{Name: "ship", Answerers: []string{ani}}}
	mine := []task.Gate{{Name: "ship", Answerers: []string{task.PrincipalAnswerer, "group:payments"}}}
	for _, c := range []struct {
		name string
		m    Meta
		ev   task.Frame
		want []string
	}{
		{"a launch whose questions are its asker's", Meta{Asker: daniel, Answerers: me}, task.Frame{Gate: "ship"}, []string{daniel}},
		{"a hand-run task's nameless gate", Meta{Asker: daniel}, task.Frame{Gate: "ship"}, nil},
		{"a gate naming its answerers", Meta{Asker: daniel, Answerers: me, Gates: named}, task.Frame{Gate: "ship"}, []string{ani}},
		{"a gate naming $principal and a group", Meta{Asker: daniel, Gates: mine}, task.Frame{Gate: "ship"}, []string{daniel, "group:payments"}},
		{"a frame naming its answerers", Meta{Asker: daniel, Answerers: me}, task.Frame{Gate: "ship", Answerers: []string{"user:bo@example.com"}}, []string{"user:bo@example.com"}},
		{"the ask tool, from a person's task", Meta{Asker: daniel}, task.Frame{Gate: "ask", Answerers: me}, []string{daniel}},
		{"the ask tool, acting for nobody", Meta{}, task.Frame{Gate: "ask", Answerers: me}, nil},
		{"the ask tool, acting for the system", Meta{Asker: entity.SystemCharter}, task.Frame{Gate: "ask", Answerers: me}, nil},
	} {
		if got := answerersFor(c.m, c.ev); !reflect.DeepEqual(got, c.want) {
			t.Errorf("%s: %v, want %v", c.name, got, c.want)
		}
	}
}
