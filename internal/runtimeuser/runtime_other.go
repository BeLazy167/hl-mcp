//go:build !linux

package runtimeuser

// PrepareDataDirectory is unnecessary outside the Linux container runtime.
func PrepareDataDirectory(string) error { return nil }
