package main

import (
	"fmt"
	"net/url"

	"github.com/scott-cotton/cli"

	"go.viff.dev/viff/pkg/api"
	"go.viff.dev/viff/pkg/schedule"
)

// viff schedule: the schedules a viff ticks on. Bare, it lists every record with its period,
// the instant the daemon is armed against and the tick standing under it; `tick` writes the
// tick for a past instant, the same entity the schedule would have written, so a rule about it
// fires as it would have. A schedule is declared in a charter beside the strata (`{name, every,
// from}`) and removed as a rule is; the standard ones come with the viff.
func scheduleCmd() *cli.Command {
	return filteredCollection("schedule", "schedules", "/schedules",
		"schedule — the schedules the viff ticks on; tick <name> --due <instant> backfills one",
		nil, func() url.Values { return nil }, scheduleTickCmd())
}

type scheduleTickConfig struct {
	*cli.Command
	Due string `cli:"name=due desc='the instant the tick was due, RFC3339, on the grid of the schedule and in the past'"`
	At  string `cli:"name=at desc='the package address the schedule was declared in; none for baseline'"`
}

func scheduleTickCmd() *cli.Command {
	cfg := &scheduleTickConfig{}
	return cli.NewCommandAt(&cfg.Command, "tick").
		WithSynopsis("tick <name> --due <instant> [--at <address>] — write the tick for a past instant, as the schedule would have").
		WithOpts(mustOpts(cfg)...).
		WithRun(cfg.run)
}

func (cfg *scheduleTickConfig) run(cc *cli.Context, args []string) error {
	name, err := oneArg(cfg.Command, cc, args, "schedule tick wants a schedule's name")
	if err != nil {
		return err
	}
	if cfg.Due == "" {
		return fmt.Errorf("%w: schedule tick wants --due <instant>", cli.ErrUsage)
	}
	id := schedule.RecordID(cfg.At, name)
	resp, err := httpPost("/schedules/"+url.PathEscape(id)+"/tick", api.ScheduleTick{Due: cfg.Due})
	if err != nil {
		return fmt.Errorf("schedule tick: %w", err)
	}
	return printBody(cc, resp)
}
