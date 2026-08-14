package task

import (
	"context"
	"testing"
)

// What a boundary costs, pinned rather than chased.
//
// The machine is one goroutine calling a pure function, but the SHELL hands the blocking seams
// off to goroutines — which is what lets an intervention preempt a standing question — and the
// scheduling round trip is most of the cost here: a CPU profile of this benchmark is
// pthread_cond_signal, newproc, and park_m, not step.
//
// It is roughly 8× what the previous loop cost, and the previous loop called every seam inline.
// That is left alone on purpose. A boundary in a real run is a model call (seconds) or an
// executor call (milliseconds); microseconds of handoff is under a thousandth of the cheapest
// realistic one, and the only way to get them back is to run the driver and executor inline
// again — which reintroduces the ordering this change exists to fix. An operator's say arriving
// while an action runs must be folded BEFORE the reply that ends the turn is acted on, or a run
// that has just been given new work delivers without it.
//
// So the number is here to be watched, not beaten. If it moves by an order of magnitude,
// something structural changed and this is where it shows.
func BenchmarkBoundary(b *testing.B) {
	const perRun = 1000
	b.ReportMetric(0, "ns/op") // the interesting figure is per boundary, below
	for i := 0; i < b.N; i++ {
		p := NewTask("t", NewSupervisor(&Vector{Default: Auto}, AutoResolver{}),
			&countingDriver{n: perRun}, nopExec{})
		if _, err := p.Run(context.Background()); err != nil {
			b.Fatal(err)
		}
	}
	b.ReportMetric(float64(b.Elapsed().Nanoseconds())/float64(b.N*perRun), "ns/boundary")
}

// countingDriver proposes n trivial actions and then ends its turn.
type countingDriver struct{ n, i int }

func (d *countingDriver) Next(context.Context, StepResult) (ToolUse, bool, error) {
	if d.i >= d.n {
		return ToolUse{}, false, nil
	}
	d.i++
	return ToolUse{Name: "x"}, true, nil
}
