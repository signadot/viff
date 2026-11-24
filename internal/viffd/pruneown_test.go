package viffd

import "testing"

// A TASK'S OWN PACKAGE IS FOUND AMONG WHAT A PRUNE WOULD UNINSTALL by the owner its id carries:
// the package whose rule started it, and no other, and none for a request no task makes.
func TestATasksOwnPackageIsFoundAmongWhatAPruneLeavesOut(t *testing.T) {
	srv := testServer(t)
	srv.ctx = t.Context()
	eng, err := srv.packageEngine("demo.pr")
	if err != nil {
		t.Fatal(err)
	}
	own := &taskCharter{task: eng.Owner() + "review@demo.pr.1"}
	if got := own.ownPackageAmong([]string{"demo.side", "demo.pr"}); got != "demo.pr" {
		t.Errorf("own package = %q, want demo.pr", got)
	}
	if got := own.ownPackageAmong([]string{"demo.side", "demo"}); got != "" {
		t.Errorf("own package = %q among packages that are not the task's", got)
	}
	if got := (&taskCharter{task: srv.Eng.Owner() + "review@demo.pr.1"}).ownPackageAmong([]string{"demo.pr"}); got != "" {
		t.Errorf("a baseline rule's task was taken for a package's: %q", got)
	}
	if got := (*taskCharter)(nil).ownPackageAmong([]string{"demo.pr"}); got != "" {
		t.Errorf("a request no task makes was taken for a task's: %q", got)
	}
}
