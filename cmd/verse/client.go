package main

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"github.com/signadot/tony-format/go-tony/gomap"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/scott-cotton/cli"
	"github.com/signadot/tony-format/go-tony/ir"
	"github.com/signadot/tony-format/go-tony/ir/kpath"

	"github.com/signadot/verse/api"
	"github.com/signadot/verse/codec"
	"github.com/signadot/verse/entity"
)

// ── HTTP helpers (all client commands connect to g.Addr) ─────────────────────

// urlFor builds a request URL, folding in the global --scope so every client
// command operates in the selected scope (baseline by default).
func urlFor(path string, q url.Values) string {
	if g.Scope != "" {
		if q == nil {
			q = url.Values{}
		}
		q.Set("scope", g.Scope)
	}
	// THE WIRE IS TONY, said explicitly on every request. The parameter governs how the
	// server reads a body as well as how it writes a response, so sending the OPERATOR's
	// output preference here made `-o yaml` an instruction to parse tony as YAML — and the
	// endpoints do not accept YAML. It cost a silent corruption rather than an error: the
	// body went out as tony with `formula: !quorum 3` in it, and what landed in the store was
	// `!quorum "3"`, a quorum whose threshold had become a string. `make smoke-all`'s second
	// pass sets VERSE_OUTPUT_FORMAT=yaml, which is how it was reachable at all.
	//
	// -o is not lost by this, because it was never what the parameter bought: printBody and
	// the watch renderer both decode the response by its Content-Type and re-render with
	// outFmt() on this side. So the operator's format is a rendering decision here, and the
	// exchange with the API stays in one dialect.
	if q == nil {
		q = url.Values{}
	}
	q.Set(codec.FormatParam, wireFmt().String())
	// Build the URL rather than concatenating it, so the path gets ESCAPED. Ids reach here
	// straight from the command line, so they may contain anything a shell will pass along.
	// A "#" is the one that punishes concatenation: it starts a URL fragment, which silently
	// truncates the path (`/tasks/run-x#d1/events` requests `/tasks/run-x`) and takes
	// the query string with it, since the "?" would land inside the fragment. The request
	// then succeeds against the WRONG route, with no scope and no format. url.URL.String()
	// escapes it to %23 and the server 404s, which is what a bad id should do.
	//
	// Gate ids no longer carry one — they delimit with task.GateSep, which is unreserved
	// and reaches the route as itself — so this is now a guard against a hostile id rather
	// than the everyday path. It stays because the everyday path is not the dangerous one.
	u := url.URL{Scheme: "http", Host: g.Addr, Path: path}
	if len(q) > 0 {
		u.RawQuery = q.Encode()
	}
	return u.String()
}

func httpGet(path string, q url.Values) (*http.Response, error) {
	return verse.Get(urlFor(path, q))
}

// httpPost sends a document in the wire format (see wireFmt — the input format when verse
// can render it, else tony) and asks for the output format back.
//
// Asking matters even though the client re-renders anyway: it is what lets the server send
// tony once it can, so the round trip stops being lossy — a tagged charter survives as tags
// rather than being flattened to the JSON key form on the way out and back.
func httpPost(path string, body any) (*http.Response, error) {
	return httpPostTo(path, nil, body)
}

// httpPostTo is httpPost with query parameters. They must come through here rather than being
// written into the path: urlFor ESCAPES the path, so a "?" concatenated onto it arrives as
// %3F and the route 404s — a request that looks right and reaches nothing.
func httpPostTo(path string, q url.Values, body any) (*http.Response, error) {
	f := wireFmt()
	b, err := codec.Marshal(body, f)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequest(http.MethodPost, urlFor(path, q), bytes.NewReader(b))
	if err != nil {
		return nil, err
	}
	setFormatHeaders(req, f)
	return verse.Do(req)
}

// httpPostNode posts an already-built document. It exists so a command that has parsed a
// file does not have to render it back to bytes in some intermediate format on the way out
// — a tagged charter goes to the server AS tags, rather than being flattened through JSON
// and rebuilt from the key form at the far end.
func httpPostNode(path string, node *ir.Node) (*http.Response, error) {
	return httpPostNodeTo(path, nil, node)
}

// httpPostNodeTo is httpPostNode with query parameters; see httpPostTo on why they cannot ride
// in the path.
func httpPostNodeTo(path string, q url.Values, node *ir.Node) (*http.Response, error) {
	f := wireFmt()
	b, err := codec.NodeBytes(node, f)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequest(http.MethodPost, urlFor(path, q), bytes.NewReader(b))
	if err != nil {
		return nil, err
	}
	setFormatHeaders(req, f)
	return verse.Do(req)
}

// httpPostNodesTo posts SEVERAL documents in one request, `---` separated — the multi-document
// spelling a charter file is already written in, and what codec.ReadNodes reads back.
//
// NOT AN ARRAY of them, and the difference is not cosmetic: a definition at document top level is
// the node the file wrote, where making it an element of a list re-renders it one indent in — and
// a comment beside a flow map does not survive that trip through the parser. A charter carries its
// author's reasons, so the shape that keeps them is the shape to send.
func httpPostNodesTo(path string, q url.Values, nodes []*ir.Node) (*http.Response, error) {
	f := wireFmt()
	var body bytes.Buffer
	for i, n := range nodes {
		if i > 0 {
			body.WriteString("---\n")
		}
		b, err := codec.NodeBytes(n, f)
		if err != nil {
			return nil, err
		}
		body.Write(b)
		if !bytes.HasSuffix(b, []byte("\n")) {
			body.WriteByte('\n') // a separator on the same line as a value would be part of it
		}
	}
	req, err := http.NewRequest(http.MethodPost, urlFor(path, q), bytes.NewReader(body.Bytes()))
	if err != nil {
		return nil, err
	}
	setFormatHeaders(req, f)
	return verse.Do(req)
}

// setFormatHeaders declares what is being sent and what is wanted back. The Accept lists
// tony as a fallback so a server that cannot produce the preferred format still answers
// with something this client can read, rather than failing the negotiation.
//
// It used to name JSON there. Doing that now would ask for the one rendering verse
// refuses, and earn a 406 in exactly the situation the fallback exists to survive.
func setFormatHeaders(req *http.Request, in codec.Format) {
	req.Header.Set("Content-Type", codec.MediaType(in))
	out, err := outFmt()
	if err != nil {
		out = codec.Tony
	}
	req.Header.Set("Accept", codec.MediaType(out)+", "+codec.MediaTony+";q=0.5")
}

// printBody renders a response in the format the operator asked for with -o.
//
// It goes through the IR rather than through map[string]any, which is what makes the
// server's FIELD ORDER survive: decoding to a map and re-encoding sorts keys, so the old
// path silently alphabetised every response.
//
// A 4xx/5xx is surfaced as an ERROR carrying the server's message — which is usually plain
// text (http.Error), so parsing it as a document fails and it would otherwise be swallowed
// entirely, leaving a rejected command looking like it succeeded. Callers that handle a
// specific status (404, 201, 204) check it before calling this, so they are unaffected.
// statusError is the refusal a request came back with, in the words the server used.
//
// A verse refusal is a SENTENCE — "stratum X still holds 3 rules, removing it would leave them
// installed and unchecked" — written into the response body on purpose, and reporting only
// `400 Bad Request` throws away the whole of what the server said. It was thrown away wherever
// a command handled its own status code instead of going through printBody, which is every
// command that has a 404 of its own to name: `verse c -r strata <name>` answered a bare
// 400 and left the operator with nothing to act on.
//
// The status is the FALLBACK, for a server that refused without saying anything.
func statusError(resp *http.Response, what string) error {
	body, _ := io.ReadAll(resp.Body)
	msg := strings.TrimSpace(string(body))
	if msg == "" {
		msg = resp.Status
	}
	return fmt.Errorf("%s: %s", what, msg)
}

func printBody(cc *cli.Context, resp *http.Response) error {
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= http.StatusBadRequest {
		msg := strings.TrimSpace(string(body))
		if msg == "" {
			msg = resp.Status
		}
		return errors.New(msg)
	}
	out, err := outFmt()
	if err != nil {
		return err
	}
	in := respFmt(resp)
	node, err := codec.ParseNode(body, in)
	if err != nil {
		return fmt.Errorf("decoding the response: %w", err)
	}
	// --if / --trim, where every listing already passes: which items, and how much of each.
	f, err := readFilters()
	if err != nil {
		return err
	}
	if node, err = f.apply(node); err != nil {
		return err
	}
	if in != out {
		// The response's layout (brackets, block literals) describes how the SERVER wrote
		// it, not the value. Asking for tony and getting `[ { name: x } ]` back — JSON's
		// shape in tony's syntax — is not what -o tony means.
		node = codec.StripLayout(node)
	}
	// EMPTY, after the filters, is remembered for the one caller that has something to add to
	// it: `verse charter` lists ONE engine, so an empty answer may mean "this engine has no
	// rules" while the charter is in packages. See elsewhereInPackages.
	lastPrintEmpty = node == nil || (node.Type == ir.ArrayType && len(node.Values) == 0)
	if colorize(cc.Out) {
		return codec.EncodeNodeColor(cc.Out, node, out)
	}
	if err := codec.EncodeNode(cc.Out, node, out); err != nil {
		return err
	}
	return nil
}

// lastPrintEmpty reports whether the last collection printed came back empty. Package-level for
// the reason the filter flags are: one command runs per invocation, and the print funnel is
// where the answer passes.
var lastPrintEmpty bool

// printDoc prints a `state` answer: printBody, with the filters applied to ENTITIES rather than
// to the document as one item — see filters.applyDoc.
func printDoc(cc *cli.Context, resp *http.Response, at *kpath.KPath) error {
	defer resp.Body.Close()
	body, err := okBody(resp)
	if err != nil {
		return err
	}
	out, err := outFmt()
	if err != nil {
		return err
	}
	in := respFmt(resp)
	node, err := codec.ParseNode(body, in)
	if err != nil {
		return fmt.Errorf("decoding the response: %w", err)
	}
	f, err := readFilters()
	if err != nil {
		return err
	}
	if node, err = f.applyDoc(node, at); err != nil {
		return err
	}
	if in != out {
		node = codec.StripLayout(node)
	}
	if colorize(cc.Out) {
		return codec.EncodeNodeColor(cc.Out, node, out)
	}
	return codec.EncodeNode(cc.Out, node, out)
}

// okBody reads a response body, turning a 4xx/5xx into the error it carries.
func okBody(resp *http.Response) ([]byte, error) {
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= http.StatusBadRequest {
		msg := strings.TrimSpace(string(body))
		if msg == "" {
			msg = resp.Status
		}
		return nil, errors.New(msg)
	}
	return body, nil
}

// printNode writes a value this CLI COMPUTED, under the same rules a passed-through response
// gets: the configured output format (`-o`, then VERSE_FORMAT/config), and colour when the
// terminal takes it. printBody is for a body the server wrote; this is for one we assembled —
// the docket, which is a list of motions with each subject's own summary folded in.
func printNode(cc *cli.Context, v any) error {
	node, err := gomap.ToTonyIR(v)
	if err != nil {
		return fmt.Errorf("encoding: %w", err)
	}
	return printIR(cc, node)
}

// printIR is printNode for a caller that BUILT the document rather than converting one — the
// charter tree, whose field order is part of what it says and which a map would alphabetise.
func printIR(cc *cli.Context, node *ir.Node) error {
	out, err := outFmt()
	if err != nil {
		return err
	}
	// The docket is assembled here rather than passed through, and it filters like every other
	// listing: one funnel per direction, both asking the same two questions.
	f, err := readFilters()
	if err != nil {
		return err
	}
	if node, err = f.apply(node); err != nil {
		return err
	}
	if colorize(cc.Out) {
		return codec.EncodeNodeColor(cc.Out, node, out)
	}
	return codec.EncodeNode(cc.Out, node, out)
}

// respFmt reads the format a response declares — a fact about the server, not a
// preference of ours, which is why it is read off the reply rather than assumed.
//
// An UNLABELLED body is read as tony, and that covers the old JSON-speaking server too:
// JSON is parsed by the tony parser (codec.readAs), so the one guess that used to be
// necessary here is no longer a guess about anything.
func respFmt(resp *http.Response) codec.Format {
	if f, ok := codec.FormatForMedia(resp.Header.Get("Content-Type")); ok {
		return f
	}
	return codec.Tony
}

func httpPut(path string, body any) (*http.Response, error) {
	f := wireFmt()
	b, err := codec.Marshal(body, f)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequest(http.MethodPut, urlFor(path, nil), bytes.NewReader(b))
	if err != nil {
		return nil, err
	}
	setFormatHeaders(req, f)
	return verse.Do(req)
}

func httpDelete(path string, q url.Values) (*http.Response, error) {
	req, err := http.NewRequest(http.MethodDelete, urlFor(path, q), nil)
	if err != nil {
		return nil, err
	}
	return verse.Do(req)
}

// decodeClose decodes a response into v, reading it in the format the response declared,
// and closes the body.
func decodeClose(resp *http.Response, v any) {
	defer resp.Body.Close()
	decodeBody(resp, v)
}

// decodeBody decodes a response into v without closing it, for callers already deferring
// their own Close.
func decodeBody(resp *http.Response, v any) {
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return
	}
	_ = codec.Unmarshal(data, v, respFmt(resp))
}

// httpPostURL posts to an ABSOLUTE url — used for a task's broker face (its dialURL),
// which lives on a sibling port, not the operator API at g.Addr.
func httpPostURL(u string, body any) (*http.Response, error) {
	f := wireFmt()
	b, err := codec.Marshal(body, f)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequest(http.MethodPost, u, bytes.NewReader(b))
	if err != nil {
		return nil, err
	}
	setFormatHeaders(req, f)
	return verse.Do(req)
}

// brokerClaim claims a task at its broker face as identity, returning the epoch (the
// fencing token to carry on report/inbox) and the control cursor (cCursor, where to read the
// inbox from). Same-identity re-claim is idempotent, so an executor can claim afresh.
func brokerClaim(base, id, identity string) (epoch, cCursor int64, err error) {
	resp, err := httpPostURL(base+"/tasks/"+id+"/claim", map[string]any{"identity": identity})
	if err != nil {
		return 0, 0, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		msg, _ := io.ReadAll(resp.Body)
		return 0, 0, fmt.Errorf("%s: %s", resp.Status, strings.TrimSpace(string(msg)))
	}
	var c struct {
		Epoch   int64 `tony:"field=epoch"`
		CCursor int64 `tony:"field=cCursor"`
	}
	decodeBody(resp, &c)
	return c.Epoch, c.CCursor, nil
}

// brokerInbox pulls the worker inbox at base for id — items with cursor > since, carrying
// epoch for the fence — returning the items and the highest cursor seen (to advance since).
func brokerInbox(base, id string, since, epoch int64) ([]api.InboxItem, int64, error) {
	u := base + "/tasks/" + id + "/inbox?since=" +
		strconv.FormatInt(since, 10) + "&epoch=" + strconv.FormatInt(epoch, 10)
	resp, err := verse.Get(u)
	if err != nil {
		return nil, since, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		msg, _ := io.ReadAll(resp.Body)
		return nil, since, fmt.Errorf("%s: %s", resp.Status, strings.TrimSpace(string(msg)))
	}
	var box struct {
		Items []api.InboxItem `tony:"field=items"`
	}
	decodeBody(resp, &box)
	max := since
	for _, it := range box.Items {
		if it.Cursor > max {
			max = it.Cursor
		}
	}
	return box.Items, max, nil
}

// streamFmt reads the format an SSE stream declares its FRAMES are in. Content-Type is
// spoken for by text/event-stream, so the server says it in a header of its own; a stream
// that does not say is read as tony, which also reads the JSON frames verse sent before it
// could say (valid JSON is valid tony).
func streamFmt(resp *http.Response) codec.Format {
	if f, err := codec.Parse(resp.Header.Get(codec.HeaderFormat)); err == nil &&
		resp.Header.Get(codec.HeaderFormat) != "" {
		return f
	}
	return codec.Tony
}

// ── the payload filter (--if / --if-at) ──────────────────────────────────────

// filterBy renders --if / --if-at into a read-plane query.
//
// Two commands select entities by what they SAY rather than only by where they live: `watch`
// streams the changes, `entities` lists the state. They mean exactly the same thing by the
// pair, so it is rendered in one place here and read back in one place at the far end
// (versed.queryPattern) — a filter that named different sets depending on which command you
// reached for would be worse than not having it.
//
// Each command declares the two fields itself rather than embedding a shared struct, because
// cli.StructOpts skips embedded fields (that is how it ignores *cli.Command): a shared struct
// would carry the tags and declare no flags at all. What is shared is the meaning, which is
// this function.
//
// The predicate is parsed HERE, in --ifmt, and re-rendered as tony for the wire. Parsing it
// first is what makes a typo a message about the pattern rather than a request that silently
// matches nothing; re-rendering is what lets `-i json` and `-i yaml` say the same predicate,
// since a GET has no body and so no Content-Type to declare a dialect in. Tags survive the
// round trip — `!glob "hotfix-*"` reaches the server as itself — which is the whole reason
// the canonical form is tony rather than JSON.
func filterBy(q url.Values, pattern, at string) error {
	if at != "" && pattern == "" {
		return fmt.Errorf("%w: --if-at roots --if; pass a pattern to root", cli.ErrUsage)
	}
	if pattern == "" {
		return nil
	}
	node, err := codec.ParseNode([]byte(pattern), inFmt())
	if err != nil {
		return fmt.Errorf("--if: %w", err)
	}
	b, err := codec.NodeBytesWire(node, codec.Tony)
	if err != nil {
		return fmt.Errorf("--if: %w", err)
	}
	q.Set("if", string(b))
	// Segment per parameter, as a write's field path travels: the wire takes the address as a
	// list, so a field name is never re-split at the far end.
	for _, f := range fieldPath(at) {
		q.Add("if-at", f)
	}
	return nil
}

// fieldPath splits a dot-separated --if-at into the segments the wire takes.
//
// It is `--if-at`'s alone now: an ADDRESS is a kinded path (entity.ParsePath), which quotes a
// field name carrying a dot, while this stays the flat split because `--if-at` says where to
// root a predicate inside one payload rather than naming a place in the verse. The wire takes
// the segments as a list either way, so nothing but this spelling is in the way if a dotted
// field name ever needs rooting.
func fieldPath(at string) []string {
	if at == "" {
		return nil
	}
	return strings.Split(at, ".")
}

// ── watch ────────────────────────────────────────────────────────────────────

type watchConfig struct {
	*cli.Command
	// THE OTHER TWO SPELLINGS, each behind a flag that says which it is.
	//
	// The positional is steps and only steps, because a lone word cannot be both one segment
	// and several (see address). A flag disambiguates by POSITION rather than by re-reading the
	// word, which is what lets these take the string spellings without the ambiguity coming
	// back: which grammar applies is declared by which flag was used, not guessed from what is
	// inside it.
	//
	//	--ref demo:pr:1            an address copied out of a document
	//	--at 'demo.pr.*.stage'     a kinded path — the one spelling that carries a WILDCARD,
	//	                           which steps cannot (a `*` step is the id that is an asterisk)
	Ref string `cli:"name=ref desc='the address as a ref (demo:pr:1) instead of steps'"`
	At  string `cli:"name=at desc='the address as a kinded path (demo.pr.1, demo.pr.*.stage) instead of steps'"`
	// NO DEFAULT, because the default was a question nobody asked. `default=0` sent `since=0`
	// on every watch — "replay the whole retained log" — so `verse watch <path>` on a store
	// that cannot replay from 0 was refused, and the refusal advised dropping a flag the
	// caller had never passed. An absent cursor streams from now; `--since N` is how somebody
	// asks for history, and `--since 0` is still from the beginning.
	//
	// ABSOLUTE ONLY: the relative cursor is `--ago N`. `--since -20` is not spellable at a
	// command line, where a leading dash is what an OPTION looks like — `verse watch -since -20`
	// reads as `-since` handed no value and then a flag named `-20`, and only this parser's rule
	// that an option's value is taken verbatim makes it mean twenty commits back. A spelling
	// that depends on knowing the parser is not a spelling, so a negative `--since` is refused
	// here, naming the flag that means it.
	Since string `cli:"name=since desc='replay from this REVISION first; 0 = from the beginning. absent = stream from now. for the last N commits, --ago'"`
	// --ago N IS THE RELATIVE CURSOR: the last N commits, whatever the head is. It is the cursor
	// somebody actually has — an absolute one means reading `storeRev` off status first — and
	// the STORE resolves it, against its watermark at the moment the watch is established and
	// clamped to the history it still retains. Twenty COMMITS rather than twenty events: a
	// watch narrowed to a path sees only the changes under it, so the span usually holds fewer.
	//
	// On the wire it is still `since=-N`, where a query parameter has no dash to be read as a
	// flag: the ambiguity this flag exists to answer is the command line's alone.
	Ago string `cli:"name=ago desc='replay the last N COMMITS first — the relative cursor, resolved by the store against its head and clamped to what it retains'"`
	// --if is the same predicate a charter's `when` carries in `value`, and means the same
	// thing: a tony match over the payload, so it takes operators (`{stage: !or [open, merged]}`)
	// and matches a payload that is not an object at all. A DELETE never matches one — there is
	// no payload left to ask about — so a filtered watch is a stream of states, not of removals.
	If   string `cli:"name=if desc='only changes whose payload matches this tony pattern (a delete never matches)'"`
	IfAt string `cli:"name=if-at desc='root --if at this dot-separated field path INSIDE the payload (e.g. labels)'"`
}

func watchCmd() *cli.Command {
	cfg := &watchConfig{}
	opts, err := cli.StructOpts(cfg)
	if err != nil {
		panic(err)
	}
	return cli.NewCommandAt(&cfg.Command, "watch").
		WithSynopsis("watch — stream store changes (cursored): watch [<path>] [--if <pattern>]").
		// --trim only: watch's own --if is the SERVER's selector, which narrows what crosses
		// the wire rather than what is printed of it.
		WithOpts(append(opts, trimOpt()...)...).
		WithRun(cfg.run)
}

// query renders the filters as the watch route's parameters. The cursor is watch's own; the
// rest is the selector every read-plane route takes, path included — `verse watch demo.pr` and
// `verse data demo.pr` name the same entities and differ only in when they answer.
func (cfg *watchConfig) query(at string) (url.Values, error) {
	q := url.Values{}
	since, err := cfg.cursor()
	if err != nil {
		return nil, err
	}
	if since != "" {
		q.Set("since", since)
	}
	if at != "" {
		q.Set("at", at)
	}
	if err := filterBy(q, cfg.If, cfg.IfAt); err != nil {
		return nil, err
	}
	return q, nil
}

// cursor renders --since/--ago as the one parameter the wire takes, or "" for a watch that
// starts from now. The two flags are the same cursor written two ways — a revision, or a count
// of commits back from the head — so passing both is a question with two answers.
//
// Only the SIGN of --since is read here. A cursor that is not a number at all stays the
// server's refusal to make: it is the end that knows what the store will take, and its message
// already says what an absent cursor does.
func (cfg *watchConfig) cursor() (string, error) {
	if cfg.Since != "" && cfg.Ago != "" {
		return "", fmt.Errorf("%w: --since and --ago are the same cursor, absolute and "+
			"relative; pass one", cli.ErrUsage)
	}
	if cfg.Ago != "" {
		n, err := strconv.ParseInt(cfg.Ago, 10, 64)
		if err != nil || n < 0 {
			return "", fmt.Errorf("%w: --ago %s: a count of commits back from the head, so a "+
				"whole number — the minus sign is what --ago spells for you", cli.ErrUsage, cfg.Ago)
		}
		// The last ZERO commits is from now, and it cannot be sent as `since=-0`: that reaches
		// the wire as `0`, the whole retained log, which is the opposite of no history at all.
		if n == 0 {
			return "", nil
		}
		return strconv.FormatInt(-n, 10), nil
	}
	if n, err := strconv.ParseInt(cfg.Since, 10, 64); err == nil && n < 0 {
		return "", fmt.Errorf("%w: --since %s names a revision, and revisions are not negative; "+
			"the last %d commits is `--ago %d`", cli.ErrUsage, cfg.Since, -n, -n)
	}
	return cfg.Since, nil
}

func (cfg *watchConfig) run(cc *cli.Context, args []string) error {
	args, err := cfg.Parse(cc, args)
	if err != nil {
		return err
	}
	at, err := addressWire(args, cfg.Ref, cfg.At)
	if err != nil {
		return err
	}
	if _, err := entity.ParseAt(at); err != nil {
		return fmt.Errorf("watch: %w", err)
	}
	q, err := cfg.query(at)
	if err != nil {
		return fmt.Errorf("watch: %w", err)
	}
	out, err := outFmt()
	if err != nil {
		return err
	}
	resp, err := httpGet("/watch", q)
	if err != nil {
		return fmt.Errorf("watch: %w (is `verse up` running on %s?)", err, g.Addr)
	}
	defer resp.Body.Close()
	// A rejected watch must not read as a quiet verse. The loop below keeps only "data: "
	// lines, so an error body scans to nothing and watch blocks-then-exits 0 exactly as if
	// the store had no changes to report.
	if resp.StatusCode >= http.StatusBadRequest {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<10))
		msg := strings.TrimSpace(string(body))
		if msg == "" {
			msg = resp.Status
		}
		return fmt.Errorf("watch: %s", msg)
	}
	in := streamFmt(resp)
	color := colorize(cc.Out)
	// --trim applies per FRAME, which is what a stream lists one of. (--if is watch's own and
	// is answered at the server, so a filtered stream costs nothing on the wire.)
	f, err := readFilters()
	if err != nil {
		return err
	}
	sc := bufio.NewScanner(resp.Body)
	sc.Buffer(make([]byte, 0, 64*1024), 1<<20)
	for sc.Scan() {
		data, ok := strings.CutPrefix(sc.Text(), "data: ")
		if !ok {
			continue
		}
		line, keep, err := f.frame(data, in)
		if err != nil {
			return err
		}
		if !keep {
			continue
		}
		fmt.Fprintln(cc.Out, renderFrame(line, in, out, color))
	}
	return nil
}

// renderFrame renders one streamed event the way every other command renders a document:
// in the requested format, with the server's layout stripped when it is not the one asked
// for, and coloured when the destination is a terminal.
//
// It used to short-circuit when the formats matched — passing the server's own bytes through
// untouched, on the grounds that re-rendering could only mangle them. What that actually meant
// is that `watch` was the one command where `-o` did half its job and `--color` did none:
// a stream is where somebody is READING, so it is the last place to render worse than
// everywhere else.
//
// Single-line per event stays, which is not the same question. A stream is read as it scrolls
// and grepped afterwards; the block form belongs to `data`, where the document is the
// whole answer rather than one of a thousand.
//
// A line it cannot parse is passed through as it arrived. On a live stream a surprising line
// is better seen than swallowed.
func renderFrame(s string, in, out codec.Format, color bool) string {
	n, err := codec.ParseNodeOrNil([]byte(s), in)
	if err != nil || n == nil {
		return s
	}
	if in != out {
		n = codec.StripLayout(n)
	}
	var b strings.Builder
	if color {
		err = codec.EncodeNodeWireColor(&b, n, out)
	} else {
		err = codec.EncodeNodeWire(&b, n, out)
	}
	if err != nil {
		return s
	}
	return strings.TrimRight(b.String(), "\n")
}

// ── gate ─────────────────────────────────────────────────────────────────────

func gateCmd() *cli.Command {
	return collection("gate", "gates", "/gates",
		"gate — pending human-in-the-loop decisions; answer one with approve|deny",
		gateAnswerCmd(true), gateAnswerCmd(false))
}

// The answer to a gate is a VERB, so it is a command — `gate approve <id>` — rather than
// `gate resolve <id> --approve|--deny`, a flag pair that could be left empty or set both
// ways and so needed a runtime check to police two states the grammar should never have
// allowed. Every other verb in this CLI is already a subcommand (flow pause/resume/cancel,
// task cancel/say); the gate's answer was the one that had
// been demoted into a flag, mirroring api.Resolve's `Approve bool` rather than the operator.
//
// `vote <motion> <choice>` deliberately stays a positional: a select motion's choice is an
// arbitrary option id (a provider name), an OPEN set, which is data. A gate's answer is a
// closed binary, which is grammar.
// --as is optional here, where `vote --as` is required, and the difference is real rather
// than an oversight: a ballot is meaningless without its voter (a tally of anonymous votes
// governs nothing), while an approval is an act that happens whether or not it is signed.
// Unset, the server records "human:cli" — which is what it recorded for EVERY approval
// before this flag existed, so the dial only ever adds attribution.
type gateAnswerConfig struct {
	*cli.Command
	Say string `cli:"name=say aliases=s desc='words to say to the agent on resolution'"`
	As  string `cli:"name=as desc='identity answering the gate (REQUIRED — a decision names who made it)'"`
	// The refusal an exclusive resolver produces names the binding and says to use this, so
	// the flag is the answer to a question the operator has just been asked. It is a named
	// act rather than a bypass: `exclusive` was never real against whoever holds this API,
	// and the record ends up naming both parties.
	Override bool `cli:"name=override desc='answer over an exclusive resolver; the record names you both'"`
}

func gateAnswerCmd(approve bool) *cli.Command {
	cfg := &gateAnswerConfig{}
	opts, err := cli.StructOpts(cfg)
	if err != nil {
		panic(err)
	}
	name, syn := "approve", "approve a decision (also: y, yes): approve <id> --as <who> [--say <text>]"
	aliases, done := []string{"y", "yes"}, "approved"
	if !approve {
		name, syn = "deny", "deny a decision (also: n, no): deny <id> --as <who> [--say <text>]"
		aliases, done = []string{"n", "no"}, "denied"
	}
	// The aliases are named in the SYNOPSIS because cli's usage never prints them — it
	// renders a child as (first word of synopsis, synopsis) and never reads Aliases, which
	// is why `ps` and `e` are invisible in the help today.
	return cli.NewCommandAt(&cfg.Command, name).WithAliases(aliases...).
		WithSynopsis(syn).
		WithOpts(opts...).
		WithRun(func(cc *cli.Context, args []string) error { return cfg.run(cc, args, name, done, approve) })
}

func (cfg *gateAnswerConfig) run(cc *cli.Context, args []string, verb, done string, approve bool) error {
	args, err := cfg.Parse(cc, args) // leaf: flags parse even after the <id> positional
	if err != nil {
		return err
	}
	if len(args) < 1 {
		return fmt.Errorf("%w: gate %s wants a decision id", cli.ErrUsage, verb)
	}
	if err := requireAs(cfg.As, "identity", "a decision names who made it"); err != nil {
		return err
	}
	body := api.Resolve{ID: args[0], Approve: approve, By: cfg.As, Override: cfg.Override}
	if cfg.Say != "" {
		body.Say = []string{cfg.Say}
	}
	resp, err := httpPost("/gate/resolve", body)
	if err != nil {
		return fmt.Errorf("gate %s: %w", verb, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		msg, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("gate %s: %s: %s", verb, resp.Status, strings.TrimSpace(string(msg)))
	}
	fmt.Fprintln(cc.Out, done, args[0])
	return nil
}

// ── entity ───────────────────────────────────────────────────────────────────

// conditions renders the guard flags as the wire's preconditions. --create and --update are
// opposite answers to the same question, so asking both is a contradiction rather than a
// pair of conditions that can never hold — say so from the flags, before anything is sent.
func (cfg *stateConfig) conditions() ([]api.Condition, error) {
	var out []api.Condition
	if cfg.Create && cfg.Update {
		return nil, fmt.Errorf("%w: --create and --update are opposites; pass one", cli.ErrUsage)
	}
	if cfg.Create || cfg.Update {
		exists := cfg.Update
		out = append(out, api.Condition{Exists: &exists})
	}
	if cfg.Expect != "" {
		pattern, err := codec.ParseNode([]byte(cfg.Expect), inFmt())
		if err != nil {
			return nil, fmt.Errorf("--expect: %w", err)
		}
		out = append(out, api.Condition{Pattern: pattern})
	}
	return out, nil
}

// state is the one collection with filters of its own, so it does not go through
// collection() — the shape is the same, the query is not.
type stateConfig struct {
	*cli.Command
	// THE OTHER TWO SPELLINGS, each behind a flag that says which it is — see watchConfig, and
	// address() for why the positional cannot carry either.
	Ref string `cli:"name=ref desc='the address as a ref (demo:pr:1) instead of steps'"`
	At  string `cli:"name=at desc='the address as a kinded path (demo.pr.1, demo.pr.*.stage) instead of steps'"`
	// The same pair `watch` takes, meaning the same thing (filterBy) — the read and the stream
	// are one selector asked at two times.
	//
	// `--if` is the FILTER, and it is the only one now. It used to have a namesake: `put` was
	// a subcommand with an `--if` of its own — a precondition rather than a filter — and the
	// two did not collide only because a parent stops parsing flags at its first positional.
	// With the actions as flags on this command there is no boundary left to keep them apart,
	// so the precondition is `--expect`, which also puts it in the family it belongs to:
	// --create expects nothing, --update expects something, --expect names what.
	If    string `cli:"name=if desc='only entities whose payload matches this tony pattern'"`
	IfAt  string `cli:"name=if-at desc='root --if at this dot-separated field path INSIDE the payload (e.g. labels)'"`
	Paths bool   `cli:"name=paths desc='print one address per line instead of the document'"`
	// Depth BOUNDS the walk rather than picking a level out of it, and the difference is
	// whether a listing can be silently wrong. Entity-hood is a lens rather than a stored fact
	// (docs/working/entity-tree.md), so nothing in an answer says where an entity ends — and
	// `--paths` asking for one depth answered with whatever happened to live there and omitted
	// everything deeper, which is how a place holding four-segment records listed as the place
	// itself and nothing else. The walk omits nothing; this says how far it goes.
	Depth int `cli:"name=depth desc='with --paths: bound the walk to addresses of at most N segments (default: no bound)'"`

	// WHAT TO DO IS A FLAG, and the positional is a path in every case — the same reason
	// `verse c` says it that way. `get`, `put` and `rm` were verbs, and a path's first segment
	// is a SYSTEM name, so `verse data put` could not be told from a system called `put`
	// except by a `--` the operator had to know about (client.go's address()).
	//
	// It also retires the rule that made `put` the exception among addresses: its payload was
	// the LAST positional, so "everything up to the final argument is the address" was a rule
	// this one command had. The payload is `--put`, and every positional is the address again.
	Put string `cli:"name=put desc='write this payload at the path (a tony/json value)'"`
	// `-r` as well, because removal is the SAME ACT as `verse c -r` and a reader who learns one
	// should not find the other missing. `--put` gets no letter on purpose: `-p` on a command
	// that also has `--paths` is a slip that WRITES, and the two are one keystroke apart.
	Rm bool `cli:"name=rm aliases=r desc='remove what stands at the path (an entity, or one field of one)'"`
	// The preconditions, which only a write reads. A family: --create expects nothing to be
	// there, --update expects something, --expect names what.
	Create bool   `cli:"name=create desc='with --put: only if nothing is there yet — the write loses a race rather than clobbering'"`
	Update bool   `cli:"name=update desc='with --put: only if something is already there'"`
	Expect string `cli:"name=expect desc='with --put: only if what is there matches this tony pattern'"`
}

// dataCmd is `verse data`: the verse's data at a path.
//
// It replaces `verse entity`, and the noun went with the answer. The old command listed rows of
// a table that the store does not hold — `[{ref, payload}, …]`, flattened out of the document it
// does hold — and addressed them in three vocabularies: `--system`/`--kind` to narrow a listing,
// `system:kind:id` to name one, `--at` to reach inside it. They are one kinded path asked at
// different depths, so that is what this takes: `verse data`, `verse data demo`,
// `verse data demo.pr`, `verse data demo.pr.1`, `verse data demo.pr.1.labels.urgent`,
// `verse data 'demo.pr.*.stage'`.
//
// A ref is that same path with `:` for a delimiter, which is why get/put/rm still take one and
// why nothing about identity moved (see entity/doc.go).
func dataCmd() *cli.Command {
	cfg := &stateConfig{}
	opts, err := cli.StructOpts(cfg)
	if err != nil {
		panic(err)
	}
	return cli.NewCommandAt(&cfg.Command, "data").WithAliases("d").
		WithSynopsis("data — the verse at a path: data [<path>] [--if <pattern>] [--paths]; --put|--rm one").
		// --trim only, for the reason watch takes only that: `data --if` is the server's
		// selector, and it names WHICH ENTITIES rather than what to print of them.
		WithOpts(append(opts, trimOpt()...)...).
		WithRun(cfg.run)
}

func (cfg *stateConfig) run(cc *cli.Context, args []string) error {
	args, err := cfg.Parse(cc, args)
	if err != nil {
		return err
	}
	if handled, err := dispatch(cfg.Command, cc, args); handled {
		return err
	}
	// FLAGS MAY FOLLOW THE PATH, which takes a second Parse to pick up.
	//
	// A command with subcommands stops parsing at its first positional, and has to: that is
	// what hands `data --expect <pattern>` --expect — <ref> <payload> to put's own a --put precondition
	// on the write — rather than eating it as this command's filter. dispatch above is where
	// that case leaves.
	//
	// What is left here is the read, whose positional is an ADDRESS rather than a verb, so
	// nothing after it belongs to anyone else. Parsing the tail is the whole fix, and it runs
	// up the command tree exactly as the first pass did — `verse data lineage --scope m` picks
	// up a root option as readily as one of ours.
	// EVERY positional is a STEP of the address (`data demo pr 1`). The flags are parsed off
	// first — the tail parse below is what lets them sit on either side of it — and what is
	// left is the vector.
	var addr []string
	if len(args) > 0 {
		addr, args = args, nil
		// A flag among the positionals ends the address: everything from it belongs to the
		// command, and the parse below is what claims it.
		for i, a := range addr {
			if strings.HasPrefix(a, "-") {
				addr, args = addr[:i], addr[i:]
				break
			}
		}
		if args, err = cfg.Parse(cc, args); err != nil {
			return err
		}
		if len(args) > 0 {
			return fmt.Errorf("%w: data takes an address as steps, and %q is after it",
				cli.ErrUsage, args[0])
		}
	}
	// THE SEGMENT VIEW IS THE WRITE'S, and is resolved only where a write is what this is: it
	// refuses a wildcard, which a read may legitimately carry (`--at 'demo.pr.*.stage'`).
	switch {
	case cfg.Put != "" && cfg.Rm:
		return fmt.Errorf("%w: data does one thing at a time: --put writes, --rm removes",
			cli.ErrUsage)
	case cfg.Put != "" || cfg.Rm:
		segs, err := addressOf(addr, cfg.Ref, cfg.At)
		if err != nil {
			return err
		}
		if cfg.Put != "" {
			return cfg.put(cc, segs)
		}
		return cfg.rm(cc, segs)
	}
	if cfg.Create || cfg.Update || cfg.Expect != "" {
		return fmt.Errorf("%w: --create, --update and --expect are conditions on a WRITE; "+
			"add --put <payload>", cli.ErrUsage)
	}
	// ONE JOIN, NEVER SCANNED BACK — and `--at` goes through as written, being that spelling
	// already. See addressWire.
	at, err := addressWire(addr, cfg.Ref, cfg.At)
	if err != nil {
		return err
	}
	kp, err := entity.ParseAt(at)
	if err != nil {
		return fmt.Errorf("%w: data: %s", cli.ErrUsage, err)
	}
	q := url.Values{}
	if at != "" {
		q.Set("at", at)
	}
	if err := filterBy(q, cfg.If, cfg.IfAt); err != nil {
		return fmt.Errorf("data: %w", err)
	}
	resp, err := httpGet("/state", q)
	if err != nil {
		return fmt.Errorf("data: %w", err)
	}
	if cfg.Paths {
		return printPaths(cc, resp, kp, cfg.Depth)
	}
	return printDoc(cc, resp, kp)
}

// put writes a payload at the path.
func (cfg *stateConfig) put(cc *cli.Context, segs []string) error {
	if len(segs) == 0 {
		return fmt.Errorf("%w: --put wants a path to write at (`demo pr 1`, `demo pr 1 labels "+
			"urgent`, or --ref demo:pr:1)", cli.ErrUsage)
	}
	// FROM THE SEGMENTS, not from a string: argv already is the vector, and assembling it into
	// a path only to parse the path back is the round trip that could not be right for every
	// input. See address.
	at, err := entity.PathOf(segs)
	if err != nil {
		return fmt.Errorf("%w: data --put: %s", cli.ErrUsage, err)
	}
	// A payload typed on the command line names no format of its own, so it is read as
	// --ifmt / the configured default. Valid JSON is also valid tony, so `{"stage":"open"}`
	// keeps working whichever the default is.
	//
	// Parsed to a NODE rather than a map: a payload is any tony value, so `42`, `true` and
	// `[a, b]` are payloads too, and decoding into a map would reject them at the CLI even
	// though the store and the wire both accept them.
	payload, err := codec.ParseNode([]byte(cfg.Put), inFmt())
	if err != nil {
		return fmt.Errorf("data --put: payload: %w", err)
	}
	when, err := cfg.conditions()
	if err != nil {
		return fmt.Errorf("data --put: %w", err)
	}
	// THE WHOLE ADDRESS IS THE PLACE. `verse data verse-dev drift head abc --put '{…}'` writes
	// there, and so does `verse data demo.pr.1.labels.urgent --put true` — a write is a value
	// at a path, and where the entity "ends" is a question this command does not answer. The
	// answer comes back from the address written, so a put still shows the merge wherever the
	// address IS an entity, which is the ordinary case.
	//
	// `ref` rides along for a server that predates `path`, carrying the first three segments as
	// it always did.
	resp, err := httpPost("/entity", api.PutEntity{
		Ref:     at.Ref.String(),
		Path:    append(at.Ref.Segs(), at.Field...),
		Payload: payload,
		When:    when,
	})
	if err != nil {
		return fmt.Errorf("data --put: %w", err)
	}
	return printBody(cc, resp)
}

// rm removes what stands at the path — an entity, or one field of one.
func (cfg *stateConfig) rm(cc *cli.Context, segs []string) error {
	if len(segs) == 0 {
		return fmt.Errorf("%w: --rm wants a path (`demo pr 1`, `demo pr 1 labels urgent`, or "+
			"--ref demo:pr:1)", cli.ErrUsage)
	}
	at, err := entity.PathOf(segs)
	if err != nil {
		return fmt.Errorf("%w: data --rm: %s", cli.ErrUsage, err)
	}
	q := url.Values{"ref": {at.Ref.String()}}
	for _, f := range at.Field {
		q.Add("field", f)
	}
	resp, err := httpDelete("/entity", q)
	if err != nil {
		return fmt.Errorf("data --rm: %w", err)
	}
	defer resp.Body.Close()
	// A REFUSAL IS NOT A REMOVAL, and this printed "removed" whatever came back. The one that
	// matters is the reserved half: `verse data verse task instance <id> --rm` is answered 403
	// — verse's own records are written by the verbs that own them — and the CLI said the
	// record was gone while it stood there untouched.
	if resp.StatusCode != http.StatusNoContent {
		return statusError(resp, "data --rm")
	}
	// Nothing stands there now — which is all there is to say, and is equally true of a path
	// that held nothing to begin with.
	//
	// NAMED IN STEPS, like every other address this command prints, so the line reads as the
	// one somebody typed rather than as the kpath it was sent as. A path that is not a plain
	// address — a glob, a reach into an array — is echoed as it stands, since it has no steps.
	fmt.Fprintf(cc.Out, "removed %s\n", inSteps(at))
	return nil
}

// inSteps renders a kinded path as the steps a person types, or as it stands when it is not a
// plain address. It is for the lines a command PRINTS at somebody, where one spelling
// throughout is the point; a `ref:` inside a rendered document keeps the document's spelling,
// which is read by a parser and not by a shell.
func inSteps(at entity.Path) string {
	segs := append(append([]string{}, at.Ref.Segs()...), at.Field...)
	if len(segs) == 0 {
		return at.String()
	}
	return entity.JoinSteps(segs)
}

// stateAt reads the verse at a path and hands back the entities standing there.
//
// It is what a command that wants ENTITIES uses, and the reason it can: the addresses are in
// the answer, as keys, so a caller reads them out of the document it already has rather than
// recovering them from a rendered ref and making a second request. Every consumer in this
// package used to do the latter — `strings.TrimPrefix(e.Ref, "provider:")` and a SplitN to get
// the capability back — which is exactly the map the store had thrown away being rebuilt by
// hand at each caller.
func stateAt(at string) ([]entity.Entity, error) {
	kp, err := entity.ParseAt(at)
	if err != nil {
		return nil, err
	}
	q := url.Values{}
	if at != "" {
		q.Set("at", at)
	}
	resp, err := httpGet("/state", q)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= http.StatusBadRequest {
		msg := strings.TrimSpace(string(body))
		if msg == "" {
			msg = resp.Status
		}
		return nil, errors.New(msg)
	}
	node, err := codec.ParseNode(body, respFmt(resp))
	if err != nil {
		return nil, fmt.Errorf("decoding the state: %w", err)
	}
	return (entity.Doc{Node: node}).Entities(kp), nil
}

// printPaths prints one address per line — EVERY address under what was asked for, deepest last
// within each branch, bounded by --depth when the reader wants less.
//
// It listed one level and took a `--depth` to say which, and that could not be got right: an
// address may be three segments or deeper (a write names a place at whatever depth), and nothing
// in the answer says where an entity ends, so the level was a guess the reader had to make on
// the store's behalf. Guessing low printed the place and nothing in it — `verse data
// verse-dev.drift.range --paths` answering `verse-dev.drift.range`, which is true and useless.
// A walk cannot be wrong that way: what is there is listed, and `--depth` trims it.
//
// Client-side, because the addresses are already IN the answer: an id is a key now rather than a
// token inside a rendered ref, so the server would only be sending back what the document
// already says. It is the `ls -R` of a read — what a script uses instead of grepping `ref:` out
// of a listing, which is what scripts had to do while the answer was a table.
func printPaths(cc *cli.Context, resp *http.Response, at *kpath.KPath, depth int) error {
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= http.StatusBadRequest {
		msg := strings.TrimSpace(string(body))
		if msg == "" {
			msg = resp.Status
		}
		return errors.New(msg)
	}
	node, err := codec.ParseNode(body, respFmt(resp))
	if err != nil {
		return fmt.Errorf("state --paths: decoding the response: %w", err)
	}
	// AS A KINDED PATH, one token per line, rendered the way kpath renders one:
	// `demo.pr."1"`, `demo.pr."a b.txt"`, `"a b".pr.1`.
	//
	// THE QUOTING IS ABOUT TONY TYPES, not about the path. `a.1.b` is three string fields and
	// an index is the bracket form (`a[1].b`), so a path is unambiguous with or without the
	// quotes; what they buy is the segment's TYPE, since a bare `1` written into a document is
	// a number and the id is not. That makes this the conservative rendering — correct wherever
	// a line is read, at the cost of quotes the path alone would not need.
	//
	// AS STEPS — the segments separated by spaces, each quoted only where a shell would
	// otherwise mis-split or expand it (entity.JoinSteps):
	//
	//	demo pr 1
	//	demo pr a.b                 a dot is ordinary in a step, which is the form's whole point
	//	demo pr 'a b.txt'
	//	git ref '*'
	//
	// ONE SPELLING, IN AND OUT. Steps are what this command line takes — `verse data demo pr
	// a.b`, no escaping by the person typing it, because the shell has already separated the
	// words — and a listing that answers the same way hands straight back:
	//
	//	verse data --paths demo.pr | xargs -L1 verse data
	//
	// IT WAS A KINDED PATH FOR A WHILE (`demo.pr."a b.txt"`), because the bare steps form was
	// ambiguous for a segment holding a SPACE: it printed as two words for one segment and read
	// back as a different address. Quoting the segment answers that in the grammar the line is
	// actually read in, and single quotes are what makes it work where the kpath form did not —
	// `xargs` understands them, and reads the kpath form's `\"` as an unterminated quote.
	// Checked, both idioms, including a segment holding a space, a `*`, a double quote, and the
	// empty one.
	for _, p := range (entity.Doc{Node: node}).Paths(at, depth) {
		fmt.Fprintln(cc.Out, p)
	}
	return nil
}

// address reads the address positionals a read command takes: a VECTOR OF STEPS, one segment
// per word, and nothing else.
//
// THE SHELL HAS ALREADY SAID WHERE EACH SEGMENT ENDS, so there is nothing left to interpret:
// word i is segment i, whatever is inside it. `verse data a:b:c pr 1` names the system `a:b:c`,
// `verse data demo pr a.b` names the id `a.b`, and neither needs a quote from the person typing
// it beyond what the shell already wanted.
//
// IT USED TO TAKE ONE TOKEN TOO — `demo.pr.1`, `demo:pr:1` — with a rule picking whichever of
// `:` `.` ` ` came first. That is ambiguous and cannot be made otherwise: a single word `w` has
// two legal readings, the one segment `w` and `split(w, delim)`, and choosing either makes the
// other unsayable. Quoting does not rescue it — quoting is putting "which reading I meant" back
// INSIDE the token, which is the escape grammar steps exist to remove. It showed up as a wrong
// answer rather than an error: `a:b:c pr 1` assembled to `a:b:c.pr."1"`, which read back as the
// two-segment ref `a`:`b` with a mangled id.
//
// So a ref is taken where it is MARKED — `verse data --ref demo:pr:1` — which is unambiguous for
// the reason the positional is not: it is a different place on the line, not a second reading of
// the same word. That is the spelling for a ref copied out of a document, which is the one
// workflow the positional form was carrying.
func address(args []string) ([]string, error) {
	// A LEADING `--` IS NOT A STEP. It is how a shell says "the options are over", and every
	// command that takes an address accepts it rather than answering about an address whose
	// first segment is two dashes.
	if len(args) > 0 && args[0] == "--" {
		args = args[1:]
	}
	for _, a := range args {
		if a == "" {
			return nil, fmt.Errorf("%w: an address has no empty step — %q", cli.ErrUsage,
				strings.Join(args, " "))
		}
	}
	return args, nil
}

// addressWire is the address as the WIRE takes it — a kinded path — from whichever of the three
// spellings was used, and exactly one may be.
//
// Steps are joined here and never scanned back: JoinSegs quotes each segment as kpath needs, and
// the server parses with ParseAt, so the round trip holds for every segment. `--at` is already
// that spelling and goes through verbatim, which is what lets it carry a WILDCARD — the one
// thing steps cannot say, since a `*` step is the id that is an asterisk.
func addressWire(args []string, ref, at string) (string, error) {
	if at != "" {
		// Declared as a kinded path, so it goes through as one — WITHOUT the segment view
		// below, which refuses a wildcard because a write may not carry one. A read may.
		if err := onlyOne(args, ref, at); err != nil {
			return "", err
		}
		if _, err := entity.ParseAt(at); err != nil {
			return "", fmt.Errorf("%w: --at: %s", cli.ErrUsage, err)
		}
		return at, nil
	}
	segs, err := addressOf(args, ref, at)
	if err != nil {
		return "", err
	}
	return entity.JoinSegs(segs), nil
}

// onlyOne refuses an address given in more than one spelling.
func onlyOne(args []string, ref, at string) error {
	given := 0
	for _, on := range []bool{len(args) > 0, ref != "", at != ""} {
		if on {
			given++
		}
	}
	if given > 1 {
		return fmt.Errorf("%w: an address is given once — as steps, as --ref, or as --at",
			cli.ErrUsage)
	}
	return nil
}

// addressOf resolves the address to its SEGMENTS, for the callers that need them (a write splits
// the entity from the field path positionally). Exactly one spelling may be given.
func addressOf(args []string, ref, at string) ([]string, error) {
	segs, err := address(args)
	if err != nil {
		return nil, err
	}
	if err := onlyOne(segs, ref, at); err != nil {
		return nil, err
	}
	switch {
	case ref != "":
		r, err := entity.ParseRef(ref)
		if err != nil {
			return nil, fmt.Errorf("%w: --ref: %s", cli.ErrUsage, err)
		}
		return r.Segs(), nil
	case at != "":
		// PathSegs is the kpath reader: quote-aware, and it refuses a wildcard, which is right
		// here — this is the segment view, and the callers that want it are the WRITES.
		out, err := entity.PathSegs(at)
		if err != nil {
			return nil, fmt.Errorf("%w: --at: %s", cli.ErrUsage, err)
		}
		return out, nil
	}
	return segs, nil
}
