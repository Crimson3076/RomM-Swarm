//go:build unix

package destination

import (
	"fmt"
	"os"
	"syscall"
)

// filesystemSupported reports whether this platform can answer the questions
// filesystem publication mode depends on.
const filesystemSupported = true

// deviceOf returns the filesystem device a path lives on.
//
// This is what makes "same filesystem" a fact rather than a guess. Comparing
// path prefixes would be wrong in both directions: a bind mount puts two
// filesystems under one prefix, and a symlink puts one filesystem under two.
func deviceOf(path string) (uint64, error) {
	info, err := os.Stat(path)
	if err != nil {
		return 0, fmt.Errorf("destination: inspecting %s: %w", path, err)
	}
	sys, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return 0, fmt.Errorf("destination: cannot determine the filesystem of %s on this platform", path)
	}
	return uint64(sys.Dev), nil
}

// freeSpace returns the bytes available to an unprivileged writer at a path.
//
// Deliberately the unprivileged figure rather than the raw free figure: on many
// filesystems a portion is reserved for root, and a Bridge that believed it had
// that space would fail partway through a large transfer with the disk
// apparently not full.
func freeSpace(path string) (int64, error) {
	var st syscall.Statfs_t
	if err := syscall.Statfs(path, &st); err != nil {
		return 0, fmt.Errorf("destination: checking free space on %s: %w", path, err)
	}
	return int64(st.Bavail) * int64(st.Bsize), nil
}
