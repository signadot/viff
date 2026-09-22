// Command iod serves verse's reflection contract for the systems verse ships a connector
// for: a git repository (or a mirror of a remote one), the issues `git issue` keeps inside
// one, a Slack workspace, a Linear team, a set of GitHub repositories.
//
//	iod git       -addr :9801 -repo verse=/src/verse
//	iod git       -addr :9801 -mirror https://github.com/signadot/verse.git -state /repos
//	iod git-issue -addr :9801 -repo verse=/src/verse
//	iod slack     -addr :9801            # SLACK_BOT_TOKEN in the environment
//	iod linear    -addr :9801 -team ENG  # LINEAR_API_KEY in the environment
//	iod github    -addr :9801 -repo signadot/verse   # GITHUB_TOKEN in the environment
//
// Then install it, from anywhere that can reach verse:
//
//	verse source add build-host http://localhost:9801 -root git
//	verse source
//
// WHY THIS IS A SECOND BINARY, and not three subcommands of `verse`: a source is a program
// somebody deploys, and the whole argument for the seam is that verse does not have to be
// built with one. Folding Slack and Linear into the engine would make verse the connector
// monolith — every API's wire format, every credential's failure mode, in the binary that runs
// everybody's triggers. They ride in the same IMAGE, which is a bag of binaries and costs
// nothing; they do not ride in the same process.
//
// To reflect a system that is not one of these three, copy
// docs/reference/examples/source/main.go, which is the smallest program that serves the same
// contract. Nothing here is privileged: these are ordinary external sources that happen to be
// maintained in this tree.
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/signadot/verse/adminface"
	"github.com/signadot/verse/entity"
	verseio "github.com/signadot/verse/io"
	verseiod "github.com/signadot/verse/io/iod"
)

func main() {
	log.SetFlags(0)
	log.SetPrefix("iod: ")
	if len(os.Args) < 2 {
		usage()
	}
	cmd, args := os.Args[1], os.Args[2:]
	var err error
	switch cmd {
	case "git":
		err = gitMain(args)
	case "git-issue":
		err = gitIssueMain(args)
	case "slack":
		err = slackMain(args)
	case "linear":
		err = linearMain(args)
	case "github":
		err = githubMain(args)
	case "-h", "--help", "help":
		usage()
	default:
		log.Printf("no such source %q", cmd)
		usage()
	}
	if err != nil {
		log.Fatal(err)
	}
}

func usage() {
	fmt.Fprint(os.Stderr, `iod serves one external system as a verse Source.

  iod git       [flags]   a git repository, or a mirror of a remote one
  iod git-issue [flags]   the issues `+"`git issue`"+` keeps inside one
  iod slack     [flags]   a Slack workspace (SLACK_BOT_TOKEN, SLACK_SIGNING_SECRET)
  iod linear    [flags]   Linear teams (LINEAR_API_KEY, LINEAR_WEBHOOK_SECRET)
  iod github    [flags]   GitHub repositories (GITHUB_TOKEN, GITHUB_WEBHOOK_SECRET)

`)
	os.Exit(2)
}

// common is what every source is served with: where to listen, how often it will really answer,
// and where its own deliveries turn into hints.
type common struct {
	addr        string
	adminAddr   string
	minInterval time.Duration
	hintURL     string
	root        string
}

func (c *common) bind(fs *flag.FlagSet) {
	fs.StringVar(&c.addr, "addr", envOr("SOURCED_ADDR", ":9801"),
		"address to serve the reflection contract on")
	fs.DurationVar(&c.minInterval, "min-interval", 0,
		"answer from the last reflection when asked again inside this window — the source's "+
			"own rate limit, since verse's resync interval is a constant and an API budget is not")
	fs.StringVar(&c.root, "root", "",
		"the slice this source writes, defaulting to the connector's own name. More than one "+
			"source gets deployed and the partition between them is drawn by whoever runs them; "+
			"two deployments sharing a root need only disjoint claims, and one that wants no "+
			"coordination at all takes a root of its own — two roots are disjoint on the "+
			"first segment. It is also how a second instance of the same product gets a slice "+
			"that is not confused with the first")
	fs.StringVar(&c.adminAddr, "admin-addr", envOr("SOURCED_ADMIN_ADDR", ""),
		"admin/pprof listen address, or off to disable (default: the -addr port + 1)")
	fs.StringVar(&c.hintURL, "hint-url", os.Getenv("VERSE_HINT_URL"),
		"verse's hint route for THIS registration (http://verse:9797/io/<name>/hint); "+
			"without it, deliveries are received and dropped and only the resync net converges")
}

// serve mounts the contract and whatever delivery routes this source owns, and runs.
//
// The contract is mounted at "/" and the delivery routes over it: iod's own handler 404s
// what it does not serve, which is the answer a client reads as "whatever is at this url, it
// does not serve this contract" — so mounting it as the fallback keeps that true for
// everything except the routes named here.
func (c *common) serve(root string, src verseio.Source, routes map[string]http.Handler) error {
	// The admin face, for the same reason versed has one: a source that has stopped
	// answering is a source whose goroutines are the whole question, and `kubectl logs` on a
	// wedged reflection says nothing at all. Started before the contract is mounted.
	adm, err := adminface.Start(adminface.Spec{
		Name: "iod " + root, Addr: c.adminAddr, Serve: c.addr, Log: log.Printf,
	})
	if err != nil {
		return err
	}
	defer adm.Close()
	adm.SetAddrs(adminface.Addr{Name: "source", Addr: c.addr})

	src = verseio.Throttle(src, c.minInterval)
	mux := http.NewServeMux()
	mux.Handle("/", verseiod.New(src).Handler())
	for pattern, h := range routes {
		mux.Handle(pattern, h)
	}
	log.Printf("%s source on %s — install it with: verse source add <name> %s -root %s",
		root, c.addr, dialable(c.addr), root)
	if c.hintURL == "" && len(routes) > 0 {
		log.Printf("no -hint-url: deliveries will be verified and dropped, and only verse's " +
			"periodic resync will converge")
	}
	return http.ListenAndServe(c.addr, mux)
}

// dialable turns a listen address into the url to install, which is a guess wherever the
// address does not name a host — `:9801` is every interface, and only the operator knows which
// of them verse can reach. Said as `<host>` rather than as `localhost`, because a line that
// prints a url which happens not to work is worse than one that says what it does not know.
func dialable(addr string) string {
	if strings.HasPrefix(addr, ":") {
		return "http://<host>" + addr
	}
	return "http://" + addr
}

// hinter is what a delivery endpoint calls once it has decided which refs went stale.
func (c *common) hinter() verseio.Hinter {
	if c.hintURL == "" {
		return func(context.Context, ...entity.Ref) {}
	}
	return verseio.Hints(c.hintURL, log.Printf)
}

// secret reads a credential from the environment and nowhere else.
//
// NOT A FLAG, and this is the one place that matters: a flag is in the pod spec, in `ps`, and
// in whatever shell history deployed it, while an env var comes from a Secret the cluster
// already knows how to hold. The three sources here differ in exactly one thing — whether they
// need one of these — and that difference is why two of them cannot live inside verse.
func secret(name string) string { return os.Getenv(name) }

func envOr(name, def string) string {
	if v := os.Getenv(name); v != "" {
		return v
	}
	return def
}

// strs collects a repeatable string flag, and splits one on commas.
//
// Both spellings, because both callers are real: a person types `-team ENG -team OPS`, and a
// deployment interpolates one env value into one argument (`-team $[linearTeams]`) — a k8s
// command is a fixed list, so a repeated flag would mean a repeated patch per element.
type strs []string

func (s strs) String() string { return strings.Join(s, ",") }

func (s *strs) Set(v string) error {
	for _, part := range strings.Split(v, ",") {
		if part = strings.TrimSpace(part); part != "" {
			*s = append(*s, part)
		}
	}
	return nil
}
