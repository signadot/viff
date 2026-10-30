package verbs

// held is what a task is told of a spawn its ask runs: it is the task's, no more.
const held = "A spawn reaches no further than this task does, its autonomy is no looser, and it runs on " +
	"this task's runner with this task's model credential."

func text(name, desc string) Param { return Param{Name: name, Type: "string", Desc: desc} }
func flag(name, desc string) Param { return Param{Name: name, Type: "boolean", Desc: desc} }
func num(name, desc string) Param {
	return Param{Name: name, Type: "integer", Desc: desc, NonNeg: true}
}

func need(p Param, why string) Param { p.Need = why; return p }

// THE ASK VERBS mirror `verse ask`, the asker's side.
var (
	AskCreate = &Verb{
		Name: "ask-create", Tool: "verse_ask_create",
		Lead: "Ask somebody something", CLI: "`verse ask [<name>] -q <question>`",
		Does: "the question, who is asked, when it settles if nobody answers, and what follows a yes or a no, " +
			"which runs on the asker's authority. It does not wait: the call answers `raised question <id>: " +
			"<question>`, and {ask-get} <id> reads the ask back.",
		MCPNote:  "Answers come back through verse_wait.",
		TaskNote: "It stands after this task ends. To ask something and wait for the answer yourself, use question-ask.",
		Params: []Param{
			text("name", "what the ask is called; its id is derived from it and answered. Absent, verse names it"),
			need(text("question", "what is asked, in the words the answerers read"), "an ask needs a question"),
			text("to", "who is asked: a name or a comma-separated list, in the grammar a grant uses "+
				"(user:ana@example.com, ana@example.com, group:leads); absent, anyone may answer"),
			text("due", "when it settles if nobody answers: an RFC 3339 date or a duration from now (48h)"),
			text("on_silence", "with due: what it settles on if nobody answers by then, yes or no (the default); yes runs the then"),
			{Name: "then", Type: "string", TaskNote: held,
				Desc: "what follows a yes, on the asker's authority, as a tony document of one key: {spawn: <a spawn, " +
					"as a charter writes one>}, {answer: {ask: <id>, choice: <option>}} or {ask: {name: …, question: …}}."},
			{Name: "else", Type: "string", TaskNote: held,
				Desc: "what follows a no, or silence settling on no, on the asker's authority; one key, as then takes."},
			text("about", "the path the ask is about, as a kinded path"),
			text("keep", "how long it stands once settled, as a duration (72h); absent, a week"),
			text("label", "what the ask says about itself, name=value, comma-separated (team=payments,kind=deploy)"),
		},
	}
	AskGet = &Verb{
		Name: "ask-get", Tool: "verse_ask_get", Reads: true,
		Lead: "Read back one of the asks raised", CLI: "`verse ask <name|id>`",
		Does: "by the name it was asked under or the id {ask-create} answered: whether it was answered " +
			"(`status`, and `answer`, pending until it settles, or `why` where it settled with no answer), " +
			"what it asked and of whom, its due, and what its then and else did.",
		MCPNote: "What waits on you is verse_question.",
		Params: []Param{
			need(text("name_or_id", "one ask, by the name it was asked under or its id"), "name the ask, by its name or its id"),
		},
	}
	AskList = &Verb{
		Name: "ask-list", Tool: "verse_ask_list", Reads: true,
		Lead: "List the asks raised", CLI: "`verse ask`",
		Does: "each with its id, question, who is asked, its due, its `status`, and its `answer`, pending " +
			"until it settles. {ask-get} reads one of them.",
		MCPNote: "What waits on you is verse_question.",
	}
)

// THE DATA VERBS mirror `verse data`: the verse at an address.
var (
	DataGet = &Verb{
		Name: "data-get", Tool: "verse_data", Reads: true,
		Lead: "Read the verse at an address", CLI: "`verse data`",
		Does: "the document stored there, or with paths the addresses beneath it. What is not in view, " +
			"because nothing stands there or it may not be read, is answered alike. Where the address may " +
			"not be read into at all, the read is refused, naming the nearest owner on record, whom to ask, " +
			"whether or not anything stands there.",
		Params: []Param{
			text("at", "the address as a kinded path, as a document prints it: demo.pr.1, demo.pr.*.stage; "+
				"empty is all of the verse that may be read"),
			text("if", "only entities whose payload matches this tony pattern, chosen by verse before anything is sent: {stage: open}"),
			text("if_at", "root the if pattern at this dot-separated field path inside the payload (labels)"),
			text("trim", "write only the parts of each entity this match document names, as a key set: {id status}"),
			flag("paths", "answer one kinded path per line instead of the document"),
			{Name: "depth", Type: "integer", NonNeg: true, With: "paths",
				Desc: "with paths: walk at most this many levels beneath the address; 0 is the address alone"},
		},
	}
	DataPut = &Verb{
		Name: "data-put", Tool: "verse_data_put",
		Lead: "Write a value at an address", CLI: "`verse data <address> --put`",
		Does: "merged into what stands there; with create, update or expect, only on that condition. " +
			"Answers what stands there after the write.",
		Params: []Param{
			need(text("at", "where to write, as a kinded path: demo.pr.1, or demo.pr.1.labels.urgent for one field; no wildcards"),
				"at says where to write"),
			need(text("value", "what to write there, a tony value: {stage: open}, 42, [a, b]. A document is merged into what stands"),
				"value is what is written there"),
			flag("create", "only if nothing stands there yet: the write loses a race rather than clobbering"),
			flag("update", "only if something already stands there"),
			text("expect", "only if what stands there matches this tony pattern"),
			text("grants", "what the node this write creates holds out, as verse perms --grants takes it: "+
				"[{read: [ana@example.com]}], or [] for nothing; refused where the node stands"),
		},
	}
	DataRm = &Verb{
		Name: "data-rm", Tool: "verse_data_rm", Takes: true,
		Lead: "Remove what stands at an address", CLI: "`verse data <address> --rm`",
		Does: "an entity, or one field of one.",
		Params: []Param{
			need(text("at", "what to remove, as a kinded path: an entity (demo.pr.1) or one field of one "+
				"(demo.pr.1.labels.urgent); no wildcards"), "at names what to remove"),
		},
	}
)

// THE CHARTER VERBS mirror `verse charter`: its read, --check, -a and -r (-R stays on the
// command line).
var (
	CharterGet = &Verb{
		Name: "charter-get", Tool: "verse_charter", Reads: true,
		Lead: "Read the charter at a path", CLI: "`verse charter`",
		Does: "an installed charter as its file (at, reads, ttl, strata, schedules, rules), a rule or level " +
			"of one as it stands, or with paths one line per rule and level. What the file cannot say follows " +
			"it as notes: a rule held, suspended or in trouble, and the charters under the address.",
		Params: []Param{
			text("path", "the charter's path as a kinded path: an address (verse-dev.drift), then a reach into "+
				"what stands there (verse-dev.drift.rules.watcher); empty is baseline's charter, and what is under it"),
			text("at", "where the address ends, when verse cannot tell: a package whose address ends in rules, "+
				"strata or schedules. path is then read beneath it"),
			num("depth", "how far under the address to go: 0 is the address alone, 1 adds its immediate children; "+
				"without it, everything"),
			flag("paths", "answer one kinded path per rule and level instead of the charter, each a path this tool takes back"),
			text("if", "keep only the parts matching this tony pattern"),
			text("trim", "write only the parts this match document names, as a key set: {rules}"),
		},
	}
	CharterCheck = &Verb{
		Name: "charter-check", Tool: "verse_charter_check", Reads: true,
		Lead: "Ask whether charters would install at an address", CLI: "`verse charter --check`",
		Does: "each charter's verdict, and what the install would add, change and take away. Nothing is " +
			"installed; a refusal is the answer, not an error.",
		Params: []Param{
			need(text("at", "the address the charters would be installed at, as a kinded path, as an install "+
				"with a prune would take it: what stands at and under it and the charters leave out would go"),
				"a check names the address the charters would be installed at"),
			need(text("charters", "the charters, in tony, each a document saying its own at: (at or under the "+
				"address) with reads, ttl, strata, schedules and rules as a charter file does; several are "+
				"separated by lines of ---"), "a check carries the charters"),
		},
	}
	CharterInstall = &Verb{
		Name: "charter-install", Tool: "verse_charter_install", Takes: true,
		Lead: "Install charters", CLI: "`verse charter -a`",
		Does: "one at an address, or several each at its own at:, and with prune what they leave out at and " +
			"under the address goes, all in one step. Answers what went where, the rules held and the notes " +
			"on them; {charter-check} asks first.",
		TaskNote: "Each spawn among the rules reaches no further than this task does, less installing " +
			"charters, under autonomy no looser and with this task's credential, on any runner: verse " +
			"refuses one that would. A rule's patch and derive are theirs, as any charter's are.",
		Params: []Param{
			text("at", "where, as a kinded path: the address one charter, rule or level is installed at "+
				"(empty is baseline); with prune, the address the charters stand for"),
			need(text("charters", "what to install, in tony: one charter (at:, reads:, ttl:, strata:, schedules:, "+
				"rules:; ttl: how long it stands, as 72h), or a rule, a level or a list of them; several "+
				"charters are separated by lines of ---, each at its own at:"), "an install carries the charters"),
			flag("prune", "make at and what is under it hold what the charters hold: a package or a rule they "+
				"leave out goes, in the same step"),
		},
	}
	CharterRemove = &Verb{
		Name: "charter-remove", Tool: "verse_charter_remove", Takes: true,
		Lead: "Remove a package, or one rule, level or schedule of one", CLI: "`verse charter -r`",
		Does:    "what it wrote stays.",
		MCPNote: "A place and everything under it is removed from the command line, with -R.",
		Params: []Param{
			text("path", "what to remove, as a kinded path: a package's address (verse-dev.drift), or one rule, "+
				"level or schedule of it (verse-dev.drift.rules.watcher). What it wrote stays"),
			text("at", "where the address ends, when verse cannot tell: a package whose address ends in rules, "+
				"strata or schedules. path is then read beneath it, and without path the package at at is removed"),
		},
		AnyOf: []string{"path", "at"},
		AnyOfNeed: "path names what to remove: a package's address, or one with rules.<name>, strata.<name> " +
			"or schedules.<name> after it",
	}
)

// All is every verb both faces offer.
var All = []*Verb{AskCreate, AskGet, AskList, DataGet, DataPut, DataRm,
	CharterGet, CharterCheck, CharterInstall, CharterRemove}
