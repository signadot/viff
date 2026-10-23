package api

// CharterCheck is what `POST /charter/check` answers: whether a tree of charters would install
// at an address over what runs there, and what would change, with nothing installed. It is a
// question, beside the install and never part of it (versed/chartercheck.go).
//
//tony:schemagen=verse-charter-check,notag
type CharterCheck struct {
	// At is the address checked, which the tree would be installed at with a prune.
	At string `tony:"field=at"`
	// Verdict is `installs` or `refused`.
	Verdict string `tony:"field=verdict"`
	// Refusal is what the install would be refused with, where it would be.
	Refusal string `tony:"field=refusal,omitzero"`
	// Packages are the packages the tree holds, each with what would change in it.
	Packages []CharterCheckPackage `tony:"field=packages,omitzero"`
	// Pruned are the packages standing under At that the tree leaves out, which the install
	// would remove.
	Pruned []string `tony:"field=pruned,omitzero"`
}

// CharterCheckPackage is one package of a checked tree: its address and what would change there,
// each entry `rules.<name>` or `strata.<name>`.
//
//tony:schemagen=verse-charter-check-package,notag
type CharterCheckPackage struct {
	At      string   `tony:"field=at"`
	Added   []string `tony:"field=added,omitzero"`
	Changed []string `tony:"field=changed,omitzero"`
	Removed []string `tony:"field=removed,omitzero"`
	// Held are the rules that would install waiting for a stratum the package does not declare.
	Held []string `tony:"field=held,omitzero"`
}

// Installed is what `POST /charter` answers: what each package of the request installed, and
// what `?prune` took out.
//
//tony:schemagen=verse-installed,notag
type Installed struct {
	Installed []InstalledPackage `tony:"field=installed"`
	Pruned    []Pruned           `tony:"field=pruned,omitzero"`
}

// InstalledPackage is one package's worth of an install: where it went (At, absent for
// baseline), the rules installed that can fire, and the strata. A rule written down and waiting
// for a stratum is Held, not counted in Rules; Notes are advisories, each with its rule. Both are
// the author's to read and nobody else's to act on.
//
//tony:schemagen=verse-installed-package,notag
type InstalledPackage struct {
	Rules  int        `tony:"field=rules"`
	Strata int        `tony:"field=strata"`
	At     string     `tony:"field=at,omitzero"`
	Held   []HeldRule `tony:"field=held,omitzero"`
	Notes  []RuleNote `tony:"field=notes,omitzero"`
}

// HeldRule is a rule installed and waiting for a stratum the package does not declare.
//
//tony:schemagen=verse-held-rule,notag
type HeldRule struct {
	Rule    string `tony:"field=rule"`
	Stratum string `tony:"field=stratum"`
}

// RuleNote is an advisory about a rule: something the install took but the author may not
// have meant.
//
//tony:schemagen=verse-rule-note,notag
type RuleNote struct {
	Rule string `tony:"field=rule"`
	Note string `tony:"field=note"`
}

// Pruned is what `?prune` took out at one address: what it Removed from a package the request
// still installs, or the whole package, Uninstalled.
//
//tony:schemagen=verse-pruned,notag
type Pruned struct {
	At          string   `tony:"field=at"`
	Removed     []string `tony:"field=removed,omitzero"`
	Uninstalled bool     `tony:"field=uninstalled,omitzero"`
}
