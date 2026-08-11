package main

import (
	"fmt"
	"os/exec"
	"sort"
	"strings"
)

// Containment is expressed as a POINT: one coordinate per leaf of a small tree of properties
// — what can reach the task, what it can reach, what it can see, whether it may write it, and
// whether it acts as the runner. Mechanisms (namespaces, seccomp, Landlock, gVisor, uid pools)
// are this file's business and nobody else's.
//
// The leaves are ordered chains, weakest first, and comparison is componentwise: a point is
// at least as contained as another when it is at least as strong at EVERY leaf. That is a
// partial order rather than a ladder, which is the point — "no network, shared filesystem" and
// "proxied network, private filesystem" are genuinely incomparable, and a design that ordered
// them anyway would let a delegation weaken the floor while appearing to tighten it.
//
// See docs/working/runner-descriptor.md. This is a SKETCH: the vocabulary is here and real,
// and only the weakest rung of each chain is implemented (below).
var chains = map[string][]string{
	"network.ingress":     {"full", "restricted", "none"},
	"network.egress":      {"full", "restricted", "none"},
	"filesystem.scope":    {"host", "shared", "private"},
	"filesystem.readOnly": {"no", "yes"},
	"principal":           {"runner", "distinct"},
}

// Point is a coordinate per leaf. A leaf a point does not name reads as the runner's default
// for that leaf — the same reading for comparison AND for execution, because a coordinate is a
// specification something is built from rather than a description of something already built.
// Two readings is the natural first answer and is a bug: the profile then compares one way and
// runs the other.
type Point map[string]string

// AtLeast reports whether p is at least as contained as q at every leaf — the check a
// delegation makes, and the check an environment's declared profile makes against what a
// runner can build.
func (p Point) AtLeast(q Point) bool {
	for leaf := range chains {
		if rank(leaf, p[leaf]) < rank(leaf, q[leaf]) {
			return false
		}
	}
	return true
}

// rank is a leaf's position in its chain; an unknown or empty value ranks 0 (weakest), so a
// coordinate nobody set never counts as containment nobody built.
func rank(leaf, value string) int {
	for i, v := range chains[leaf] {
		if v == value {
			return i
		}
	}
	return 0
}

func (p Point) String() string {
	out := make([]string, 0, len(p))
	for leaf, v := range p {
		out = append(out, leaf+"="+v)
	}
	sort.Strings(out)
	return strings.Join(out, " ")
}

// Confiner builds a point: it configures a command so that running it lands the task at that
// containment, and REFUSES a point it cannot build.
//
// Refusing is the whole contract. A confiner that quietly did less than it was asked would
// give an operator a task running with a weaker floor than the environment declared, which is
// undetectable from the outside — the direction that costs. A refusal, by contrast, comes back
// as a failed spawn carrying this runner's own words, onto the flow.
//
// It is also the seam where the DEPLOYMENT decides how a point is built, and the mechanism
// varies by more than taste. On a single host it is a uid pool and namespaces; on Kubernetes it
// is a sandbox the platform hands you (docs/roadmap/expose.md surveys Agent Sandbox, which
// reaches several of these leaves at once with gVisor or a lightweight VM). Neither is more
// correct than the other, and neither changes anything above this line: the classes, the
// comparison and the refusal are the same either way, which is what makes the vocabulary worth
// having separately from any one substrate.
type Confiner interface {
	// Provides reports whether this confiner can build p, and says what it cannot if not.
	Provides(p Point) error
	// Confine configures cmd to run at p, rooted at dir. Called only after Provides said yes.
	Confine(cmd *exec.Cmd, p Point, dir string) error
}

// hostConfiner is the CHANGE THIS of this file: it provides the weakest rung of every leaf and
// refuses everything else, which is what a runner on a developer's laptop can honestly claim.
// A task under it runs as the runner, in the runner's own filesystem, with whatever network the
// runner has — so `filesystem.scope: shared` is real (each task gets its own worktree
// directory) and nothing else is.
//
// What could replace it depends entirely on where the runner is deployed, and no single mechanism
// covers the cases:
//
//   - a pod on Linux: ROOTLESS RUNC — an OCI spec with mount/pid/ipc/uts/user namespaces and a
//     read-only rootfs reaches `filesystem.scope: private` and `filesystem.readOnly`. Note that
//     the usual UID mapping is size ONE (container 0 → the outer process's own uid), which is
//     namespace isolation WITHOUT a distinct host principal — so it does not reach
//     `principal: distinct`, and does not need to. Those leaves are independent, which is why
//     they are separate leaves.
//   - a Linux host without an image runtime: `bwrap`, same leaves.
//   - macOS: neither exists. `sandbox-exec` does, and an SBPL profile denying `file-write*`
//     outside the workspace reaches `filesystem.scope` and nothing else — no namespaces, no
//     principal, no network.
//   - `principal: distinct` is a per-task uid from a pool, and is orthogonal to all of the above.
//
// A confiner claims the leaves its mechanism actually reaches and refuses the rest. That is the
// point of naming leaves separately rather than shipping "a sandbox": "sandboxed" is a word, and
// four of these five mechanisms would qualify for it while providing different things.
type hostConfiner struct{}

// hostProvides is the strongest point this confiner can build. `filesystem.scope: shared` is
// claimable because a worktree per task is a real directory per task; everything else is at
// its weakest rung, which is to say absent.
var hostProvides = Point{
	"network.ingress":     "full",
	"network.egress":      "full",
	"filesystem.scope":    "shared",
	"filesystem.readOnly": "no",
	"principal":           "runner",
}

func (hostConfiner) Provides(p Point) error {
	var missing []string
	for leaf := range chains {
		if rank(leaf, p[leaf]) > rank(leaf, hostProvides[leaf]) {
			missing = append(missing, fmt.Sprintf("%s: asked for %q, this runner provides at most %q",
				leaf, p[leaf], hostProvides[leaf]))
		}
	}
	if len(missing) == 0 {
		return nil
	}
	sort.Strings(missing)
	return fmt.Errorf("this runner cannot provide the containment this environment declares:\n  %s",
		strings.Join(missing, "\n  "))
}

// Confine sets the working directory and, critically, the ENVIRONMENT — which is the one part
// of the floor a plain process can enforce. Everything else this confiner declines to claim.
func (hostConfiner) Confine(cmd *exec.Cmd, _ Point, dir string) error {
	cmd.Dir = dir
	return nil
}

// bwrapConfiner builds a point with bubblewrap: namespaces and bind mounts, no daemon, no
// image runtime, no privilege. It is the Linux single-host implementation, and what a container
// image can carry (see the Dockerfile beside this program).
//
// It reaches `filesystem.scope: private` — a read-only system rootfs with the workspace as the
// only writable path — and, when asked, no network. It does NOT reach `principal: distinct`:
// `--unshare-user` maps the caller to itself, so the host uid is unchanged. That is the same
// property rootless runc has with a size-one UID mapping, and it is why the two are separate
// leaves: a private filesystem does not require a distinct principal, and taking the first
// without the second is what keeps an agent's credential working.
type bwrapConfiner struct{ bin string }

// bwrapProvides is the strongest point bwrap can build, per leaf. The combination rule below
// is what a per-leaf ceiling cannot express.
var bwrapProvides = Point{
	"network.ingress":     "none",
	"network.egress":      "none",
	"filesystem.scope":    "private",
	"filesystem.readOnly": "yes",
	"principal":           "runner",
}

func (b bwrapConfiner) Provides(p Point) error {
	var missing []string
	for leaf := range chains {
		if rank(leaf, p[leaf]) > rank(leaf, bwrapProvides[leaf]) {
			missing = append(missing, fmt.Sprintf("%s: asked for %q, bwrap provides at most %q",
				leaf, p[leaf], bwrapProvides[leaf]))
		}
	}
	// The combination rule, and the reason a catalogue of buildable POINTS is not the same
	// thing as a product of chains. bwrap's network control is one switch: `--unshare-net`
	// takes ingress and egress together. So it builds (full, full) and (none, none), and
	// nothing in between or either side of it — an egress-only restriction needs a proxy or a
	// configured netns, which is a different mechanism rather than a stronger flag.
	in, eg := p["network.ingress"], p["network.egress"]
	switch {
	case in == "restricted" || eg == "restricted":
		missing = append(missing, "network: bwrap has no `restricted` — it can share the "+
			"host's network or unshare it, and an allowlist is a proxy's job")
	case in != eg && in != "" && eg != "":
		missing = append(missing, fmt.Sprintf(
			"network: asked for ingress %q with egress %q, and --unshare-net takes both together",
			in, eg))
	}
	if len(missing) == 0 {
		return nil
	}
	sort.Strings(missing)
	return fmt.Errorf("this runner cannot provide the containment this environment declares:\n  %s",
		strings.Join(missing, "\n  "))
}

// Confine rewrites cmd to run under bwrap. Called only after Provides said yes.
func (b bwrapConfiner) Confine(cmd *exec.Cmd, p Point, dir string) error {
	args := []string{
		// The child dies with this runner rather than outliving it as an orphan holding a
		// workspace open.
		"--die-with-parent",
		"--unshare-pid", "--unshare-ipc", "--unshare-uts",
		// A fresh /proc is what makes --unshare-pid mean anything, and /dev is the minimal
		// device set bwrap synthesizes rather than binding the host's.
		"--proc", "/proc", "--dev", "/dev",
		// Scratch that is not the workspace, so a program with nowhere to write does not
		// decide the workspace is the place for it.
		"--tmpfs", "/tmp",
	}
	// The system, read-only. Named explicitly rather than binding / because binding the root
	// read-only and then re-binding a writable subtree is the shape that quietly re-exposes
	// whatever was not thought of.
	for _, ro := range []string{"/usr", "/bin", "/sbin", "/lib", "/lib64", "/etc", "/opt"} {
		args = append(args, "--ro-bind-try", ro, ro)
	}
	// The workspace, writable, and the only thing that is.
	args = append(args, "--bind", dir, dir, "--chdir", dir)
	if p["filesystem.readOnly"] == "yes" {
		args[len(args)-4] = "--ro-bind" // the workspace bind, made read-only
	}
	if p["network.egress"] == "none" {
		args = append(args, "--unshare-net")
	}
	args = append(args, "--")
	args = append(args, cmd.Args...)

	cmd.Path = b.bin
	cmd.Args = append([]string{b.bin}, args...)
	cmd.Dir = dir
	return nil
}

// confinerNamed resolves the -confine flag. A name this build does not have, or a mechanism
// this machine cannot run, fails at STARTUP rather than at the first spawn — the alternative
// being a runner that accepts work it will not contain and says nothing until something
// escapes.
func confinerNamed(name string) (Confiner, error) {
	switch name {
	case "host":
		return hostConfiner{}, nil
	case "bwrap":
		bin, err := exec.LookPath("bwrap")
		if err != nil {
			return nil, fmt.Errorf("-confine bwrap: bubblewrap is not on PATH (%w). It is "+
				"Linux-only; on macOS neither it nor runc exists, and `-confine host` claims "+
				"almost nothing on purpose", err)
		}
		return bwrapConfiner{bin: bin}, nil
	default:
		return nil, fmt.Errorf("-confine %q: want host or bwrap", name)
	}
}
