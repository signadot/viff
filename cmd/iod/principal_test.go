package main

import (
	"testing"

	verseio "github.com/signadot/verse/io"
)

// THE OUT HALF ACTS AS ITS ROOT'S IOD, which is the principal `verse iod add` writes the out
// half's grants for (`agent:<root>-iod`): the connector's name where -root is not given, and
// -root's where it is, so a second deployment under a root of its own needs no -as on either side.
func TestTheOutHalfActsAsItsRootsIod(t *testing.T) {
	for _, tc := range []struct{ root, want string }{
		{"", "agent:git-iod"},
		{"git-buildhost", "agent:git-buildhost-iod"},
	} {
		c := &common{root: tc.root}
		git := verseio.NewGit()
		if err := git.SetRoot(c.root); err != nil {
			t.Fatal(err)
		}
		if got := c.principal(git.Root()); got != tc.want {
			t.Errorf("-root %q: the out half acts as %s, want %s", tc.root, got, tc.want)
		}
	}
	if got := (&common{as: "agent:ci"}).principal("git"); got != "agent:ci" {
		t.Errorf("-as agent:ci: the out half acts as %s", got)
	}
}
