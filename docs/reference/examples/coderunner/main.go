// Command coderunner is the verse runner under the name and flags the deployment starts it
// with: `runnerd verse` is the same program (package verserunner), and this translates the
// coderunner's command line to its own.
//
//	go run ./docs/reference/examples/coderunner -config coderunner.tony -addr :9802
//	verse runner add code http://localhost:9802
//
// What a coding runner is — a workspace per task over the repositories the spawn names, a
// process per task inside a sandbox, a push the runner makes from its mirror when the spawn
// permits it — is the verse runner's, and the examples README describes it. This program goes
// with the deployment that names it.
package main

import (
	"flag"
	"log"
	"os"

	"github.com/signadot/verse/runner/runnerd/verserunner"
)

func main() {
	log.SetFlags(0)
	log.SetPrefix("coderunner: ")
	// `-task` is the task process, which takes no flags: everything it is told arrives on stdin.
	if len(os.Args) > 1 && os.Args[1] == "-task" {
		if err := verserunner.Main([]string{"-task"}); err != nil {
			log.Fatal(err)
		}
		return
	}
	fs := flag.NewFlagSet("coderunner", flag.ExitOnError)
	addr := fs.String("addr", ":9802", "address to serve the task-control contract on")
	cfgPath := fs.String("config", "coderunner.tony", "the runner's config: environments")
	data := fs.String("data", "", "directory for mirrors and workspaces (default: a temp dir)")
	keep := fs.Bool("keep", false, "keep workspaces after a task ends, for debugging")
	tokenEnv := fs.String("token-env", "CODERUNNER_GIT_TOKEN",
		"environment variable holding the git token this runner fetches and pushes with; a task never holds it")
	confine := fs.String("confine", "host",
		"what confines each task's process: host (claims almost nothing) | bwrap")
	adminAddr := fs.String("admin-addr", os.Getenv("CODERUNNER_ADMIN_ADDR"),
		"admin/pprof listen address, or off to disable (default: the -addr port + 1)")
	if err := fs.Parse(os.Args[1:]); err != nil {
		log.Fatal(err)
	}
	sandbox := "none"
	if *confine == "bwrap" {
		sandbox = "bwrap"
	} else if *confine != "host" {
		log.Fatalf("-confine %q: want host or bwrap", *confine)
	}
	args := []string{"-config", *cfgPath, "-listen", *addr, "-sandbox", sandbox, "-git-token-env", *tokenEnv}
	if *data != "" {
		args = append(args, "-data", *data)
	}
	if *keep {
		args = append(args, "-keep")
	}
	if *adminAddr != "" {
		args = append(args, "-admin-addr", *adminAddr)
	}
	if err := verserunner.Main(args); err != nil {
		log.Fatal(err)
	}
}
