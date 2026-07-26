package harness

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/signadot/verse/process"
)

// demoDeriver maps the model's tool vocabulary onto typed capabilities so the
// gate vector can match on them. This is the pluggable classification seam — the
// place a foreign agent's tools become the platform's typed capabilities.
type demoDeriver struct{}

func (demoDeriver) Derive(tu *process.ToolUse) {
	if len(tu.Effects) > 0 {
		return
	}
	if tu.CallAgent != nil {
		tu.Effects = append(tu.Effects, process.Effect{Verb: process.CallAgent, Resource: process.AgentRes(tu.CallAgent.Agent)})
		return
	}
	path, _ := tu.Input["path"].(string)
	switch tu.Name {
	case "read_file", "list_dir":
		if path != "" {
			tu.Effects = append(tu.Effects, process.Effect{Verb: process.Read, Resource: process.File(path)})
		}
	case "write_file":
		if path != "" {
			tu.Effects = append(tu.Effects, process.Effect{Verb: process.Write, Resource: process.File(path)})
		}
	case "run_bash":
		cmd, _ := tu.Input["command"].(string)
		tu.Effects = append(tu.Effects, process.Effect{Verb: process.Exec, Resource: process.Resource{Kind: process.KindProcess, Ref: cmd}})
	}
}

// ExecRecord is the structured record of one tool execution — the executor's
// read surface. Process output is captured with stdout and stderr kept
// SEPARATE, alongside the exit code; the full (untruncated) streams are retained
// here even though the model is fed a truncated rendering.
type ExecRecord struct {
	Seq    int
	Tool   string
	Args   map[string]any
	Stdout string
	Stderr string
	Exit   *int   // process exit code (run_bash); nil for non-process tools
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
// every execution. When content is non-nil, read_file reads federated files through
// the verse (the docd content controller) instead of the local filesystem, so the
// process need not share a disk with the files it validates.
type fsExec struct {
	root    string
	log     io.Writer
	content process.ContentReader
	seq     int
	records []ExecRecord
}

// Records returns the full execution ledger (part of the read surface).
func (e *fsExec) Records() []ExecRecord { return e.records }

func (e *fsExec) Execute(ctx context.Context, tu process.ToolUse) (process.StepResult, []process.ToolUse, error) {
	e.seq++
	rec := ExecRecord{Seq: e.seq, Tool: tu.Name, Args: tu.Input}
	fmt.Fprintf(e.log, "   ▶ %s %s\n", tu.Name, compactArgs(tu.Input))

	switch tu.Name {
	case "read_file":
		if e.content != nil {
			// Federated: read the file's bytes through the verse (docd content
			// controller), by its path relative to the federated tree.
			path := str(tu.Input, "path")
			switch body, ok, err := e.content.ReadContent(path); {
			case err != nil:
				// A failed lookup is NOT a missing file, and must not be reported as
				// one: an agent told the file does not exist will go and create it.
				rec.Err = "reading " + path + " through the verse failed: " + err.Error()
			case !ok:
				rec.Err = "file not found in the verse: " + path
			default:
				rec.Stdout = body
			}
		} else if p, err := e.path(tu, "path"); err != nil {
			rec.Err = err.Error()
		} else if b, err := os.ReadFile(p); err != nil {
			rec.Err = err.Error()
		} else {
			rec.Stdout = string(b)
		}
	case "list_dir":
		if p, err := e.path(tu, "path"); err != nil {
			rec.Err = err.Error()
		} else if entries, err := os.ReadDir(p); err != nil {
			rec.Err = err.Error()
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
		rec.Err = fmt.Sprintf("unknown tool %q", tu.Name)
	}

	e.echo(rec)
	e.records = append(e.records, rec)
	return process.StepResult{Use: tu, Output: rec.modelContent()}, nil, nil
}

// runBash executes a command with stdout and stderr captured into SEPARATE
// buffers and the real exit code recorded.
func (e *fsExec) runBash(ctx context.Context, command string, rec *ExecRecord) {
	c := exec.CommandContext(ctx, "bash", "-lc", command)
	c.Dir = e.root
	var stdout, stderr bytes.Buffer
	c.Stdout, c.Stderr = &stdout, &stderr
	err := c.Run()
	rec.Stdout, rec.Stderr = stdout.String(), stderr.String()
	code := 0
	if err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			code = ee.ExitCode()
		} else {
			rec.Err = err.Error() // spawn / cancellation failure, not a process exit
			return
		}
	}
	rec.Exit = &code
}

func (e *fsExec) path(tu process.ToolUse, key string) (string, error) {
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

// safePath confines a model-supplied path to root (rejects traversal/escape).
func safePath(root, p string) (string, error) {
	if strings.TrimSpace(p) == "" {
		return "", fmt.Errorf("empty path")
	}
	full := filepath.Join(root, p)
	rel, err := filepath.Rel(root, full)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("path %q escapes the working directory", p)
	}
	return full, nil
}

func compactArgs(m map[string]any) string {
	b, _ := json.Marshal(m)
	return truncate(string(b), 200)
}
