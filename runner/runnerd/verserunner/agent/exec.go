package agent

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/signadot/verse/codec"
	"github.com/signadot/verse/task"
)

// demoDeriver maps the model's tool vocabulary onto typed capabilities so the
// gate vector can match on them. This is the pluggable classification seam — the
// place a foreign agent's tools become the platform's typed capabilities.
//
// It classifies the runtime's own tools and hands everything else to task.BasicDeriver,
// which is where the four the harness offers are classified. One classification, because a
// scripted run proposes the same run_bash under task's supervisor, and the envelope and the
// gates must read the call the same way whichever driver proposed it.
type demoDeriver struct {
	// extra is the runtime's own vocabulary (see Tool). Classification is delegated to
	// whoever added the tool, because the effect a runtime's action has is exactly the part
	// this package cannot know — a push resolves to a ref only the runner's environment names.
	extra map[string]Tool
}

func (d demoDeriver) Derive(tu *task.ToolUse) {
	if len(tu.Effects) > 0 {
		return
	}
	if t, ok := d.extra[tu.Name]; ok {
		if t.Derive != nil {
			t.Derive(tu)
		}
		return
	}
	task.BasicDeriver{}.Derive(tu)
}

// fsProblem turns a filesystem error into something an agent can act on, in the vocabulary it
// actually has.
//
// Two things were wrong with handing back the os error, and a drive found both in one line:
//
//	ERROR: read /Users/scott/.verse-coderunner/workspaces/docsdrift-e1f3.../verse/trigger: is a directory
//
// It does not say what to do. "is a directory" is a fact about the path and not an instruction,
// so the model spends a turn working out that the tool it wanted was list_dir — and the whole
// point of the offered-tool set is that the loop knows which tools exist and the model has to be
// told. Naming the sibling tool costs one clause and saves a model call.
//
// And it speaks in ABSOLUTE paths, when the frame told the agent every path is relative to its
// working directory. That is a contradiction the agent has to resolve, it publishes the runner's
// filesystem layout onto a read plane other people watch, and under `filesystem.scope: private`
// it would be a path that does not exist from where the task is standing. So every message here
// is phrased in the path the agent ASKED for, including the fall-through.
func fsProblem(tool, asked, resolved string, err error) string {
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return fmt.Sprintf("%s: no such file or directory", asked)
	case errors.Is(err, fs.ErrPermission):
		return fmt.Sprintf("%s: permission denied", asked)
	}
	// Stat rather than errno matching: EISDIR and ENOTDIR are the two confusions worth naming,
	// and asking the filesystem what the path IS answers both without a platform table.
	if st, serr := os.Stat(resolved); serr == nil {
		switch {
		case st.IsDir() && tool == "read_file":
			return fmt.Sprintf("%s is a directory, not a file — use list_dir to see what is in it", asked)
		case !st.IsDir() && tool == "list_dir":
			return fmt.Sprintf("%s is a file, not a directory — use read_file to read it", asked)
		}
	}
	return strings.ReplaceAll(err.Error(), resolved, asked)
}

// ExecRecord is the structured record of one tool execution — the executor's
// read surface. Task output is captured with stdout and stderr kept
// SEPARATE, alongside the exit code; the full (untruncated) streams are retained
// here even though the model is fed a truncated rendering.
type ExecRecord struct {
	Seq    int
	Tool   string
	Args   map[string]any
	Stdout string
	Stderr string
	Exit   *int   // task exit code (run_bash); nil for non-task tools
	Err    string // executor-level failure (path escape, fs error, spawn failure)
}

// maxToolResult bounds a tool result fed to the model (any tool — read_file,
// list_dir, run_bash …). This is the *agent's* view, so it is generous: it fits
// essentially any source file whole (~43× the old 6 KB cap) while staying within a
// large-context model's window, so it doesn't trade a truncation for a
// context-overflow API error. The tiny read-plane/log previews (evTrunc, echo) are a
// separate concern — those are for observers, not the agent.
const maxToolResult = 256 << 10 // 256 KiB

// modelContent renders the record as the tool_result the model sees. stdout and
// stderr are labeled separately; each is bounded by maxToolResult so the agent sees a
// whole file, not a slice.
func (r ExecRecord) modelContent() string {
	if r.Err != "" {
		return "ERROR: " + r.Err
	}
	var b strings.Builder
	if r.Exit != nil {
		fmt.Fprintf(&b, "exit: %d\n", *r.Exit)
	}
	if s := strings.TrimRight(r.Stdout, "\n"); s != "" {
		if r.Exit != nil {
			b.WriteString("stdout:\n")
		}
		b.WriteString(truncate(s, maxToolResult) + "\n")
	}
	if s := strings.TrimRight(r.Stderr, "\n"); s != "" {
		b.WriteString("stderr:\n" + truncate(s, maxToolResult) + "\n")
	}
	if b.Len() == 0 {
		return "(ok)"
	}
	return strings.TrimRight(b.String(), "\n")
}

// fsExec performs the tools for real, confined to root, keeping a full record of
// every execution.
type fsExec struct {
	root    string
	log     io.Writer
	extra   map[string]Tool // the runtime's own tools (see Tool)
	env     []string        // the variables run_bash passes through (see Config.Env)
	set     []string        // the variables run_bash is given outright (see Config.Set)
	seq     int
	records []ExecRecord
}

// Records returns the full execution ledger (part of the read surface).
func (e *fsExec) Records() []ExecRecord { return e.records }

func (e *fsExec) Execute(ctx context.Context, tu task.ToolUse) (task.StepResult, []task.ToolUse, error) {
	e.seq++
	rec := ExecRecord{Seq: e.seq, Tool: tu.Name, Args: tu.Input}
	fmt.Fprintf(e.log, "   ▶ %s %s\n", tu.Name, compactArgs(tu.Input))

	switch tu.Name {
	case "read_file":
		if p, err := e.path(tu, "path"); err != nil {
			rec.Err = err.Error()
		} else if b, err := os.ReadFile(p); err != nil {
			rec.Err = fsProblem("read_file", str(tu.Input, "path"), p, err)
		} else {
			rec.Stdout = string(b)
		}
	case "list_dir":
		if p, err := e.path(tu, "path"); err != nil {
			rec.Err = err.Error()
		} else if entries, err := os.ReadDir(p); err != nil {
			rec.Err = fsProblem("list_dir", str(tu.Input, "path"), p, err)
		} else {
			var sb strings.Builder
			for _, en := range entries {
				kind := "f"
				if en.IsDir() {
					kind = "d"
				}
				fmt.Fprintf(&sb, "%s %s\n", kind, en.Name())
			}
			rec.Stdout = sb.String()
		}
	case "write_file":
		if p, err := e.path(tu, "path"); err != nil {
			rec.Err = err.Error()
		} else if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			rec.Err = err.Error()
		} else {
			content := str(tu.Input, "content")
			if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
				rec.Err = err.Error()
			} else {
				rec.Stdout = fmt.Sprintf("wrote %d bytes to %s", len(content), str(tu.Input, "path"))
			}
		}
	case "run_bash":
		e.runBash(ctx, str(tu.Input, "command"), &rec)
	default:
		t, ok := e.extra[tu.Name]
		if !ok || t.Do == nil {
			rec.Err = fmt.Sprintf("unknown tool %q", tu.Name)
			break
		}
		// The runtime's own action. Its ledger entry is the ordinary one — nothing on the read
		// plane or in the record should be able to tell which side of this seam a tool came
		// from — and the three outcomes go three ways on purpose:
		//
		//   err      the action FAILED. Propagated rather than folded into the record, because
		//            that is what makes a closing act fail its task: a run whose work did not
		//            leave is not a run that succeeded.
		//   blocked  the far side REFUSED. The loop's own word for it, so the driver re-plans
		//            rather than retrying something it has been told no about, and a closing
		//            act ends `done` carrying the refusal.
		//   neither  it happened.
		out, blocked, err := t.Do(ctx, tu)
		if err != nil {
			rec.Err = err.Error()
			e.echo(rec)
			e.records = append(e.records, rec)
			return task.StepResult{Use: tu}, nil, err
		}
		rec.Stdout = out
		e.echo(rec)
		e.records = append(e.records, rec)
		return task.StepResult{Use: tu, Output: rec.modelContent(), Blocked: blocked, Note: out}, nil, nil
	}

	e.echo(rec)
	e.records = append(e.records, rec)
	// Exit and Err ride the result as well as the text, for the same reason: this executor used
	// to say what went wrong only in prose, because its driver reads prose. A driver that is not
	// a model cannot read "exec: bash: not found" out of a paragraph, and a call list whose
	// command never started would otherwise run its remaining calls and report done.
	//
	// Neither changes what the AGENT sees materially — resultContent already renders an Err as
	// "ERROR: …", and the record text still carries the detail — and neither changes the loop,
	// which fails a task on the error an Executor RETURNS, not on the one its result carries.
	// The two are different questions: this action failed, versus this executor is broken.
	res := task.StepResult{Use: tu, Output: rec.modelContent(), Exit: rec.Exit}
	if rec.Err != "" {
		res.Err = errors.New(rec.Err)
	}
	return res, nil, nil
}

// orphanWaitDelay is how long a finished-or-killed command is given to let go of its output
// before this gives up on it. It is what makes `Execute` honour its context, which the
// task.Executor interface states as an obligation and which this executor did NOT meet.
//
// The trap is that exec.CommandContext LOOKS like it meets it. Cancelling the context kills
// bash — but stdout here is a buffer rather than a file, so os/exec puts a pipe in between and
// copies on a goroutine, and Wait waits for that COPY rather than for the process. Anything the
// command backgrounded inherits the pipe and holds it open, so Wait never returns:
// `run_bash {command: "sleep 300 & echo started"}` was a task that could not be cancelled, ever.
// Not a corner case — `npm run dev &` and every other detached server is this shape.
//
// Two seconds, and the number is doing less than it looks. It applies ONLY when something still
// holds the pipe after the process is gone, which means an orphan: an ordinary command's pipes
// close at exit and never wait at all. So it is "how long to wait for an orphan to flush",
// where too short truncates a straggler and too long stalls a task that deliberately detached
// something. It is not a command timeout — a command nobody cancels still runs as long as it
// likes, which is a policy question for whoever installs the executor.
const orphanWaitDelay = 2 * time.Second

// runBash executes a command with stdout and stderr captured into SEPARATE
// buffers and the real exit code recorded. The shell's environment is the declared passthrough
// and nothing else: this process is frequently the one holding the model credentials the agent
// runs on, and a shell that inherited them would hand them to the agent.
func (e *fsExec) runBash(ctx context.Context, command string, rec *ExecRecord) {
	c := exec.CommandContext(ctx, "bash", "-lc", command)
	c.WaitDelay = orphanWaitDelay
	c.Dir = e.root
	c.Env = append(task.ShellEnv(e.env), e.set...)
	var stdout, stderr bytes.Buffer
	c.Stdout, c.Stderr = &stdout, &stderr
	err := c.Run()
	rec.Stdout, rec.Stderr = stdout.String(), stderr.String()
	code := 0
	if err != nil {
		var ee *exec.ExitError
		switch {
		case errors.As(err, &ee):
			code = ee.ExitCode()
		case errors.Is(err, exec.ErrWaitDelay):
			// The COMMAND succeeded; something it left running still holds the pipe. Reporting
			// that as an executor failure would tell the agent its command broke when it did
			// not, so it is an ordinary exit-zero result with the caveat attached — the agent
			// needs to know the output may be short, and it needs to know why.
			rec.Stderr = strings.TrimRight(rec.Stderr, "\n") +
				"\n(the command exited but left something running that still holds its output; " +
				"anything printed after this was not captured)"
		default:
			rec.Err = err.Error() // spawn failure, not a task exit
			return
		}
	}
	rec.Exit = &code
}

func (e *fsExec) path(tu task.ToolUse, key string) (string, error) {
	return safePath(e.root, str(tu.Input, key))
}

// echo prints the execution result live to the terminal — exit code and each
// stream, labeled and separated.
func (e *fsExec) echo(rec ExecRecord) {
	if rec.Err != "" {
		fmt.Fprintf(e.log, "   ┆ error: %s\n", rec.Err)
		return
	}
	if rec.Exit != nil {
		fmt.Fprintf(e.log, "   ┆ exit %d\n", *rec.Exit)
	}
	echoStream(e.log, "stdout", rec.Stdout)
	echoStream(e.log, "stderr", rec.Stderr)
}

func echoStream(w io.Writer, name, s string) {
	s = strings.TrimRight(s, "\n")
	if s == "" {
		return
	}
	for _, line := range strings.Split(truncate(s, 2000), "\n") {
		fmt.Fprintf(w, "   ┆ %-6s %s\n", name, line)
	}
}

// safePath confines a model-supplied path to root. It rejects traversal (`../..`) and, since
// the lexical check alone is not enough, escape through a SYMLINK.
//
// The lexical check was all there was, and a symlink walked straight past it: `link/pwned`
// contains no `..`, so `filepath.Join` and `filepath.Rel` both approve it, and the write then
// follows the link wherever it goes. That is not a hypothetical shape. A working directory here
// is frequently a git checkout, and git checks symlinks out as symlinks — so a repository
// carrying one is an escape that needs no shell, no traversal and no cleverness from the model.
//
// So the path is also resolved. EvalSymlinks needs something that exists, and a write commonly
// names something that does not yet, so the DEEPEST EXISTING ANCESTOR is resolved and the
// not-yet-created tail rejoined to it. The root is resolved too, and must be: /var is a symlink
// to /private/var on macOS, so comparing a resolved candidate against an unresolved root would
// refuse every legitimate write under a temp dir.
//
// What this does not do is close the race. A symlink planted between this check and the open
// would still be followed, and closing that needs the whole walk to be openat2/O_NOFOLLOW rather
// than a check followed by an ordinary open. It is not reachable through the tools this package
// offers — none of them creates a symlink — so it is a hazard only where `run_bash` is, and a
// task with a shell has larger problems than this function.
func safePath(root, p string) (string, error) {
	if strings.TrimSpace(p) == "" {
		return "", fmt.Errorf("empty path")
	}
	full := filepath.Join(root, p)
	if escapes(root, full) {
		return "", fmt.Errorf("path %q escapes the working directory", p)
	}
	realRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		return "", fmt.Errorf("working directory %q: %w", root, err)
	}
	real, err := resolveExisting(full)
	if err != nil {
		return "", fmt.Errorf("path %q: %w", p, err)
	}
	if escapes(realRoot, real) {
		return "", fmt.Errorf("path %q escapes the working directory through a symlink", p)
	}
	return full, nil
}

// escapes reports whether target lies outside root, lexically.
func escapes(root, target string) bool {
	rel, err := filepath.Rel(root, target)
	return err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// resolveExisting resolves the symlinks in p as far as p exists, and rejoins whatever tail does
// not exist yet. A write names a file that is about to be created; every directory above it is
// already there, and those are the ones a symlink can hide in.
func resolveExisting(p string) (string, error) {
	tail := ""
	for cur := p; ; {
		resolved, err := filepath.EvalSymlinks(cur)
		if err == nil {
			return filepath.Join(resolved, tail), nil
		}
		if !errors.Is(err, os.ErrNotExist) {
			return "", err
		}
		parent := filepath.Dir(cur)
		if parent == cur { // reached the filesystem root without finding anything that exists
			return "", fmt.Errorf("no part of it exists")
		}
		tail = filepath.Join(filepath.Base(cur), tail)
		cur = parent
	}
}

func compactArgs(m map[string]any) string {
	b, _ := codec.MarshalWire(m, codec.Tony)
	return truncate(string(b), 200)
}
