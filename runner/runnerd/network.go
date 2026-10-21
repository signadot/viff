package runnerd

import (
	"fmt"
	"net"

	"github.com/signadot/verse/runner"
	"github.com/signadot/verse/systems"
	"github.com/signadot/verse/task"
)

// connectionRefused is why a connection the task's proxy is asked to open is refused, or nil:
// the network's `tcp-client` step to host and port (lower case, no trailing dot), decided as every
// step is. Outside the environment's `tcp-client` shapes it is refused, saying what they are; a
// shape naming no port allows any. Within them a row of the task's current autonomy on the
// network decides: allow lets it through and deny refuses it (such a row may not ask, since a
// connection can't wait for an answer). The default row does not decide a connection: the shapes
// are what the spawn's author said the task reaches, so one no row on the network matches goes
// ahead. Then the runner's ceiling over the network, whatever the rows said: the environment lies
// within it, so a refusal there is the launch's check having missed one.
func connectionRefused(env systems.Environment, p task.Autonomy, ceiling systems.Ceiling, host, port string) error {
	st := task.Step{Name: task.StepNetworkTCPClient,
		Fields: map[string]string{"mode": runner.NetworkAllow, "host": host, "port": port}}
	dest := net.JoinHostPort(host, port)
	if why, outside := env.Refuses(st.Name, st.Fields); outside {
		return fmt.Errorf("egress: %s is outside the environment: %s", dest, why)
	}
	if row, i := p.Decide(st); row.Match != nil && row.Match.System == "network" && row.Then.Mode == task.ThenDeny {
		return fmt.Errorf("egress: %s is refused by the autonomy's row %s", dest, p.NameOf(i))
	}
	if len(ceiling) > 0 {
		if why, beyond := ceiling.Refuses(st.Name, st.Fields); beyond {
			return fmt.Errorf("egress: %s is beyond the runner's ceiling for network: %s", dest, why)
		}
	}
	return nil
}
