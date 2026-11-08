// nstest: step 0c for pkg-nix (2b5art3x). Measures whether verse's runner, as it runs (uid
// 65532, every capability dropped, inside the pod's user namespace), can build a task's
// user+mount namespace, hold it, and later re-enter it as its owner to add a read-only bind the
// task sees — the mechanism the userns-ownership rework needs. It models the runner building the
// sandbox's namespace itself rather than letting bwrap create one.
//
// Modes:
//
//	nstest run <workdir> <src>   the runner: builds the holder (a child born via clone in a fresh
//	                             user+mount namespace, single uid 65532->65532, as bwrap maps),
//	                             then re-enters through nsenter to bind <src> into the holder's
//	                             mount namespace at <workdir>/store/pkg, and checks the result.
//	nstest holder <workdir>      the task's namespace holder; born by run with Cloneflags set, so
//	                             it is the namespace's creator. Proves creator-caps by mounting a
//	                             tmpfs at <workdir>/store, then waits.
//	nstest bind <src> <dst>      run by nsenter already inside the holder's namespaces: binds <src>
//	                             at <dst> read-only via mount(2), reporting each syscall's result.
//	nstest look <workdir>        run by nsenter inside the holder's namespaces: reports what the
//	                             task sees at <workdir>/store/pkg and whether it can write it.
package main

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"time"
)

func capEff() string {
	b, _ := os.ReadFile("/proc/self/status")
	for _, ln := range strings.Split(string(b), "\n") {
		if strings.HasPrefix(ln, "CapEff:") {
			return strings.TrimSpace(strings.TrimPrefix(ln, "CapEff:"))
		}
	}
	return "?"
}

func nsid(kind string) string {
	s, err := os.Readlink("/proc/self/ns/" + kind)
	if err != nil {
		return err.Error()
	}
	return s
}

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "nstest run|holder|bind|look ...")
		os.Exit(2)
	}
	switch os.Args[1] {
	case "run":
		run(os.Args[2], os.Args[3])
	case "holder":
		holder(os.Args[2])
	case "bind":
		bind(os.Args[2], os.Args[3])
	case "look":
		look(os.Args[2])
	default:
		fmt.Fprintf(os.Stderr, "nstest: unknown mode %q\n", os.Args[1])
		os.Exit(2)
	}
}

// holder: born by run with CLONE_NEWUSER|CLONE_NEWNS and the uid map already written, so it is
// the namespace's creator and holds capabilities in it. It mounts the task's store view and waits.
func holder(workdir string) {
	fmt.Printf("[holder] born: uid=%d CapEff=%s user=%s mnt=%s\n", os.Getuid(), capEff(), nsid("user"), nsid("mnt"))
	// Make the store view a private mount point owned by this namespace: a tmpfs the runner will
	// bind package paths under. If this mount succeeds, the creator holds CAP_SYS_ADMIN here.
	store := workdir + "/store"
	os.MkdirAll(store, 0o755)
	if err := syscall.Mount("none", store, "tmpfs", 0, "mode=0755"); err != nil {
		fmt.Printf("[holder] tmpfs at store FAILED: %v (the creator lacks mount caps)\n", err)
	} else {
		fmt.Printf("[holder] tmpfs at store ok (creator can mount); store=%s\n", store)
	}
	os.MkdirAll(store+"/pkg", 0o755)
	// Signal ready, then wait for run to finish its injection and checks.
	os.WriteFile(workdir+"/ready", []byte("x"), 0o644)
	for i := 0; i < 1200; i++ {
		if _, err := os.Stat(workdir + "/quit"); err == nil {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	fmt.Printf("[holder] store/pkg now holds: %v\n", ls(store+"/pkg"))
}

func ls(dir string) []string {
	es, err := os.ReadDir(dir)
	if err != nil {
		return []string{err.Error()}
	}
	var out []string
	for _, e := range es {
		out = append(out, e.Name())
	}
	return out
}

// bind: already inside the holder's namespaces (nsenter put us here). Bind src at dst read-only
// through mount(2), which checks capabilities rather than euid==0.
func bind(src, dst string) {
	fmt.Printf("[bind] inside: uid=%d CapEff=%s user=%s mnt=%s\n", os.Getuid(), capEff(), nsid("user"), nsid("mnt"))
	os.MkdirAll(dst, 0o755)
	if err := syscall.Mount(src, dst, "", syscall.MS_BIND|syscall.MS_REC, ""); err != nil {
		fmt.Printf("[bind] mount(2) bind %s -> %s FAILED: %v\n", src, dst, err)
		os.Exit(1)
	}
	fmt.Printf("[bind] mount(2) bind ok: %s -> %s\n", src, dst)
	if err := syscall.Mount("", dst, "", syscall.MS_BIND|syscall.MS_REMOUNT|syscall.MS_RDONLY|syscall.MS_NOSUID|syscall.MS_NODEV, ""); err != nil {
		fmt.Printf("[bind] remount ro FAILED: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("[bind] remount ro ok\n")
}

// look: inside the holder's namespaces, as the task would see things.
func look(workdir string) {
	pkg := workdir + "/store/pkg"
	fmt.Printf("[look] uid=%d CapEff=%s; %s holds %v\n", os.Getuid(), capEff(), pkg, ls(pkg))
	if b, err := os.ReadFile(pkg + "/hello"); err == nil {
		fmt.Printf("[look] read pkg/hello: %q\n", strings.TrimSpace(string(b)))
	} else {
		fmt.Printf("[look] read pkg/hello: %v\n", err)
	}
	werr := os.WriteFile(pkg+"/planted", []byte("x"), 0o644)
	fmt.Printf("[look] write into pkg: %v\n", werr)
}

func run(workdir, src string) {
	fmt.Printf("[run] the runner: uid=%d CapEff=%s user=%s (this is how the runner runs)\n", os.Getuid(), capEff(), nsid("user"))
	os.RemoveAll(workdir)
	os.MkdirAll(workdir, 0o755)
	os.Remove(workdir + "/ready")
	os.Remove(workdir + "/quit")

	self, _ := os.Executable()
	// Build the holder: a child born in a fresh user+mount namespace, setgroups denied. The runner
	// (this process) stays in the parent user namespace and writes the child's map. Two mappings
	// are tested, by MAPROOT: the single non-root self-map bwrap uses (65532->65532, nothing in
	// the sandbox privileged), and the root-in-userns map (0->65532) rootless containers use, under
	// which a process entering as uid 0 keeps capabilities across execve.
	inside := os.Getuid()
	if os.Getenv("MAPROOT") == "1" {
		inside = 0
	}
	fmt.Printf("[run] holder maps inside-uid %d -> host-uid %d\n", inside, os.Getuid())
	cmd := exec.Command(self, "holder", workdir)
	cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
	cmd.SysProcAttr = &syscall.SysProcAttr{
		Cloneflags:                 syscall.CLONE_NEWUSER | syscall.CLONE_NEWNS,
		GidMappingsEnableSetgroups: false,
		UidMappings:                []syscall.SysProcIDMap{{ContainerID: inside, HostID: os.Getuid(), Size: 1}},
		GidMappings:                []syscall.SysProcIDMap{{ContainerID: inside, HostID: os.Getgid(), Size: 1}},
	}
	if err := cmd.Start(); err != nil {
		fmt.Printf("[run] starting the holder FAILED: %v\n", err)
		os.Exit(1)
	}
	pid := cmd.Process.Pid
	defer func() { os.WriteFile(workdir+"/quit", []byte("x"), 0o644); cmd.Wait() }()

	ready := false
	for i := 0; i < 100; i++ {
		if _, err := os.Stat(workdir + "/ready"); err == nil {
			ready = true
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if !ready {
		fmt.Printf("[run] the holder never signalled ready\n")
		os.Exit(1)
	}
	hu, _ := os.Readlink(fmt.Sprintf("/proc/%d/ns/user", pid))
	hm, _ := os.Readlink(fmt.Sprintf("/proc/%d/ns/mnt", pid))
	fmt.Printf("[run] holder pid=%d user=%s mnt=%s\n", pid, hu, hm)

	// The source the runner will bind in (a stand-in store path), and a file the task should see.
	os.MkdirAll(src, 0o755)
	os.WriteFile(src+"/hello", []byte("a package file"), 0o644)

	// Re-enter the holder's user+mount namespaces through nsenter (single-threaded C, which the
	// setns of a user namespace requires) and run `nstest bind` there. --preserve-credentials
	// keeps euid 65532: the test is whether the runner, as the namespace's owner from the parent,
	// gains capabilities on entry.
	// Under the non-root map, keep euid 65532 (the runner's). Under the root map, enter as uid 0
	// inside the namespace (nsenter's default), where capabilities survive the helper's execve.
	nsArgs := []string{"-U", "-m", "-t", fmt.Sprint(pid)}
	if os.Getenv("MAPROOT") == "1" {
		nsArgs = append(nsArgs, "--setuid", "0", "--setgid", "0")
	} else {
		nsArgs = append(nsArgs, "--preserve-credentials")
	}
	fmt.Printf("[run] --- entering the holder's namespaces to bind, via nsenter %s\n", strings.Join(nsArgs, " "))
	inj := exec.Command("nsenter", append(nsArgs, "--", self, "bind", src, workdir+"/store/pkg")...)
	inj.Stdout, inj.Stderr = os.Stdout, os.Stderr
	injErr := inj.Run()
	fmt.Printf("[run] the injection exited: %v\n", injErr)

	// What the task sees now.
	fmt.Printf("[run] --- what the task sees, via nsenter into the same namespaces\n")
	lk := exec.Command("nsenter", "-U", "-m", "-t", fmt.Sprint(pid), "--preserve-credentials",
		"--", self, "look", workdir)
	lk.Stdout, lk.Stderr = os.Stdout, os.Stderr
	lk.Run()

	// Does the bind leak to the pod? The runner's own mount namespace should not show it.
	fmt.Printf("[run] --- in the runner's own namespace, store/pkg holds: %v (a leak would list hello)\n", ls(workdir+"/store/pkg"))
}
