//go:build !(linux || freebsd || netbsd || openbsd || dragonfly || darwin)

package main

import (
	"fmt"
	"runtime"
)

func newAlerter() (alerter, error) {
	return nil, fmt.Errorf("viff has no desktop notifications on %s", runtime.GOOS)
}
