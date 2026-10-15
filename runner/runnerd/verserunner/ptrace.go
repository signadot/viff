package verserunner

import (
	"os"
	"strings"
)

// ptraceScopePath is where Linux publishes the yama setting the CONTAINMENT OF A SECRET rests
// on. Absent on macOS and on a kernel without yama, which is a real answer and not an error.
const ptraceScopePath = "/proc/sys/kernel/yama/ptrace_scope"

// ptraceScope reads it and says what it means, for a log line at startup.
//
// This is the cheap half of a residual, taken on its own and deliberately not made into a
// refusal (git-issue km5mzagq). `principal: runner` — the strongest
// rung any confiner here reaches — means the task process and the AGENT'S SHELL are the same
// principal: `--unshare-user` maps the caller to itself, so nothing about uids or namespaces
// separates a credential in a task's process from a shell the model drives.
//
// What separates them is yama: at 1 or above a process may trace its descendants and not its
// ancestors, and the shell is a descendant. That is a HOST sysctl, outside the sandbox, and
// nothing here checks it — so the containment the model key depends on rests on a setting this
// program never looked at.
//
// Reading it does not make it true. It turns an assumption from unverified into merely accepted,
// which is a different thing, and it costs one file read. Refusing on it wants a leaf an
// environment can ASK for — nothing today declares "I hold a secret this process must contain",
// so a bare refusal either stops launches that never needed it or stops nothing — and that leaf
// is the residual's, not this line's.
func ptraceScope() string {
	raw, err := os.ReadFile(ptraceScopePath)
	if err != nil {
		return "no yama on this host, so a secret in a task process is not separated from the " +
			"agent's shell by ptrace at all"
	}
	switch v := strings.TrimSpace(string(raw)); v {
	case "0":
		return "kernel.yama.ptrace_scope=0: any process of the same uid may attach to any other, " +
			"so a secret in a task process is READABLE from the agent's shell"
	default:
		return "kernel.yama.ptrace_scope=" + v + ": descendants only, which is what keeps a " +
			"secret in a task process out of the agent's shell"
	}
}
