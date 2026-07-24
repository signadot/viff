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

// ResourceKind classifies what a capability acts on.
type ResourceKind string

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
