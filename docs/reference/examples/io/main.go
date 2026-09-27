// Command io is a verse io you can copy and change: the smallest thing that serves
// the verse-facing reflection contract, so a drive can reflect a system verse itself knows
// nothing about — one behind a corp firewall, one whose credentials belong to somebody else,
// one that has not been written yet.
//
//	go run ./docs/reference/examples/io -addr :9801 -repo verse=/path/to/verse
//	verse io add build-host http://localhost:9801 -root git
//	verse io
//
// It is a real program and it compiles with the tree, which is the point: a stub in a fenced
// block goes stale the first time a signature moves and nobody finds out. It also is not a
// toy — as shipped it serves verseio.Git, the same value verse runs in task, so what you
// copy already works before you change anything. See ../../../io.md for what an io IS
// and git issue 1yke34rr for what is left.
//
// # What to change
//
// One function: reflect. Read your system's truth, and return it. Everything else here is
// plumbing you can leave alone — iod serves the two routes, and verse converges its own
// store with whatever you answer.
//
// # What NOT to change
//
// A source NEVER writes entities, and is given no way to. That is not a restriction verse
// imposes to be safe with your credentials; it is what makes a source cheap to write. Three
// rules govern turning a reflection into store writes — skip what has not changed, tombstone
// what is gone, never tombstone what you did not claim — and every one of them is a way to
// fire two hundred triggers at three in the morning. They live on verse's side, once, and you
// do not implement them.
//
// The one thing you DO owe: `Covers` must say what your answer is complete for, and no more.
// Everything under a covered pattern that you did not return is deleted. If you can only see
// half of your system, cover half of it.
//
// And an error is an error. Returning an empty Reflection because a read failed says "the
// external system is empty now", and verse will believe you and converge to it.
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"net/http"
	"strings"

	"github.com/signadot/verse/entity"
	verseio "github.com/signadot/verse/io"
	verseiod "github.com/signadot/verse/io/iod"
)

func main() {
	addr := flag.String("addr", ":9801", "address to serve the reflection contract on")
	var repos repoFlag
	flag.Var(&repos, "repo", "a repository to reflect, as <id>=<dir> (repeatable)")
	flag.Parse()

	src := &demo{git: verseio.NewGit()}
	for id, dir := range repos {
		src.git.AddRepo(id, dir)
	}
	// iod is the contract: GET /io says what this program is, GET /reflect says what
	// it sees. verse drives exactly this over HTTP, and its own in-process source is the same
	// value behind the same handler — so there is no privileged local path for this to differ
	// from.
	log.Printf("source listening on %s — install it with: verse io add <name> http://<host>%s -root <root>",
		*addr, *addr)
	if err := http.ListenAndServe(*addr, verseiod.New(src).Handler()); err != nil {
		log.Fatalf("source: %v", err)
	}
}

// demo is the source this program serves. It delegates to verseio.Git, so the sample runs
// against a real system out of the box; replace the delegation with your own reading.
type demo struct{ git *verseio.Git }

// Root is the slice this source owns and is the SINGLE WRITER of: `git.*`, `linear.*`,
// whatever you are reflecting. One source per system per scope — verse refuses an install
// that cannot be proved disjoint from the ones already standing.
func (d *demo) Root() string { return d.git.Root() }

// Owns is what this source claims across every pass, which is what verse proves that
// disjointness against. Coarser than a Covers below — it says what nobody else may write — and
// spelled as ref globs (`git.*.verse/*`) rather than as a payload predicate, because an install
// compares two declarations with no entities in hand and only addresses compare statically.
func (d *demo) Owns() []verseio.Own { return d.git.Owns() }

// reflect reads external truth for the slice `ref` names and returns it. CHANGE THIS.
//
// A zero ref asks for everything you own — the full pass verse runs on a timer and at
// startup. A concrete ref narrows it, and narrowing is optional: answering the whole thing is
// always correct, just more work. There is no cursor, no since-token and no diff to keep; the
// pass is level-triggered, so what you return is compared against what stands and only the
// difference is written.
//
// The sketch of your own, with the two lines that matter marked:
//
//	func (d *demo) Reflect(ctx context.Context, ref entity.Ref) (verseio.Reflection, error) {
//	    issues, err := d.api.ListIssues(ctx, project)
//	    if err != nil {
//	        return verseio.Reflection{}, err          // an error, NEVER an empty reflection
//	    }
//	    cover := verseio.Cover("linear", "issue", "*")
//	    cover.Predicate = entity.Obj{"project": project}.Payload() // say what you are complete FOR
//	    rn := verseio.Reflection{Covers: []entity.Pattern{cover}}
//	    for _, i := range issues {
//	        rn.Add(entity.RefAt("linear", "issue", i.ID),
//	            entity.Obj{"project": project, "title": i.Title, "stage": i.State})
//	    }
//	    return rn, nil
//	}
func (d *demo) Reflect(ctx context.Context, ref entity.Ref) (verseio.Reflection, error) {
	return d.git.Reflect(ctx, ref)
}

// repoFlag collects repeated -repo <id>=<dir> flags.
type repoFlag map[string]string

func (r repoFlag) String() string { return fmt.Sprint(map[string]string(r)) }

func (r *repoFlag) Set(v string) error {
	id, dir, ok := strings.Cut(v, "=")
	if !ok || id == "" || dir == "" {
		return fmt.Errorf("want <id>=<dir>, got %q", v)
	}
	if *r == nil {
		*r = repoFlag{}
	}
	(*r)[id] = dir
	return nil
}
