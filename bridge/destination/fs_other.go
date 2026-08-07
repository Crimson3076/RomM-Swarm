//go:build !unix

package destination

import "errors"

// On a platform where the filesystem device and free space cannot be
// determined, filesystem publication mode is refused rather than attempted.
//
// Scope of Work Phase 6: the Bridge "must use the destination-filesystem
// temporary copy and final rename above, or refuse the transfer until a safe
// path is configured." A platform where the same-filesystem preflight cannot run
// is exactly that case. API-only mode is unaffected and remains available.

const filesystemSupported = false

var errUnsupportedPlatform = errors.New(
	"destination: filesystem publication mode is not supported on this platform, because the same-filesystem preflight cannot be performed; use API-only mode")

func deviceOf(string) (uint64, error) { return 0, errUnsupportedPlatform }

func freeSpace(string) (int64, error) { return 0, errUnsupportedPlatform }
