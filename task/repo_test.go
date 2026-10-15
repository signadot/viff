package task

import (
	"strings"
	"testing"
)

// What a spawn may say about a repository is checked where it is written, with its author
// present: a push on a repository that may only read, an access that is not a word, a pattern
// that will not parse, and the same repository twice are each refused with the reason.
func TestARepoIsCheckedAsWritten(t *testing.T) {
	cases := []struct {
		name  string
		repos []Repo
		want  string // "" = accepted
	}{
		{"read by default", []Repo{{Repo: "acme/app"}}, ""},
		{"write with nothing to push", []Repo{{Repo: "acme/app", Access: AccessWrite}},
			"names what may be pushed in `push`"},
		{"write with pushes", []Repo{{Repo: "acme/app", Access: AccessWrite,
			Push: &RepoPush{New: []string{"verse/*"}, Existing: []string{"main"}, Force: true}}}, ""},
		{"a url", []Repo{{Repo: "https://github.com/acme/app", From: "release"}}, ""},
		{"no repo", []Repo{{Access: AccessRead}}, "names no repository"},
		{"an access that is not a word", []Repo{{Repo: "acme/app", Access: "rw"}}, `access "rw"`},
		{"push without write", []Repo{{Repo: "acme/app", Push: &RepoPush{New: []string{"verse/*"}}}},
			"`push` needs `access: write`; `read` may not push"},
		{"push naming nothing", []Repo{{Repo: "acme/app", Access: AccessWrite, Push: &RepoPush{}}},
			"names no branch"},
		{"a character class, which the runner would read literally", []Repo{{Repo: "acme/app", Access: AccessWrite,
			Push: &RepoPush{New: []string{"release/[ab]"}}}}, "only `*`, `**` and `?` are special"},
		{"an escape", []Repo{{Repo: "acme/app", Access: AccessWrite,
			Push: &RepoPush{New: []string{`verse\x`}}}}, "only `*`, `**` and `?` are special"},
		{"force with nothing existing", []Repo{{Repo: "acme/app", Access: AccessWrite,
			Push: &RepoPush{New: []string{"verse/*"}, Force: true}}}, "`force` applies to `existing`"},
		{"the same repository twice", []Repo{{Repo: "acme/app"}, {Repo: "acme/app.git", Access: AccessWrite, Push: &RepoPush{New: []string{"verse/*"}}}},
			"would both check out as app/"},
		{"the same repository as shorthand and as a url", []Repo{{Repo: "acme/app"}, {Repo: "https://github.com/acme/app.git"}},
			"would both check out as app/"},
		{"two repositories with one name", []Repo{{Repo: "acme/app"}, {Repo: "other/app"}},
			"would both check out as app/"},
		{"two names", []Repo{{Repo: "acme/app"}, {Repo: "acme/lib"}}, ""},
		{"a path only the runner can name, beside another", []Repo{{Repo: "."}, {Repo: "../lib.git"}}, ""},
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

// The directory a checkout takes is read from the text the spawn wrote, the same way on the
// side that installs the charter and the side that checks the repository out — so what install
// refuses as a collision is what the runner would have refused, and nothing passes one to fail
// at the other. A bare `.` is the one spelling only the runner's working directory can name.
func TestARepoNamesItsDirectory(t *testing.T) {
	cases := []struct {
		repo   string
		dir    string
		ok     bool
		kind   RepoKind
		remote string
	}{
		{"acme/app", "app", true, RepoGitHub, "https://github.com/acme/app"},
		{"acme/app.git", "app", true, RepoGitHub, "https://github.com/acme/app.git"},
		{"https://github.com/acme/app", "app", true, RepoURL, "https://github.com/acme/app"},
		{"https://github.com/acme/app.git/", "app", true, RepoURL, "https://github.com/acme/app.git/"},
		{"git@github.com:acme/app.git", "app", true, RepoURL, "git@github.com:acme/app.git"},
		{"/srv/git/app.git", "app", true, RepoPath, "/srv/git/app.git"},
		{"./scratch/x.git", "x", true, RepoPath, "./scratch/x.git"},
		{"../lib", "lib", true, RepoPath, "../lib"},
		{".", "", false, RepoPath, "."},
		{"..", "", false, RepoPath, ".."},
	}
	for _, c := range cases {
		r := Repo{Repo: c.repo}
		dir, ok := r.Dir()
		if dir != c.dir || ok != c.ok {
			t.Errorf("Repo{%q}.Dir() = %q, %v; want %q, %v", c.repo, dir, ok, c.dir, c.ok)
		}
		if k := r.Kind(); k != c.kind {
			t.Errorf("Repo{%q}.Kind() = %v, want %v", c.repo, k, c.kind)
		}
		if rem := r.Remote(); rem != c.remote {
			t.Errorf("Repo{%q}.Remote() = %q, want %q", c.repo, rem, c.remote)
		}
	}
}

// The glob a `push.new` pattern is checked with at install is the glob the runner matches a
// push against: `*` within a segment, `**` across them, `?` one character, and nothing else.
func TestABranchPatternMatchesAsTheRunnerReadsIt(t *testing.T) {
	cases := []struct {
		pattern, branch string
		want            bool
	}{
		{"verse/auto/*", "verse/auto/x1", true},
		{"verse/auto/*", "verse/auto/x1/y", false},
		{"verse/auto/**", "verse/auto/x1/y", true},
		{"verse/train", "verse/train", true},
		{"verse/train", "verse/train2", false},
		{"refs/heads/verse/train", "verse/train", true},
		{"release/?", "release/a", true},
		{"release/?", "release/ab", false},
		{"release/[ab]", "release/a", false}, // not a class: the text as written, which no branch is
	}
	for _, c := range cases {
		if got := MatchBranch(c.pattern, c.branch); got != c.want {
			t.Errorf("MatchBranch(%q, %q) = %v, want %v", c.pattern, c.branch, got, c.want)
		}
	}
}
