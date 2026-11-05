//go:build !linux

package runnerd

// undumpable does nothing where there is no procfs and no prctl.
func undumpable() error { return nil }
