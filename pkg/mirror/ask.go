package mirror

import "github.com/signadot/verse/pkg/entity"

// An ask is a person asking somebody something, kept in verse's half at `verse.ask.<id>`
// (docs/asking.md). It is written by the verb that owns it (`verse ask`, POST /asks) and is its
// asker's record; versed poses it, writes the answer on it, and runs what follows a yes on the
// asker's authority (internal/versed/asks.go).
//
// verse's half because an ask is a rendezvous verse keeps, beside the question it becomes, and
// because the verb decides what may change while it waits: the answer is consent to what was
// posed, and a record anybody could edit would not be what was consented to.
const AskSystem = "ask"

// AskRef addresses one ask.
func AskRef(id string) entity.Ref { return entity.VerseRef(AskSystem, id) }

// IsAsk reports whether ref is an ask.
func IsAsk(ref entity.Ref) bool { return ref.ChildOf(entity.VerseRoot, AskSystem) }
