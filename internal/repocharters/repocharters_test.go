package repocharters

import (
	"bytes"
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"go.viff.dev/viff/pkg/codec"
)

var update = flag.Bool("update", false, "rewrite the reference example from the template")

// examplePath is the reference example, relative to this package.
const examplePath = "../../docs/reference/examples/repo-charters/repo-charters.tony"

// The reference example is a rendering of the template for the laptop instance, and parses as a
// charter: a change to one is a change to the other, and `go test -update` carries it over.
func TestTheReferenceExampleIsTheRenderedTemplate(t *testing.T) {
	got, err := Render(Example)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := codec.ParseNodes(got, codec.Tony); err != nil {
		t.Fatalf("the rendering does not parse: %v\n%s", err, got)
	}
	path := filepath.Join(examplePath)
	if *update {
		if err := os.WriteFile(path, got, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("%s differs from the template's rendering; run `go test ./internal/repocharters -update`", path)
	}
}

// A rendering names the repository everywhere the charter speaks of it, the answerers where it
// asks, and nothing of another instance.
func TestARenderingIsForItsRepository(t *testing.T) {
	got, err := Render(Params{Repo: "r", GitHub: "acme/r", InstallAt: "repos.r", Answerers: []string{"user:*", "group:owners"}})
	if err != nil {
		t.Fatal(err)
	}
	s := string(got)
	for _, want := range []string{
		"reads: [git.r, github.acme.r, mirror.schedule]", `path: 'mirror.schedule."repo-charters.r:minute".tick.*'`, "at: repo-charters.r\n", "- from: install\n", "to: git.r.out", "to: github.acme.r.out", `path: "repo-charters.r.candidate.*"`,
		`to: "install.repos.r"`, `-answerers "user:*" -answerers "group:owners" -answerers "agent:git-iod"`, "tree.charters", "into repos.r?", `path: "github.acme.r.pr.*"`, `to: "pr.$[subject.value.pr]"`,
	} {
		if !strings.Contains(s, want) {
			t.Errorf("the rendering for r lacks %q", want)
		}
	}
	if strings.Contains(s, "viff.out") || strings.Contains(s, "signadot") {
		t.Errorf("the rendering for r speaks of another instance")
	}
	// No answerers: the question is left to whom the task acts for, and the key is absent.
	got, err = Render(Params{Repo: "r", GitHub: "acme/r", InstallAt: "repos.r"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(got), "answerers") {
		t.Errorf("a rendering with no answerers names them")
	}
	if _, err := codec.ParseNodes(got, codec.Tony); err != nil {
		t.Fatalf("the rendering for r does not parse: %v", err)
	}
	for _, bad := range []Params{
		{Repo: "", GitHub: "acme/r", InstallAt: "repos.r"},
		{Repo: "a.b", GitHub: "acme/r", InstallAt: "repos.r"},
		{Repo: "r", GitHub: "acme", InstallAt: "repos.r"},
		{Repo: "r", GitHub: "acme/r", InstallAt: ""},
		{Repo: "r", GitHub: "acme/r", InstallAt: "repos.r", Tree: "a.b"},
		{Repo: "r", GitHub: "acme/r", InstallAt: "repos.r", Branch: "a b"},
		{Repo: "r", GitHub: "acme/r", InstallAt: "repos.r", Branch: `a"b`},
		{Repo: "r", GitHub: "acme/r", InstallAt: "repos.r", Branch: "$b"},
		{Repo: "r", GitHub: "acme/r", InstallAt: "repos.r", Branch: "a..b"},
		{Repo: "r", GitHub: "acme/r", InstallAt: "repos.r", Branch: "-b"},
	} {
		if _, err := Render(bad); err == nil {
			t.Errorf("Render(%+v) rendered, want a refusal", bad)
		}
	}
}

// A rendering follows its branch: it reads that ref, asks about pull requests onto it, and names
// it where it speaks to people. Unnamed, the branch is main.
func TestARenderingFollowsItsBranch(t *testing.T) {
	for _, tc := range []struct{ branch, ref, other string }{
		{"", `path: "git.r.ref.main"`, "master"},
		{"master", `path: "git.r.ref.master"`, "main"},
		{"release-1.2", `path: "git.r.ref.\"release-1.2\""`, "main"},
	} {
		got, err := Render(Params{Repo: "r", GitHub: "acme/r", InstallAt: "repos.r", Branch: tc.branch})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := codec.ParseNodes(got, codec.Tony); err != nil {
			t.Fatalf("the rendering for branch %q does not parse: %v", tc.branch, err)
		}
		s := string(got)
		branch := tc.branch
		if branch == "" {
			branch = "main"
		}
		for _, want := range []string{
			tc.ref, `base: "` + branch + `"`, "charters: " + branch + "'s charters would be refused",
			"Open a pull request to " + branch + " to change the charters",
		} {
			if !strings.Contains(s, want) {
				t.Errorf("the rendering for branch %q lacks %q", tc.branch, want)
			}
		}
		if strings.Contains(s, "git.r.ref."+tc.other+`"`) || strings.Contains(s, `base: "`+tc.other+`"`) {
			t.Errorf("the rendering for branch %q follows %s", tc.branch, tc.other)
		}
	}
}
