// Package protocol holds the versioned wire types shared by the Bridge, the
// Network Host, the relay, and every client.
//
// Scope of Work §13.9 requires the protocol types to be frozen before Phase 1
// begins. "Frozen" here means: the types in this package may gain new optional
// fields within a major version, but an existing field never changes meaning and
// never changes type. Anything that must change incompatibly gets a new major
// protocol version and a documented migration.
//
// This package deliberately depends on nothing outside the standard library.
package protocol

import "fmt"

// Version numbers for the wire protocol. Major changes are incompatible; minor
// changes are additive and safe for an older peer to ignore.
const (
	VersionMajor = 0
	VersionMinor = 1
)

// Version is the protocol version this build speaks.
func Version() string {
	return fmt.Sprintf("%d.%d", VersionMajor, VersionMinor)
}

// Compatible reports whether a peer advertising major.minor can interoperate
// with this build. Majors must match exactly. A peer with a newer minor is
// acceptable: it may send fields we ignore. A peer with an older minor is also
// acceptable: we must not require fields it does not know about.
func Compatible(major, minor int) bool {
	_ = minor
	return major == VersionMajor
}

// SchemaVersion is carried inside persisted documents (manifests, fixture
// bundles) so a stored file can be migrated independently of the live protocol.
const SchemaVersion = 1
