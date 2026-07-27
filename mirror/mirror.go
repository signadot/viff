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
	"strings"

	"github.com/signadot/verse/entity"
	"github.com/signadot/verse/process"
)

// System is the entity-verse system reflected processes live under. Keep it out
// of any default/ambient trigger match set.
const System = "process"

// The reflected status vocabulary is process.Status (running/completed/cancelled/
// failed/orphaned), carried on the entity payload as the "status" field; its
// Terminal() decides when to stop projecting.

// Ref is the entity that mirrors process id.
func Ref(id string) entity.Ref { return entity.Ref{System: System, Kind: "instance", ID: id} }

// Meta is the provenance Reflect publishes alongside a process's own state: where it runs
// and what it is about. Grouped rather than passed as four positional strings, which is
// both unreadable at the call site and easy to transpose.
type Meta struct {
	// Subject is the flow this process belongs to. It is carried on the entity as "flow"
	// so a completion trigger can advance that subject when the process finishes, and does
	// not collide with the $subject binding (the matched ref).
	Subject string
	// Runner is the process-control backend hosting it — "local", "broker", or a remote URL — so a
	// restart can decide resume-or-orphan.
	Runner string
	// Chain is the kpath of the action that spawned it, inside its trigger: "review",
	// "review.onDone". Empty for a process with no trigger behind it (`verse process run`).
	Chain string
	// Task is what the process was asked to do, already bound. Small, and the process's
	// identity to a reader — so it belongs on the index next to what the process produced,
	// not only on the read plane that outlives nothing.
	Task string
}

// TriggerOf returns the trigger a chain belongs to — its root kpath segment. The chain is
// a kpath into that trigger's Action, so the root is everything up to the first descent.
func TriggerOf(chain string) string {
	if i := strings.IndexAny(chain, ".[{("); i >= 0 {
		return chain[:i]
	}
	return chain
}

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
// The provenance a process carries beyond its own state is Meta.
func Reflect(store entity.Store, id string, h process.Handle, m Meta) {
	ch, unsub := h.Subscribe()
	// A brokered process starts in the pre-running `unclaimed` state — it is hosted on
	// the broker awaiting an executor; the claim emits an EvStatus{running} that moves it
	// on. Local/remote processes execute immediately, so they start `running`.
	initial := process.Running
	if m.Runner == "broker" {
		initial = process.Unclaimed
	}
	st := entity.Obj{"status": string(initial)}
	if m.Subject != "" {
		st["flow"] = m.Subject
	}
	if m.Runner != "" { // which runner runs this — so a restart can resume-or-orphan
		st["runner"] = m.Runner
	}
	// Where this process came from in the charter. `chain` is the spawning action's kpath
	// inside its trigger (review, review.onDone, …) and `trigger` is its root segment,
	// published separately because a payload predicate is equality-only: "every process of
	// the review trigger" is then `fields: {trigger: review}` rather than a glob over ids.
	if m.Chain != "" {
		st["chain"] = m.Chain
		st["trigger"] = TriggerOf(m.Chain)
	}
	if m.Task != "" {
		st["task"] = m.Task
	}
	// A dial-in runner (broker) has a worker-facing base URL distinct from how verse
	// controls it: publish it so a worker can be told where to dial in (the mint→publish→
	// dial loop) rather than being pre-configured. Local/remote-processd handles have none.
	if d, ok := h.(interface{ DialURL() string }); ok {
		if u := d.DialURL(); u != "" {
			st["dialURL"] = u
		}
	}
	store.Put(Ref(id), st.Payload())
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
					if process.Status(ev.Text).Terminal() {
						if r := h.Result(); r != "" { // keep the full final answer, durably
							st["result"] = r
						}
						store.Put(Ref(id), st.Payload())
						return // stop projecting; the entity remains, marked terminal
					}
				}
				store.Put(Ref(id), st.Payload())
				continue
			case process.EvVector: // an autonomy slide — project the dial so `entity get` shows it
				st["vector"] = ev.Mode
				if ev.By != "" {
					st["vectorBy"] = ev.By
				}
			default: // boundary / steer / child / gap: not projected (index, not warehouse)
				continue
			}
			store.Put(Ref(id), st.Payload()) // Store clones the payload; safe to reuse st
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

// ClaimKind is the kind a run CLAIM lives under: process:claim:<chain>@<subject>. It shares
// the process system deliberately — the engine keeps that system out of the default trigger
// match set, so writing a claim cannot wake the very trigger that took it. A claim is
// machinery, and the ignition guard is where machinery belongs.
const ClaimKind = "claim"

// ClaimRef is the entity a run is claimed at. The id is the spawn id, so the claim and the
// process it guards share one address — there is nothing to keep in step.
func ClaimRef(id string) entity.Ref { return entity.Ref{System: System, Kind: ClaimKind, ID: id} }
