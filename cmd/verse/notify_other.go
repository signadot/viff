//go:build !(linux || freebsd || netbsd || openbsd || dragonfly || darwin)

package main

import (
	"fmt"
	"runtime"
)

func newAlerter() (alerter, error) {
	return nil, fmt.Errorf("verse has no desktop notifications on %s", runtime.GOOS)
}
