package github

import (
	"context"
	"fmt"
	"net/url"
	"strings"
	"sync"

	verseio "github.com/signadot/verse/pkg/io"
)

// Tree is a directory of a repository that an OPEN pull request reports when it changes it, as
// `changes.<Name>`: the directory's tree sha at the pull request's head, present where that
// differs from the directory at the head's merge base with the pull request's base. The sha is
// the identity of the contents the pull request proposes, which is what lets a charter tell a
// pull request that changes the directory from one that does not, and which change it is,
// without reading a file (git-issue dzxn44gw: the repo-charters reconciler asks about a change
// to the charters on the pull request to main).
//
// AGAINST THE MERGE BASE, NOT THE BASE'S TIP: a pull request branched before the base's copy of
// the directory changed carries the older copy at its head, and merging it leaves the base's
// copy as it is, so it changes nothing there. Compared with the tip it would look like a change
// back to the old copy, and every pull request opened before a change to the directory would be
// held for it. The merge base of a head stays where it is as the base moves on, so it is read
// once per head.
//
// A DIRECTORY THAT IS NOT THERE IS THE EMPTY TREE (verseio.EmptyTree), at the head or at the
// merge base, as the git io reflects it: a pull request that removes the directory changes it to
// the empty tree, and one that adds it changes it from there. Where it is at neither, nothing
// changes. So every open head costs the compare, whether or not it has the directory.
//
// It is the git io's `-tree` for a pull request, and it reaches the pull requests a git mirror
// cannot: a fork's.
type Tree struct {
	// Name is the word the sha stands under, `changes.<Name>`.
	Name string
	// Path is the directory, slash-separated from the repository's root.
	Path string
}

// ParseTree reads `[<name>=]<path>`: the name defaults to the path's last segment, and is one
// plain word, which a charter can spell in a reference where a path with a dot or a slash in it
// cannot.
func ParseTree(spec string) (Tree, error) {
	name, path, named := strings.Cut(spec, "=")
	if !named {
		path, name = spec, ""
	}
	path = strings.Trim(strings.TrimSpace(path), "/")
	if path == "" {
		return Tree{}, fmt.Errorf("-tree %q names no directory", spec)
	}
	if name == "" {
		name = path[strings.LastIndex(path, "/")+1:]
		name = strings.TrimPrefix(name, ".")
	}
	if name == "" || strings.ContainsAny(name, "./* \t\"=") {
		return Tree{}, fmt.Errorf("-tree %q: %q is not one plain word to name it by", spec, name)
	}
	return Tree{Name: name, Path: path}, nil
}

// treeCache keeps what was read about a commit, by repository and sha: the trees it carries, or
// its merge base with a pull request's base. Neither changes for a commit, so each is asked
// about once for as long as the cache keeps it.
type treeCache struct {
	mu   sync.Mutex
	seen map[string]map[string]string
}

// treeCacheMax bounds the cache: past it, it starts again rather than growing with every head a
// busy repository ever had.
const treeCacheMax = 2048

func (c *treeCache) get(key string) (map[string]string, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	t, ok := c.seen[key]
	return t, ok
}

func (c *treeCache) put(key string, t map[string]string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.seen == nil || len(c.seen) >= treeCacheMax {
		c.seen = map[string]map[string]string{}
	}
	c.seen[key] = t
}

// gitTree is GitHub's answer about one tree object: its entries, one level deep.
type gitTree struct {
	SHA  string `json:"sha"`
	Tree []struct {
		Path string `json:"path"`
		Type string `json:"type"`
		SHA  string `json:"sha"`
	} `json:"tree"`
}

// headTrees answers the trees this source is configured for, at one commit of a repository: name
// to sha, a directory absent at that commit as the empty tree. One request per segment of each
// path the first time a commit is asked about, none after.
func (g *GitHub) headTrees(ctx context.Context, repo, sha string) (map[string]string, error) {
	if len(g.trees) == 0 || sha == "" {
		return nil, nil
	}
	key := repo + "@" + sha
	if t, ok := g.treeCache.get(key); ok {
		return t, nil
	}
	out := map[string]string{}
	for _, t := range g.trees {
		at := sha // a commit sha names its tree to this endpoint
		found := true
		for _, seg := range strings.Split(t.Path, "/") {
			var tr gitTree
			if err := g.api.getOnce(ctx, "/repos/"+repo+"/git/trees/"+url.PathEscape(at), &tr); err != nil {
				if NotFound(err) {
					found = false
					break
				}
				return nil, err
			}
			next := ""
			for _, e := range tr.Tree {
				if e.Path == seg && e.Type == "tree" {
					next = e.SHA
					break
				}
			}
			if next == "" {
				found = false
				break
			}
			at = next
		}
		if !found {
			at = verseio.EmptyTree
		}
		out[t.Name] = at
	}
	g.treeCache.put(key, out)
	return out, nil
}

// mergeBase answers the merge base of a pull request's head with its base branch, through the
// compare endpoint. Cached by head: the merge base of a head stays put as the base moves on.
func (g *GitHub) mergeBase(ctx context.Context, repo, base, head string) (string, error) {
	key := "merge-base:" + repo + "@" + head
	if t, ok := g.treeCache.get(key); ok {
		return t["sha"], nil
	}
	var cmp struct {
		MergeBase struct {
			SHA string `json:"sha"`
		} `json:"merge_base_commit"`
	}
	if err := g.api.getOnce(ctx, "/repos/"+repo+"/compare/"+url.PathEscape(base)+"..."+url.PathEscape(head)+"?per_page=1", &cmp); err != nil {
		return "", err
	}
	if cmp.MergeBase.SHA == "" {
		return "", fmt.Errorf("GitHub names no merge base of %s and %s", base, head)
	}
	g.treeCache.put(key, map[string]string{"sha": cmp.MergeBase.SHA})
	return cmp.MergeBase.SHA, nil
}

// withTrees adds to an open pull request's payload the configured directories it changes, as
// `changes`, each at its head. A pull request whose trees or merge base cannot be read now is
// reflected without them, and said: the next pass asks again, and a field left out is absent
// rather than wrong.
func (g *GitHub) withTrees(ctx context.Context, repo string, p pull, payload map[string]any) {
	if len(g.trees) == 0 || p.State != "open" || p.Head == nil || p.Base == nil {
		return
	}
	head, err := g.headTrees(ctx, repo, p.Head.SHA)
	if err != nil {
		g.log("github: %s #%d: reading the trees of its head %s: %v", repo, p.Number, p.Head.SHA, err)
		return
	}
	mb, err := g.mergeBase(ctx, repo, p.Base.Ref, p.Head.SHA)
	if err != nil {
		g.log("github: %s #%d: finding the merge base of %s with %s: %v", repo, p.Number, p.Head.SHA, p.Base.Ref, err)
		return
	}
	base, err := g.headTrees(ctx, repo, mb)
	if err != nil {
		g.log("github: %s #%d: reading the trees of its merge base %s: %v", repo, p.Number, mb, err)
		return
	}
	changes := map[string]any{}
	for name, sha := range head {
		if base[name] != sha {
			changes[name] = sha
		}
	}
	if len(changes) > 0 {
		payload["changes"] = changes
	}
}
