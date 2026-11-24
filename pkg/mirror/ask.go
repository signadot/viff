package mirror

import "go.viff.dev/viff/pkg/entity"

// An ask is a person asking somebody something, kept in viff's half at `mirror.ask.<id>`
// (docs/asking.md). It is written by the verb that owns it (`viff ask`, POST /asks) and is its
// asker's record; viffd poses it, writes the answer on it, and runs what follows a yes on the
// asker's authority (internal/viffd/asks.go).
//
// viff's half because an ask is a rendezvous viff keeps, beside the question it becomes, and
// because the verb decides what may change while it waits: the answer is consent to what was
// posed, and a record anybody could edit would not be what was consented to.
const AskSystem = "ask"

// AskRef addresses one ask.
func AskRef(id string) entity.Ref { return entity.ViffRef(AskSystem, id) }

// IsAsk reports whether ref is an ask.
func IsAsk(ref entity.Ref) bool { return ref.ChildOf(entity.ViffRoot, AskSystem) }
