package api

import "github.com/signadot/tony-format/go-tony/ir"

// Status is what GET /status answers: what this viff is, what it is running, and where to
// reach the pieces around it.
//
// The counters are the cheap health read; the three optional blocks are discovery. `docd` is
// how a store client beside this viff finds docd without guessing the ephemeral port `up`
// chose, `listen` publishes this viff's own addresses, and `llm` says whether agent creds
// are configured ON THIS SERVER — the common gotcha, since the runtime reads its own
// environment rather than the client's.
//
//tony:schemagen=viff-status,notag
type Status struct {
	StoreRev int64     `tony:"field=storeRev"`
	Flows    FlowCount `tony:"field=flows"`
	Gates    int       `tony:"field=gates"`
	// Suspensions is what is suspended in the scope asked about, absent when nothing is.
	Suspensions *SuspensionCount `tony:"field=suspensions,omitzero"`
	Tasks       int              `tony:"field=tasks"`
	Fired       int              `tony:"field=fired"`
	// Firings is what has gone wrong at the rules' firings since this process started, absent
	// when nothing has. `viff charter` says which rule, and with what.
	Firings *Firings `tony:"field=firings,omitzero"`

	// WHAT IS INSTALLED, as against what is happening. The counts above move on their own;
	// these three are things somebody put here, and a viff that has lost them looks healthy by
	// every other line — which is exactly the state a wiped store comes back in.
	//
	// Runners are the refs a spawn may name (`mirror.runners.code`), which is a different question
	// from `runner` below — that one is how THIS viff runs a task when a spawn names nobody.
	// IOs are the refs of what reaches the world for this viff (`mirror.iods.slack`) — the one
	// line here that says anything is arriving at all, since a viff whose iods are gone goes on
	// reporting a fine store rev and an idle charter.
	//
	// The charter used to be here too, as `rules` (a count) and `strata` (names in level
	// order). Both were BASELINE's alone while every count beside them is the whole viff's, so
	// a viff whose program is packaged reported `rules: 0` and no levels — the scoping was the
	// bug, not the zero. `charter` below carries both, per node, where the shape says whose.
	//
	// All three are refs rather than records: status is an orientation, and `viff charter`,
	// `viff runners` and `viff iod` are the surfaces that answer what one IS. An io's health —
	// when its last pass completed, whether it is failing, what its out half delivered — is on
	// `viff iod`.
	Runners []string `tony:"field=runners,omitzero"`
	IOs     []string `tony:"field=iods,omitzero"`
	// Charter is the charter as a CENSUS: how much is installed, and where.
	//
	//	charter:
	//	  rules: 2          # baseline's own, at the root — the charter with no address
	//	  strata: 1
	//	  children:
	//	    viff-dev:      # a PLACE: nothing installed here, so no counts
	//	      children:
	//	        drift: {rules: 4, strata: 2}
	//	        merge: {rules: 1}
	//
	// SAME SHAPE AS `viff charter`, counted instead of enumerated: `rules`, `strata` and
	// `children` mean what they mean there, and a node with no charter carries only `children`
	// — so `rules: 0` appears where a charter really holds none, and nowhere else. Status is a
	// census of numbers and the charter surface is what answers what a rule IS.
	//
	// `arming`, when present, names rules counted in `rules` whose watch is still being opened:
	// restored from the charter over a store that did not answer, in the charter and firing on
	// nothing until it does. Absent on a viff that is up.
	//
	// A TREE rather than a flat list of addresses, because addresses SHARE PREFIXES and a
	// status line is read at a glance: `viff-dev.drift` and `viff-dev.merge` are two charters
	// in one place, and a list said `viff-dev` twice and the shape not at all.
	Charter *ir.Node `tony:"field=charter,omitzero"`

	Runner *RunnerStatus `tony:"field=runner,omitzero"`
	Docd   *Docd         `tony:"field=docd,omitzero"`
	LLM    *LLM          `tony:"field=llm,omitzero"`
	Listen *Listen       `tony:"field=listen,omitzero"`
	// Issuer is this viff as an issuer of its own tokens (auth.issuer): absent where it issues
	// none.
	Issuer *Issuer `tony:"field=issuer,omitzero"`
	// Perms is what the permission check has refused since this process started: a count
	// that climbs is a flow somebody is being refused, which is either the autonomy's intent or
	// a row the seed is missing.
	Perms *Perms `tony:"field=perms,omitzero"`
	// Restoring is the phases of the server's start still being asked again, by name: each
	// reads a slice of the store, the store did not answer, and what it would have restored —
	// the sources, the docket's deadlines, a charter — is not running yet. A read that did
	// not happen is asked again until it does; this is the sign that one is. Absent on a viff
	// that is up.
	Restoring []string `tony:"field=restoring,omitzero"`
}

// Firings counts what has gone wrong at the rules' firings, over every charter installed.
//
// A count that climbs is a rule doing less than it says. Which rule, where in it, about what
// and why are on the rule's record, under `trouble`, which `viff charter` says on stderr.
//
//tony:schemagen=viff-firings,notag
type Firings struct {
	// Failed is actions that did not do what their rule says: a reference that did not
	// resolve, a patch or a derive that failed, a spawn refused, a pose that did not expand.
	Failed int `tony:"field=failed,omitzero"`
	// Unread is commits a rule could not read, and so did not evaluate.
	Unread int `tony:"field=unread,omitzero"`
}

// RunnerStatus is this viff's local runner, the runnerd `viff up` starts beside it and
// registers as `local`: whether it is described and what it offers, and where it is not, why
// not, asked of the runner when the status is, never stored. Absent where the viff starts none.
//
//tony:schemagen=viff-runner-status,notag
type RunnerStatus struct {
	// At is its registration, `mirror.runners.local`.
	At string `tony:"field=at"`
	// URL is where it serves viff.
	URL string `tony:"field=url,omitzero"`
	// Described is whether it has said what it offers since it was registered.
	Described bool `tony:"field=described"`
	// Offers is the doers it offers, an agent's name or `script`; empty where it is not described.
	Offers []string `tony:"field=offers"`
	// Why is why it is not described, as the runner answers now.
	Why string `tony:"field=why,omitzero"`
}

// Docd is the document store's client face: the address store clients connect to.
//
//tony:schemagen=viff-docd,notag
type Docd struct {
	Client string `tony:"field=client,omitzero"`
}

// LLM is a SECRETS-SAFE summary of the task runtime's model config: whether it is usable
// at all, and masked keys so an operator can tell which credential is configured without the
// credential leaving the server.
//
//tony:schemagen=viff-llm,notag
type LLM struct {
	Available  bool   `tony:"field=available"`
	APIKey     string `tony:"field=apiKey,omitzero"`
	VirtualKey string `tony:"field=virtualKey,omitzero"`
	Model      string `tony:"field=model,omitzero"`
	BaseURL    string `tony:"field=baseURL,omitzero"`
}

// Issuer is this viff as an issuer: the url every token it signs names as `iss`, and where its
// key is from. Key is `config` for one auth.issuer names, and otherwise the file `viff up` made
// it in, in its data dir, or `memory` for one made for this process alone, which a restart
// replaces and with it every token signed by the last.
//
//tony:schemagen=viff-issuer,notag
type Issuer struct {
	URL string `tony:"field=url"`
	Key string `tony:"field=key"`
}

// Listen is this viff's own addresses: the operator API, and the admin face.
//
//tony:schemagen=viff-listen,notag
type Listen struct {
	API string `tony:"field=api,omitzero"`
	// Admin is the pprof/stacks face. It is reported because its address is DERIVED from
	// the api one rather than fixed, and an operator reaching for it is mid-incident.
	Admin string `tony:"field=admin,omitzero"`
}

// Health is what `GET /healthz` answers, read without touching the store: OK while the process
// serves, Started once its engine is restored (the route answers 503 until then), and Head, the
// store's head as this process last heard it.
//
//tony:schemagen=viff-health,notag
type Health struct {
	OK      bool  `tony:"field=ok"`
	Started bool  `tony:"field=started"`
	Head    int64 `tony:"field=head"`
}
