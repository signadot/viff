package trigger

import (
	"sort"

	"github.com/signadot/verse/entity"
	"github.com/signadot/verse/mirror"
	"github.com/signadot/verse/process"
)

// The flow TREE is what a subject's work looks like as a shape rather than a list: the
// trigger that acted on it, the process that ran, and the completion hooks hanging off that
// process — each hook a branch, nested as deep as the charter nests them.
//
// It is DERIVED, never stored. Materializing it would rebuild the flows[subject] index that
// migration step 4 deliberately replaced with a query (docs/trigger-model.md), and a stored
// tree would need maintaining on every event. So it is assembled per request from three
// sources, and the interesting part is that no single one of them is sufficient:
//
//   - the CHARTER gives the shape. A trigger's Action tree is known before anything runs, so
//     a hook that has not fired yet can be shown as pending rather than be missing. Nothing
//     in the runtime knows this — it is the only source that can speak about the future.
//   - the MIRROR gives the history. `retire` deletes a finished process from the live
//     registry (engine.go), so a completed chain exists ONLY as process:instance entities.
//     This is why the chain is published there: without it, reading history back would mean
//     parsing ids.
//   - the LIVE registry gives the present — what is still tracked and steerable.
//
// The join key is the chain: a kpath naming the action that spawned each process inside its
// trigger. Parent/child in the tree IS prefix/extension in the kpath, so the nesting needs
// no separate parent pointer to maintain.

// FlowNode is one position in a subject's flow: an action in some trigger, and the process
// it did or did not spawn.
type FlowNode struct {
	// Chain is the action's kpath inside its trigger — "review", "review.onDone". Empty for
	// a process with no trigger behind it (`verse process run`, or an adopted process whose
	// chain was never published).
	Chain string `tony:"field=chain,omitzero"`
	// Kind is what KIND of position this is: "root" for the trigger's own spawn, the hook's
	// field name ("onDone", "onFailed", …) for a completion branch, and "process" for a
	// process that answers to no charter action. It is carried explicitly rather than left to
	// be re-derived from the chain's last segment, because typing a node is the response's
	// job — the id's job is only to be unique and pasteable.
	Kind string `tony:"field=kind"`
	// ID is the process this position spawned; empty when it has not run.
	ID     string `tony:"field=id,omitzero"`
	Task   string `tony:"field=task,omitzero"`
	Status string `tony:"field=status"`
	// Pending, Gate and LastAction are the live detail, present only while a process is
	// in flight.
	Gate       string     `tony:"field=gate,omitzero"`
	LastAction string     `tony:"field=lastAction,omitzero"`
	Children   []FlowNode `tony:"field=children,omitzero"`
}

// Statuses a node carries that no process ever reports: a position in the charter that has
// not run. Distinguishing them matters — "pending" invites waiting for something that is
// still coming, "skipped" says the branch is dead because its parent ended another way.
const (
	StatusPending = "pending" // its parent has not finished, so this may still run
	StatusSkipped = "skipped" // its parent ended on a DIFFERENT terminal status
	// StatusApplied is a hook that fired but spawned nothing — a `set` leaves no process,
	// so there is no runtime trace to read it back from. It is INFERRED from the engine's
	// own rule (a hook fires exactly when its process reaches that status), which is the
	// same kind of inference pending/skipped already are. Reporting it as pending would be
	// worse than inferring: the act is over, and nothing is coming.
	StatusApplied = "applied"
)

// FlowTree is one subject's flow: every root that acted on it, with its branches.
type FlowTree struct {
	Subject string     `tony:"field=subject"`
	Roots   []FlowNode `tony:"field=roots,omitzero"`
}

// FlowTrees renders the flow tree of every subject the address selects. A concrete address
// yields one tree (possibly empty); a pattern yields one per subject that has processes.
//
// Subjects come from the mirror rather than from the live registry, so a subject whose work
// has all finished still has a tree — which is the point of asking after the fact.
func (e *Engine) FlowTrees(m Match) []FlowTree {
	bySubject := map[string][]entity.Entity{}
	if l, ok := e.store.(entity.Lister); ok {
		for _, ent := range l.List(func(r entity.Ref) bool { return r.System == mirror.System }) {
			subj, _ := ent.Payload["flow"].(string)
			if subj == "" {
				continue // a process about nothing is in no flow
			}
			ref, err := entity.ParseRef(subj)
			if err != nil || !m.MatchRef(ref) {
				continue
			}
			bySubject[subj] = append(bySubject[subj], ent)
		}
	}
	// A live process whose mirror has not landed yet (or whose store cannot list) still
	// belongs in the tree: the registry is the present, and a tree that omits what is
	// running now would be worse than one that omits what ran yesterday.
	for _, fp := range e.matchFlow(m) {
		if _, seen := bySubject[fp.subject]; !seen {
			bySubject[fp.subject] = nil
		}
	}

	subjects := make([]string, 0, len(bySubject))
	for s := range bySubject {
		subjects = append(subjects, s)
	}
	sort.Strings(subjects) // deterministic for a CLI listing

	trees := make([]FlowTree, 0, len(subjects))
	for _, s := range subjects {
		trees = append(trees, FlowTree{Subject: s, Roots: e.rootsFor(bySubject[s])})
	}
	return trees
}

// rootsFor builds one subject's roots from its mirrored processes, then extends each root
// with the charter shape it has not reached yet.
func (e *Engine) rootsFor(ents []entity.Entity) []FlowNode {
	// Index the run processes by chain. A chain is unique per (trigger, subject) by
	// construction — spawnID derives from exactly that pair — so this cannot collide.
	run := map[string]entity.Entity{}
	var bare []FlowNode // processes with no chain: no charter action to hang them under
	for _, ent := range ents {
		chain, _ := ent.Payload["chain"].(string)
		if chain == "" {
			bare = append(bare, nodeFrom("", "process", ent))
			continue
		}
		run[chain] = ent
	}

	// One root per trigger that has actually acted on this subject. A trigger that never
	// fired for it is not part of its flow, so the charter is read only BELOW a root that ran.
	roots := map[string]bool{}
	for chain := range run {
		roots[mirror.TriggerOf(chain)] = true
	}
	names := make([]string, 0, len(roots))
	for n := range roots {
		names = append(names, n)
	}
	sort.Strings(names)

	out := make([]FlowNode, 0, len(names)+len(bare))
	for _, name := range names {
		t, ok := e.triggerNamed(name)
		if !ok {
			// The trigger was removed after it spawned. The history is still true, so show
			// what ran; there is simply no shape left to extend it with.
			out = append(out, e.node(name, "root", run, nil, ""))
			continue
		}
		out = append(out, e.node(name, "root", run, &t.Action, ""))
	}
	sort.Slice(bare, func(i, j int) bool { return bare[i].ID < bare[j].ID })
	return append(out, bare...)
}

// node builds the node at chain and recurses into the action's completion hooks. act is the
// charter action at this position (nil once the charter runs out — a removed trigger, or a
// process whose action is gone); parentStatus is the terminal status of the process above,
// which decides whether an unrun branch is still coming or is dead.
func (e *Engine) node(chain, kind string, run map[string]entity.Entity, act *Action, parentStatus string) FlowNode {
	n := FlowNode{Chain: chain, Kind: kind, Status: StatusPending}
	ent, didRun := run[chain]
	switch {
	case didRun:
		n = nodeFrom(chain, kind, ent)
	case kind == "root": // nothing above it to be unreachable from
	case act != nil && act.Spawn == nil && process.TerminalStatus(parentStatus) &&
		kind == hookField(process.Status(parentStatus)):
		// A set-only hook whose parent ended on exactly this status: it fired, and left no
		// process to say so.
		n.Status = StatusApplied
	case parentStatus == StatusSkipped:
		// Unreachable by inheritance: a branch under a dead branch is dead too.
		n.Status = StatusSkipped
	case process.TerminalStatus(parentStatus) && kind != hookField(process.Status(parentStatus)):
		// The parent ENDED, and on a different status than this hook answers to, so this
		// branch will never fire. Only a terminal parent can decide that — a running one
		// has not chosen yet, and calling its branches dead would be a lie with a deadline.
		n.Status = StatusSkipped
	}
	if act == nil {
		return n
	}
	if !didRun && act.Spawn != nil {
		// An unrun node's task can only come from the charter, and it is the declared
		// (unbound) form — `review $subject` — because there was no firing to bind it.
		n.Task = act.Spawn.Task
	}
	for _, h := range []struct {
		field string
		act   *Action
	}{
		{"onDone", act.OnDone},
		{"onFailed", act.OnFailed},
		{"onCancelled", act.OnCancelled},
		{"onOrphaned", act.OnOrphaned},
	} {
		if h.act == nil {
			continue
		}
		n.Children = append(n.Children, e.node(chain+"."+h.field, h.field, run, h.act, n.Status))
	}
	return n
}

// nodeFrom projects a mirrored process onto a node. The mirror is an index, not a warehouse,
// so this reads exactly the fields it publishes.
func nodeFrom(chain, kind string, ent entity.Entity) FlowNode {
	str := func(k string) string { s, _ := ent.Payload[k].(string); return s }
	n := FlowNode{
		Chain:      chain,
		Kind:       kind,
		ID:         ent.Ref.ID,
		Task:       str("task"),
		Status:     str("status"),
		Gate:       str("pendingGate"),
		LastAction: str("lastAction"),
	}
	if n.Status == "" {
		n.Status = string(process.Running)
	}
	return n
}

// triggerNamed looks a trigger up by name — the root segment of a chain, which is what makes
// a chain a lookup key into the charter rather than an opaque label.
func (e *Engine) triggerNamed(name string) (Trigger, bool) {
	e.trigMu.Lock()
	defer e.trigMu.Unlock()
	for _, t := range e.triggers {
		if t.Name == name {
			return t, true
		}
	}
	return Trigger{}, false
}
