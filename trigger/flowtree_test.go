package trigger

import (
	"context"
	"testing"

	"github.com/signadot/verse/entity"
	"github.com/signadot/verse/launch"
	"github.com/signadot/verse/process"
)

// treeEngine is the onhook harness plus a mirrored task, which is what a tree renders.
func treeEngine(t *testing.T, store *entity.MemStore) *Engine {
	t.Helper()
	eng := New(store, process.AutoResolver{})
	eng.SetSpawner(func(id string, spec launch.Spec) process.ProcHandle {
		return startHookProc(store, id, spec)
	})
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	eng.ObserveScoped(ctx)
	return eng
}

// find walks a tree for the node at a chain — the tree's nesting IS kpath extension, so
// this is the assertion helper the shape deserves.
func find(nodes []FlowNode, chain string) (FlowNode, bool) {
	for _, n := range nodes {
		if n.Chain == chain {
			return n, true
		}
		if c, ok := find(n.Children, chain); ok {
			return c, true
		}
	}
	return FlowNode{}, false
}

func onlyTree(t *testing.T, eng *Engine, addr string) FlowTree {
	t.Helper()
	m, err := ParseExpr(addr)
	if err != nil {
		t.Fatalf("ParseExpr(%q): %v", addr, err)
	}
	trees := eng.FlowTrees(m)
	if len(trees) != 1 {
		t.Fatalf("FlowTrees(%q) = %d trees; want 1 (%+v)", addr, len(trees), trees)
	}
	return trees[0]
}

// The tree is the join of history and charter: a finished chain is read back from the
// mirror (the live registry has already retired it), and the branch the charter declares
// but nothing reached is present as a node rather than missing.
func TestFlowTreeJoinsHistoryAndCharter(t *testing.T) {
	store := entity.NewMemStore()
	eng := treeEngine(t, store)

	if err := eng.AddTrigger(Trigger{
		Name:      "review",
		Condition: Condition{When: prWhen("open")},
		Action: Action{
			Patch:   entity.Obj{"stage": "reviewing"}.Payload(),
			Spawn: &launch.Spec{Task: "review $subject"},
			OnDone: &Action{
				Patch:      entity.Obj{"stage": "reviewed"}.Payload(),
				Spawn:    &launch.Spec{Task: "merge $subject"},
				OnDone:   &Action{Patch: entity.Obj{"stage": "merged"}.Payload()},
				OnFailed: &Action{Spawn: &launch.Spec{Task: "escalate $subject"}},
			},
		},
	}); err != nil {
		t.Fatalf("AddTrigger: %v", err)
	}

	store.Put(prRef("1"), entity.Obj{"stage": "open"}.Payload())
	waitField(t, store, prRef("1"), "stage", "merged")

	tree := onlyTree(t, eng, "demo:pr:1")
	if len(tree.Roots) != 1 || tree.Roots[0].Chain != "review" || tree.Roots[0].Kind != "root" {
		t.Fatalf("want one root `review`; got %+v", tree.Roots)
	}

	// What ran: both spawns, read back from the mirror after retirement.
	for _, chain := range []string{"review", "review.onDone"} {
		n, ok := find(tree.Roots, chain)
		if !ok {
			t.Fatalf("node %q missing from tree %+v", chain, tree.Roots)
		}
		if n.Status != string(process.Done) {
			t.Errorf("node %q status = %q; want done", chain, n.Status)
		}
		if n.ID != chain+ChainSep+"demo:pr:1" {
			t.Errorf("node %q id = %q; want %q", chain, n.ID, chain+ChainSep+"demo:pr:1")
		}
		if n.Task == "" {
			t.Errorf("node %q has no task", chain)
		}
	}

	// The branch that did not run because its sibling status won: declared in the charter,
	// so it is a node — and SKIPPED, not pending, because its parent is done, not failed.
	esc, ok := find(tree.Roots, "review.onDone.onFailed")
	if !ok {
		t.Fatalf("declared onFailed branch missing from tree %+v", tree.Roots)
	}
	if esc.Status != StatusSkipped {
		t.Errorf("onFailed branch status = %q; want %q (its parent completed)", esc.Status, StatusSkipped)
	}
	if esc.ID != "" {
		t.Errorf("unrun branch carries a process id %q", esc.ID)
	}
	if esc.Task != "escalate $subject" {
		t.Errorf("unrun branch task = %q; want the charter's declared (unbound) task", esc.Task)
	}
	// A set-only hook still appears — a node is a position in the charter, not only a spawn.
	// It spawned no process, so nothing observes it directly; its parent ended on exactly
	// the status it answers to, so it fired, and saying "pending" would be plainly wrong.
	set, ok := find(tree.Roots, "review.onDone.onDone")
	if !ok {
		t.Fatal("set-only onDone hook missing from the tree")
	}
	if set.Status != StatusApplied {
		t.Errorf("set-only hook status = %q; want %q — it fired but spawned nothing",
			set.Status, StatusApplied)
	}
}

// While a chain is still moving, the branch below it is PENDING (it may yet run) — the
// distinction from skipped is the whole reason both statuses exist.
func TestFlowTreePendingWhileRunning(t *testing.T) {
	store := entity.NewMemStore()
	eng := treeEngine(t, store)

	// A gate the test never resolves would be one way to hold it; simpler and race-free:
	// mirror a running process by hand, exactly as a Spawner would, and read the tree.
	if err := eng.AddTrigger(Trigger{
		Name:      "review",
		Condition: Condition{When: prWhen("never")},
		Action: Action{
			Spawn:  &launch.Spec{Task: "review $subject"},
			OnDone: &Action{Spawn: &launch.Spec{Task: "merge $subject"}},
		},
	}); err != nil {
		t.Fatalf("AddTrigger: %v", err)
	}
	store.Put(entity.Ref{System: "process", Kind: "instance", ID: "review@demo:pr:2"},
		entity.Obj{"status": "running", "flow": "demo:pr:2", "chain": "review", "trigger": "review"}.Payload())

	tree := onlyTree(t, eng, "demo:pr:2")
	root, ok := find(tree.Roots, "review")
	if !ok || root.Status != "running" {
		t.Fatalf("root not running: %+v", tree.Roots)
	}
	next, ok := find(tree.Roots, "review.onDone")
	if !ok {
		t.Fatalf("onDone branch missing: %+v", tree.Roots)
	}
	if next.Status != StatusPending {
		t.Errorf("onDone status = %q; want %q — its parent has not finished", next.Status, StatusPending)
	}
}

// A process with no trigger behind it (`verse process run --subject`) is still part of the
// subject's flow. It has no charter position, so it is a root of its own rather than being
// dropped — a tree that silently omits work is worse than one with a bare node in it.
func TestFlowTreeIncludesChainlessProcesses(t *testing.T) {
	store := entity.NewMemStore()
	eng := treeEngine(t, store)
	store.Put(entity.Ref{System: "process", Kind: "instance", ID: "run-abc"},
		entity.Obj{"status": "running", "flow": "demo:pr:3", "task": "poke at it"}.Payload())

	tree := onlyTree(t, eng, "demo:pr:3")
	if len(tree.Roots) != 1 {
		t.Fatalf("want one root; got %+v", tree.Roots)
	}
	n := tree.Roots[0]
	if n.Kind != "process" || n.ID != "run-abc" || n.Chain != "" {
		t.Errorf("chainless process rendered as %+v; want kind=process id=run-abc chain=\"\"", n)
	}
}

// History outlives the charter: a trigger removed after its processes ran leaves a tree
// that still shows what happened, with no shape to extend it by.
func TestFlowTreeSurvivesTriggerRemoval(t *testing.T) {
	store := entity.NewMemStore()
	eng := treeEngine(t, store)
	store.Put(entity.Ref{System: "process", Kind: "instance", ID: "gone@demo:pr:4"},
		entity.Obj{"status": "done", "flow": "demo:pr:4", "chain": "gone", "trigger": "gone"}.Payload())

	tree := onlyTree(t, eng, "demo:pr:4")
	n, ok := find(tree.Roots, "gone")
	if !ok {
		t.Fatalf("history lost when the trigger was gone: %+v", tree.Roots)
	}
	if n.Status != "done" || len(n.Children) != 0 {
		t.Errorf("node = %+v; want the run process with no declared children", n)
	}
}

// The address grammar is the same one every other flow command speaks, so a pattern yields
// one tree per subject — and a subject nothing has touched yields none.
func TestFlowTreeAddressing(t *testing.T) {
	store := entity.NewMemStore()
	eng := treeEngine(t, store)
	for _, id := range []string{"1", "2"} {
		store.Put(entity.Ref{System: "process", Kind: "instance", ID: "review@demo:pr:" + id},
			entity.Obj{"status": "done", "flow": "demo:pr:" + id, "chain": "review", "trigger": "review"}.Payload())
	}
	m, _ := ParseExpr("demo:pr:*")
	if trees := eng.FlowTrees(m); len(trees) != 2 {
		t.Fatalf("pattern selected %d trees; want 2", len(trees))
	}
	m, _ = ParseExpr("demo:pr:99")
	if trees := eng.FlowTrees(m); len(trees) != 0 {
		t.Fatalf("untouched subject yielded %d trees; want 0", len(trees))
	}
}
