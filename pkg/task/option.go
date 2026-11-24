package task

import "time"

// Deadline is how long a question stands, and what it settles on when the clock passes.
//
//tony:schemagen=viff-deadline,notag
type Deadline struct {
	// Within is how long the question stands from when it is posed: "24h".
	Within string `tony:"field=within,omitzero"`
	// AtFrom names the field of the subject's value holding the instant it settles at instead —
	// an RFC 3339 time a record carries, `by: "2026-10-02T17:00:00Z"`. It is how an ask says
	// "this week": a date, not a duration from whenever it happened to be posed. Written in place
	// of Within, never beside it; read at the firing, and read AGAIN while the question stands,
	// so whoever may edit the record moves the clock by editing the date. A record without the
	// field has no clock: the question stands until it is answered.
	AtFrom string `tony:"field=atFrom,omitzero"`
	// At is the instant AtFrom resolved to at the firing. The engine's to set, never a charter's:
	// a charter says where the date is, and the record says what it is.
	At      string `tony:"field=at,omitzero"`
	Default string `tony:"field=default,omitzero"`
}

// After is the deadline's duration.
func (d *Deadline) After() (time.Duration, error) {
	if d == nil {
		return 0, nil
	}
	return time.ParseDuration(d.Within)
}

// NoOption is what a deadline settles on when it was given no default: the question closed
// with nothing chosen. It is never an option a ballot can name.
const NoOption = "none"

// Option is one outcome a question can settle on, and how many ballots settle it.
//
//tony:schemagen=viff-option,notag
type Option struct {
	ID   string `tony:"field=id"`
	Need int    `tony:"field=need,omitzero"`
	// Says is what choosing this option MEANS, in words, for whoever is being asked. An id is
	// what an answer is spelled with and a name somebody chose for the rule to read; `staging`
	// beside `proceed` and `deny` says nothing to the person deciding, and a question whose
	// options are three ids is a question answered by guessing. Empty where the id says it all.
	Says string `tony:"field=says,omitzero"`
}
