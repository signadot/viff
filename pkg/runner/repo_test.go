package runner

import (
	"strings"
	"testing"

	"github.com/signadot/tony-format/go-tony/ir"
	"github.com/signadot/verse/pkg/task"
)

// What a launch says about its repos is checked before anything is checked out: a name that
// isn't one is refused with the reason. Two repos in one directory, and a tracker of a repo not
// checked out, are the environment's to refuse (systems.Environment.Check).
func TestALaunchsReposAreChecked(t *testing.T) {
	cases := []struct {
		name  string
		repos []Repo
		want  string // "" = accepted
	}{
		{"read only", []Repo{{Repo: "acme/app", From: "release"}}, ""},
		{"on another forge", []Repo{{Repo: "gitlab.example.com/acme/app"}}, ""},
		{"a host in capitals", []Repo{{Repo: "GitHub.com/acme/app"}}, ""},
		{"a host with a port", []Repo{{Repo: "gitlab.corp:8443/acme/app"}}, ""},
		{"a port that isn't one", []Repo{{Repo: "gitlab.corp:x/acme/app"}}, "is not owner/name"},
		{"a url", []Repo{{Repo: "https://github.com/acme/app"}}, "is not owner/name"},
		{"no owner", []Repo{{Repo: "/app"}}, "is not owner/name"},
		{"too deep", []Repo{{Repo: "a.com/acme/app/x"}}, "is not owner/name"},
		{"a host that isn't one", []Repo{{Repo: "acme/app/x"}}, "is not owner/name"},
		{"an owner that climbs out", []Repo{{Repo: "../app"}}, "is not owner/name"},
		{"an owner that stays", []Repo{{Repo: "./app"}}, "is not owner/name"},
		{"a name that climbs out", []Repo{{Repo: "acme/.."}}, "is not owner/name"},
		{"a character GitHub doesn't spell", []Repo{{Repo: "acme/a b"}}, "is not owner/name"},
		{"a .git suffix", []Repo{{Repo: "acme/app.git"}}, "without .git: acme/app"},
		{"dots and dashes", []Repo{{Repo: "my-org/app.v2_x"}}, ""},
		{"two names", []Repo{{Repo: "acme/app"}, {Repo: "acme/lib"}}, ""},
	}
	for _, c := range cases {
		err := CheckRepos(c.repos)
		switch {
		case c.want == "" && err != nil:
			t.Errorf("%s: refused: %v", c.name, err)
		case c.want != "" && err == nil:
			t.Errorf("%s: accepted, want a refusal mentioning %q", c.name, c.want)
		case c.want != "" && !strings.Contains(err.Error(), c.want):
			t.Errorf("%s: refused for %q, want %q in it", c.name, err, c.want)
		}
	}
}

// A repo's host, path and name are read off its spelling.
func TestARepoIsReadOffItsSpelling(t *testing.T) {
	for _, c := range []struct{ repo, host, path, name string }{
		{"acme/app", "github.com", "acme/app", "app"},
		{"GitLab.com/acme/app", "gitlab.com", "acme/app", "app"},
		{"git.corp:8443/acme/lib", "git.corp:8443", "acme/lib", "lib"},
	} {
		r := Repo{Repo: c.repo}
		if r.Host() != c.host || r.Path() != c.path || r.Name() != c.name {
			t.Errorf("%s: host %s path %s name %s", c.repo, r.Host(), r.Path(), r.Name())
		}
	}
}

// A REPO IS NAMED IN LOWER CASE in a step, as the forge reads it: a spawn's `Acme/Secret` is the
// repository a row on `github.com/acme/secret` denies.
func TestARowNamesARepoWhateverItsCase(t *testing.T) {
	p := task.Autonomy{{Match: &task.Match{System: "git", Verb: "push", Params: map[string]*ir.Node{"repo": ir.FromString("github.com/acme/secret")}},
		Then: task.Then{Mode: task.ThenDeny}}, {Then: task.Then{Mode: task.ThenAllow}}}
	if err := p.Check(); err != nil {
		t.Fatal(err)
	}
	for _, spelled := range []string{"Acme/Secret", "GitHub.com/ACME/secret"} {
		r := Repo{Repo: spelled}
		step := task.NewStep(task.StepGitPush, map[string]string{"repo": r.Canonical(), "ref": "refs/heads/main", "delete": "false", "force": "false"})
		if row, _ := p.Decide(step); row.Then.Mode != task.ThenDeny {
			t.Errorf("%s (%s): %s, want the row's deny", spelled, r.Canonical(), row.Then.Mode)
		}
	}
}

// A branch a task names is read as its full ref, and what git refuses in a name is refused.
func TestABranchRefIsChecked(t *testing.T) {
	for _, c := range []struct{ branch, ref, says string }{
		{"verse/auto/t1", "refs/heads/verse/auto/t1", ""},
		{"refs/heads/verse/auto/t1", "refs/heads/verse/auto/t1", ""},
		{" main ", "refs/heads/main", ""},
		{"verse/auto/..", "", "not a branch name"},
		{"-f", "", "not a branch name"},
		{"", "", "no branch named"},
		{"a b", "", "not a branch name"},
	} {
		ref, err := BranchRef(c.branch)
		switch {
		case c.says == "" && (err != nil || ref != c.ref):
			t.Errorf("%q: %q %v, want %q", c.branch, ref, err, c.ref)
		case c.says != "" && (err == nil || !strings.Contains(err.Error(), c.says)):
			t.Errorf("%q: %v, want a refusal saying %q", c.branch, err, c.says)
		}
	}
}
