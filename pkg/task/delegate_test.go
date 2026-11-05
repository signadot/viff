package task

import (
	"context"
	"testing"

	"github.com/signadot/verse/pkg/systems/systemstest"
)

// A delegation a row names asks first, and `deny` is a no: the sub-agent never starts.
func TestADelegationDeniedStartsNothing(t *testing.T) {
	reg := NewRegistry()
	g := Autonomy{
		{Name: "delegate", Match: on("agent", "call-sub-agent", "agent", "child"), Then: Then{Mode: ThenAsk}},
		{Then: Then{Mode: ThenAllow}}}
	parent := NewTask("p", systemstest.Opened(NewSupervisor(anyEnv(), g, reg), anyEnv()), &callThenDone{}, &recExec{})
	parent.SetDelegate(func(pTask *Task, req *CallAgentRequest) (*Task, error) {
		t.Error("a denied delegation started a child")
		return pTask.NewChild("kid", req, &scriptDriver{}, &recExec{}), nil
	})
	done := make(chan struct{})
	go func() { parent.Run(context.Background()); close(done) }()
	waitFor(t, func() bool { return len(reg.Pending()) == 1 })
	reg.Answer(reg.Pending()[0].Question.ID, Choose(Deny, "human"))
	<-done
}
