package mirror

import (
	"reflect"
	"testing"

	"go.viff.dev/viff/pkg/entity"
	"go.viff.dev/viff/pkg/task"
)

// WHOM A RELAYED TASK'S QUESTION ASKS: a gate naming its answerers keeps them, then a frame's
// own, then the launch's default for a question naming nobody (Meta.Answerers), and otherwise
// the docket. $principal is put as whom the task acts for, and dropped where that is nobody.
func TestARelayedQuestionAsksWhomItNames(t *testing.T) {
	const victor, peggy = "user:victor@example.com", "user:peggy@example.com"
	me := []string{task.PrincipalAnswerer}
	named := task.Autonomy{{Name: "ship", Then: task.Then{Mode: task.ThenAsk, Ask: &task.Ask{Answerers: []string{peggy}}}}}
	mine := task.Autonomy{{Name: "ship", Then: task.Then{Mode: task.ThenAsk, Ask: &task.Ask{Answerers: []string{task.PrincipalAnswerer, "group:payments"}}}}}
	for _, c := range []struct {
		name string
		m    Meta
		ev   task.Event
		want []string
	}{
		{"a launch whose questions are its asker's", Meta{ActsFor: victor, Answerers: me}, task.Event{Gate: "ship"}, []string{victor}},
		{"a hand-run task's nameless gate", Meta{ActsFor: victor}, task.Event{Gate: "ship"}, nil},
		{"a gate naming its answerers", Meta{ActsFor: victor, Answerers: me, Autonomy: named}, task.Event{Gate: "ship"}, []string{peggy}},
		{"a gate naming $principal and a group", Meta{ActsFor: victor, Autonomy: mine}, task.Event{Gate: "ship"}, []string{victor, "group:payments"}},
		{"a frame naming its answerers", Meta{ActsFor: victor, Answerers: me}, task.Event{Gate: "ship", Answerers: []string{"user:bo@example.com"}}, []string{"user:bo@example.com"}},
		{"the ask tool, from a person's task", Meta{ActsFor: victor}, task.Event{Gate: "ask", Answerers: me}, []string{victor}},
		{"the ask tool, acting for nobody", Meta{}, task.Event{Gate: "ask", Answerers: me}, nil},
		{"the ask tool, acting for the system", Meta{ActsFor: entity.SystemCharter}, task.Event{Gate: "ask", Answerers: me}, nil},
	} {
		if got := answerersFor(c.m, c.ev); !reflect.DeepEqual(got, c.want) {
			t.Errorf("%s: %v, want %v", c.name, got, c.want)
		}
	}
}
