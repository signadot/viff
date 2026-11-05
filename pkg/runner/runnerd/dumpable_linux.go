package runnerd

import "golang.org/x/sys/unix"

// undumpable makes this process non-dumpable: its /proc/<pid>/environ and mem, and ptrace, are
// refused to processes of the same uid without CAP_SYS_PTRACE. A task's process holds the
// runner's secrets in its memory, and the task's shell runs as the same uid, in the same pid
// namespace; without this, only the kernel's ptrace_scope would stand between them.
func undumpable() error {
	return unix.Prctl(unix.PR_SET_DUMPABLE, 0, 0, 0, 0)
}
