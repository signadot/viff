package io

import (
	"archive/tar"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/signadot/tony-format/go-tony/ir"

	"github.com/signadot/verse/client"
	"github.com/signadot/verse/codec"
	"github.com/signadot/verse/entity"
)

// The install input of the git io's out half: a tree of charters in the repository, installed
// into the verse at an address, by this io, from its own copy of the repository.
//
//	to:   install.repos.verse     # everything after the first dot is the address, as `verse c`
//	                              # spells one
//	tree: 4b825dc6…               # the tree object of the charters directory, as the reflection
//	                              # reports it (`tree.<name>` on a ref)
//	sha:  4f668c1c…               # the commit it is installed for, carried onto the status
//
// This archives the tree out of the repository, reads every charter file in it, and asks the
// verse to install them as `verse c -a <address> -d <dir> --prune` would: one request, every
// charter in it or none, and what the directory leaves out under the address removed. The status
// answers `tree`, `sha`, `at`, and `charters`, how many were installed. A tree already installed
// for the same commit costs nothing.
//
// IT IS THE REPO-CHARTERS RECONCILER'S DOER (git-issue j9vbwvja, dzxn44gw). The reconciler
// decides, and the install has to happen where a copy of the repository is: on a deployment that
// is this io's pod, which holds the mirror, and nowhere a task runs. The io installs as itself,
// so it has to own the address, or hold `child-owner` above it; what the install refuses, the
// verse says, and a refusal (a 4xx) is recorded for good until the input changes, since the same
// tree asked again would be refused again. A verse that cannot be reached is tried again.
//
// WHAT IT DOES NOT DO: it does not read the charters, judge them, or choose the tree. The
// reconciler has decided by the time it writes the input; this carries the decision out.

// installTarget is the kind an install input's `to` begins with.
const installTarget = "install"

// install carries one install input out, and answers the status fields.
func (k *GitSink) install(ctx context.Context, rp repo, in Input, address string) (entity.Obj, error) {
	if k.Verse == nil {
		return nil, Permanent(fmt.Errorf("this io reads no verse, so it installs nothing: it runs with no address to install into"))
	}
	segs, err := entity.PathSegs(address)
	if err != nil || len(segs) == 0 {
		return nil, Permanent(fmt.Errorf("`to` %q: %q is not an address", in.To, address))
	}
	at := entity.JoinSegs(segs)
	tree := strings.TrimSpace(entity.Str(in.Value, "tree"))
	if tree == "" {
		return nil, Permanent(fmt.Errorf("an install input carries {to, tree}, and this one has no tree"))
	}
	sha := strings.TrimSpace(entity.Str(in.Value, "sha"))
	if entity.Str(in.Status, "tree") == tree && entity.Str(in.Status, "sha") == sha && entity.Get(in.Status, "error") == nil {
		return nil, nil // installed for this commit, and the status says so
	}
	// WHICH ASKING, said beside any failure from here on: a reconciler reading the status of a
	// refused install has nothing else to tell which tree and which commit were refused.
	asked := entity.Obj{"tree": tree, "address": at}
	if sha != "" {
		asked["sha"] = sha
	}
	// The tree object, in full and one the repository has. A tree it lacks is refused for good,
	// as a missing commit is for a ref: the reflection read it before anything asked to install it.
	full, err := gitCmd(ctx, rp.dir, "rev-parse", "--verify", "-q", tree+"^{tree}")
	if err != nil || !fullSHA.MatchString(full) {
		return asked, Permanent(fmt.Errorf("tree %q is no tree this repository has", tree))
	}
	docs, readErr, err := treeCharters(ctx, rp.dir, full)
	if err != nil {
		return asked, err
	}
	if readErr != nil {
		return asked, Permanent(fmt.Errorf("tree %s: %w", tree, readErr))
	}
	q := url.Values{"prune": {at}}
	resp, err := k.Verse.PostNodes("/charter", q, docs)
	if err != nil {
		return asked, fmt.Errorf("installing tree %s at %s: %w", tree, at, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		err := client.StatusError(resp, "installing tree "+tree+" at "+at)
		if resp.StatusCode >= 400 && resp.StatusCode < 500 && resp.StatusCode != http.StatusRequestTimeout && resp.StatusCode != http.StatusTooManyRequests {
			return asked, Permanent(err)
		}
		return asked, err
	}
	asked["at"], asked["charters"] = k.now().UTC().Format(time.RFC3339), len(docs)
	return asked, nil
}

// treeCharters reads the charter files of a tree object, through a directory of its own that is
// gone when it answers. err is a failure to read the tree at all, which may pass; readErr is the
// tree's own fault, a file that is not a charter, which asking again will not mend.
func treeCharters(ctx context.Context, repoDir, tree string) (docs []*ir.Node, readErr, err error) {
	dir, err := os.MkdirTemp("", "verse-tree-")
	if err != nil {
		return nil, nil, err
	}
	defer os.RemoveAll(dir)
	if err := archiveTree(ctx, repoDir, tree, dir); err != nil {
		return nil, nil, err
	}
	docs, readErr = charterDocs(dir)
	return docs, readErr, nil
}

// archiveTree writes the files of a tree object into dir, through `git archive`: the one way to
// read a tree out of a bare mirror without a checkout.
func archiveTree(ctx context.Context, repoDir, tree, dir string) error {
	tarPath := filepath.Join(dir, ".tree.tar")
	if _, err := gitCmd(ctx, repoDir, "archive", "--format=tar", "-o", tarPath, tree); err != nil {
		return fmt.Errorf("archiving tree %s: %w", tree, err)
	}
	f, err := os.Open(tarPath)
	if err != nil {
		return err
	}
	defer f.Close()
	defer os.Remove(tarPath)
	tr := tar.NewReader(f)
	for {
		h, err := tr.Next()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return fmt.Errorf("reading the archive of %s: %w", tree, err)
		}
		name := filepath.Clean(h.Name)
		if name == "." || strings.HasPrefix(name, "..") {
			continue
		}
		path := filepath.Join(dir, name)
		switch h.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(path, 0o755); err != nil {
				return err
			}
		case tar.TypeReg:
			if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
				return err
			}
			out, err := os.Create(path)
			if err != nil {
				return err
			}
			if _, err := io.Copy(out, tr); err != nil {
				out.Close()
				return err
			}
			out.Close()
		}
	}
}

// charterDocs reads every charter file under dir, one document each, as `verse c -a -d` does: a
// file whose name is a charter format's is a charter, and anything else is left alone.
func charterDocs(dir string) ([]*ir.Node, error) {
	var docs []*ir.Node
	err := filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		format, ok := codec.ForPathStrict(path)
		if !ok {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		parsed, err := codec.ParseNodes(data, format)
		if err != nil {
			return fmt.Errorf("%s: %w", strings.TrimPrefix(path, dir+"/"), err)
		}
		var kept []*ir.Node
		for _, n := range parsed {
			if in := codec.Unwrap(n); in != nil && in.Type != ir.NullType {
				kept = append(kept, n)
			}
		}
		switch len(kept) {
		case 0:
			return fmt.Errorf("%s: no charter in it", strings.TrimPrefix(path, dir+"/"))
		case 1:
			docs = append(docs, kept[0])
			return nil
		default:
			return fmt.Errorf("%s: a file in a directory holds one charter, and this one holds %d", strings.TrimPrefix(path, dir+"/"), len(kept))
		}
	})
	if err != nil {
		return nil, err
	}
	// NO CHARTER FILE IS A TREE OF NO CHARTERS, not a fault: installed with its prune it takes
	// down every charter at the address, which is what a repository that removed its charters
	// directory asks for (the empty tree, system/repocharters).
	return docs, nil
}
