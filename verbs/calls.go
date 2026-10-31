package verbs

import (
	"fmt"
	"net/url"
	"time"

	"github.com/signadot/tony-format/go-tony/ir"
	"github.com/signadot/tony-format/go-tony/ir/kpath"

	"github.com/signadot/verse/api"
	"github.com/signadot/verse/charterfile"
	"github.com/signadot/verse/codec"
	"github.com/signadot/verse/entity"
)

// WHAT A CALL SENDS, read from its arguments once for both faces, as the command reads its
// flags: in the grammar of api and charterfile, documents in tony. A refusal names the parameter.

// AskOf is the ask an ask-create raises, as `verse ask` builds it (api.NewAsk), as of now.
func AskOf(a Args, now time.Time) (api.Ask, error) {
	spec := api.AskSpec{Name: a.Word("name"), Question: a.Word("question"), To: a.Word("to"), Due: a.Word("due"),
		OnSilence: a.Word("on_silence"), About: a.Word("about"), Keep: a.Word("keep"), Label: a.Word("label")}
	var err error
	if spec.Then, err = thenOf(a, "then"); err != nil {
		return api.Ask{}, err
	}
	if spec.Else, err = thenOf(a, "else"); err != nil {
		return api.Ask{}, err
	}
	return api.NewAsk(spec, now)
}

func thenOf(a Args, key string) (entity.Payload, error) {
	if a.Word(key) == "" {
		return nil, nil
	}
	n, err := ThenDoc([]byte(a.Word(key)), codec.Tony)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", key, err)
	}
	return n, nil
}

// ThenDoc reads what follows an answer, a then or an else, from a document in the format in: one
// key naming what runs. What it may say is POST /asks's to check.
func ThenDoc(src []byte, in codec.Format) (entity.Payload, error) {
	n, err := codec.ParseNode(src, in)
	if err != nil {
		return nil, err
	}
	if !entity.IsObj(n) {
		return nil, fmt.Errorf("one key naming what runs: {spawn: …}, {answer: …} or {ask: …}")
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

// CharterCheckOf is what a charter-check asks of POST /charter/check: the address and the
// charters (charterfile.CheckDocs).
func CharterCheckOf(a Args) (string, []*ir.Node, error) {
	segs, err := Address(a, "at")
	if err != nil {
		return "", nil, err
	}
	at := entity.JoinSegs(segs)
	if at == "" {
		return "", nil, fmt.Errorf("a check names the address the charters would be installed at")
	}
	docs, err := charterfile.CheckDocs(a.Word("charters"), codec.Tony)
	return at, docs, err
}

// CharterInstallOf is what a charter-install sends POST /charter: the query and the definitions
// (charterfile.InstallQuery), as `verse charter -a` reads its flags.
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
		if _, err := time.ParseDuration(w); err != nil {
			return nil, nil, fmt.Errorf("wait: %q is no duration (as 30s)", w)
		}
		q.Set("wait", w)
	}
	return q, defs, nil
}

// CharterRemoveOf is what a charter-remove removes: the names of its path, and where its address
// ends (-1 where verse tells), as charterfile.Query takes them.
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
