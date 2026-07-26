// Package process is the governed-process substrate for verse: a running,
// gated unit of work whose actions pass an inter-action boundary where a
// live-mutable gate vector is evaluated and blocking gates defer to a
// late-bound, substitutable resolver. It is the cleaned, renamed core of the
// autonomy-agnosticity model (formerly agentgate), with the partial higher-level
// trigger/gateway attempts left behind — those are rebuilt top-down elsewhere.
package process

//go:generate go tool tony-codegen -dir .

import (
	"fmt"
	"strings"
)

// Mode is the three-way gate state, on a resolution-expectation axis. It is not
// a block×notify matrix: notification is a delivery preference, not a
// control-flow axis, so it is deliberately absent here.
type Mode uint8

const (
	// Auto: no stop; proceed.
	Auto Mode = iota
	// Ask: blocked; a posed decision is pending; a resolution is required to
	// proceed.
	Ask
	// Park: blocked; no posed decision; resume is a bare "continue" and it may
	// sit indefinitely.
	Park
)

// Ordered by restrictiveness: Auto < Ask < Park. Evaluation takes the max.

func (m Mode) String() string {
	switch m {
	case Auto:
		return "AUTO"
	case Ask:
		return "ASK"
	case Park:
		return "PARK"
	default:
		return "UNKNOWN"
	}
}

// Blocks reports whether the mode halts the process at the boundary.
func (m Mode) Blocks() bool { return m != Auto }

// MarshalText renders the mode as its lowercase name ("auto"/"ask"/"park") so vectors
// read plainly over the wire. encoding.TextMarshaler is the encoding-agnostic spelling:
// it is what tony's gomap honors (on both its reflection and generated paths), and it is
// what encoding/json falls back to when a type has no MarshalJSON. One vocabulary, every
// encoding.
func (m Mode) MarshalText() ([]byte, error) {
	return []byte(strings.ToLower(m.String())), nil
}

// UnmarshalText accepts the name (any case) or the numeric value.
func (m *Mode) UnmarshalText(b []byte) error {
	switch strings.ToLower(strings.TrimSpace(string(b))) {
	case "auto", "0":
		*m = Auto
	case "ask", "1":
		*m = Ask
	case "park", "2":
		*m = Park
	default:
		return fmt.Errorf("invalid gate mode %q (want auto|ask|park)", string(b))
	}
	return nil
}

// MarshalJSON / UnmarshalJSON delegate to the text form. They exist only because
// encoding/json quotes a TextMarshaler's output but would otherwise also accept a bare
// number for an integer-kinded type; keeping the pair explicit means the JSON surface
// stays exactly what it was — a quoted name, decoding a name or a number.
func (m Mode) MarshalJSON() ([]byte, error) {
	t, err := m.MarshalText()
	if err != nil {
		return nil, err
	}
	return []byte(`"` + string(t) + `"`), nil
}

func (m *Mode) UnmarshalJSON(b []byte) error {
	return m.UnmarshalText([]byte(strings.Trim(string(b), `"`)))
}
