//go:build linux

package runtimeuser

import (
	"fmt"
	"os"
	"path/filepath"
	"syscall"
)

const unprivilegedID = 65532

// PrepareDataDirectory makes a mounted SQLite directory writable, then
// permanently drops root before the server opens the database or network.
func PrepareDataDirectory(databasePath string) error {
	if os.Geteuid() != 0 {
		return nil
	}
	absolute, err := filepath.Abs(databasePath)
	if err != nil {
		return fmt.Errorf("resolve database path before dropping privileges: %w", err)
	}
	directory := filepath.Dir(absolute)
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return fmt.Errorf("create database directory before dropping privileges: %w", err)
	}
	if err := os.Chown(directory, unprivilegedID, unprivilegedID); err != nil {
		return fmt.Errorf("change database directory owner: %w", err)
	}
	if err := os.Chmod(directory, 0o700); err != nil {
		return fmt.Errorf("protect database directory: %w", err)
	}
	for _, path := range []string{absolute, absolute + "-wal", absolute + "-shm", absolute + "-journal"} {
		if err := protectExistingFile(path); err != nil {
			return err
		}
	}
	syscall.Umask(0o077)
	if err := syscall.Setgroups([]int{}); err != nil {
		return fmt.Errorf("drop supplementary groups: %w", err)
	}
	if err := syscall.Setgid(unprivilegedID); err != nil {
		return fmt.Errorf("drop group privileges: %w", err)
	}
	if err := syscall.Setuid(unprivilegedID); err != nil {
		return fmt.Errorf("drop user privileges: %w", err)
	}
	if os.Geteuid() != unprivilegedID || os.Getegid() != unprivilegedID {
		return fmt.Errorf("privilege drop verification failed: uid=%d gid=%d", os.Geteuid(), os.Getegid())
	}
	return nil
}

func protectExistingFile(path string) error {
	if err := os.Chown(path, unprivilegedID, unprivilegedID); err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("change database file owner: %w", err)
	}
	if err := os.Chmod(path, 0o600); err != nil {
		return fmt.Errorf("protect database file: %w", err)
	}
	return nil
}
