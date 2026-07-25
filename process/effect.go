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
	// Reserved: an asynchronous "spawn"/"go" verb (fork a concurrent process,
	// teardown-at-join) is deliberately omitted — it needs a different lifetime
	// model (structured concurrency + monitoring) and breaks the quiescent
	// boundary, so it is a separate verb added later, not a flag on CallAgent.
)

// MarshalText / UnmarshalText spell Verb as its text, which is what it already is.
//
// They exist because a named string type is otherwise emitted as an integer conversion
// by tony-codegen — it resolves such a type to no schema and falls back to int64,
// producing code that does not compile. TextMarshaler is the one hook honored by both
// gomap paths (reflection and generated), so it is how a scalar vocabulary declares its
// wire form. Same reason as Mode's, and tracked upstream as
// tony-format#f69agjyeh12ksa25cnn0.
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
