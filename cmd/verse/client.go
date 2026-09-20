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
// It is `--if-at`'s alone now: an ADDRESS is a kinded path (entity.TargetOf), which quotes a
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
	// THE OTHER SPELLING, behind a flag that says which it is.
	//
	// The positional is steps and only steps, because a lone word cannot be both one segment
	// and several (see address). A flag disambiguates by POSITION rather than by re-reading the
	// word: which grammar applies is declared by which flag was used, not guessed from what is
	// inside it.
	//
	//	--at demo.pr.1             a kinded path copied out of a document, as one token
	At string `cli:"name=at desc='the address as a kinded path (demo.pr.1, demo.pr.*.stage) instead of steps'"`
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
	// --until is where the stream STOPS, which is the client's question: the server does not
	// know what the watcher was waiting for, and a cursor cannot say "when this becomes true".
	// It matches the FRAME rather than the payload — see the type, which is where the
	// coordinate system and the refusal that guards it are explained.
	Until string `cli:"name=until desc='stop after the first FRAME matching this tony pattern — a frame is {at op to rev gap}, so a payload match is {to: {…}}. It is printed before the stream ends'"`
	// --nowire trades the line for the shape: see renderFrame, which is where the default goes
	// the other way and says why.
	NoWire bool `cli:"name=nowire desc='print each frame as a --- separated BLOCK rather than on one line (a wide frame stops being a wide line; grep stops seeing a whole event)'"`
	// A watch at a path nothing stands at is REFUSED, the way a read of it is, because the
	// address came from a person and a typo held open forever looks exactly like a quiet verse.
	// Waiting is a real thing to want — watching for what a task is about to write — so it is
	// asked for rather than assumed, and it is the substrate's own option under its own name.
	WaitIfAbsent bool `cli:"name=wait-if-absent desc='watch a path that holds nothing yet, and report when a value arrives (default: refuse it, as a read of the same path does)'"`
}

func watchCmd() *cli.Command {
	cfg := &watchConfig{}
	opts, err := cli.StructOpts(cfg)
	if err != nil {
		panic(err)
	}
	return cli.NewCommandAt(&cfg.Command, "watch").
		WithSynopsis("watch — stream store changes (cursored): watch [<step>…] [--if <pattern>]").
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
	at, err := addressWire(args, cfg.At)
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
	if cfg.WaitIfAbsent {
		q.Set("wait-if-absent", "true")
	}
	u, err := readUntil(cfg.Until)
	if err != nil {
		return err
	}
	return streamChanges(cc, "watch", q, stream{until: u, nowire: cfg.NoWire})
}

// stream is what a caller asks of a change stream beyond its selector: where it ends, and what
// shape its frames are printed in. Both are the CLIENT's questions — the server neither knows
// when this watcher has seen enough nor what its terminal is like — which is why neither of
// them appears in the query.
type stream struct {
	until  until
	nowire bool
}

// streamChanges prints the store's change stream under one selector — one frame per line, or
// as --- separated blocks when the caller's stream asks for --nowire — until the server closes it.
//
// It is shared rather than `watch`'s own, because watching is not `watch`'s alone: a collection
// that lives at a known address offers the same stream under its own name (`verse motion
// --watch`), and a second copy of this loop is a second place a frame can come out rendered
// differently from everywhere else. `what` is the command asking, which is what the refusals
// name — the operator typed one command and should be told about that one.
func streamChanges(cc *cli.Context, what string, q url.Values, s stream) error {
	out, err := outFmt()
	if err != nil {
		return err
	}
	resp, err := httpGet("/watch", q)
	if err != nil {
		return fmt.Errorf("%s: %w (is `verse up` running on %s?)", what, err, g.Addr)
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
		return fmt.Errorf("%s: %s", what, msg)
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
		if keep {
			fmt.Fprintln(cc.Out, renderFrame(line, in, out, color, !s.nowire))
		}
		// AFTER the print, which is what makes the matching frame the last line of output
		// rather than the one the operator never sees. A stream that ends because the server
		// closed it ends here too, having printed everything and matched nothing — both are
		// exit 0, and the output says which happened.
		done, err := s.until.reached(data, in)
		if err != nil {
			return err
		}
		if done {
			return nil
		}
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
// Single-line per event is the DEFAULT, which is not the same question. A stream is read as it
// scrolls and grepped afterwards, and one frame per line is what lets `grep` see a whole event
// rather than the two lines of it that happened to hold the word; the block form belongs to
// `data`, where the document is the whole answer rather than one of a thousand.
//
// `wire` is false for the stream somebody is READING rather than filtering, where a frame
// wider than the terminal is worse than a frame six lines tall. It is the caller's to decide
// because the tradeoff is the operator's: what is bought is the shape, and what is spent is
// the line that made the stream greppable.
//
// A line it cannot parse is passed through as it arrived. On a live stream a surprising line
// is better seen than swallowed.
func renderFrame(s string, in, out codec.Format, color, wire bool) string {
	n, err := codec.ParseNodeOrNil([]byte(s), in)
	if err != nil || n == nil {
		return s
	}
	if in != out {
		n = codec.StripLayout(n)
	}
	// THE BLOCK FORM IS INDENTATION, so the wire's brackets have to go with the wire. Left on,
	// they are replayed six lines tall — the wire's punctuation with none of its compactness —
	// which is a frame written twice rather than a frame written differently.
	//
	// And once they are gone nothing separates one frame from the next: `rev: 2` above
	// `at: demo.pr.2` reads as a single document with a repeated key. So each frame opens with
	// `---`, which is what tony already spells a document boundary with, and the stream parses
	// back as the documents it is (codec.ParseNodes). BEFORE each frame rather than between
	// them, because a stream has no last frame to place a separator against — a watch cut by
	// --until or by ^C is still a stream of whole documents.
	if !wire {
		n = codec.StripBrackets(n)
	}
	var b strings.Builder
	switch {
	case color && wire:
		err = codec.EncodeNodeWireColor(&b, n, out)
	case color:
		err = codec.EncodeNodeColor(&b, n, out)
	case wire:
		err = codec.EncodeNodeWire(&b, n, out)
	default:
		err = codec.EncodeNode(&b, n, out)
	}
	if err != nil {
		return s
	}
	text := strings.TrimRight(b.String(), "\n")
	if !wire {
		return "---\n" + text
	}
	return text
}

// ── gate ─────────────────────────────────────────────────────────────────────

// gateConfig is the docket command: bare lists it, `<id>` shows one record, and `<id>
// --answer <option> --as <who>` answers it by option. approve and deny remain as subcommands
// for the two options every question has unless it names its own.
type gateConfig struct {
	*cli.Command
	Answer   string `cli:"name=answer desc='the option to choose, by id (proceed|deny on a yes-or-no question)'"`
	As       string `cli:"name=as desc='identity answering; the configured user when left off (a decision names who made it)'"`
	Say      string `cli:"name=say aliases=s desc='words to say to the agent on resolution'"`
	Override bool   `cli:"name=override desc='answer over an exclusive resolver; the record names you both'"`
}

func gateCmd() *cli.Command {
	cfg := &gateConfig{}
	opts, err := cli.StructOpts(cfg)
	if err != nil {
		panic(err)
	}
	return cli.NewCommandAt(&cfg.Command, "gate").
		WithAliases("gates").
		WithSynopsis("gate — the docket: bare lists it, <id> shows one, <id> --answer <option> --as <who> answers it (approve|deny are the two default options)").
		WithOpts(append(opts, filterOpts()...)...).
		WithSubs(gateAnswerCmd(true), gateAnswerCmd(false), gateEscalateCmd()).
		WithRun(func(cc *cli.Context, args []string) error {
			args, err := cfg.Parse(cc, args)
			if err != nil {
				return err
			}
			if handled, err := dispatch(cfg.Command, cc, args); handled {
				return err
			}
			if len(args) == 0 {
				if cfg.Answer != "" {
					return fmt.Errorf("%w: --answer answers one question, so it wants an id: "+
						"`verse gate <id> --answer %s --as <who>`", cli.ErrUsage, cfg.Answer)
				}
				resp, err := httpGet("/gates", nil)
				if err != nil {
					return fmt.Errorf("gate: %w", err)
				}
				return printBody(cc, resp)
			}
			// A command with subcommands stops parsing flags at its first positional, so a
			// verb can be dispatched — which leaves `--answer canary --as lead` unread after
			// the id. The id is the one positional; what follows it is flags, parsed here.
			rest, err := cfg.Parse(cc, args[1:])
			if err != nil {
				return err
			}
			if len(rest) > 0 {
				return fmt.Errorf("%w: gate takes one id — and this takes ONE token, so %q is "+
					"after it", cli.ErrUsage, rest[0])
			}
			id := args[0]
			switch {
			case id == "list":
				// `list` is THE mistake a bare-listing collection invites, and an id it is not:
				// refused by name, before anything is dialled, as every collection refuses it.
				return noArgs(cfg.Command, "gate", args)
			case cfg.Answer == "":
				// One record, as it stands: the options, the ballots, the deadline, the
				// outcome — what the listing compacts.
				resp, err := httpGet("/entity", url.Values{"at": {questionPath(id)}})
				if err != nil {
					return fmt.Errorf("gate: %w", err)
				}
				return printBody(cc, resp)
			default:
				as, err := requireAs(cfg.As, "identity", "a decision names who made it")
				if err != nil {
					return err
				}
				return answerGate(cc, id, cfg.Answer, as, cfg.Say, cfg.Override, "answered")
			}
		})
}

// questionPath is where a question's record stands in the store.
func questionPath(id string) string {
	return entity.VerseRef("gate", "question", id).KPath()
}

// answerGate posts one answer and says what became of it: settled, or recorded on a question
// that takes more than one voice, with where the tally stands.
func answerGate(cc *cli.Context, id, choice, as, say string, override bool, done string) error {
	body := api.Resolve{ID: id, Choice: choice, By: as, Override: override}
	if say != "" {
		body.Say = []string{say}
	}
	resp, err := httpPost("/gate/resolve", body)
	if err != nil {
		return fmt.Errorf("gate %s: %w", choice, err)
	}
	defer resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusNoContent:
		fmt.Fprintln(cc.Out, done, id)
	case http.StatusAccepted:
		// Recorded and not yet settled: the question takes more than one voice, and the
		// server says where the tally stands. Not an error, and not settled either.
		msg, _ := io.ReadAll(resp.Body)
		fmt.Fprintln(cc.Out, id+":", strings.TrimSpace(string(msg)))
	default:
		msg, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("gate %s: %s: %s", choice, resp.Status, strings.TrimSpace(string(msg)))
	}
	return nil
}

// The answer to a gate is a VERB, so it is a command — `gate approve <id>` — rather than
// `gate resolve <id> --approve|--deny`, a flag pair that could be left empty or set both
// ways and so needed a runtime check to police two states the grammar should never have
// allowed. Every other verb in this CLI is already a subcommand (flow suspend/resume/say/cancel,
// task cancel/say); the gate's answer was the one that had
// been demoted into a flag, mirroring api.Resolve's `Approve bool` rather than the operator.
//
// `motion <id> --vote <choice>` draws that line in the other direction, and for the same
// reason: a select motion's choice is an arbitrary option id (a provider name), an OPEN set,
// which is data — and data is a flag's value. A gate's answer is a closed binary, which is
// grammar.
// --as may be left off when the config names a user, who is then the answerer (requireAs);
// with neither, the answer is refused, since a decision names who made it.
type gateAnswerConfig struct {
	*cli.Command
	Say string `cli:"name=say aliases=s desc='words to say to the agent on resolution'"`
	As  string `cli:"name=as desc='identity answering the gate; the configured user when left off (a decision names who made it)'"`
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
	as, err := requireAs(cfg.As, "identity", "a decision names who made it")
	if err != nil {
		return err
	}
	return answerGate(cc, args[0], choiceOf(approve), as, cfg.Say, cfg.Override, done)
}

type gateEscalateConfig struct {
	*cli.Command
	As  string `cli:"name=as desc='who is handing the question on; the configured user when left off'"`
	Say string `cli:"name=say desc='a word about why, on the record'"`
}

// gateEscalateCmd is `gate escalate <id>`: hand a question on to the next rung covering its
// subject — a person's form of what a service does through its instance.
func gateEscalateCmd() *cli.Command {
	cfg := &gateEscalateConfig{}
	opts, err := cli.StructOpts(cfg)
	if err != nil {
		panic(err)
	}
	return cli.NewCommandAt(&cfg.Command, "escalate").
		WithSynopsis("escalate a question to the next rung covering its subject: escalate <id> [--as <who>] [--say <why>]").
		WithOpts(opts...).
		WithRun(func(cc *cli.Context, args []string) error {
			args, err := cfg.Parse(cc, args)
			if err != nil {
				return err
			}
			if len(args) < 1 {
				return fmt.Errorf("%w: gate escalate wants a question id", cli.ErrUsage)
			}
			as, err := requireAs(cfg.As, "identity", "an escalation names who handed the question on")
			if err != nil {
				return err
			}
			resp, err := httpPost("/gate/escalate", api.Escalate{ID: args[0], By: as, Say: cfg.Say})
			if err != nil {
				return fmt.Errorf("gate escalate: %w", err)
			}
			defer resp.Body.Close()
			if resp.StatusCode != http.StatusNoContent {
				return statusError(resp, "gate escalate")
			}
			fmt.Fprintf(cc.Out, "escalated %s\n", args[0])
			return nil
		})
}

// choiceOf spells a yes-or-no answer as the option it chooses. approve and deny are the two
// options every question has unless it names its own, and these commands answer only those.
func choiceOf(approve bool) string {
	if approve {
		return "proceed"
	}
	return "deny"
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
	// THE OTHER SPELLING, behind a flag that says which it is — see watchConfig, and address()
	// for why the positional cannot carry it.
	At string `cli:"name=at desc='the address as a kinded path (demo.pr.1, demo.pr.*.stage) instead of steps'"`
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
	Paths bool   `cli:"name=paths desc='print one address per line instead of the document, as steps'"`
	// KPaths is the same listing in the KINDED PATH spelling: one TOKEN per line, quoted where
	// the grammar needs it. Steps are what a person types and what `xargs -L1` hands back; a
	// kinded path is what survives being captured into a shell variable and passed as ONE
	// argument, which steps cannot — `ref=$(… | head -1); verse data --at "$ref"`.
	KPaths bool `cli:"name=kpaths desc='…the same listing as one kinded path per line (for --at)'"`
	// Depth BOUNDS the walk rather than picking a level out of it, and the difference is
	// whether a listing can be silently wrong. Entity-hood is a lens rather than a stored fact
	//, so nothing in an answer says where an entity ends — and
	// `--paths` asking for one depth answered with whatever happened to live there and omitted
	// everything deeper, which is how a place holding four-segment records listed as the place
	// itself and nothing else. The walk omits nothing; this says how far it goes.
	Depth int `cli:"name=depth desc='with --paths/--kpaths: bound the walk to addresses of at most N segments (default: no bound)'"`

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
		WithSynopsis("data — the verse at an address: data [<step>…] [--if <pattern>] [--paths]; --put|--rm one").
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
		segs, err := addressOf(addr, cfg.At)
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
	at, err := addressWire(addr, cfg.At)
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
	// EITHER FLAG ASKS FOR THE LISTING; --kpaths only says which spelling, so it need not be
	// passed beside --paths.
	listing := cfg.Paths || cfg.KPaths
	if listing {
		// ASKED OF THE SERVER, which asks the store: the addresses come back without the
		// nodes, so a listing of a wide container ships names rather than everything they
		// stand for. A read with a predicate still answers the document and is walked here,
		// since `--if` picks per node the path reached.
		q.Set("paths", "1")
		if cfg.Depth > 0 {
			q.Set("depth", strconv.Itoa(cfg.Depth))
		}
	}
	resp, err := httpGet("/state", q)
	if err != nil {
		return fmt.Errorf("data: %w", err)
	}
	if listing {
		return printPaths(cc, resp, kp, cfg.Depth, cfg.KPaths)
	}
	return printDoc(cc, resp, kp)
}

// put writes a payload at the path.
func (cfg *stateConfig) put(cc *cli.Context, segs []string) error {
	if len(segs) == 0 {
		return fmt.Errorf("%w: --put wants a path to write at (`demo pr 1`, `demo pr 1 labels "+
			"urgent`, or --at demo.pr.1)", cli.ErrUsage)
	}
	// FROM THE SEGMENTS, not from a string: argv already is the vector, and assembling it into
	// a path only to parse the path back is the round trip that could not be right for every
	// input. See address. The access is set when a step reaches into an ARRAY, which an address
	// of names cannot spell — see entity.Path.Access.
	at, err := entity.TargetOf(segs)
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
	resp, err := httpPost("/entity", api.PutEntity{
		At:      at.Ref.KPath(),
		Sub:     at.Access,
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
			"--at demo.pr.1)", cli.ErrUsage)
	}
	at, err := entity.TargetOf(segs)
	if err != nil {
		return fmt.Errorf("%w: data --rm: %s", cli.ErrUsage, err)
	}
	q := url.Values{"at": {at.Ref.KPath()}}
	if at.Access != "" {
		q.Set("sub", at.Access)
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
// throughout is the point; an `at:` inside a rendered document keeps the document's spelling,
// which is read by a parser and not by a shell.
func inSteps(at entity.Path) string {
	segs := at.Ref.Segs()
	if len(segs) == 0 {
		return at.String()
	}
	// An access takes no space before it, as it takes no dot in a kpath: `demo pr 1 votes[0]`
	// is four steps, the last of them reaching into an array.
	return entity.JoinSteps(segs) + at.Access
}

// stateAt reads the verse at a kinded path and hands back each node it reached, at its address:
// `provider.*.*` is every provider, one node each.
//
// The addresses are in the answer, as keys, so a caller reads them out of the document it already
// has rather than recovering them from a rendered string and making a second request.
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
	// A LISTING'S EMPTY, not a reader's not-found. This reads a WELL-KNOWN address on behalf of
	// a command that named it — `verse motion`, `verse connect` — where "nothing stands there"
	// is what an empty list means and the caller says so in its own words ("no motions"). The
	// address a PERSON typed is `verse data`'s, and that one reports the 404, which is the whole
	// point of the route answering one (git-issue 7e5gv3n9h12krd10jhn0).
	if resp.StatusCode == http.StatusNotFound {
		return nil, nil
	}
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
	return (entity.Doc{Node: node}).Reached(kp), nil
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
// token inside a rendered address, so the server would only be sending back what the document
// already says. It is the `ls -R` of a read — what a script uses instead of grepping `at:` out
// of a listing, which is what scripts had to do while the answer was a table.
func printPaths(cc *cli.Context, resp *http.Response, at *kpath.KPath, depth int, kpaths bool) error {
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
	for _, addr := range answered(node, at, depth) {
		fmt.Fprintln(cc.Out, renderAddr(addr, kpaths))
	}
	return nil
}

// answered is the addresses a listing prints, from either answer the route gives.
//
// A LISTING, where the store answered one: the addresses arrive as kinded paths, and an address
// is recovered by splitting each where it stops naming (LiteralPrefix) -- the names are the ref,
// the rest is the access, which is the same cut TargetOf makes on the way in.
//
// A DOCUMENT, where the read had to answer one: a path with a wildcard or a descent, or a `--if`
// that picks per node, is answered over the node the read reached, and the walk over it is the
// listing (entity.Doc.Paths). It is the same set of addresses either way -- which is what
// TestPathsAtAnswersWhatTheDocumentWalkDoes says of the two walks.
func answered(node *ir.Node, at *kpath.KPath, depth int) []entity.Path {
	var listed entity.Paths
	if err := codec.FromNode(node, &listed); err == nil && listed.Paths != nil {
		out := make([]entity.Path, 0, len(listed.Paths))
		for _, kp := range listed.Paths {
			out = append(out, pathOfKPath(kp))
		}
		return out
	}
	return (entity.Doc{Node: node}).Paths(at, depth)
}

// pathOfKPath reads one listed address: the names it spells, and the access below them.
func pathOfKPath(kp string) entity.Path {
	parsed, err := entity.ParseAt(kp)
	if err != nil {
		return entity.Path{Ref: entity.RefAt(kp)} // unreadable: echoed as it stands
	}
	names, rest := entity.LiteralPrefix(parsed)
	access := ""
	if rest != nil {
		access = rest.String()
	}
	return entity.Path{Ref: entity.RefAt(names...), Access: access}
}

// renderAddr is one address in the spelling a listing was asked for: STEPS by default — the
// words this command line takes — or a KINDED PATH under `--kpaths`, which is the one token
// `--at` reads back. The walk answers addresses so this is the only place either is chosen.
//
// An address that reaches into an array carries its access (entity.Path), and both spellings
// take one: `demo pr 1 votes '[0]'` in steps, `demo.pr."1".votes[0]` as a kinded path.
func renderAddr(at entity.Path, kpaths bool) string {
	if kpaths {
		return at.KPath()
	}
	return entity.JoinStepsAt(at)
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
// So a whole address in ONE token is taken where it is MARKED — `verse data --at demo.pr.1` —
// which is unambiguous for the reason the positional is not: it is a different place on the line,
// not a second reading of the same word. That is the spelling for an address copied out of a
// document.
//
// A WHOLE `*` WORD IS THE SEGMENT WILDCARD (entity.StepIsWild), which is still one word naming
// one step: `verse data git-issue verse issue '*'` is every issue. The id that is an asterisk is
// `'"*"'`, the quoted clause, and that is how a listing prints it.
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

// addressWire is the address as the WIRE takes it — a kinded path — from whichever of the two
// spellings was used, and exactly one may be.
//
// Steps are joined here and never scanned back: KPathOf quotes each segment as kpath needs and
// applies the step clauses (a name, a quoted name, an array access, the wildcard), and the
// server parses with ParseAt, so the round trip holds for every segment. `--at` is already that
// spelling and goes through verbatim.
func addressWire(args []string, at string) (string, error) {
	if at != "" {
		// Declared as a kinded path, so it goes through as one — WITHOUT the segment view
		// below, which refuses a wildcard because a write may not carry one. A read may.
		if err := onlyOne(args, at); err != nil {
			return "", err
		}
		if _, err := entity.ParseAt(at); err != nil {
			return "", fmt.Errorf("%w: --at: %s", cli.ErrUsage, err)
		}
		return at, nil
	}
	segs, err := addressOf(args, at)
	if err != nil {
		return "", err
	}
	// KPathOf, not JoinSegs: the steps carry the clauses (a name, a quoted name, an array
	// access, the wildcard), and a READ reaches an element or every field as readily as one.
	return entity.KPathOf(segs)
}

// pairPath turns the address an operator types for one of verse's own registries into the two
// segments its route takes: the whole address as it is stored and listed
// (`verse.runners.pool.gpu`), or the bare pair below it (`pool.gpu`). An operator reading
// `verse data` and one typing `verse runner show` should not have to know two spellings.
//
// ONE READER for the three registries, and one grammar for both depths. A splitter per registry
// is three copies of an address grammar that is not the address grammar, and a copy drifts: the
// command ends up asking for a spelling the verse itself does not print.
func pairPath(route, system, ref string) (string, error) {
	pair, ok := pairSegs(system, ref)
	if !ok {
		return "", fmt.Errorf("%w: want an address like %s.%s.<kind>.<name> (or <kind>.<name>), got %q",
			cli.ErrUsage, entity.VerseRoot, system, ref)
	}
	return "/" + route + "/" + pair[0] + "/" + pair[1], nil
}

// pairSegs is pairPath's reading, for the `add` verbs that want the two segments themselves
// rather than a route built from them.
func pairSegs(system, ref string) ([2]string, bool) {
	segs, err := entity.PathSegs(ref)
	if err != nil {
		return [2]string{}, false
	}
	if len(segs) == entity.Depth+1 && segs[0] == entity.VerseRoot && segs[1] == system {
		segs = segs[2:]
	}
	if len(segs) != 2 || segs[0] == "" || segs[1] == "" {
		return [2]string{}, false
	}
	return [2]string{segs[0], segs[1]}, true
}

// onlyOne refuses an address given in more than one spelling.
func onlyOne(args []string, at string) error {
	if len(args) > 0 && at != "" {
		return fmt.Errorf("%w: an address is given once — as steps, or as --at", cli.ErrUsage)
	}
	return nil
}

// addressOf resolves the address to its SEGMENTS, for the callers that need them (a write asks
// where an array access begins). Exactly one spelling may be given.
func addressOf(args []string, at string) ([]string, error) {
	segs, err := address(args)
	if err != nil {
		return nil, err
	}
	if err := onlyOne(segs, at); err != nil {
		return nil, err
	}
	if at != "" {
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
