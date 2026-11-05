package trigger

import (
	"fmt"
	"strings"

	"github.com/signadot/tony-format/go-tony/ir"

	"github.com/signadot/verse/pkg/entity"
)

// SendsField is where a package declares its OUTBOXES: places inside its own address that an io's
// out half reads as it reads its own out place (git-issue 84a83gkwh12krst5phn0).
//
// A package writes only inside its address, and an io's out half reads only inside its claim, so
// without this the two never meet and every rule that asks an io to act is baseline's. An outbox
// is where they meet, and only because the package says so: a place is not an outbox for having
// a name an io might look for, so the existence of an io gives no package's values a meaning
// their package did not declare.
//
//	at: verse-dev.announce
//	sends:
//	- from: out.slack                # inside this package's address
//	  to: slack.Signadot.out         # an io's out place, as its registration names it
//
// ONE IO PER ENTRY: a `from` feeds exactly one `to`, which is the simplest thing to audit. And no
// `from` beneath another: an io reads its outbox at every depth, so an outbox inside another would
// be read by both iods, each writing a status beside the same inputs.
const SendsField = "sends"

// Send is one outbox: From is the place inside the package (as an address, once read against the
// package's own), and To the io out place it sends to.
type Send struct {
	From, To string
}

// sendKeys are the keys an entry of `sends:` carries, and the only ones.
const (
	sendFrom = "from"
	sendTo   = "to"
)

// SendsFromNode reads a fielded charter's `sends:`, each `from` read against at, the package's
// address. A charter with no `sends:` declares no outbox.
func SendsFromNode(node *ir.Node, at string) ([]Send, error) {
	field := ir.Uncomment(entity.Get(ir.Uncomment(node), SendsField))
	if field == nil {
		return nil, nil
	}
	if field.Type != ir.ArrayType {
		return nil, fmt.Errorf("sends: a list of `{from, to}` (`sends: [{from: out.slack, to: "+
			"slack.Signadot.out}]`), not %v", field.Type)
	}
	pkg, err := ParseAt(at)
	if err != nil {
		return nil, err
	}
	if pkg.Stamp() == "" {
		return nil, fmt.Errorf("sends: a charter with no address has nothing to send FROM — " +
			"`sends:` belongs to a package: give it `at: <address>`")
	}
	out := make([]Send, 0, len(field.Values))
	seen := map[string]bool{}
	for i, el := range field.Values {
		send, err := sendOf(ir.Uncomment(el), pkg)
		if err != nil {
			return nil, fmt.Errorf("sends[%d]: %w", i, err)
		}
		if seen[send.From] {
			return nil, fmt.Errorf("sends[%d]: %s is sent from twice — a `from` feeds one io, so "+
				"an io reading it is the only one that answers for it", i, send.From)
		}
		seen[send.From] = true
		out = append(out, send)
	}
	for i, a := range out {
		for _, b := range out {
			if a.From != b.From && strings.HasPrefix(a.From, b.From+".") {
				return nil, fmt.Errorf("sends[%d]: %s is inside %s, which is sent from too — an io "+
					"reads its outbox at every depth, so both iods would answer for what is asked "+
					"there", i, a.From, b.From)
			}
		}
	}
	return out, nil
}

// sendOf reads one entry, its `from` resolved under the package's address.
func sendOf(n *ir.Node, pkg Subtree) (Send, error) {
	if n == nil || n.Type != ir.ObjectType {
		return Send{}, fmt.Errorf("an outbox is `{from: <place in this package>, to: <io out place>}`")
	}
	var from, to string
	for i, key := range n.Fields {
		v := ir.Uncomment(n.Values[i])
		if key == nil || v == nil || v.Type != ir.StringType || v.String == "" {
			return Send{}, fmt.Errorf("an outbox's `%s` is an address", sendKeyText(key))
		}
		switch key.String {
		case sendFrom:
			from = v.String
		case sendTo:
			to = v.String
		default:
			return Send{}, fmt.Errorf("an outbox's keys are %s and %s; %q is neither",
				sendFrom, sendTo, key.String)
		}
	}
	if from == "" || to == "" {
		return Send{}, fmt.Errorf("an outbox names both where it is (`%s`) and the io it sends "+
			"to (`%s`)", sendFrom, sendTo)
	}
	rel, err := entity.ParseAddr(from)
	if err != nil {
		return Send{}, fmt.Errorf("from %q: %w", from, err)
	}
	if strings.HasPrefix(from, entity.VerseRoot+".") {
		return Send{}, fmt.Errorf("from %q: an outbox is a place inside the package, written "+
			"relative to its address (`out.slack`)", from)
	}
	dest, err := entity.ParseAddr(to)
	if err != nil {
		return Send{}, fmt.Errorf("to %q: %w", to, err)
	}
	place := entity.RefAt(append(pkg.Path(), rel.Segs()...)...)
	return Send{From: place.KPath(), To: dest.KPath()}, nil
}

// StampSends renders outboxes the way the package's record keeps them.
func StampSends(in []Send) []any {
	out := make([]any, 0, len(in))
	for _, s := range in {
		out = append(out, entity.Obj{sendFrom: s.From, sendTo: s.To})
	}
	return out
}

// SendsOf reads the outboxes a package's record keeps, already resolved.
func SendsOf(p entity.Payload) []Send {
	field := ir.Uncomment(entity.Get(p, SendsField))
	if field == nil || field.Type != ir.ArrayType {
		return nil
	}
	out := make([]Send, 0, len(field.Values))
	for _, el := range field.Values {
		from, to := entity.Str(el, sendFrom), entity.Str(el, sendTo)
		if from != "" && to != "" {
			out = append(out, Send{From: from, To: to})
		}
	}
	return out
}

func sendKeyText(k *ir.Node) string {
	if k == nil {
		return "?"
	}
	return k.String
}
