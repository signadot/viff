// mounter: what the runner would run inside a task's user and mount namespaces (entered with
// nsenter -U -m as the uid that made them), to bind store paths into the task's /nix/store
// while the task runs, with the store itself reachable by no path the task has.
//
//	mounter agent <hidden> <over>
//
// opens <hidden> (the directory bwrap bound the runner's state at), mounts an empty tmpfs over
// <over> (its parent) so no path leads to it any more, and then reads commands on stdin, which
// the runner holds from outside:
//
//	bind <rel> <dst>     clone the mount at <hidden>/<rel> (open_tree), make it read-only
//	                     (mount_setattr), and attach it at <dst> (move_mount); <dst> exists
//	unbind <dst>         detach the mount at <dst>
//	mark <label>         print the label, the commands since the last mark, and their time
//	peek                 print what <over> and <hidden> show by path now
//	quit
//
// The new mount API is used because the agent's /proc/self is not there: it is outside the
// task's pid namespace, so a source named /proc/self/fd/N/<rel> would not resolve, and mount(2)
// takes a path. open_tree takes a directory fd.
package main

import (
	"bufio"
	"fmt"
	"os"
	"strings"
	"syscall"
	"time"
	"unsafe"
)

const (
	sysOpenTree     = 428
	sysMoveMount    = 429
	sysMountSetattr = 442

	openTreeClone       = 1
	atRecursive         = 0x8000
	atEmptyPath         = 0x1000
	moveMountFEmptyPath = 0x4
	mountAttrRdonly     = 0x1
	mountAttrNosuid     = 0x2
	mountAttrNodev      = 0x4
	oPath               = 0x200000 // O_PATH, which package syscall does not name on linux
)

type mountAttr struct {
	attrSet, attrClr, propagation, usernsFd uint64
}

func openTree(dfd int, path string, flags uint) (int, error) {
	p, _ := syscall.BytePtrFromString(path)
	fd, _, e := syscall.Syscall(sysOpenTree, uintptr(dfd), uintptr(unsafe.Pointer(p)), uintptr(flags))
	if e != 0 {
		return -1, e
	}
	return int(fd), nil
}

func moveMount(fromFd int, fromPath string, toFd int, toPath string, flags uint) error {
	fp, _ := syscall.BytePtrFromString(fromPath)
	tp, _ := syscall.BytePtrFromString(toPath)
	_, _, e := syscall.Syscall6(sysMoveMount, uintptr(fromFd), uintptr(unsafe.Pointer(fp)), uintptr(toFd), uintptr(unsafe.Pointer(tp)), uintptr(flags), 0)
	if e != 0 {
		return e
	}
	return nil
}

func setReadOnly(fd int) error {
	a := mountAttr{attrSet: mountAttrRdonly | mountAttrNosuid | mountAttrNodev}
	p, _ := syscall.BytePtrFromString("")
	_, _, e := syscall.Syscall6(sysMountSetattr, uintptr(fd), uintptr(unsafe.Pointer(p)), atEmptyPath, uintptr(unsafe.Pointer(&a)), unsafe.Sizeof(a), 0)
	if e != 0 {
		return e
	}
	return nil
}

func ls(dir string) string {
	es, err := os.ReadDir(dir)
	if err != nil {
		return err.Error()
	}
	names := make([]string, 0, len(es))
	for _, e := range es {
		names = append(names, e.Name())
	}
	if len(names) > 6 {
		names = append(names[:6], fmt.Sprintf("… %d entries", len(es)))
	}
	return "[" + strings.Join(names, " ") + "]"
}

func main() {
	if len(os.Args) < 4 || os.Args[1] != "agent" {
		fmt.Fprintln(os.Stderr, "usage: mounter agent <hidden> <over>")
		os.Exit(2)
	}
	hidden, over := os.Args[2], os.Args[3]
	fmt.Printf("agent: uid=%d euid=%d; %s shows %s\n", os.Getuid(), os.Geteuid(), hidden, ls(hidden))
	hfd, err := syscall.Open(hidden, oPath|syscall.O_DIRECTORY|syscall.O_CLOEXEC, 0)
	if err != nil {
		fmt.Printf("agent: open %s: %v\n", hidden, err)
		os.Exit(1)
	}
	if err := syscall.Mount("none", over, "tmpfs", syscall.MS_RDONLY|syscall.MS_NOSUID|syscall.MS_NODEV, "size=4k,mode=0555"); err != nil {
		fmt.Printf("agent: tmpfs over %s: %v\n", over, err)
		os.Exit(1)
	}
	fmt.Printf("agent: %s hidden; by path %s now shows %s, %s %s\n", hidden, over, ls(over), hidden, ls(hidden))
	sc := bufio.NewScanner(os.Stdin)
	sc.Buffer(make([]byte, 1<<20), 1<<20)
	n, failed := 0, 0
	mark := time.Now()
	for sc.Scan() {
		f := strings.Fields(sc.Text())
		if len(f) == 0 {
			continue
		}
		switch f[0] {
		case "bind":
			if len(f) != 3 {
				fmt.Println("agent: bind <rel> <dst>")
				continue
			}
			err := func() error {
				tfd, err := openTree(hfd, f[1], openTreeClone|atRecursive|syscall.O_CLOEXEC)
				if err != nil {
					return fmt.Errorf("open_tree %s: %w", f[1], err)
				}
				defer syscall.Close(tfd)
				if err := setReadOnly(tfd); err != nil {
					return fmt.Errorf("mount_setattr: %w", err)
				}
				if err := moveMount(tfd, "", -100, f[2], moveMountFEmptyPath); err != nil {
					return fmt.Errorf("move_mount to %s: %w", f[2], err)
				}
				return nil
			}()
			if err != nil {
				failed++
				if failed <= 5 {
					fmt.Printf("agent: %v\n", err)
				}
			} else {
				n++
			}
		case "unbind":
			if err := syscall.Unmount(f[1], syscall.MNT_DETACH); err != nil {
				failed++
				fmt.Printf("agent: unmount %s: %v\n", f[1], err)
			} else {
				n++
			}
		case "mark":
			fmt.Printf("agent: %s: %d done, %d failed, %s\n", strings.Join(f[1:], " "), n, failed, time.Since(mark).Round(time.Millisecond))
			n, failed, mark = 0, 0, time.Now()
		case "peek":
			fmt.Printf("agent: by path %s shows %s, %s %s; through the fd: %s\n", over, ls(over), hidden, ls(hidden), ls("/proc/thread-self/fd/"+fmt.Sprint(hfd)))
		case "quit":
			fmt.Println("agent: quit")
			return
		default:
			fmt.Printf("agent: unknown %q\n", f[0])
		}
	}
}
