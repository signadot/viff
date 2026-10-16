package delivery

import (
	"strings"
	"testing"

	"github.com/signadot/verse/runner/runnerd/workspace"
	"github.com/signadot/verse/task"
)

// What a launch is admitted for is what its task is then offered: the names Offered gives for a
// set of repositories are the names of the tools a Client over them has, for every shape.
func TestOfferedIsWhatTheToolsOffer(t *testing.T) {
	rw := &task.RepoPush{New: []string{"verse/auto/*"}}
	for name, repos := range map[string][]workspace.Repo{
		"none":       nil,
		"read-only":  {{Name: "l", Repo: "/tmp/x.git", Base: "main"}},
		"writable":   {{Name: "l", Repo: "/tmp/x.git", Base: "main", Access: task.AccessWrite, Push: rw}},
		"forge":      {{Name: "s", Repo: "https://github.com/signadot/verse.git", Base: "main", Access: task.AccessWrite, Push: rw}},
		"forge read": {{Name: "s", Repo: "https://github.com/signadot/verse.git", Base: "main"}},
	} {
		trees := make([]*workspace.Tree, 0, len(repos))
		for _, r := range repos {
			trees = append(trees, &workspace.Tree{Repo: r})
		}
		c := &Client{Conn: fakeConn{}, Workspace: &workspace.Workspace{Branch: "verse/auto/t1", Trees: trees}}
		var have []string
		for _, tl := range c.Tools() {
			have = append(have, tl.Name)
		}
		if got, want := strings.Join(have, ","), strings.Join(Offered(repos), ","); got != want {
			t.Errorf("%s: the tools offer %q, Offered says %q", name, got, want)
		}
	}
}
