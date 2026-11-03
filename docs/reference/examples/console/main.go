// Command console serves a read-and-steer face for a running verse: the docket, the
// charter, a subject's flow, and the tasks in flight, in the verse's own vocabulary.
//
// IT IS A SEPARATE PROGRAM, and that is the point. Whether versed should serve a UI of its
// own is a decision about verse's surface, and this does not take it: the console is an
// example program beside the coderunner, dialling a verse over the same HTTP API the CLI
// uses. Nothing in versed changes to run it.
//
// WHY IT PROXIES RATHER THAN LETTING THE PAGE DIAL VERSE. versed sets no CORS headers, so a
// page served from anywhere else cannot read its responses — the browser refuses before
// verse is asked. Serving the page and the API from one origin is what removes that, and a
// proxy is the smallest thing that does it. `/v/<route>` here is `<route>` there.
//
// WHY THE PAGE PARSES YAML. Verse renders no JSON — `application/json` alone is a 406, and
// that is a decision in codec, not an omission (codec/codec_test.go). So this asks for
// `application/yaml`, which verse does render, and hands it to the page unchanged. Nothing
// here re-renders a response: a field this program does not know about still reaches the
// page, which is what keeps the console from going stale every time a route grows a field.
//
// THE PRINCIPAL IS A FLAG, because every write names who made it. `-as` is sent as
// Verse-Principal on anything that is not a GET, and without it the writes come back 400
// saying so — which is the right failure, and the reason the page shows it rather than
// hiding the buttons.
package main

import (
	"embed"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

//go:embed index.html
var ui embed.FS

func main() {
	addr := flag.String("addr", ":9899", "what to listen on")
	verse := flag.String("verse", "localhost:19797", "the verse to dial, host:port (VERSE_ADDR)")
	as := flag.String("as", "", "the principal writes are made as: user:<email>, agent:<name> (VERSE_USER)")
	flag.Parse()

	// An explicit flag beats the environment; the environment beats the default. Which is
	// the CLI's own order, so a shell already set up for `verse` needs no second spelling.
	set := map[string]bool{}
	flag.Visit(func(f *flag.Flag) { set[f.Name] = true })
	target := pick(set["verse"], *verse, os.Getenv("VERSE_ADDR"))
	principal := pick(set["as"], *as, os.Getenv("VERSE_USER"))
	if principal != "" && !strings.Contains(principal, ":") {
		// The grammar is kind-qualified; a bare email is the one abbreviation the CLI takes.
		principal = "user:" + principal
	}

	mux := http.NewServeMux()
	// No method on the root pattern: "GET /" and "/v/" conflict in ServeMux's precedence
	// rules (one is more specific in path, the other in method), and the panic is at startup.
	mux.Handle("/", http.FileServer(http.FS(ui)))
	mux.HandleFunc("/v/", proxy(target, principal))
	// So the page can say who it is writing as, and show it rather than discovering it in a 400.
	mux.HandleFunc("GET /who", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		fmt.Fprintf(w, "%s\n%s\n", principal, target)
	})

	log.Printf("console on http://%s → verse %s", listenName(*addr), target)
	if principal == "" {
		log.Printf("no principal: reads work, writes will be refused. Pass -as user:you@example.com")
	} else {
		log.Printf("writing as %s", principal)
	}
	log.Fatal(http.ListenAndServe(*addr, mux))
}

// proxy forwards /v/<route> to <route> on the verse, asking for yaml and naming the
// principal on anything that writes.
func proxy(target, principal string) http.HandlerFunc {
	// NO OVERALL TIMEOUT, because /watch is a stream and an overall deadline is a stream that
	// dies at the deadline. What still needs bounding is a verse that accepts the connection
	// and never answers, and that is ResponseHeaderTimeout — it covers the wait for the
	// headers and stops there, leaving the body to run as long as the watch does.
	client := &http.Client{
		Transport: &http.Transport{ResponseHeaderTimeout: 30 * time.Second},
	}
	return func(w http.ResponseWriter, r *http.Request) {
		route := strings.TrimPrefix(r.URL.Path, "/v")
		u := url.URL{Scheme: "http", Host: target, Path: route, RawQuery: r.URL.RawQuery}

		req, err := http.NewRequestWithContext(r.Context(), r.Method, u.String(), r.Body)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		// Verse renders no JSON; yaml is what it does render and what the page parses.
		req.Header.Set("Accept", "application/yaml")
		if r.Method != http.MethodGet {
			// The page posts tony/json bodies; verse reads json on the way in.
			if ct := r.Header.Get("Content-Type"); ct != "" {
				req.Header.Set("Content-Type", ct)
			}
			if principal != "" {
				req.Header.Set("Verse-Principal", principal)
			}
		}

		resp, err := client.Do(req)
		if err != nil {
			// A verse that is not up is the ordinary case while somebody is setting this up,
			// so it reads as a sentence rather than a stack.
			http.Error(w, "cannot reach verse at "+target+": "+err.Error(), http.StatusBadGateway)
			return
		}
		defer resp.Body.Close()
		for k, vs := range resp.Header {
			for _, v := range vs {
				w.Header().Add(k, v)
			}
		}
		w.WriteHeader(resp.StatusCode)
		stream(w, resp.Body)
	}
}

// stream copies the response through, flushing each chunk.
//
// A plain io.Copy buffers, and a buffered SSE stream is one the page is told about whenever
// the buffer happens to fill — which for a watch that carries one small frame a minute is
// never. Flushing per chunk costs nothing on an ordinary response, since it is one chunk.
func stream(w http.ResponseWriter, body io.Reader) {
	rc := http.NewResponseController(w)
	// FLUSH THE HEADERS BEFORE READING A BYTE. net/http holds the response until the handler
	// writes enough to fill its buffer or flushes, so a stream that has nothing to say yet is
	// a stream whose 200 and Content-Type never leave this process — the page's EventSource
	// sits in `connecting` and no `onopen` ever fires. A busy verse hides it, because the first
	// frame arrives immediately and flushes on its way past; a quiet one does not.
	rc.Flush()
	buf := make([]byte, 16<<10)
	for {
		n, err := body.Read(buf)
		if n > 0 {
			if _, werr := w.Write(buf[:n]); werr != nil {
				return
			}
			rc.Flush()
		}
		if err != nil {
			return
		}
	}
}

// pick resolves one setting: the flag when it was given, else the environment, else the
// flag's own default.
func pick(flagGiven bool, flagValue, env string) string {
	if flagGiven {
		return flagValue
	}
	if e := strings.TrimSpace(env); e != "" {
		return e
	}
	return flagValue
}

func listenName(addr string) string {
	if h, p, err := net.SplitHostPort(addr); err == nil && h == "" {
		return "localhost:" + p
	}
	return addr
}
