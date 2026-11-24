package verbs

import (
	"fmt"
	"net/url"
	"time"

	"github.com/signadot/tony-format/go-tony/ir"
	"github.com/signadot/tony-format/go-tony/ir/kpath"

	"go.viff.dev/viff/pkg/api"
	"go.viff.dev/viff/pkg/charterfile"
	"go.viff.dev/viff/pkg/codec"
	"go.viff.dev/viff/pkg/entity"
)

// WHAT A CALL SENDS, read from its arguments once for both faces, as the command reads its
// flags: in the grammar of api and charterfile, documents in tony. A refusal names the parameter.

// AskOf is the ask an ask-create raises, as `viff ask` builds it (api.NewAsk), as of now.
func AskOf(a Args, now time.Time) (api.Ask, error) {
	spec := api.AskSpec{Name: a.Word("name"), Question: a.Word("question"), To: a.Word("to"), Due: a.Word("due"),
		OnSilence: a.Word("on_silence"), About: a.Word("about"), Keep: a.Word("keep"), Label: a.Word("label")}
	if a.Word("on") != "" {
		n, err := OnDoc([]byte(a.Word("on")), codec.Tony)
		if err != nil {
			return api.Ask{}, fmt.Errorf("on: %w", err)
		}
		spec.On = n
	}
	return api.NewAsk(spec, now)
}

// OnDoc reads what follows an answer, an ask's `on`, from a document in the format in: a list of
// rows, each {match, then}. What a row may say is POST /asks's to check.
func OnDoc(src []byte, in codec.Format) (entity.Payload, error) {
	n, err := codec.ParseNode(src, in)
	if err != nil {
		return nil, err
	}
	if n = ir.Uncomment(n); n == nil || n.Type != ir.ArrayType {
		return nil, fmt.Errorf("a list of rows, each {match, then}: the first whose match the answer meets runs its then")
	}
	return n, nil
}

// Match is the match document a call gives as name (if, trim), or nil for none.
func Match(a Args, name string) (*ir.Node, error) {
	if a.Word(name) == "" {
		return nil, nil
	}
	n, err := codec.ParseNode([]byte(a.Word(name)), codec.Tony)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", name, err)
	}
	return n, nil
}

// Address is the kinded path a call gives as name, in segments; none for none.
func Address(a Args, name string) ([]string, error) {
	segs, err := entity.PathSegs(a.Word(name))
	if err != nil {
		return nil, fmt.Errorf("%s: %w", name, err)
	}
	return segs, nil
}

// DataRead is a data-get: the read GET /state takes (api.StateQuery), the address its answer is
// walked by, and how it is answered.
type DataRead struct {
	Query url.Values
	At    *kpath.KPath
	// Paths answers the addresses beneath At, at most Depth levels (entity.Unbounded without
	// one); otherwise the document, trimmed by Trim where there is one.
	Paths bool
	Depth int
	Trim  *ir.Node
}

// DataGetOf is the read a data-get makes.
func DataGetOf(a Args) (DataRead, error) {
	r := DataRead{Paths: a.Flag("paths"), Depth: entity.Unbounded}
	if d, ok := a.Num("depth"); ok {
		r.Depth = d
	}
	var err error
	if r.Query, r.At, err = api.StateQuery(a.Word("at"), a.Word("if"), a.Word("if_at"), r.Paths, r.Depth, codec.Tony); err != nil {
		return r, err
	}
	r.Trim, err = Match(a, "trim")
	return r, err
}

// DataPutOf is the write a data-put makes (api.DataWrite).
func DataPutOf(a Args) (api.PutEntity, error) {
	segs, err := Address(a, "at")
	if err != nil {
		return api.PutEntity{}, err
	}
	return api.DataWrite{Payload: a.Word("value"), Create: a.Flag("create"), Update: a.Flag("update"),
		Expect: a.Word("expect"), Grants: a.Word("grants")}.Body(segs, codec.Tony)
}

// CharterRead is a charter-get: the read GET /charter takes (charterfile.Query), the full path it
// reads, and how it is answered: with Paths one kinded path per rule and level, otherwise kept
// and trimmed (api.Filter).
type CharterRead struct {
	Query      url.Values
	Full       string
	Paths      bool
	Keep, Trim *ir.Node
}

// CharterGetOf is the read a charter-get makes.
func CharterGetOf(a Args) (CharterRead, error) {
	names, split, err := charterPath(a)
	if err != nil {
		return CharterRead{}, err
	}
	depth := -1
	if d, ok := a.Num("depth"); ok {
		depth = d
	}
	r := CharterRead{Paths: a.Flag("paths")}
	if r.Keep, err = Match(a, "if"); err != nil {
		return r, err
	}
	if r.Trim, err = Match(a, "trim"); err != nil {
		return r, err
	}
	r.Query, r.Full = charterfile.Query(names, split, depth)
	return r, nil
}

// CharterCheckOf is what a charter-check asks of POST /charter/check: the query, the address and
// whether the runners are to come (charterfile.CheckQuery), and the charters
// (charterfile.CheckDocs).
func CharterCheckOf(a Args) (url.Values, []*ir.Node, error) {
	segs, err := Address(a, "at")
	if err != nil {
		return nil, nil, err
	}
	at := entity.JoinSegs(segs)
	if at == "" {
		return nil, nil, fmt.Errorf("a check names the address the charters would be installed at")
	}
	docs, err := charterfile.CheckDocs(a.Word("charters"), codec.Tony)
	return charterfile.CheckQuery(at, a.Flag("runners-to-come")), docs, err
}

// CharterInstallOf is what a charter-install sends POST /charter: the query and the definitions
// (charterfile.InstallQuery), as `viff charter -a` reads its flags.
func CharterInstallOf(a Args) (url.Values, []*ir.Node, error) {
	segs, err := Address(a, "at")
	if err != nil {
		return nil, nil, err
	}
	docs, names, err := charterfile.Text(a.Word("charters"), codec.Tony)
	if err != nil {
		return nil, nil, err
	}
	q, defs, err := charterfile.InstallQuery(docs, names, entity.JoinSegs(segs), a.Flag("prune"))
	if err != nil {
		return nil, nil, err
	}
	if w := a.Word("wait"); w != "" {
		q.Set("wait", w) // read by the server, the one reader of it (internal/viffd/charter.go)
	}
	return q, defs, nil
}

// CharterRemoveOf is what a charter-remove removes: the names of its path, and where its address
// ends (-1 where viff tells), as charterfile.Query takes them.
func CharterRemoveOf(a Args) ([]string, int, error) {
	return charterPath(a)
}

// charterPath is a charter path a call gives as at and path: its names, and where the address
// ends.
func charterPath(a Args) ([]string, int, error) {
	addr, err := Address(a, "at")
	if err != nil {
		return nil, 0, err
	}
	reach, err := Address(a, "path")
	if err != nil {
		return nil, 0, err
	}
	split := -1
	if a.Word("at") != "" {
		split = len(addr)
	}
	return append(addr, reach...), split, nil
}
