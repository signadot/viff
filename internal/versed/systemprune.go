package versed

import (
	"fmt"
	"os"
	"sort"

	"github.com/signadot/verse/internal/trigger"
	"github.com/signadot/verse/internal/trigger/charter"
	"github.com/signadot/verse/pkg/codec"
	"github.com/signadot/verse/pkg/entity"
)

// pruneSystemCharters takes down each charter package the system installed that none of the
// charters it installs at start names any more, with what the package kept at its address.
//
// THE CONFIG IS WHAT THE SYSTEM RUNS. The system installs at start the charters the deployment
// names (its charter file, SystemCharters: the repo-charters reconcilers `verse up` renders from
// `repoCharters`), and nobody else may suspend or remove a rule the system installed. So a
// package the config stopped naming would run on for good, restored from the store at every
// start with no request able to take it down. Taking it down here is what makes dropping it from
// the config turn it off.
//
// WHAT IT KEPT AT ITS ADDRESS goes with it, unlike an operator's uninstall (removePackage): the
// system wrote it for a charter the deployment no longer runs, and a config that names it again
// starts it from nothing rather than from what an older verse concluded. What it caused
// elsewhere is not its own and stays: a reconciler turned off leaves running the charters it had
// installed.
//
// THE RULE RECORDS SAY WHOSE A PACKAGE IS, not the engine: a rule the store holds and this binary
// cannot decode (a charter syntax that changed) is in no engine, and that is a reconciler's
// state exactly when turning it off matters.
//
// NOTHING, WHEN A NAMED TEXT CANNOT BE READ: its address is then unknown, and a charter the
// config still names must not be taken for one it dropped.
func (s *Server) pruneSystemCharters() {
	named, ok := s.systemChartersNamed()
	if !ok {
		return
	}
	release, err := s.lockCharter(s.ctxOrBackground())
	if err != nil {
		fmt.Fprintf(os.Stderr, "verse: system charters the config dropped: %v\n", err)
		return
	}
	defer release()
	// The listing's own failure: the ActsAs reads below would otherwise set or clear the
	// store's flag before it was asked.
	rules, err := entity.BelowErr(s.Store, charter.IsRule, 2, entity.VerseRoot, charter.System)
	if err != nil {
		fmt.Fprintf(os.Stderr, "verse: system charters the config dropped: %v\n", err)
		return
	}
	system := map[string]bool{}
	for _, ent := range rules {
		at := entity.Str(ent.Payload, charter.AtField)
		if at == "" || named[at] {
			continue
		}
		if who, err := entity.ActsAs(s.Store, ent.Ref); err == nil && who == entity.SystemCharter {
			system[at] = true
		}
	}
	gone := make([]string, 0, len(system))
	for at := range system {
		gone = append(gone, at)
	}
	// Deepest first, so a package is gone before the address above it is.
	sort.Sort(sort.Reverse(sort.StringSlice(gone)))
	store := entity.As(s.Store, entity.SystemCharter)
	for _, at := range gone {
		if err := s.takeDown(store, at); err != nil {
			fmt.Fprintf(os.Stderr, "verse: system charter at %s, which the config no longer names: %v\n", at, err)
			continue
		}
		// Its address is its own only while no package stands under it.
		if len(s.packagesUnder(at)) == 0 {
			segs, err := entity.PathSegs(at)
			if err == nil {
				_, _, err = store.Write(entity.Write{At: entity.RefAt(segs...).At(), Value: entity.Removed()})
			}
			if err != nil {
				fmt.Fprintf(os.Stderr, "verse: system charter at %s: taken down, and what it kept there stands: %v\n", at, err)
				continue
			}
		}
		fmt.Fprintf(os.Stderr, "verse: system charter at %s: taken down, as the config no longer names it\n", at)
	}
}

// systemChartersNamed is the addresses of the packages the charters the system installs at start
// name, and false when one of those texts cannot be read.
func (s *Server) systemChartersNamed() (map[string]bool, bool) {
	texts := [][]byte{baseCharter}
	if s.SystemCharter != "" {
		data, err := os.ReadFile(s.SystemCharter)
		if err != nil {
			return nil, false
		}
		texts = append(texts, data)
	}
	for _, c := range s.SystemCharters {
		texts = append(texts, c.Text)
	}
	named := map[string]bool{}
	for _, text := range texts {
		docs, err := codec.ParseNodes(text, codec.Tony)
		if err != nil {
			return nil, false
		}
		groups, err := charterFiles(docs, "")
		if err != nil {
			return nil, false
		}
		for _, g := range groups {
			if g.at == "" {
				continue
			}
			c, err := trigger.ParseAt(g.at)
			if err != nil {
				return nil, false
			}
			named[c.Stamp()] = true
		}
	}
	return named, true
}
