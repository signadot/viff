package versed

import (
	"slices"
	"testing"

	"github.com/signadot/verse/pkg/entity"
	"github.com/signadot/verse/pkg/task"
)

// WHOM A HOSTED TASK'S QUESTION ASKS. A question naming nobody is the docket's, unless the launch
// says its questions are somebody's (an ask's `then`, `as: subject`). $principal — which the `ask`
// tool and a conversation's turn name — is whom the task acts for: its launcher, or the asker of a
// pair. A firing and the system act for nobody, so their ask tool asks the docket.
func TestAHostedQuestionAsksWhomItNames(t *testing.T) {
	const alice, bob = "user:alice@example.com", "user:bob@example.com"
	me := []string{task.PrincipalAnswerer}
	pair := entity.Pair(alice, "verse.charters.review")
	for _, tc := range []struct {
		name, principal string
		launch, named   []string
		want            []string
	}{
		{"an ask, launched by a person", alice, nil, me, []string{alice}},
		{"an ask, launched by an agent", "agent:ci-bot", nil, me, []string{"agent:ci-bot"}},
		{"a nameless gate, launched by a person", alice, nil, nil, nil},
		{"a gate naming $principal and bob", alice, nil, []string{task.PrincipalAnswerer, bob}, []string{alice, bob}},
		{"a gate naming bob", alice, nil, []string{bob}, []string{bob}},
		{"a nameless gate, acting for somebody", pair, me, nil, []string{alice}},
		{"a nameless gate, an ask's then", alice, me, nil, []string{alice}},
		{"a gate naming bob, acting for somebody", pair, me, []string{bob}, []string{bob}},
		{"an ask, acting for somebody", pair, me, me, []string{alice}},
		{"an ask, from a firing", "verse.charters.review", nil, me, nil},
		{"an ask, from the system", entity.SystemDocket, nil, me, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ht := hostedTask{answerers: tc.launch, principal: tc.principal}
			q := hostedQuestion(ht, task.GateQuestion{ID: "t1~1", Answerers: tc.named})
			if !slices.Equal(q.Answerers, tc.want) {
				t.Errorf("held out to %v, want %v", q.Answerers, tc.want)
			}
		})
	}
}
