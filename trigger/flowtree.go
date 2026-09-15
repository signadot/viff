package trigger

import (
	"slices"
	"sort"
	"strings"

	"github.com/signadot/verse/entity"
	"github.com/signadot/verse/mirror"
	"github.com/signadot/verse/task"
)

// The flow TREE is what a subject's work looks like as a shape rather than a list: the
// trigger that acted on it, the task that ran, and the completion hooks hanging off that
// task — each hook a branch, nested as deep as the charter nests them.
//
// It is DERIVED, never stored. Materializing it would rebuild the flows[subject] index that
// migration step 4 deliberately replaced with a query (docs/trigger-model.md), and a stored
// tree would need maintaining on every event. So it is assembled per request from three
// sources, and the interesting part is that no single one of them is sufficient:
//
//   - the CHARTER gives the shape. A trigger's Action tree is known before anything runs, so
//     a hook that has not fired yet can be shown as pending rather than be missing. Nothing
//     in the runtime knows this — it is the only source that can speak about the future.
//   - the MIRROR gives the history. `retire` deletes a finished task from the live
//     registry (engine.go), so a completed chain exists ONLY as task:instance entities.
//     This is why the chain is published there: without it, reading history back would mean
//     parsing ids.
//   - the LIVE registry gives the present — what is still tracked and steerable.
//
// The join key is the chain: a kpath naming the action that spawned each task inside its
// trigger. Parent/child in the tree IS prefix/extension in the kpath, so the nesting needs
// no separate parent pointer to maintain.

// FlowNode is one position in a subject's flow: an action in some trigger, and the task
// it did or did not spawn.
//
// It stays in `trigger` rather than moving to `api`: `api` is for types that exist only as
// wire shapes and need one symbol on both sides, and this is a domain type the engine
// produces (Engine.FlowTrees). `flow show` decodes nothing — printBody re-renders the
// document — so no shared symbol is needed. Being a response type means it must be
// DESCRIBED, not relocated.
//
//tony:schemagen=verse-flow-node,notag
type FlowNode struct {
	// Chain is the action's kpath inside its trigger — "review", "review.onDone". Empty for
	// a task with no trigger behind it (`verse task run`, or an adopted task whose
	// chain was never published).
	Chain string `tony:"field=chain,omitzero"`
	// Kind is what KIND of position this is: "root" for the trigger's own spawn, the hook's
	// field name ("onDone", "onFailed", …) for a completion branch, and "task" for a
	// task that answers to no charter action. It is carried explicitly rather than left to
	// be re-derived from the chain's last segment, because typing a node is the response's
	// job — the id's job is only to be unique and pasteable.
	Kind string `tony:"field=kind"`
	// ID is the task this position spawned; empty when it has not run.
	ID     string `tony:"field=id,omitzero"`
	Prompt string `tony:"field=prompt,omitzero"`
	Status string `tony:"field=status"`
	// Err is why a node with StatusFailed failed, for the one failure that has no task to
	// read it off: a completion hook whose write did not resolve. A hook spawns nothing in
	// that case, so without this the tree would say `failed` and give nowhere to look.
	Err string `tony:"field=err,omitzero"`
	// Pending, Gate and LastAction are the live detail, present only while a task is
	// in flight.
	Gate       string `tony:"field=gate,omitzero"`
	LastAction string `tony:"field=lastAction,omitzero"`
	// HeldBy is who holds a live task at its boundary: `operator` for `flow suspend --task`, and the
	// suspensions that paused it.
	HeldBy []string `tony:"field=heldBy,omitzero"`
	// Suspended is the suspensions holding this position's work while it has not finished: a
	// branch that will not run, a question that stands withdrawn, until they lift.
	Suspended []string   `tony:"field=suspended,omitzero"`
	Children  []FlowNode `tony:"field=children,omitzero"`
}

// Statuses a node carries that no task ever reports: a position in the charter that has
// not run. Distinguishing them matters — "pending" invites waiting for something that is
// still coming, "skipped" says the branch is dead because its parent ended another way.
const (
	StatusPending = "pending" // its parent has not finished, so this may still run
	StatusSkipped = "skipped" // its parent ended on a DIFFERENT terminal status
	// StatusApplied is a hook that fired but spawned nothing — a `set` leaves no task,
	// so there is no runtime trace to read it back from. It is INFERRED from the engine's
	// own rule (a hook fires exactly when its task reaches that status), which is the
	// same kind of inference pending/skipped already are. Reporting it as pending would be
	// worse than inferring: the act is over, and nothing is coming.
	StatusApplied = "applied"
	// StatusFailed is that same hook when its write did NOT land — a reference that did not
	// resolve, a refused write. The inference above is that a hook fires when its task
	// reaches that status, and firing is not landing: the engine records the failure on the
	// task whose completion fired it (recordHookFailure), so unlike `applied` this one is
	// read rather than inferred. It is spelled as the task status of the same name, because
	// a reader of the tree is asking one question of every node.
	StatusFailed = string(task.Failed)
)

// FlowTree is one subject's flow: every root that acted on it, with its branches.
//
//tony:schemagen=verse-flow-tree,notag
type FlowTree struct {
	Subject string `tony:"field=subject"`
	// Active is whether a task about the subject is live in some engine now.
	Active bool `tony:"field=active,omitzero"`
	// Suspended is the suspensions holding work about the subject: what an installed rule whose
	// subject could select it would be held by, and what holds its live tasks. A subject with no
	// work at all has a tree when something would hold it, with this and no roots.
	Suspended []string   `tony:"field=suspended,omitzero"`
	Roots     []FlowNode `tony:"field=roots,omitzero"`
}

// FlowTrees renders the flow tree of every subject the address selects, as this engine alone
// sees it. FlowTreesAcross is the same over every engine a request may see.
func (e *Engine) FlowTrees(m Match) []FlowTree {
	return FlowTreesAcross([]*Engine{e}, m, nil)
}

// FlowTreesAcross renders the flow tree of every subject the address selects: one per subject
// that has tasks, and one per subject with no work that a suspension would hold — the address
// itself when it names one entity, else each stored entity it selects. active narrows to the trees
// with a live task (true) or without one (false); nil is every tree.
//
// Subjects come from the mirror rather than from the live registry, so a subject whose work
// has all finished still has a tree — which is the point of asking after the fact. The mirror and
// the entities are read through the first engine's store, which is baseline's or a scope's.
//
// EACH TASK IS RENDERED BY THE ENGINE THAT MINTED IT. A package's charter runs in an engine of its
// own, so its rules, its live tasks, who holds them and which suspensions cover them are that
// engine's to answer: asked of baseline's, a package's task has no charter shape to extend, holds
// nothing and is covered only by suspensions of every charter. A task goes to the engine tracking
// it live, else to the engine whose prefix its id carries (ownerOf), else to the first.
func FlowTreesAcross(engines []*Engine, m Match, active *bool) []FlowTree {
	if len(engines) == 0 {
		return []FlowTree{}
	}
	// The first engine's roots first, then each package's by its address, so a tree reads the
	// same at every request whatever order the engines were handed over in.
	engines = slices.Clone(engines)
	slices.SortStableFunc(engines[1:], func(a, b *Engine) int { return strings.Compare(a.At(), b.At()) })
	first := engines[0]
	bySubject := map[string]map[*Engine][]entity.Entity{}
	at := func(subj string) map[*Engine][]entity.Entity {
		if bySubject[subj] == nil {
			bySubject[subj] = map[*Engine][]entity.Entity{}
		}
		return bySubject[subj]
	}
	// Instances, not every kind the task system holds: a flow tree is the LIVE shape of the
	// work, one node per task, and a run record is history about a node already in it (see
	// mirror.RunKind). Including them would double every node that has run more than once.
	for _, ent := range entity.ListUnder(first.store, func(r entity.Ref) bool {
		return mirror.IsTask(r)
	}, entity.VerseRoot, mirror.System, mirror.InstanceKind) {
		subj := entity.Str(ent.Payload, "flow")
		if subj == "" {
			continue // a task about nothing is in no flow
		}
		ref, err := entity.ParseAddr(subj)
		if err != nil || !m.MatchRef(ref) {
			continue
		}
		subj = subjectKey(subj)
		eng := mintedBy(engines, ent.Ref.ID())
		at(subj)[eng] = append(at(subj)[eng], ent)
	}
	// A live task whose mirror has not landed yet still belongs in the tree: the
	// registry is the present, and a tree that omits what is running now would be worse
	// than one that omits what ran yesterday.
	live := map[string]bool{}
	for _, eng := range engines {
		for _, fp := range eng.matchFlow(m) {
			at(subjectKey(fp.subject))
			live[subjectKey(fp.subject)] = true
		}
	}
	// A subject with no work is shown when something would hold it, unless only active trees
	// were asked for, which it cannot be one of.
	idle := map[string][]string{}
	if active == nil || !*active {
		for _, ref := range m.Select(first.store) {
			subj := ref.String()
			if _, worked := bySubject[subj]; worked {
				continue
			}
			if held := coveringAcross(engines, ref); len(held) > 0 {
				at(subj)
				idle[subj] = held
			}
		}
	}

	subjects := make([]string, 0, len(bySubject))
	for s := range bySubject {
		subjects = append(subjects, s)
	}
	sort.Strings(subjects) // deterministic for a CLI listing

	trees := make([]FlowTree, 0, len(subjects))
	for _, s := range subjects {
		tree := FlowTree{Subject: s, Active: live[s]}
		if active != nil && *active != tree.Active {
			continue
		}
		if held, ok := idle[s]; ok {
			tree.Suspended = held
			trees = append(trees, tree)
			continue
		}
		for _, eng := range engines {
			if ents, ok := bySubject[s][eng]; ok || eng == first {
				tree.Roots = append(tree.Roots, eng.rootsFor(s, ents)...)
			}
		}
		held := coveringAcross(engines, subjectOf(s))
		for _, id := range nodeSuspensions(tree.Roots) {
			if !slices.Contains(held, id) {
				held = append(held, id)
			}
		}
		slices.Sort(held)
		tree.Suspended = held
		trees = append(trees, tree)
	}
	return trees
}

// subjectKey is a subject in its one spelling, so two quotings of one address are one tree.
func subjectKey(s string) string {
	if ref, err := entity.ParseAddr(s); err == nil && ref != (entity.Ref{}) {
		return ref.String()
	}
	return s
}

// coveringAcross is Covering over every engine, each id once, sorted.
func coveringAcross(engines []*Engine, subj entity.Ref) []string {
	var out []string
	for _, eng := range engines {
		for _, id := range eng.Covering(subj) {
			if !slices.Contains(out, id) {
				out = append(out, id)
			}
		}
	}
	slices.Sort(out)
	return out
}

// nodeSuspensions is every suspension named on a position of a tree.
func nodeSuspensions(nodes []FlowNode) []string {
	var out []string
	for _, n := range nodes {
		out = append(out, n.Suspended...)
		out = append(out, nodeSuspensions(n.Children)...)
	}
	return out
}

// Covering is the suspensions that would hold this engine's rules about a subject: for each
// installed rule whose subject could select it — by address, since a suspension covers by address
// and a payload predicate would only say whether the rule selects it now — what holds that rule's
// work about it, asked with authority as a firing asks. A subject with no work yet is held by
// these as much as one with work.
func (e *Engine) Covering(subj entity.Ref) []string {
	e.trigMu.Lock()
	trigs := slices.Clone(e.triggers)
	e.trigMu.Unlock()
	var out []string
	for _, t := range trigs {
		if t.Condition.Subject.IsEmpty() || !t.Condition.Subject.MatchRef(subj) {
			continue
		}
		for _, id := range e.holding(t.Name, subj) {
			if !slices.Contains(out, id) {
				out = append(out, id)
			}
		}
	}
	slices.Sort(out)
	return out
}

// mintedBy is the engine a task belongs to: the one tracking it live, else the one whose id
// prefix it carries — the longest, since baseline's is empty — else the first.
func mintedBy(engines []*Engine, id string) *Engine {
	for _, eng := range engines {
		if _, ok := eng.Live(id); ok {
			return eng
		}
	}
	best, bestLen := engines[0], 0
	for _, eng := range engines {
		if owner := eng.idOwner(); len(owner) > bestLen && strings.HasPrefix(id, owner) {
			best, bestLen = eng, len(owner)
		}
	}
	return best
}

// rootsFor builds one subject's roots from its mirrored tasks, then extends each root
// with the charter shape it has not reached yet.
func (e *Engine) rootsFor(subject string, ents []entity.Entity) []FlowNode {
	// Index the run tasks by chain. A chain is unique per (trigger, subject) by
	// construction — spawnID derives from exactly that pair — so this cannot collide.
	run := map[string]entity.Entity{}
	var bare []FlowNode // tasks with no chain: no charter action to hang them under
	for _, ent := range ents {
		chain := entity.Str(ent.Payload, "chain")
		if chain == "" {
			bare = append(bare, e.nodeFrom("", "task", ent))
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
		root := e.node(name, "root", run, &t.Action, "")
		// A carrier spawns nothing itself, so its own status can only come from the sequence
		// it holds — otherwise a finished sequence would render under a root marked `pending`,
		// which is the tree lying about the one thing it is for.
		applySeqProgress(&root, e.seqProgressFor(subject, name))
		// …and the hooks under it were derived against the status it had BEFORE that
		// correction, which is the same lie one level down: a finished sequence's `onDone`
		// rendered `pending` while the patch it applied was already on the subject. Re-derive
		// them against the corrected status rather than restating the inference here.
		e.reviseHooks(&root, name, run, &t.Action)
		out = append(out, root)
	}
	sort.Slice(bare, func(i, j int) bool { return bare[i].ID < bare[j].ID })
	out = append(out, bare...)
	e.markSuspended(out, subjectOf(subject))
	return out
}

// markSuspended says, on every position that has not finished, what suspends its work.
func (e *Engine) markSuspended(nodes []FlowNode, subj entity.Ref) {
	for i := range nodes {
		n := &nodes[i]
		if !task.TerminalStatus(n.Status) && n.Status != StatusSkipped && n.Status != StatusApplied {
			n.Suspended = e.holding(mirror.TriggerOf(n.Chain), subj)
		}
		e.markSuspended(n.Children, subj)
	}
}

// node builds the node at chain and recurses into the action's completion hooks. act is the
// charter action at this position (nil once the charter runs out — a removed trigger, or a
// task whose action is gone); parentStatus is the terminal status of the task above,
// which decides whether an unrun branch is still coming or is dead.
func (e *Engine) node(chain, kind string, run map[string]entity.Entity, act *Action, parentStatus string) FlowNode {
	n := FlowNode{Chain: chain, Kind: kind, Status: StatusPending}
	ent, didRun := run[chain]
	failed := hookFailedAt(run, chain)
	switch {
	case didRun:
		n = e.nodeFrom(chain, kind, ent)
	case kind == "root": // nothing above it to be unreachable from
	case failed != "":
		// It fired and did not land. Read before the inference below, because that one
		// answers "did this hook run" and this is the other half of the same question: a
		// hook whose write failed spawned nothing, so `applied` is exactly what it would
		// otherwise be called.
		n.Status, n.Err = StatusFailed, failed
	case act != nil && act.Spawn == nil && task.TerminalStatus(parentStatus) &&
		kind == hookField(task.Status(parentStatus)):
		// A set-only hook whose parent ended on exactly this status: it fired, and left no
		// task to say so.
		n.Status = StatusApplied
	case parentStatus == StatusSkipped:
		// Unreachable by inheritance: a branch under a dead branch is dead too.
		n.Status = StatusSkipped
	case task.TerminalStatus(parentStatus) && kind != hookField(task.Status(parentStatus)):
		// The parent ENDED, and on a different status than this hook answers to, so this
		// branch will never fire. Only a terminal parent can decide that — a running one
		// has not chosen yet, and calling its branches dead would be a lie with a deadline.
		n.Status = StatusSkipped
	}
	if act == nil {
		return n
	}
	// Sequence elements come first and IN INDEX ORDER — a sequence's whole content is its
	// order, so rendering it any other way (alphabetically, say) would hide the one thing it
	// says. They precede the hooks because they run before them: the carrier's onDone is
	// what happens AFTER the sequence.
	stopped := false
	for i := range act.Seq {
		child := seqChain(chain, i)
		el := e.node(child, "seq", run, &act.Seq[i], n.Status)
		if stopped && el.ID == "" {
			// Everything after an element that ended badly is unreachable, not merely
			// waiting — the sequence is fail-stop.
			el.Status = StatusSkipped
		}
		if st := el.Status; task.TerminalStatus(st) && st != string(task.Done) {
			stopped = true
		}
		n.Children = append(n.Children, el)
	}
	if !didRun && act.Spawn != nil {
		// An unrun node's task can only come from the charter, and it is the declared
		// (unbound) form — `review $subject` — because there was no firing to bind it.
		n.Prompt = act.Spawn.Prompt
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

// reviseHooks rebuilds a node's completion-hook children against the node's CURRENT status.
// It exists for the one node whose status is settled after its children were built — a `seq`
// carrier, whose own status comes from the sequence's progress facts rather than from a
// task of its own. Rebuilding rather than patching keeps one inference: whatever `node`
// decides an unrun hook means under a terminal parent is what a carrier's hook means too.
func (e *Engine) reviseHooks(n *FlowNode, chain string, run map[string]entity.Entity, act *Action) {
	if act == nil {
		return
	}
	for i := range n.Children {
		c := &n.Children[i]
		if c.Kind == "seq" || c.Kind == "task" {
			continue
		}
		h := hookAction(act, c.Kind)
		if h == nil {
			continue
		}
		*c = e.node(chain+"."+c.Kind, c.Kind, run, h, n.Status)
	}
}

// hookFailedAt reads the failure a completion hook at this chain recorded, empty when it
// recorded none. A hook that fails writes a `hook` record onto the task whose completion
// fired it (Engine.recordHookFailure) — which for a plain spawn is the task at the chain
// above, and for a sequence's carrier is its last element — so this looks across the
// subject's tasks by the chain the record NAMES rather than guessing which one holds it.
//
// A re-run cannot leave a stale one behind: the first write of a run REPLACES the record
// (mirror.Reflect), so a chain that failed and then succeeded carries nothing here.
func hookFailedAt(run map[string]entity.Entity, chain string) string {
	for _, ent := range run {
		h := entity.Get(ent.Payload, "hook")
		if entity.Str(h, "chain") == chain && entity.Str(h, "status") == StatusFailed {
			return entity.Str(h, "err")
		}
	}
	return ""
}

// hookAction returns the action a hook field names, or nil for a field that is not one.
func hookAction(act *Action, field string) *Action {
	switch field {
	case "onDone":
		return act.OnDone
	case "onFailed":
		return act.OnFailed
	case "onCancelled":
		return act.OnCancelled
	case "onOrphaned":
		return act.OnOrphaned
	}
	return nil
}

// nodeFrom projects a mirrored task onto a node. The mirror is an index, not a warehouse,
// so this reads exactly the fields it publishes — and `pending` is a ref, so the gate's NAME
// is one hop away, on the question itself. Following it costs a read per blocked task and
// keeps the name in one place; carrying a copy on the task would be the second spelling
// this step exists to remove.
func (e *Engine) nodeFrom(chain, kind string, ent entity.Entity) FlowNode {
	str := func(k string) string { return entity.Str(ent.Payload, k) }
	n := FlowNode{
		Chain:      chain,
		Kind:       kind,
		ID:         ent.Ref.ID(),
		Prompt:     str("prompt"),
		Status:     str("status"),
		Gate:       e.gateNameOf(str("pending")),
		LastAction: str("lastAction"),
		HeldBy:     e.HeldBy(ent.Ref.ID()),
	}
	if n.Status == "" {
		n.Status = string(task.Running)
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

// seqProgress is what the subject's own payload says about a sequence: how far it got, and
// whether it finished. The tasks alone cannot answer this — an element that only patches
// leaves no task behind, so without the facts it would render as never having run.
type seqProgress struct {
	step int
	done bool
	has  bool
}

// seqProgressFor reads verse.<trigger> off the subject.
func (e *Engine) seqProgressFor(subject, trigger string) seqProgress {
	ref, err := entity.ParseAddr(subject)
	if err != nil {
		return seqProgress{}
	}
	ent, ok, err := e.store.Get(ref)
	if err != nil {
		e.logger.Warn("a flow's sequence progress could not be read; it renders as no progress",
			"subject", subject, "err", err)
		return seqProgress{}
	}
	if !ok {
		return seqProgress{}
	}
	p := entity.Get(entity.Get(entity.Get(ent.Payload, VerseRoot), CharterKey), trigger)
	if p == nil {
		return seqProgress{}
	}
	out := seqProgress{has: true}
	if s := entity.Get(p, seqStep); s != nil && s.Int64 != nil {
		out.step = int(*s.Int64)
	}
	if d := entity.Get(p, seqDone); d != nil {
		out.done = d.Bool
	}
	return out
}

// applySeqProgress corrects the statuses only the facts can know: a carrier's own state, and
// elements that completed without leaving a task.
func applySeqProgress(carrier *FlowNode, p seqProgress) {
	if !p.has {
		return
	}
	if carrier.ID == "" { // the carrier itself never spawns
		switch {
		case p.done:
			carrier.Status = string(task.Done)
		default:
			carrier.Status = string(task.Running)
		}
	}
	seen := 0
	for i := range carrier.Children {
		c := &carrier.Children[i]
		if c.Kind != "seq" {
			continue
		}
		if c.ID == "" && c.Status == StatusPending && seen < p.step {
			// It is behind the step count and left no task: it ran and only wrote.
			c.Status = StatusApplied
		}
		seen++
	}
}

// gateNameOf follows a task's `pending` ref to the question it names and reads the gate
// off it. Empty when nothing is pending, or when the question has already been reaped — a
// blocked-looking task with no question is a display question, not a correctness one.
func (e *Engine) gateNameOf(pendingRef string) string {
	if pendingRef == "" {
		return ""
	}
	ref, err := entity.ParseAddr(pendingRef)
	if err != nil {
		return ""
	}
	ent, ok, err := e.store.Get(ref)
	if err != nil {
		e.logger.Warn("a pending gate's name could not be read; the flow renders without it",
			"at", pendingRef, "err", err)
		return ""
	}
	if !ok {
		return ""
	}
	return entity.Str(ent.Payload, "gate")
}
