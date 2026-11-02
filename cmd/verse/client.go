package main

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/signadot/tony-format/go-tony/gomap"

	"github.com/scott-cotton/cli"
	"github.com/signadot/tony-format/go-tony/ir"
	"github.com/signadot/tony-format/go-tony/ir/kpath"

	"github.com/signadot/verse/api"
	"github.com/signadot/verse/client"
	"github.com/signadot/verse/codec"
	"github.com/signadot/verse/entity"
)

// ── HTTP helpers (all client commands connect to g.Addr) ─────────────────────

// apiClient is the operator API as the flags and the config stand when a request is made: the
// address, the scope, and the formats. It is built per request because the formats are per
// command (-i, -o); the transport, and with it the version and the principal, is `verse`'s.
//
// THE WIRE IS NEVER THE OPERATOR'S OUTPUT PREFERENCE. The format parameter governs how the server
// reads a body as well as how it writes a response, so sending -o there made `-o yaml` an
// instruction to parse tony as YAML — and the endpoints do not accept YAML. It cost a silent
// corruption rather than an error: the body went out as tony with `formula: !quorum 3` in it, and
// what landed in the store was `!quorum "3"`, a quorum whose threshold had become a string.
// `make smoke-all`'s second pass sets VERSE_OUTPUT_FORMAT=yaml, which is how it was reachable.
//
// -o is not lost by this, because it was never what the parameter bought: printBody and the watch
// renderer both decode the response by its Content-Type and re-render with outFmt() on this side.
// So the operator's format is a rendering decision, asked for only as Accept, and the exchange
// with the API stays in one dialect.
func apiClient() *client.Client {
	out, err := outFmt()
	if err != nil {
		out = codec.Tony
	}
	return &client.Client{Addr: g.Addr, Scope: g.Scope, Wire: wireFmt(), Accept: out, HTTP: verse}
}

// urlFor builds a request URL, folding in the global --scope so every client command operates in
// the selected scope (baseline by default). See client.Client.URL for why it is built rather than
// concatenated.
func urlFor(path string, q url.Values) string { return apiClient().URL(path, q) }

func httpGet(path string, q url.Values) (*http.Response, error) { return apiClient().Get(path, q) }

// httpPost sends a document in the wire format (see wireFmt) and asks for the output format back.
// Asking matters even though the client re-renders anyway: it is what lets the server send tony,
// so a tagged charter survives the round trip as tags rather than flattened to the key form.
func httpPost(path string, body any) (*http.Response, error) {
	return apiClient().Post(path, nil, body)
}

// httpPostTo is httpPost with query parameters, which must come through here rather than being
// written into the path: the path is ESCAPED, so a "?" in it arrives as %3F and reaches nothing.
func httpPostTo(path string, q url.Values, body any) (*http.Response, error) {
	return apiClient().Post(path, q, body)
}

// httpPostNode posts an already-built document, so a command that parsed a file sends it AS
// itself; see client.Client.PostNode.
func httpPostNode(path string, node *ir.Node) (*http.Response, error) {
	return apiClient().PostNode(path, nil, node)
}

func httpPostNodeTo(path string, q url.Values, node *ir.Node) (*http.Response, error) {
	return apiClient().PostNode(path, q, node)
}

// statusError is the refusal a request came back with, in the words the server used. It matters
// wherever a command handles its own status code instead of going through printBody, which is
// every command with a 404 of its own to name; see client.StatusError.
func statusError(resp *http.Response, what string) error { return client.StatusError(resp, what) }

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
// said is printBody for a command that names itself in what it refuses with: the server's own
// words — what is not in the asker's view, a read that did not happen — under the command's
// name, and never a sentence of the CLI's in their place.
func said(cc *cli.Context, resp *http.Response, what string) error {
	if err := printBody(cc, resp); err != nil {
		return fmt.Errorf("%s: %w", what, err)
	}
	return nil
}

func printBody(cc *cli.Context, resp *http.Response) error {
	out, err := outFmt()
	if err != nil {
		resp.Body.Close()
		return err
	}
	// --if / --trim, where every listing already passes: which items, and how much of each.
	f, err := readFilters()
	if err != nil {
		resp.Body.Close()
		return err
	}
	return renderBody(cc.Out, resp, f, out, colorize(cc.Out))
}

// renderBody writes a response the server wrote to w in the format out, with f applied: printBody
// with what the command line decides passed in. A 4xx/5xx is the error, in the server's words.
func renderBody(w io.Writer, resp *http.Response, f filters, out codec.Format, color bool) error {
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= http.StatusBadRequest {
		msg := strings.TrimSpace(string(body))
		if msg == "" {
			msg = resp.Status
		}
		return errors.New(msg)
	}
	in := respFmt(resp)
	node, err := codec.ParseNode(body, in)
	if err != nil {
		return fmt.Errorf("decoding the response: %w", err)
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
	if color {
		return codec.EncodeNodeColor(w, node, out)
	}
	return codec.EncodeNode(w, node, out)
}

// printDoc prints a `state` answer: printBody, with the filters applied to ENTITIES rather than
// to the document as one item — see filters.applyDoc.
func printDoc(cc *cli.Context, resp *http.Response, at *kpath.KPath) error {
	out, err := outFmt()
	if err != nil {
		resp.Body.Close()
		return err
	}
	f, err := readFilters()
	if err != nil {
		resp.Body.Close()
		return err
	}
	return renderDoc(cc.Out, resp, at, f, out, colorize(cc.Out))
}

// renderDoc writes a `state` answer to w in the format out, with f applied to the nodes the path
// reached. It is printDoc with what the command line decides passed in, for a caller that is not
// one command of one invocation (`verse mcp`).
func renderDoc(w io.Writer, resp *http.Response, at *kpath.KPath, f filters, out codec.Format, color bool) error {
	defer resp.Body.Close()
	body, err := okBody(resp)
	if err != nil {
		return err
	}
	in := respFmt(resp)
	node, err := codec.ParseNode(body, in)
	if err != nil {
		return fmt.Errorf("decoding the response: %w", err)
	}
	if node, err = f.applyDoc(node, at); err != nil {
		return err
	}
	if in != out {
		node = codec.StripLayout(node)
	}
	if color {
		return codec.EncodeNodeColor(w, node, out)
	}
	return codec.EncodeNode(w, node, out)
}

// okBody reads a response body, turning a 4xx/5xx into the error it carries.
func okBody(resp *http.Response) ([]byte, error) { return client.OKBody(resp) }

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
	return renderIR(cc.Out, node, f, out, colorize(cc.Out))
}

// renderIR writes a value this CLI computed to w in the format out, with f applied: printIR with
// what the command line decides passed in.
func renderIR(w io.Writer, node *ir.Node, f filters, out codec.Format, color bool) error {
	node, err := f.apply(node)
	if err != nil {
		return err
	}
	if color {
		return codec.EncodeNodeColor(w, node, out)
	}
	return codec.EncodeNode(w, node, out)
}

// respFmt reads the format a response declares; see client.ResponseFormat.
func respFmt(resp *http.Response) codec.Format { return client.ResponseFormat(resp) }

func httpPut(path string, body any) (*http.Response, error) { return apiClient().Put(path, body) }

func httpPatch(path string, body any) (*http.Response, error) { return apiClient().Patch(path, body) }

func httpDelete(path string, q url.Values) (*http.Response, error) {
	return apiClient().Delete(path, q)
}

// decodeClose decodes a response into v, reading it in the format the response declared, and
// closes the body.
func decodeClose(resp *http.Response, v any) { _ = client.DecodeClose(resp, v) }

// decodeBody decodes a response into v without closing it, for callers already deferring their
// own Close.
func decodeBody(resp *http.Response, v any) { _ = client.Decode(resp, v) }

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

// filterBy puts --if and --if-at on q, as the server's selector (api.FilterQuery).
func filterBy(q url.Values, pattern, at string, in codec.Format) error {
	return dataErr("", api.FilterQuery(q, pattern, at, in))
}

// dataFlags is how the command line says each field of the data grammar (api.FieldError): by
// its flag, the payload by name since --put is the verb, and the address by nothing, since it
// is given as steps or as --at.
var dataFlags = map[string]string{
	"if": "--if", "if_at": "--if-at", "value": "payload", "create": "--create", "update": "--update",
	"expect": "--expect", "grants": "--grants",
}

// dataErr is a data grammar error as the command line says it, after what: the flag it is about,
// and a usage error only for a misuse of the flags, since a value that does not read is not one.
func dataErr(what string, err error) error {
	if err == nil {
		return nil
	}
	prefix := ""
	if what != "" {
		prefix = what + ": "
	}
	var fe *api.FieldError
	if !errors.As(err, &fe) {
		return fmt.Errorf("%s%w", prefix, err)
	}
	if name := dataFlags[fe.Field]; name != "" {
		prefix += name + ": "
	}
	if fe.Misuse {
		return fmt.Errorf("%w: %s%w", cli.ErrUsage, prefix, fe.Err)
	}
	return fmt.Errorf("%s%w", prefix, fe.Err)
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
	// --init opens the stream with what already stands at the path, as a `set` per node at the
	// revision the watch opened at, and then the changes. It is the default because a watch is
	// mostly asked "tell me when this is so", and a stream that starts from now answers that
	// only if it was not already so: `--until` over a state already reached waits for ever.
	//
	// It is the substrate's opening state, so it lines up with the stream exactly, and it is
	// there only where the watch is the substrate's: a path with a literal prefix. One without
	// (`verse watch`, `'*' …`) joins the verse-wide stream, which has no opening state, and a
	// cursor asks for history in its place.
	Init bool `cli:"name=init default=true desc='open with what already stands at the path — a set per node — then stream changes. not with --since/--ago, which replay history instead, nor at a path with no literal prefix'"`
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
	if err := filterBy(q, cfg.If, cfg.IfAt, inFmt()); err != nil {
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
	if cfg.Init && !q.Has("since") && hasLiteralPrefix(at) {
		q.Set("init", "true")
	}
	u, err := readUntil(cfg.Until)
	if err != nil {
		return err
	}
	return streamChanges(cc, "watch", q, stream{until: u, nowire: cfg.NoWire})
}

// hasLiteralPrefix is whether a watch at `at` opens at a path, which is where an opening state
// exists (see versed's watchFor).
func hasLiteralPrefix(at string) bool {
	kp, err := entity.ParseAt(at)
	if err != nil {
		return false
	}
	prefix, _ := entity.LiteralPrefix(kp)
	return len(prefix) > 0
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

// gateConfig is `verse question`, the answerer's side as `verse ask` is the asker's: bare lists
// the questions waiting on you, `--all` the whole docket, `<id>` shows one record, `<id> --answer
// <option>` answers it by option, and `--watch` follows what starts waiting on you. approve and
// deny remain as subcommands for the two options every question has unless it names its own.
// `questions` and `qs` are its aliases, and `gate` and `gates`, the name it had.
type gateConfig struct {
	*cli.Command
	All    bool   `cli:"name=all desc='every pending ask you can read, whoever raised it and whoever it waits on: the whole docket'"`
	Watch  bool   `cli:"name=watch desc='print a line when a question starts waiting on you, and when it settles, until stopped'"`
	Since  string `cli:"name=since desc='with --watch: replay from this revision first, so nothing between two runs is missed'"`
	Notify bool   `cli:"name=notify desc='with --watch: also notify on the desktop when a question starts waiting on you, and when it settles (D-Bus on Linux, Notification Center on macOS)'"`
	Answer string `cli:"name=answer desc='the option to choose, by id (proceed|deny on a yes-or-no question)'"`
	As     string `cli:"name=as desc='identity answering; the configured user when left off (a decision names who made it)'"`
	Say    string `cli:"name=say aliases=s desc='words to say to the agent on resolution'"`
	Force  bool   `cli:"name=force desc='answer yourself an ask a resolver you granted serves for you'"`
	Served bool   `cli:"name=served desc='list the asks a resolver serves for you too, each saying which'"`
}

func gateCmd() *cli.Command {
	cfg := &gateConfig{}
	opts, err := cli.StructOpts(cfg)
	if err != nil {
		panic(err)
	}
	return cli.NewCommandAt(&cfg.Command, "question").
		WithAliases("questions", "qs", "gate", "gates").
		WithSynopsis("question — what is asked of you (also: questions, qs, gate): bare lists the questions waiting on you, --all the whole docket, <id> shows one, <id> --answer <option> answers it (proceed|deny are the two default options), --watch follows them").
		WithOpts(append(opts, filterOpts()...)...).
		WithSubs(gateAnswerCmd(true), gateAnswerCmd(false)).
		WithRun(func(cc *cli.Context, args []string) error {
			args, err := cfg.Parse(cc, args)
			if err != nil {
				return err
			}
			if handled, err := dispatch(cfg.Command, cc, args); handled {
				return err
			}
			if len(args) == 0 {
				if err := cfg.together(false); err != nil {
					return err
				}
				if cfg.Answer != "" {
					return fmt.Errorf("%w: --answer answers one question, so it wants an id: "+
						"`verse question <id> --answer %s`", cli.ErrUsage, cfg.Answer)
				}
				if cfg.Watch {
					return watchAsks(cc, cfg.Since, cfg.Notify, false, true)
				}
				resp, err := httpGet("/asks", questionQuery(cfg.All, cfg.Served))
				if err != nil {
					return fmt.Errorf("question: %w", err)
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
			// After the parse, so a flag after the id is checked as one before it is.
			if err := cfg.together(true); err != nil {
				return err
			}
			if len(rest) > 0 {
				return fmt.Errorf("%w: question takes one id — and this takes ONE token, so %q is "+
					"after it", cli.ErrUsage, rest[0])
			}
			id := args[0]
			switch {
			case id == "list":
				// `list` is THE mistake a bare-listing collection invites, and an id it is not:
				// refused by name, before anything is dialled, as every collection refuses it.
				return noArgs(cfg.Command, "question", args)
			case cfg.Answer == "":
				// One record, as it stands: the options, the ballots, the deadline, the
				// outcome — what the listing compacts.
				resp, err := httpGet("/asks/"+url.PathEscape(id), nil)
				if err != nil {
					return fmt.Errorf("question: %w", err)
				}
				return printBody(cc, resp)
			default:
				as, err := requireAs(cfg.As, "identity", "a decision names who made it")
				if err != nil {
					return err
				}
				return answerGate(cc, id, cfg.Answer, as, cfg.Say, cfg.Force, "answered")
			}
		})
}

// together refuses the flags that mean nothing together, rather than following one and dropping
// the other. --served widens the listing of your questions, and only that: the docket has the
// served ones already, the watch says each one and who serves it, and one record is the record
// whoever serves it.
func (cfg *gateConfig) together(id bool) error {
	switch {
	case cfg.Since != "" && !cfg.Watch:
		return fmt.Errorf("%w: --since goes with --watch", cli.ErrUsage)
	case cfg.Notify && !cfg.Watch:
		return fmt.Errorf("%w: --notify goes with --watch", cli.ErrUsage)
	case id && (cfg.Watch || cfg.All):
		return fmt.Errorf("%w: --watch and --all are about every question, and take no id", cli.ErrUsage)
	case id && cfg.Served:
		return fmt.Errorf("%w: --served widens the listing, and takes no id", cli.ErrUsage)
	case cfg.Served && cfg.All:
		return fmt.Errorf("%w: --all lists every pending ask, served ones included; --served "+
			"widens the questions waiting on you, so pass one", cli.ErrUsage)
	case cfg.Served && cfg.Watch:
		return fmt.Errorf("%w: --watch says the questions a resolver serves for you already, "+
			"each naming it; --served is the listing's", cli.ErrUsage)
	case cfg.All && cfg.Watch:
		return fmt.Errorf("%w: --watch follows what waits on you, and not the docket --all "+
			"lists; pass one", cli.ErrUsage)
	}
	return nil
}

// answerGate posts one answer and says what became of it: settled, or recorded on a question
// that takes more than one voice, with where the tally stands.
func answerGate(cc *cli.Context, id, choice, as, say string, force bool, done string) error {
	said, err := answerVia(apiClient(), id, choice, as, say, force, done)
	if err != nil {
		return err
	}
	fmt.Fprintln(cc.Out, said)
	return nil
}

// answerVia is answerGate through c, answering what it says rather than printing it.
func answerVia(c *client.Client, id, choice, as, say string, force bool, done string) (string, error) {
	body := api.Resolve{ID: id, Choice: choice, By: as, Force: force}
	if say != "" {
		body.Say = []string{say}
	}
	resp, err := c.Post("/asks/"+url.PathEscape(id)+"/answer", nil, body)
	if err != nil {
		return "", fmt.Errorf("answer %s: %w", choice, err)
	}
	defer resp.Body.Close()
	msg, _ := io.ReadAll(resp.Body)
	switch resp.StatusCode {
	case http.StatusNoContent:
		return done + " " + id, nil
	case http.StatusAccepted:
		// Recorded and not yet settled: the question takes more than one voice, and the
		// server says where the tally stands. Not an error, and not settled either.
		var rec api.Recorded
		if err := codec.Unmarshal(msg, &rec, respFmt(resp)); err != nil {
			return "", fmt.Errorf("answer %s: recorded, and the tally did not read: %w", choice, err)
		}
		return id + ": " + rec.String(), nil
	}
	return "", fmt.Errorf("answer %s: %s: %s", choice, resp.Status, strings.TrimSpace(string(msg)))
}

// The answer to a question is a VERB, so it is a command — `question approve <id>` — rather than
// `question resolve <id> --approve|--deny`, a flag pair that could be left empty or set both
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
	Say   string `cli:"name=say aliases=s desc='words to say to the agent on resolution'"`
	As    string `cli:"name=as desc='identity answering the gate; the configured user when left off (a decision names who made it)'"`
	Force bool   `cli:"name=force desc='answer yourself an ask a resolver you granted serves for you'"`
}

func gateAnswerCmd(approve bool) *cli.Command {
	cfg := &gateAnswerConfig{}
	opts, err := cli.StructOpts(cfg)
	if err != nil {
		panic(err)
	}
	name, syn := "approve", "approve a question (also: y, yes): approve <id> --as <who> [--say <text>]"
	aliases, done := []string{"y", "yes"}, "approved"
	if !approve {
		name, syn = "deny", "deny a question (also: n, no): deny <id> --as <who> [--say <text>]"
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
		return fmt.Errorf("%w: question %s wants a question id", cli.ErrUsage, verb)
	}
	as, err := requireAs(cfg.As, "identity", "a decision names who made it")
	if err != nil {
		return err
	}
	return answerGate(cc, args[0], choiceOf(approve), as, cfg.Say, cfg.Force, done)
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
	//
	// COUNTED FROM THE ADDRESS the path names, so the number is the same one at every level:
	// `verse data a --paths --depth 1` and `verse data a b --paths --depth 1` are each one level
	// down, and walking deeper is a step rather than a step and a bigger number. Counted from
	// the ROOT it would be two edits, and a script that bounds a walk would carry the sum of an
	// address written three lines above it.
	//
	// A STRING because absent is not a number: 0 is the address alone and an empty flag is
	// everything beneath it, which an int whose zero value is also a legal depth cannot tell
	// apart. go-tony's own `o list -depth` is a string for the same reason.
	Depth string `cli:"name=depth desc='with --paths/--kpaths: walk at most N levels beneath the address (0 is the address alone; absent is everything)'"`

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
	// Grants is what the node --put CREATES holds out, in the list `verse perms --grants` takes,
	// in place of the default above it. A write carrying it is a create, so it sits beside
	// --create and refuses --update and --expect, which are about a node that stands.
	Grants string `cli:"name=grants desc='with --put: create the node holding out this list, as verse perms --grants takes it ([{read: [ana@example.com]}]; [] for nothing), in place of the default above it; refused where the node stands'"`
}

// dataCmd is `verse data`: the verse's data at a path.
//
// The answer is the document the store holds, not rows flattened out of it, and every question
// is one path asked at a different depth, typed as steps: `verse data`, `verse data demo`,
// `verse data demo pr`, `verse data demo pr 1`, `verse data demo pr 1 labels urgent`. A path in
// one token, as a document prints it, goes after `--at`: `verse data --at 'demo.pr.*.stage'`
// (see address).
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
	case cfg.Grants != "" && cfg.Put == "":
		return fmt.Errorf("%w: --grants is what a node created by --put holds out; add --put <payload>",
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
	// EITHER FLAG ASKS FOR THE LISTING; --kpaths only says which spelling, so it need not be
	// passed beside --paths.
	listing := cfg.Paths || cfg.KPaths
	depth, err := cfg.depth(listing)
	if err != nil {
		return err
	}
	q, kp, err := stateQuery(at, cfg.If, cfg.IfAt, listing, depth, inFmt())
	if err != nil {
		return err
	}
	resp, err := httpGet("/state", q)
	if err != nil {
		return fmt.Errorf("data: %w", err)
	}
	if listing {
		return printPaths(cc, resp, kp, depth, cfg.KPaths)
	}
	return printDoc(cc, resp, kp)
}

// stateQuery is a read of the verse at the kinded path at, as GET /state takes it
// (api.StateQuery), and the path parsed, which is what the answer is walked by.
func stateQuery(at, pattern, patternAt string, listing bool, depth int, in codec.Format) (url.Values, *kpath.KPath, error) {
	q, kp, err := api.StateQuery(at, pattern, patternAt, listing, depth, in)
	if err != nil {
		return nil, nil, dataErr("data", err)
	}
	return q, kp, nil
}

// depth reads --depth: how far beneath the address the listing walks, entity.Unbounded when the
// flag was not given. Refused where it means nothing — with no listing to bound, or negative —
// rather than dropped, since a flag that quietly does nothing is worse than one that says no.
func (cfg *stateConfig) depth(listing bool) (int, error) {
	if cfg.Depth == "" {
		return entity.Unbounded, nil
	}
	if !listing {
		return 0, fmt.Errorf("%w: --depth bounds a listing; ask for one with --paths or --kpaths",
			cli.ErrUsage)
	}
	n, err := strconv.Atoi(cfg.Depth)
	if err != nil || n < 0 {
		return 0, fmt.Errorf("%w: --depth %s: how far BENEATH the address to walk — 0 is the "+
			"address alone — and never negative", cli.ErrUsage, cfg.Depth)
	}
	return n, nil
}

// put writes a payload at the path.
func (cfg *stateConfig) put(cc *cli.Context, segs []string) error {
	body, err := putBody(api.DataWrite{Payload: cfg.Put, Create: cfg.Create, Update: cfg.Update, Expect: cfg.Expect,
		Grants: cfg.Grants}, segs, inFmt())
	if err != nil {
		return err
	}
	resp, err := httpPost("/entity", body)
	if err != nil {
		return fmt.Errorf("data --put: %w", err)
	}
	return printBody(cc, resp)
}

// putBody is the request a write as `data --put` takes it sends at segs (api.DataWrite.Body).
func putBody(p api.DataWrite, segs []string, in codec.Format) (api.PutEntity, error) {
	if len(segs) == 0 {
		return api.PutEntity{}, fmt.Errorf("%w: --put wants a path to write at (`demo pr 1`, `demo pr 1 labels "+
			"urgent`, or --at demo.pr.1)", cli.ErrUsage)
	}
	body, err := p.Body(segs, in)
	if err != nil {
		return api.PutEntity{}, dataErr("data --put", err)
	}
	return body, nil
}

// rm removes what stands at the path — an entity, or one field of one.
func (cfg *stateConfig) rm(cc *cli.Context, segs []string) error {
	said, err := rmVia(apiClient(), segs)
	if err != nil {
		return err
	}
	fmt.Fprintln(cc.Out, said)
	return nil
}

// rmVia is the removal `data --rm` makes, through c, and the line it says.
func rmVia(c *client.Client, segs []string) (string, error) {
	if len(segs) == 0 {
		return "", fmt.Errorf("%w: --rm wants a path (`demo pr 1`, `demo pr 1 labels urgent`, or "+
			"--at demo.pr.1)", cli.ErrUsage)
	}
	q, at, err := api.RemoveQuery(segs)
	if err != nil {
		return "", dataErr("data --rm", err)
	}
	resp, err := c.Delete("/entity", q)
	if err != nil {
		return "", fmt.Errorf("data --rm: %w", err)
	}
	defer resp.Body.Close()
	// A REFUSAL IS NOT A REMOVAL, and this printed "removed" whatever came back. The one that
	// matters is the reserved half: `verse data verse task instance <id> --rm` is answered 403
	// — verse's own records are written by the verbs that own them — and the CLI said the
	// record was gone while it stood there untouched.
	if resp.StatusCode != http.StatusNoContent {
		return "", statusError(resp, "data --rm")
	}
	return api.RemovedLine(at), nil
}

// printPaths prints one address per line — EVERY address under what was asked for, deepest last
// within each branch, bounded by --depth when the reader wants less.
//
// It listed one level and took a `--depth` to say which, and that could not be got right: an
// address may be at any depth (a write names a place at whatever depth), and nothing
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
	return renderPaths(cc.Out, resp, at, depth, kpaths)
}

// renderPaths is printPaths to any writer.
func renderPaths(w io.Writer, resp *http.Response, at *kpath.KPath, depth int, kpaths bool) error {
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
	for _, addr := range api.ListedPaths(node, at, depth) {
		fmt.Fprintln(w, renderAddr(addr, kpaths))
	}
	return nil
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

// namePath turns what an operator types for one of verse's own registries — runners, resolvers,
// sources, each of whose things stands one name beneath it — into its route: the whole address as it is stored and listed (`verse.runners.gpu`), or the
// name alone (`gpu`).
func namePath(route, system, ref string) (string, error) {
	name, ok := nameSeg(system, ref)
	if !ok {
		return "", fmt.Errorf("%w: want an address like %s.%s.<name> (or <name>), got %q",
			cli.ErrUsage, entity.VerseRoot, system, ref)
	}
	return "/" + route + "/" + name, nil
}

// nameSeg is namePath's reading, for the `add` verbs that want the name itself.
func nameSeg(system, ref string) (string, bool) {
	segs, err := entity.PathSegs(ref)
	if err != nil {
		return "", false
	}
	if at := entity.RefAt(segs...); at.ChildOf(entity.VerseRoot, system) {
		return at.Name(), at.Name() != ""
	}
	if len(segs) != 1 || segs[0] == "" {
		return "", false
	}
	return segs[0], true
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
