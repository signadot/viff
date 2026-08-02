package process

// Verb is what an action does to a resource — the typed-capability axis the
// model insists on classifying by, never the self-reported tool name.
type Verb string

const (
	Read      Verb = "read"
	Write     Verb = "write"
	Create    Verb = "create"
	Delete    Verb = "delete"
	Exec      Verb = "exec"
	Network   Verb = "network"
	CallAgent Verb = "call-agent" // synchronous sub-process invocation — a function call that opens a defer frame (distinct from a plain tool call)
	// Resolve answers a gate question. It is its own verb for the reason CallAgent is:
	// a predicate that cannot name resolution on its own can only speak about everything
	// an answerer does at once, so "may answer questions about this, and nothing else"
	// becomes unsayable. Answering is also not a Write — nothing in the store moves, and
	// the thing it acts on is a question rather than a document.
	Resolve Verb = "resolve"
	// Reserved: an asynchronous "spawn"/"go" verb (fork a concurrent process,
	// teardown-at-join) is deliberately omitted — it needs a different lifetime
	// model (structured concurrency + monitoring) and breaks the quiescent
	// boundary, so it is a separate verb added later, not a flag on CallAgent.
)

// MarshalText / UnmarshalText spell Verb as its text, which is what it already is.
//
// They were REQUIRED until go-tony v0.0.94: a named string type was emitted as an integer
// conversion by tony-codegen, which resolved it to no schema and fell back to int64 —
// producing a wrong signature and code that did not compile (item 8 of
// tony-format#f69agjyeh12ksa25cnn0). v0.0.93 fixed the signature and v0.0.94 the emitted
// conversion, so a bare named string works now.
//
// They stay because they are right independently: TextMarshaler is the hook honored by both
// gomap paths (reflection and generated) and by encoding/text consumers, so it is how a
// scalar vocabulary declares its wire form rather than leaving each path to infer one. Same
// for Mode and entity.Op.
//
// Deliberately permissive: an unknown verb decodes rather than erroring, matching what
// encoding/json does today. Validation belongs where a verb is interpreted, not at the
// serialization boundary, or a new verb from a newer peer becomes a decode failure.
func (v Verb) MarshalText() ([]byte, error) { return []byte(v), nil }

func (v *Verb) UnmarshalText(b []byte) error { *v = Verb(b); return nil }

// ResourceKind classifies what a capability acts on.
type ResourceKind string

func (k ResourceKind) MarshalText() ([]byte, error) { return []byte(k), nil }

func (k *ResourceKind) UnmarshalText(b []byte) error { *k = ResourceKind(b); return nil }

const (
	KindFile    ResourceKind = "file"
	KindGitRef  ResourceKind = "gitref"
	KindHost    ResourceKind = "host"    // network endpoint
	KindProcess ResourceKind = "process" // an exec'd command
	KindAgent   ResourceKind = "agent"   // a delegated sub-process
	KindGate    ResourceKind = "gate"    // a gate question, by its id
)

// Resource is a typed reference to the thing a capability touches.
type Resource struct {
	Kind  ResourceKind
	Ref   string            // path | git ref | host:port | command | process id/spec
	Attrs map[string]string // kind-specific extras
}

// Effect is one typed capability of an action: a verb applied to a resource.
// It is what gates and scope match on — the derived action, not the
// self-reported tool name. We deliberately do NOT track reversibility:
// whether an action can be undone is unobservable in general (is the fs
// append-only? did the write escape the sandbox?), so we don't pretend to know.
// Control is "stop between actions," never "undo one."
type Effect struct {
	Verb     Verb
	Resource Resource
}

// Convenience constructors for typed resources.
func File(path string) Resource   { return Resource{Kind: KindFile, Ref: path} }
func GitRef(ref string) Resource  { return Resource{Kind: KindGitRef, Ref: ref} }
func Host(hp string) Resource     { return Resource{Kind: KindHost, Ref: hp} }
func AgentRes(id string) Resource { return Resource{Kind: KindAgent, Ref: id} }

// GateRes names the question a resolution answers. The ref is the question id, which
// carries its origin (`<origin>~d<n>`), so a predicate can select the questions of one
// process without a second field to match on.
func GateRes(questionID string) Resource { return Resource{Kind: KindGate, Ref: questionID} }
