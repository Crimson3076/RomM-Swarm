module github.com/Crimson3076/RomM-Swarm

go 1.24

// Phase 0 is deliberately dependency-free. The verification harness, protocol
// types, and RomM capability probe are all built on the standard library so the
// Phase 0 evidence can be reproduced in any environment without a module proxy.
// Dependencies arrive with Phase 1 (Bridge persistence) and Phase 2 (Host).

require (
	github.com/jackc/pgpassfile v1.0.0 // indirect
	github.com/jackc/pgservicefile v0.0.0-20240606120523-5a60cdf6a761 // indirect
	github.com/jackc/pgx/v5 v5.7.6 // indirect
	github.com/jackc/puddle/v2 v2.2.2 // indirect
	golang.org/x/crypto v0.37.0 // indirect
	golang.org/x/sync v0.13.0 // indirect
	golang.org/x/text v0.24.0 // indirect
)
