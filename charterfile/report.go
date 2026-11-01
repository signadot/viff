package charterfile

import (
	"fmt"
	"io"
	"net/url"

	"github.com/signadot/tony-format/go-tony/ir"

	"github.com/signadot/verse/codec"
	"github.com/signadot/verse/entity"
)

// WHAT AN INSTALL AND A REMOVAL SAY BACK, in one voice for `verse charter`, verse_charter_install
// and _remove, and the verse system's tools: kpaths spells an address as a kinded path, the way
// a tool takes it, and otherwise as the steps a command line takes.

// CheckQuery is what a check sends beside the charters: the address, and with runnersToCome the
// vet a restore places a rule with, which holds a spawn no runner offers where the install
// refuses it (POST /charter/check?runners=to-come).
func CheckQuery(at string, runnersToCome bool) url.Values {
	q := url.Values{"at": {at}}
	if runnersToCome {
		q.Set("runners", "to-come")
	}
	return q
}

// InstallQuery is how an install sends charters given as text: one document is a file, installed
// at at, and may be the shorthand (a rule, a level, a list of them); several, or a prune, are a
// directory, each a charter at its own at:, and with prune what they leave out under at goes. It
// answers the query and the definitions to send, as `verse charter -a` reads -f and -d.
func InstallQuery(docs []*ir.Node, names []string, at string, prune bool) (url.Values, []*ir.Node, error) {
	if len(docs) == 0 {
		return nil, nil, fmt.Errorf("charters: there is none to install")
	}
	q, defs := url.Values{}, docs
	switch {
	case prune:
		if at == "" {
			return nil, nil, fmt.Errorf("prune clears an address of what the charters leave out: name it in at")
		}
		q.Set("prune", at)
	case len(docs) == 1:
		if at != "" {
			q.Set("at", at)
		}
		if !IsDoc(docs[0]) {
			if defs = Defs(docs[0]); len(defs) == 0 {
				return nil, nil, fmt.Errorf("charters: no rule, level or charter in it")
			}
		}
	case at != "":
		return nil, nil, fmt.Errorf("several charters are each installed at their own at:, so at goes " +
			"with one, or with prune")
	}
	if len(docs) > 1 || prune {
		if err := Only(docs, names); err != nil {
			return nil, nil, fmt.Errorf("charters: %w", err)
		}
	}
	return q, defs, nil
}

// InstallAt is the address an install acts at, as the verse system's charter-install step names
// it (field `at`): with a prune, the prune's; otherwise the deepest address every charter of docs
// lies at or under, each at its own `at:` or, saying none, at q's `at` (baseline, empty), as verse
// places them. Everything the install writes is at or under it, so a shape on it bounds the
// install.
func InstallAt(q url.Values, docs []*ir.Node) (string, error) {
	if prune := q.Get("prune"); prune != "" {
		segs, err := AddrSegs(prune)
		if err != nil {
			return "", fmt.Errorf("prune: %w", err)
		}
		return entity.JoinSegs(segs), nil
	}
	var common []string
	first := true
	for _, doc := range docs {
		if in := codec.Unwrap(doc); in == nil || in.Type == ir.NullType {
			continue
		}
		at := q.Get("at")
		if IsDoc(doc) && str(doc, "at") != "" {
			at = str(doc, "at")
		}
		segs, err := AddrSegs(at)
		if err != nil {
			return "", fmt.Errorf("at %q: %w", at, err)
		}
		if first {
			common, first = segs, false
			continue
		}
		n := 0
		for n < len(common) && n < len(segs) && common[n] == segs[n] {
			n++
		}
		common = common[:n]
	}
	if first {
		segs, err := AddrSegs(q.Get("at"))
		if err != nil {
			return "", fmt.Errorf("at: %w", err)
		}
		common = segs
	}
	return entity.JoinSegs(common), nil
}

// Installed says what went where, in both coordinates, from what POST /charter answered (node,
// api.Installed); scope is the install's.
//
// "baseline" is the unpackaged charter — the one that may write anywhere — and it is also the
// word for the default SCOPE, so a scoped install that says only "baseline" reads as the one
// place it did not go. A scope is the coordinate somebody carries in a flag and forgets, which
// is exactly the one a report owes them back.
//
// It answers whether any rule is still ARMING — accepted, its watch not open yet — which a caller
// that asked the install to wait turns into its exit status.
func Installed(w io.Writer, node *ir.Node, scope string, kpaths bool) (arming bool) {
	inScope := ""
	if scope != "" {
		inScope = " in scope " + scope
	}
	packaged := false
	list := codec.Unwrap(field(node, "installed"))
	if list == nil || list.Type != ir.ArrayType {
		fmt.Fprintln(w, "installed")
		return false
	}
	for _, el := range list.Values {
		e := codec.Unwrap(el)
		at := str(e, "at")
		rules := num(e, "rules")
		strata := num(e, "strata")
		where := "baseline"
		if at != "" {
			// IN STEPS, like every other address a command prints, so the package this names is
			// spelled the way `verse c <address>` takes it. The charter FILE declares it as a
			// path, which is right there — a charter is read by a parser — and this is the line
			// a person reads.
			where, packaged = "package "+Spelled(at, kpaths), true
		}
		where += inScope
		if strata == 0 {
			fmt.Fprintf(w, "%s: installed %d trigger(s)\n", where, rules)
		} else {
			fmt.Fprintf(w, "%s: installed %d stratum(a) and %d trigger(s)\n", where, strata, rules)
		}
		arming = installNotes(w, e) || arming
	}
	pruned(w, node, kpaths)
	// WHERE TO LOOK, said at the moment it is not obvious: a charter installed into packages
	// leaves `verse c` — the next thing anybody types — answering baseline's rules, which is an
	// empty list. Saying so here is what keeps somebody from having to be surprised first.
	if packaged {
		reader := "`verse c`"
		if kpaths {
			reader = "verse_charter"
		}
		fmt.Fprintf(w, "%s shows every charter and where each may write\n", reader)
	}
	return arming
}

// installNotes says what installing one package had to say: when its ttl ends, the rules held
// and the ones still arming, and the advisories. It answers whether any is still arming.
//
// UNDER THE LINE THAT SAYS WHAT WAS INSTALLED, because each changes what that line means: a
// charter with a ttl is installed until then, a held rule is counted out of it, and an advisory
// is a rule that was counted and does something other than it reads. All are the author's to act
// on, and this is where the author is.
func installNotes(w io.Writer, e *ir.Node) (arming bool) {
	if expires := str(e, "expires"); expires != "" {
		fmt.Fprintf(w, "  ttl: it is removed at %s, as -r removes it; installing it again renews it.\n", expires)
	}
	if held := codec.Unwrap(field(e, "held")); held != nil && held.Type == ir.ArrayType {
		for _, h := range held.Values {
			h = codec.Unwrap(h)
			if boolean(h, "arming") {
				arming = true
				fmt.Fprintf(w, "  arming: rule %s is accepted, and %s (wait=<duration> holds the answer for it).\n",
					str(h, "rule"), str(h, "waits"))
				continue
			}
			fmt.Fprintf(w, "  held: rule %s is %s. It fires on nothing until then.\n",
				str(h, "rule"), str(h, "waits"))
		}
	}
	if notes := codec.Unwrap(field(e, "notes")); notes != nil && notes.Type == ir.ArrayType {
		for _, n := range notes.Values {
			n = codec.Unwrap(n)
			fmt.Fprintf(w, "  note: rule %s: %s\n", str(n, "rule"), str(n, "note"))
		}
	}
	return arming
}

// pruned says what a prune took away: out of a package the directory kept, each thing by name; a
// package it left out, whole.
func pruned(w io.Writer, node *ir.Node, kpaths bool) {
	list := codec.Unwrap(field(node, "pruned"))
	if list == nil || list.Type != ir.ArrayType {
		return
	}
	tree, file := "the directory", "its file"
	if kpaths {
		tree, file = "the charters given", "its charter"
	}
	for _, el := range list.Values {
		e := codec.Unwrap(el)
		where := "package " + Spelled(str(e, "at"), kpaths)
		if u := codec.Unwrap(field(e, "uninstalled")); u != nil && u.Type == ir.BoolType && u.Bool {
			fmt.Fprintf(w, "%s: uninstalled, not in %s; what it wrote is still there\n", where, tree)
			continue
		}
		if removed := codec.Unwrap(field(e, "removed")); removed != nil && removed.Type == ir.ArrayType {
			for _, r := range removed.Values {
				if r = codec.Unwrap(r); r != nil && r.Type == ir.StringType {
					fmt.Fprintf(w, "%s: removed the %s, not in %s\n", where, r.String, file)
				}
			}
		}
	}
}

// Removed is what a removal of names says went, at as Query takes it, scope the removal's: a
// package leaves its writes behind, and a rule removed in a SCOPE was the scope's own — an
// inherited one is refused as baseline's — so baseline holds what it held.
//
// WHAT WENT is said back, because the path said it and a path is easy to mistype.
func Removed(names []string, at int, scope string, recurse, kpaths bool) string {
	_, reach := SplitAt(names, at)
	name, kind, item := lastItem(reach)
	where := Spelled(entity.JoinSegs(names), kpaths)
	switch {
	case item && scope != "":
		return fmt.Sprintf("removed the %s %s in scope %s; baseline is untouched", oneOfKind(kind), name, scope)
	case item:
		return fmt.Sprintf("removed the %s %s", oneOfKind(kind), name)
	case recurse:
		// `-r` reaches a PLACE as well as a charter, and often that is all it reaches — the
		// address itself holding nothing and the charters being under it. Saying "the charter
		// at demo" about a demo that never had one is a sentence somebody will re-check.
		return fmt.Sprintf("uninstalled the charters at and under %s; what they wrote is still there", where)
	}
	return fmt.Sprintf("uninstalled the charter at %s; what it wrote is still there", where)
}

// lastItem reports whether a path ends at a named rule, level or schedule, and which.
func lastItem(steps []string) (name, kind string, ok bool) {
	if len(steps) >= 2 {
		if k := steps[len(steps)-2]; k == "rules" || k == "strata" || k == "schedules" {
			return steps[len(steps)-1], k, true
		}
	}
	return "", "", false
}

// oneOfKind is the key said about a single thing: a charter holds rules, LEVELS and schedules.
func oneOfKind(kind string) string {
	switch kind {
	case "strata":
		return "level"
	case "schedules":
		return "schedule"
	}
	return "rule"
}
