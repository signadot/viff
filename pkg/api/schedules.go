package api

// ScheduleRow is one schedule as `viff schedule` lists it: its record's id (the declared name,
// `<address>:<name>` for a package's), its grid, the instant the daemon is armed against, and
// the tick standing under it.
//
//tony:schemagen=viff-schedule-row,notag
type ScheduleRow struct {
	ID    string `tony:"field=id"`
	Every string `tony:"field=every"`
	From  string `tony:"field=from,omitzero"`
	Next  string `tony:"field=next,omitzero"`
	// Tick is the due instant of the standing tick, and Missed how many instants before it
	// went unticked; empty when nothing stands.
	Tick   string `tony:"field=tick,omitzero"`
	Missed int64  `tony:"field=missed,omitzero"`
	// At is the package address the schedule was declared in, empty for baseline; By its
	// installer.
	At string `tony:"field=at,omitzero"`
	By string `tony:"field=by,omitzero"`
}

// ScheduleTick is a backfill (`viff schedule tick`): the past instant to write the tick for.
//
//tony:schemagen=viff-schedule-tick,notag
type ScheduleTick struct {
	Due string `tony:"field=due"`
}

// Ticked is what `POST /schedules/{id}/tick` answers: the schedule, and the tick it backfilled,
// by its key (schedule.Key).
//
//tony:schemagen=viff-ticked,notag
type Ticked struct {
	Schedule string `tony:"field=schedule"`
	Tick     string `tony:"field=tick"`
}
