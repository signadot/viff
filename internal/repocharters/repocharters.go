// Package repocharters is the reconciler of a repository's charters: a system-level charter
// viff ships, rendered for one repository and installed by a deployment, which keeps what runs
// in viff equal to the last state of the repository's charters directory on its main branch
// that the team said yes to (git-issue dzxn44gw; the design is j5vbwvja and its parts).
//
// IT IS NOT PART OF THE ENGINE. Nothing in viffd knows this charter: it is a charter like any
// other, written against the rule grammar, the git io's reflection and out half, and the github
// io's out half, and it reaches a viff the way the deployment's other system charters do, as a
// text viffd installs at start as the system (viffd.Server.SystemCharters). This package holds
// the charter's text as a template, renders it for a repository, and is where its test lives.
// The reference example (docs/reference/examples/repo-charters/repo-charters.tony) is a
// rendering of it, checked by the test to be the same.
package repocharters

import (
	"bytes"
	_ "embed"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"text/template"

	"go.viff.dev/viff/pkg/entity"
	viffio "go.viff.dev/viff/pkg/io"
)

//go:embed charter.tony.tmpl
var charterTemplate string

// The answer a candidate's question settles on is the propose task's result, which is text:
// `<principal> chose <option>`, with `: <words>` when the person said more. A principal has no
// space in it, so the option chosen is the word after the first space and ` chose `, followed by
// the words or by nothing: `^\S+ chose proceed(: |\z)`. Anchored at the start, the person's words
// cannot read as a choice: `user:a chose proceed: last time they chose deny` is a proceed.
// `\z` rather than `$` for the end, so the pattern holds no `$` a reader takes for a reference.
// (In a double-quoted Tony string a backslash is a JSON escape, hence the doubled ones.)
const (
	answeredProceed = `!regexp "^\\S+ chose proceed(: |\\z)"`
	answeredDeny    = `!regexp "^\\S+ chose deny(: |\\z)"`
	// answeredObsolete is the git io's sweep settling a question nothing proposes any more.
	answeredObsolete = `!regexp "^\\S+ chose obsolete(: |\\z)"`
)

// gitIO is the git io's principal, which the rendered question names among its answerers beside
// the deployment's own, so that the io's obsolete sweep may settle it with nobody granting
// anything. A rendering with no answerers asks the docket, which the io is not, and its questions
// are not swept.
const gitIO = "agent:git-iod"

// Params name the one repository a rendering is for.
type Params struct {
	// Repo is the git io's id for the repository, which it reflects as `git.<Repo>`: `viff`.
	Repo string
	// GitHub is the repository as GitHub names it, `org/name`, which the github io reflects as
	// `github.<org>.<name>` and reads inputs for under `github.<org>.<name>.out`.
	GitHub string
	// Branch is the branch the repository's charters follow, the one a change is proposed onto
	// and whose charters are added to the record viff/live: `main`, or `master`. Empty is `main`.
	Branch string
	// InstallAt is the address the repository's charters install at, as `viff c -a` spells
	// one: `repos.viff`. The git io installs there as itself, so it has to own the address or
	// hold `child-owner` above it.
	InstallAt string
	// Tree is the name the git io reflects the charters directory's tree under (`-tree
	// [<name>=]<path>`, `tree.<name>` on every ref). Empty is `charters`.
	Tree string
	// Answerers is who may answer the question a candidate raises, grantees as a gate names
	// them (`user:*`, `group:owners`). Empty leaves the question to whom the task acts for: the
	// installer of this charter, which for a system charter is nobody, so a deployment names
	// them.
	Answerers []string
}

// branchName is a branch as a rendering names one: what git takes in a ref, less what the
// charter's strings and paths would read as something else (a quote, a `$`, a `*`, a space).
var branchName = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9._/-]*$`)

// Example is the laptop instance the reference example is rendered from: the viff repository,
// reflected by a git io beside a clone, its charters installed at `repos.viff`.
var Example = Params{Repo: "viff", GitHub: "signadot/viff", InstallAt: "repos.viff", Tree: "charters"}

// Render is the charter for one repository, as a viff installs it.
func Render(p Params) ([]byte, error) {
	if p.Repo == "" || strings.ContainsAny(p.Repo, ".* \t\n\"") {
		return nil, fmt.Errorf("repo-charters: Repo %q is not one plain segment", p.Repo)
	}
	org, name, ok := strings.Cut(p.GitHub, "/")
	if !ok || org == "" || name == "" || strings.ContainsAny(p.GitHub, ".* \t\n\"") {
		return nil, fmt.Errorf("repo-charters: GitHub %q is not org/name", p.GitHub)
	}
	segs, err := entity.PathSegs(p.InstallAt)
	if err != nil || len(segs) == 0 {
		return nil, fmt.Errorf("repo-charters: InstallAt %q is not an address", p.InstallAt)
	}
	tree := p.Tree
	if tree == "" {
		tree = "charters"
	}
	if strings.ContainsAny(tree, ".* \t\n\"") {
		return nil, fmt.Errorf("repo-charters: Tree %q is not one plain word", tree)
	}
	branch := p.Branch
	if branch == "" {
		branch = "main"
	}
	if !branchName.MatchString(branch) || strings.Contains(branch, "..") || strings.HasSuffix(branch, ".lock") {
		return nil, fmt.Errorf("repo-charters: Branch %q is not a branch name", branch)
	}
	var quoted []string
	for _, a := range p.Answerers {
		a = strings.TrimSpace(a)
		if a == "" {
			continue
		}
		if strings.ContainsAny(a, "\"\n") {
			return nil, fmt.Errorf("repo-charters: answerer %q is not a grantee", a)
		}
		quoted = append(quoted, `"`+a+`"`)
	}
	data := struct {
		Repo, GitHub, GitHubOrg, GitHubName, InstallAt, Tree string
		// Branch is the branch as text, in the charter's strings; BranchSeg is it as a segment
		// of a path inside a double-quoted string, quoted where a path needs it (`"release/1"`).
		Branch, BranchSeg                                 string
		Answerers                                         []string
		AnswererFlags, Proceed, Deny, Obsolete, EmptyTree string
	}{
		Repo: p.Repo, GitHub: p.GitHub, GitHubOrg: org, GitHubName: name, InstallAt: entity.JoinSegs(segs),
		Tree: tree, Branch: branch, BranchSeg: strings.ReplaceAll(entity.JoinSegs([]string{branch}), `"`, `\"`), Answerers: quoted, AnswererFlags: answererFlags(append(quoted, `"`+gitIO+`"`)),
		Proceed: answeredProceed, Deny: answeredDeny, Obsolete: answeredObsolete, EmptyTree: viffio.EmptyTree,
	}
	tmpl, err := template.New("repo-charters").Parse(charterTemplate)
	if err != nil {
		return nil, fmt.Errorf("repo-charters: %w", err)
	}
	var out bytes.Buffer
	if err := tmpl.Execute(&out, data); err != nil {
		return nil, fmt.Errorf("repo-charters: %w", err)
	}
	if out.Len() == 0 {
		return nil, errors.New("repo-charters: rendered nothing")
	}
	return out.Bytes(), nil
}

// QuestionID is the id of the question a candidate tree raises, as the charter spells it: the
// propose task's id with the number of the call that asks, its second action after the script
// itself.
func QuestionID(repo, tree string) string {
	return "repo-charters." + repo + ":propose@repo-charters." + repo + ".candidate." + tree + "~2"
}

// answererFlags is the ask's answerers as the call tool takes them: one `-answerers` per
// grantee, each a quoted word in the script's shell.
func answererFlags(quoted []string) string {
	var b strings.Builder
	for _, q := range quoted {
		b.WriteString(" -answerers ")
		b.WriteString(q)
	}
	return b.String()
}
