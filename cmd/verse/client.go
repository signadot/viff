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
// command that has a 404 of its own to name: `verse charter remove <stratum>` answered a bare
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
	// NO DEFAULT, because the default was a question nobody asked. `default=0` sent `since=0`
	// on every watch — "replay the whole retained log" — so `verse watch <path>` on a store
	// that cannot replay from 0 was refused, and the refusal advised dropping a flag the
	// caller had never passed. An absent cursor streams from now; `--since N` is how somebody
	// asks for history, and `--since 0` is still from the beginning.
	Since string `cli:"name=since desc='replay from this store revision first (cursor); 0 = from the beginning. absent = stream from now'"`
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
// `verse state demo.pr` name the same entities and differ only in when they answer.
func (cfg *watchConfig) query(at string) (url.Values, error) {
	q := url.Values{}
	if cfg.Since != "" {
		q.Set("since", cfg.Since)
	}
	if at != "" {
		q.Set("at", at)
	}
	if err := filterBy(q, cfg.If, cfg.IfAt); err != nil {
		return nil, err
	}
	return q, nil
}

func (cfg *watchConfig) run(cc *cli.Context, args []string) error {
	args, err := cfg.Parse(cc, args)
	if err != nil {
		return err
	}
	at, err := address(args) // one token, or a vector of steps
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
// and grepped afterwards; the block form belongs to `state get`, where the document is the
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

type entityGetConfig struct{ *cli.Command }

func entityGetCmd() *cli.Command {
	cfg := &entityGetConfig{}
	return cli.NewCommandAt(&cfg.Command, "get").
		WithOpts(filterOpts()...).
		WithSynopsis("get one place: get <path> (demo:pr:1 · demo.pr.1.labels · merge.proposal.X.votes(scott))").
		WithRun(cfg.run)
}

// get names ONE place and answers what stands there, at whatever depth the path reaches. It
// goes through /state, which is the read that takes a whole path — so `--at` is gone, and a
// keyed array element (`votes(scott)`) is reachable, which no verse address could do at all
// before. Nothing stands there is a 404, because a path this deep names a thing.
func (cfg *entityGetConfig) run(cc *cli.Context, args []string) error {
	args, err := cfg.Parse(cc, args)
	if err != nil {
		return err
	}
	if len(args) < 1 {
		return fmt.Errorf("%w: state get wants a path (demo:pr:1, or demo.pr.1.labels.urgent)", cli.ErrUsage)
	}
	addr, err := address(args)
	if err != nil {
		return err
	}
	at, kp, err := readAt(addr)
	if err != nil {
		return fmt.Errorf("%w: state get: %s", cli.ErrUsage, err)
	}
	// get names ONE place, so it wants a path that reaches one. A shorter path describes a
	// slice, and the bare read is what answers those.
	if entity.AtDepth(kp) < entity.Depth {
		return fmt.Errorf("%w: state get names one place — %q names a slice, so ask `verse state %s`",
			cli.ErrUsage, args[0], at)
	}
	resp, err := httpGet("/state", url.Values{"at": {at}})
	if err != nil {
		return fmt.Errorf("state get: %w", err)
	}
	return printBody(cc, resp)
}

// readAt is entity.ReadPath — the one grammar decision, made where the two spellings are
// defined rather than here.
func readAt(s string) (string, *kpath.KPath, error) { return entity.ReadPath(s) }

type entityPutConfig struct {
	*cli.Command
	Create bool   `cli:"name=create desc='only if nothing is there yet — the write loses a race rather than clobbering'"`
	Update bool   `cli:"name=update desc='only if something is already there'"`
	If     string `cli:"name=if desc='only if what is there matches this tony pattern'"`
}

func entityPutCmd() *cli.Command {
	cfg := &entityPutConfig{}
	opts, err := cli.StructOpts(cfg)
	if err != nil {
		panic(err)
	}
	return cli.NewCommandAt(&cfg.Command, "put").
		WithSynopsis("put at a path: put <path> <payload> [--create|--update] [--if <pattern>]").
		WithOpts(opts...).
		WithRun(cfg.run)
}

// conditions renders the guard flags as the wire's preconditions. --create and --update are
// opposite answers to the same question, so asking both is a contradiction rather than a
// pair of conditions that can never hold — say so from the flags, before anything is sent.
func (cfg *entityPutConfig) conditions() ([]api.Condition, error) {
	var out []api.Condition
	if cfg.Create && cfg.Update {
		return nil, fmt.Errorf("%w: --create and --update are opposites; pass one", cli.ErrUsage)
	}
	if cfg.Create || cfg.Update {
		exists := cfg.Update
		out = append(out, api.Condition{Exists: &exists})
	}
	if cfg.If != "" {
		pattern, err := codec.ParseNode([]byte(cfg.If), inFmt())
		if err != nil {
			return nil, fmt.Errorf("--if: %w", err)
		}
		out = append(out, api.Condition{Pattern: pattern})
	}
	return out, nil
}

func (cfg *entityPutConfig) run(cc *cli.Context, args []string) error {
	args, err := cfg.Parse(cc, args)
	if err != nil {
		return err
	}
	if len(args) < 2 {
		return fmt.Errorf("%w: state put wants a path and a payload", cli.ErrUsage)
	}
	// ONE ADDRESS, all the way down: `state put demo.pr.1.labels.urgent true` is what
	// `--at labels.urgent` used to spell. The wire keeps {ref, field[]} — it has no reason to
	// re-parse what the CLI has already split, and the structured form is what the store's
	// writePath consumes — so this is the one parser, and Path.String is its inverse.
	//
	// The PAYLOAD IS LAST, which is what lets the address be a vector of steps here too:
	// `state put demo pr 1 '{stage: open}'`. It is the one command where the address is not
	// the end of the line, and the rule that keeps it unambiguous is positional rather than
	// syntactic — everything up to the final argument is the address.
	addr, err := address(args[:len(args)-1])
	if err != nil {
		return err
	}
	at, err := entity.ParsePath(addr)
	if err != nil {
		return fmt.Errorf("%w: state put: %s", cli.ErrUsage, err)
	}
	// A payload typed on the command line names no format of its own, so it is read as
	// --ifmt / the configured default. Valid JSON is also valid tony, so `{"stage":"open"}`
	// keeps working whichever the default is.
	//
	// Parsed to a NODE rather than a map: a payload is any tony value, so `42`, `true` and
	// `[a, b]` are payloads too, and decoding into a map would reject them at the CLI even
	// though the store and the wire both accept them.
	payload, err := codec.ParseNode([]byte(args[len(args)-1]), inFmt())
	if err != nil {
		return fmt.Errorf("state put: payload: %w", err)
	}
	when, err := cfg.conditions()
	if err != nil {
		return fmt.Errorf("state put: %w", err)
	}
	// THE WHOLE ADDRESS AS A PATH, which is what a write at any depth needs: `state put
	// verse-dev drift head abc` names a place four segments down, and a ref can spell three.
	// The field path rides along as it did — a write may still address inside a payload.
	// THE WHOLE ADDRESS IS THE PLACE. `state put verse-dev drift head abc '{…}'` writes there,
	// and so does `state put demo.pr.1.labels.urgent true` — a write is a value at a path, and
	// where the entity "ends" is a question this command does not have to answer. The answer
	// comes back from the address written, so a put still shows the merge wherever the address
	// IS an entity, which is the ordinary case.
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
		return fmt.Errorf("state put: %w", err)
	}
	return printBody(cc, resp)
}

type entityRmConfig struct{ *cli.Command }

func entityRmCmd() *cli.Command {
	cfg := &entityRmConfig{}
	return cli.NewCommandAt(&cfg.Command, "rm").
		WithSynopsis("rm — remove what stands at a path: rm <path> (an entity, or one field of one)").
		WithRun(cfg.run)
}

func (cfg *entityRmConfig) run(cc *cli.Context, args []string) error {
	args, err := cfg.Parse(cc, args)
	if err != nil {
		return err
	}
	if len(args) < 1 {
		return fmt.Errorf("%w: state rm wants a path (demo:pr:1, or demo.pr.1.labels.urgent)", cli.ErrUsage)
	}
	addr, err := address(args)
	if err != nil {
		return err
	}
	at, err := entity.ParsePath(addr)
	if err != nil {
		return fmt.Errorf("%w: state rm: %s", cli.ErrUsage, err)
	}
	q := url.Values{"ref": {at.Ref.String()}}
	for _, f := range at.Field {
		q.Add("field", f)
	}
	resp, err := httpDelete("/entity", q)
	if err != nil {
		return fmt.Errorf("state rm: %w", err)
	}
	defer resp.Body.Close()
	// A REFUSAL IS NOT A REMOVAL, and this printed "removed" whatever came back. The one that
	// matters is the reserved half: `verse state rm verse task instance <id>` is answered 403 —
	// verse's own records are written by the verbs that own them — and the CLI said the record
	// was gone while it stood there untouched. An operator reading that has been told the
	// opposite of the truth about a store they are trying to clean up.
	if resp.StatusCode != http.StatusNoContent {
		return statusError(resp, "state rm")
	}
	// Nothing stands there now — which is all there is to say, and is equally true of a path
	// that held nothing to begin with.
	fmt.Fprintf(cc.Out, "removed %s\n", at)
	return nil
}

// state is the one collection with filters of its own, so it does not go through
// collection() — the shape is the same, the query is not.
type stateConfig struct {
	*cli.Command
	// The same pair `watch` takes, meaning the same thing (filterBy) — the read and the stream
	// are one selector asked at two times.
	//
	// `state put --if` is a DIFFERENT --if, and they do not collide: a parent stops parsing
	// flags at its first positional, so `state put <ref> <payload> --if <pattern>` hands the
	// flag to `put`, where it is a precondition rather than a filter. Pinned by
	// TestStatePutKeepsItsOwnIf.
	If    string `cli:"name=if desc='only entities whose payload matches this tony pattern'"`
	IfAt  string `cli:"name=if-at desc='root --if at this dot-separated field path INSIDE the payload (e.g. labels)'"`
	Paths bool   `cli:"name=paths desc='print one address per line instead of the document'"`
	// Depth says how deep an address is, for a listing of them. It exists because nothing else
	// knows: `{a: {b: {c: {stage: open}}}}` holds an entity at `a.b.c` or at `a.b`, and which
	// one is a property of the question (docs/working/entity-tree.md). Unset answers at the
	// depth an address has always had, which is what every listing written before this wants.
	Depth int `cli:"name=depth desc='with --paths: how many segments an address has (default: 3, or 4 under verse)'"`
}

// stateCmd is `verse state`: the verse's state at a path.
//
// It replaces `verse entity`, and the noun went with the answer. The old command listed rows of
// a table that the store does not hold — `[{ref, payload}, …]`, flattened out of the document it
// does hold — and addressed them in three vocabularies: `--system`/`--kind` to narrow a listing,
// `system:kind:id` to name one, `--at` to reach inside it. They are one kinded path asked at
// different depths, so that is what this takes: `verse state`, `verse state demo`,
// `verse state demo.pr`, `verse state demo.pr.1`, `verse state demo.pr.1.labels.urgent`,
// `verse state 'demo.pr.*.stage'`.
//
// A ref is that same path with `:` for a delimiter, which is why get/put/rm still take one and
// why nothing about identity moved (see entity/doc.go).
func stateCmd() *cli.Command {
	cfg := &stateConfig{}
	opts, err := cli.StructOpts(cfg)
	if err != nil {
		panic(err)
	}
	return cli.NewCommandAt(&cfg.Command, "state").WithAliases("s").
		WithSynopsis("state — the verse at a path: state [--if <pattern>] [--paths] [<path>]; get|put|rm one by <ref>").
		// --trim only, for the reason watch takes only that: `state --if` is the server's
		// selector, and it names WHICH ENTITIES rather than what to print of them.
		WithOpts(append(opts, trimOpt()...)...).
		WithSubs(entityGetCmd(), entityPutCmd(), entityRmCmd()).
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
	// what hands `state put <ref> <payload> --if <pattern>` to put's own --if — a precondition
	// on the write — rather than eating it as this command's filter. dispatch above is where
	// that case leaves.
	//
	// What is left here is the read, whose positional is an ADDRESS rather than a verb, so
	// nothing after it belongs to anyone else. Parsing the tail is the whole fix, and it runs
	// up the command tree exactly as the first pass did — `verse state lineage --scope m` picks
	// up a root option as readily as one of ours.
	// EVERY positional is the address: one token, or a vector of steps (`state demo pr 1`).
	// The flags are parsed off first — the tail parse below is what lets them sit on either
	// side of it — and what is left is the path.
	at := ""
	if len(args) > 0 {
		var addr []string
		addr, args = args, nil
		// A flag among the positionals ends the address: everything from it belongs to the
		// command, and the parse below is what claims it.
		for i, a := range addr {
			if strings.HasPrefix(a, "-") {
				addr, args = addr[:i], addr[i:]
				break
			}
		}
		if at, err = address(addr); err != nil {
			return err
		}
		if args, err = cfg.Parse(cc, args); err != nil {
			return err
		}
		if len(args) > 0 {
			return fmt.Errorf("%w: state takes one path (as one token or as steps), and %q is "+
				"after it", cli.ErrUsage, args[0])
		}
	}
	// Parsed HERE as well as at the server, so a malformed path is a usage error against the
	// command the operator typed rather than a 400 from a route they did not.
	kp, err := entity.ParseAt(at)
	if err != nil {
		return fmt.Errorf("state: %w", err)
	}
	q := url.Values{}
	if at != "" {
		q.Set("at", at)
	}
	if err := filterBy(q, cfg.If, cfg.IfAt); err != nil {
		return fmt.Errorf("state: %w", err)
	}
	resp, err := httpGet("/state", q)
	if err != nil {
		return fmt.Errorf("state: %w", err)
	}
	if cfg.Paths {
		return printPaths(cc, resp, kp, cfg.Depth)
	}
	return printDoc(cc, resp, kp)
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

// printPaths prints one address per line, derived from the document the read answered with.
//
// Client-side, because the addresses are already IN the answer: an id is a key now rather than a
// token inside a rendered ref, so the server would only be sending back what the document
// already says. It is the `ls -1` of a read — what a script uses instead of grepping `ref:` out
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
	// AS STEPS, which is the spelling this command line takes on the way in: `verse state
	// --paths | xargs -L1 verse state get` round-trips, and no address in the listing needs
	// quoting to be usable — a step is a whole word whatever is inside it, where the colon form
	// needs quoting the moment an id holds a dot and the dotted form the moment it holds one
	// too. The colon form stays what a ref renders as inside a DOCUMENT, which is read by a
	// parser rather than by a shell.
	doc := entity.Doc{Node: node}
	ents := doc.Entities(at)
	if depth > 0 {
		ents = doc.EntitiesAt(at, depth)
	}
	for _, e := range ents {
		fmt.Fprintln(cc.Out, strings.Join(e.Ref.Segs(), " "))
	}
	return nil
}

// address reads the address positionals a read command takes: ONE token in either delimiter
// (`demo:pr:1`, `demo.pr.1`) or a VECTOR OF STEPS (`demo pr 1`).
//
// The vector is the ergonomic half of the addressing model — `' '` as the delimiter a person
// types, since a shell already knows how to quote a word and nobody should be escaping dots
// inside a path they typed. `demo.pr."a.b".labels` is `demo pr a.b labels`, and the shell's
// own quoting is the only quoting anybody does.
//
// It is accepted where the address is the LAST thing on the line, which is every read; a
// command that takes something after it (`state put <addr…> <payload>`) takes the address as
// everything before that.
//
// A STEP ABOVE THE ID MAY NOT HOLD A DELIMITER, and that is the ref grammar's own rule rather
// than a new one: a system and a kind may not contain `:` while an id may. So a dot or a colon
// in one of those steps means the two spellings have been mixed — `verse state demo.pr 1` — and
// the address somebody typed is not the one that would be read.
//
// From the id down, anything goes: an id holds dots and colons (`x@git:ref:y`), and the field
// path below it is a person's own key names. That is the whole ergonomic win — `demo pr a.b
// labels urgent` needs no quoting anywhere, where the one-token forms need it twice.
func address(args []string) (string, error) {
	// A LEADING `--` IS NOT A STEP. It is how a shell says "the options are over", and on a
	// command whose positional can collide with a verb (`verse charter -- remove`) it is how
	// the address wins — so every command that takes one accepts it, rather than one of them
	// answering about an address whose first segment is two dashes.
	if len(args) > 0 && args[0] == "--" {
		args = args[1:]
	}
	switch len(args) {
	case 0:
		return "", nil
	case 1:
		return args[0], nil // one token: the delimiter rule decides which spelling it is
	}
	// Where the id sits: one deeper in verse's own half, whose root is a step of its own.
	id := entity.Depth - 1
	if args[0] == entity.VerseRoot {
		id++
	}
	for i, a := range args {
		if i >= id {
			break
		}
		if strings.ContainsAny(a, ".:") {
			return "", fmt.Errorf("%w: %q names the %s of an address written as steps, and that "+
				"holds no `.` or `:` — the two spellings have been mixed. Write it as one token "+
				"(%s), or as steps with none above the id", cli.ErrUsage, a, stepName(i, id),
				strings.Join(args, "."))
		}
	}
	return entity.JoinSegs(args), nil
}

// stepName is what a step at this position is called, so a refusal can say which one is wrong.
func stepName(i, id int) string {
	switch {
	case id == entity.Depth && i == 0:
		return "root"
	case i == id-2:
		return "system"
	case i == id-1:
		return "kind"
	}
	return "prefix"
}
