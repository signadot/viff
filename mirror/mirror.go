// Package mirror reflects a running Process's state into the entity-verse as a
// process:instance:<id> entity — one-way, index-not-warehouse. This is what makes
// "observe another process" the same act as "observe an entity": once a process
// is in the verse, a trigger can match on its state like any other entity.
//
// Reflected entities live in the "process" system, which the trigger engine keeps
// OUT of the default match set — meta-observation is opt-in, so the loop cannot
// ignite on its own machinery.
package mirror

import (
	"github.com/example/infra/verse/entity"
	"github.com/example/infra/verse/process"
)

// System is the entity-verse system reflected processes live under. Keep it out
// of any default/ambient trigger match set.
const System = "process"

// Status values a reflected process carries in its entity payload.
const (
	StatusRunning   = "running"
	StatusCompleted = "completed"
	StatusCancelled = "cancelled"
	// StatusOrphaned marks a process whose runtime died (e.g. a monolith restart):
	// the in-memory process is gone, so its last durable state is retired rather than
	// left claiming to run. It is terminal but did NOT complete its work — a
	// completion trigger (status=completed) deliberately does not match it.
	StatusOrphaned = "orphaned"
)

// Terminal reports whether a reflected status is an end state (no further work).
func Terminal(status string) bool {
	return status == StatusCompleted || status == StatusCancelled || status == StatusOrphaned
}

// Ref is the entity that mirrors process id.
func Ref(id string) entity.Ref { return entity.Ref{System: System, Kind: "instance", ID: id} }

// Reflect subscribes to p's read plane and mirrors its state into store as a
// process:instance:<id> entity, until p reaches a terminal status. It projects
// significant transitions (pending gate, last action, status) plus a bounded view
// of the process's output — the latest narration ("say") and last tool output
// ("lastOutput") as previews, and the full final answer ("result") on completion.
// It stays an index, not a warehouse: the running fine grain (and any large output)
// lives on the read plane; the entity keeps the outcome, durably, so `entity get
// process:instance:<id>` shows what a process said and produced even after its
// ephemeral read plane is gone. Projection is one-way: an entity is a view, never a
// handle (control still goes through the process API).
//
// subject, if non-empty, is the flow this process belongs to; it is carried on the
// entity as "flow" so a completion trigger can advance that subject when the
// process finishes. It does not collide with the $subject binding (the matched ref).
func Reflect(store entity.Store, id string, h process.ProcHandle, subject, policy string) {
	ch, unsub := h.Subscribe()
	st := entity.Payload{"status": StatusRunning}
	if subject != "" {
		st["flow"] = subject
	}
	if policy != "" { // which controlPolicy runs this — so a restart can resume-or-orphan
		st["policy"] = policy
	}
	// A dial-in policy (broker) has a worker-facing base URL distinct from how verse
	// controls it: publish it so a worker can be told where to dial in (the mint→publish→
	// dial loop) rather than being pre-configured. Local/remote-processd handles have none.
	if d, ok := h.(interface{ DialURL() string }); ok {
		if u := d.DialURL(); u != "" {
			st["dialURL"] = u
		}
	}
	store.Put(Ref(id), st)
	go func() {
		defer unsub()
		for ev := range ch {
			if ev.Cursor > 0 { // high-water for resume: verse re-subscribes ?since=cursor
				st["cursor"] = ev.Cursor
			}
			switch ev.Kind {
			case process.EvPending: // now blocked on a gate — visible to other processes
				st["pending"] = ev.ID
				st["pendingGate"] = ev.Gate
			case process.EvDecision:
				delete(st, "pending")
				delete(st, "pendingGate")
				st["lastAction"] = ev.Tool
			case process.EvExec:
				st["lastAction"] = ev.Tool
				if ev.Text != "" { // the last action's output (already a bounded summary)
					st["lastOutput"] = ev.Text
				}
			case process.EvSay: // an utterance; only the process's OWN words are the preview
				if ev.By == "" { // By set = a say-to folded in, not the agent's narration
					st["say"] = preview(ev.Text)
				}
			case process.EvStatus:
				// paused/resumed ride the same channel as lifecycle status but are
				// transient control acks, not a lifecycle state — project them as a
				// `paused` flag so `status` stays running→completed/cancelled.
				switch ev.Text {
				case "paused":
					st["paused"] = true
				case "resumed":
					st["paused"] = false
				default:
					st["status"] = ev.Text
					if Terminal(ev.Text) {
						if r := h.Result(); r != "" { // keep the full final answer, durably
							st["result"] = r
						}
						store.Put(Ref(id), st)
						return // stop projecting; the entity remains, marked terminal
					}
				}
				store.Put(Ref(id), st)
				continue
			default: // boundary / steer / child / gap: not projected (index, not warehouse)
				continue
			}
			store.Put(Ref(id), st) // Store clones the payload; safe to reuse st
		}
	}()
}

// preview bounds a narration line for the index; the full text is on the read plane
// (and the full final answer is projected separately as "result").
func preview(s string) string {
	const max = 500
	if len(s) <= max {
		return s
	}
	return s[:max] + "…"
}
