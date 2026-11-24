// Command viff-secret-exec runs one command with one secret of the runner's store, from a shell
// of the task's, as if the command ran in place: its stdout and stderr are this command's, its
// exit status this command's, and with `-stdin` this command's stdin is its input.
//
//	viff-secret-exec -name npm-token -- npm publish --ignore-scripts
//	viff-secret-exec -name npm-token -dir packages/a -env NODE_OPTIONS=--x -timeout 120 -- npm view .
//	printf 'yes\n' | viff-secret-exec -name signadot-key -stdin -- signadot sandbox apply -f -
//
// It is the task's `secret-exec` tool and what a caller would otherwise do around each call of
// it: the call, which the task's autonomy decides as it decides any; a connection to each of the
// run's sockets, the outputs read from their start; the run's end, read from its `.exit` file;
// and, on SIGINT, SIGTERM or SIGHUP, `secret-exec-kill` for the run — or the call given up, where
// the command has not started, which withdraws a question still standing about it. The signal is
// not passed to the command: the run is killed, as a run is.
//
// The exit status is the command's, where it exited. Otherwise: 124, the run's timeout killed
// it; 125, this command failed, or the run ended some other way, and stderr says how; 126, the
// call was refused, by a gate or by the runner, and stderr says why; 128 and the signal's number,
// a signal to this command ended the run.
//
// The secret's value is redacted from the output as the tool redacts it, and the command's
// program is a bare name the runner finds, as the tool has it (docs/systems/secret.md). The task
// process answers on the socket $VIFF_TOOLS names, or the one `-socket` does.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"go.viff.dev/viff/pkg/runner/runnerd/calltool"
)

const (
	exitTimeout = 124
	exitFailed  = 125
	exitRefused = 126
	exitSignal  = 128

	execTool = "secret-exec"
	killTool = "secret-exec-kill"

	// started is how the tool's answer opens for a run that started: the id, these words, then
	// the directory its sockets and files are in, which is the rest of the line (stock's
	// execStarted).
	started = ": started, in "

	// poll is how often a file the task's process is about to write is looked for.
	poll = 20 * time.Millisecond
	// killWait bounds the call that kills a run.
	killWait = 30 * time.Second
)

func main() {
	sigs := make(chan os.Signal, 4)
	signal.Notify(sigs, os.Interrupt, syscall.SIGTERM, syscall.SIGHUP)
	os.Exit(run(os.Args[1:], os.Stdin, os.Stdout, os.Stderr, sigs))
}

// vars are the `-env NAME=value` flags.
type vars map[string]any

func (v vars) String() string { return "" }
func (v vars) Set(s string) error {
	name, value, ok := strings.Cut(s, "=")
	if !ok || name == "" {
		return fmt.Errorf("%q is not NAME=value", s)
	}
	v[name] = value
	return nil
}

// run is the command: args are the arguments after the program's name, sigs the signals that end
// the run, and the result is its exit status.
func run(args []string, stdin io.Reader, stdout, stderr io.Writer, sigs <-chan os.Signal) int {
	fs := flag.NewFlagSet("viff-secret-exec", flag.ContinueOnError)
	fs.SetOutput(stderr)
	socket := fs.String("socket", os.Getenv(calltool.Env), "the socket the task process answers on (default $"+calltool.Env+")")
	name := fs.String("name", "", "the secret's name, one the environment gives (required)")
	dir := fs.String("dir", "", "where the command runs, a path under the workspace root (default the root)")
	timeout := fs.Int("timeout", 0, "seconds the command may run (default and most are the tool's)")
	input := fs.Bool("stdin", false, "give the command this command's stdin, where the environment's shape admits it")
	env := vars{}
	fs.Var(env, "env", "a plain variable set beside the secret, NAME=value (repeat for each)")
	fs.Usage = func() {
		fmt.Fprint(stderr, "usage: viff-secret-exec -name <secret> [-dir <dir>] [-env NAME=value]... [-timeout <seconds>] [-stdin] [--] <program> [<argument>]...\n\n")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return exitFailed
	}
	fail := func(format string, a ...any) int {
		fmt.Fprintf(stderr, "viff-secret-exec: "+format+"\n", a...)
		return exitFailed
	}
	switch {
	case *socket == "":
		return fail("no socket: $%s is unset and -socket was not given; this command runs in a viff task's workspace, "+
			"where the task process sets it", calltool.Env)
	case *name == "":
		return fail("-name is required: the secret the command runs with")
	case fs.NArg() == 0:
		return fail("no command: the program and its arguments follow the flags")
	}
	argv := make([]any, fs.NArg())
	for i, a := range fs.Args() {
		argv[i] = a
	}
	in := map[string]any{"name": *name, "argv": argv}
	if *dir != "" {
		in["dir"] = *dir
	}
	if len(env) > 0 {
		in["env"] = map[string]any(env)
	}
	if *timeout != 0 {
		in["timeout"] = int64(*timeout)
	}
	if *input {
		in["stdin"] = true
	}
	c := calltool.Dial(*socket)

	// The call, which answers once the command has started. A signal before then gives the call
	// up; one that crossed with the answer finds a run, and kills it.
	type called struct {
		res calltool.Result
		err error
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	answered := make(chan called, 1)
	go func() {
		res, err := c.Call(ctx, execTool, in)
		answered <- called{res, err}
	}()
	var a called
	var sig os.Signal
	select {
	case a = <-answered:
	case sig = <-sigs:
		cancel()
		a = <-answered
		if a.err != nil {
			return signalled(sig)
		}
	}
	switch {
	case a.err != nil:
		return fail("%v", a.err)
	case a.res.Refused:
		fmt.Fprintln(stderr, "viff-secret-exec: "+a.res.Out)
		return exitRefused
	}
	first, _, _ := strings.Cut(a.res.Out, "\n")
	id, where, ok := strings.Cut(first, started)
	if !ok {
		// The call answered an end and no run: the command did not start.
		return fail("%s", a.res.Out)
	}
	at := func(suffix string) string { return filepath.Join(where, id+suffix) }
	kill := func() bool {
		kctx, done := context.WithTimeout(context.Background(), killWait)
		defer done()
		if _, err := c.Call(kctx, killTool, map[string]any{"id": id}); err != nil {
			fmt.Fprintf(stderr, "viff-secret-exec: killing %s: %v\n", id, err)
			return false
		}
		return true
	}
	if sig != nil && !kill() {
		return signalled(sig)
	}

	if *input {
		// The input ends with this command's stdin, or with the run: nothing waits on it.
		go func() {
			conn, err := net.Dial("unix", at(".in.sock"))
			if err != nil {
				fmt.Fprintf(stderr, "viff-secret-exec: the command's stdin: %v\n", err)
				return
			}
			defer conn.Close()
			_, _ = io.Copy(conn, stdin)
		}()
	}
	// Each output, to its end. The socket gives the stream from its start for as long as this
	// reads it, and the file holds what the socket did not give: a run that ended before the
	// connection, or one whose reader was dropped.
	ended := make(chan struct{})
	var copying sync.WaitGroup
	problems := make(chan error, 2)
	for _, s := range []struct {
		sock, file string
		w          io.Writer
	}{{at(".sock"), at(".out"), stdout}, {at(".err.sock"), at(".err"), stderr}} {
		copying.Add(1)
		go func() {
			defer copying.Done()
			var n int64
			if conn, err := net.Dial("unix", s.sock); err == nil {
				n, _ = io.Copy(s.w, conn)
				conn.Close()
			}
			<-ended
			if err := rest(s.w, s.file, n); err != nil {
				problems <- err
			}
		}()
	}
	// The end is in its file once the command has ended.
	end := make(chan string, 1)
	go func() {
		for {
			if b, err := os.ReadFile(at(".exit")); err == nil {
				close(ended)
				copying.Wait()
				end <- strings.TrimSpace(string(b))
				return
			}
			time.Sleep(poll)
		}
	}()
	var said string
wait:
	for {
		select {
		case said = <-end:
			break wait
		case s := <-sigs:
			if sig != nil || !kill() {
				// A second signal, or a run that could not be killed: nothing is waited for.
				return signalled(s)
			}
			sig = s
		}
	}
	close(problems)
	for err := range problems {
		fmt.Fprintf(stderr, "viff-secret-exec: %v\n", err)
	}
	if sig != nil {
		return signalled(sig)
	}
	if rest, ok := strings.CutPrefix(said, "exited "); ok {
		status, _, _ := strings.Cut(rest, ";")
		if n, err := strconv.Atoi(status); err == nil {
			if status != rest {
				// It exited, and something more is said of its end.
				fmt.Fprintf(stderr, "viff-secret-exec: %s: %s\n", id, said)
			}
			return n
		}
	}
	fmt.Fprintf(stderr, "viff-secret-exec: %s: %s\n", id, said)
	if strings.HasPrefix(said, "killed after ") {
		return exitTimeout
	}
	return exitFailed
}

// rest copies to w what the stream's file holds past the first n bytes, which the socket gave.
func rest(w io.Writer, file string, n int64) error {
	f, err := os.Open(file)
	if err != nil {
		if n > 0 {
			return nil // the socket gave it, and there is no file to hold more
		}
		return err
	}
	defer f.Close()
	if _, err := f.Seek(n, io.SeekStart); err != nil {
		return err
	}
	_, err = io.Copy(w, f)
	return err
}

// signalled is the exit status of a run a signal to this command ended.
func signalled(sig os.Signal) int {
	if s, ok := sig.(syscall.Signal); ok {
		return exitSignal + int(s)
	}
	return exitSignal
}
