module github.com/Crimson3076/RomM-Swarm

go 1.24

// Phase 0 is deliberately dependency-free. The verification harness, protocol
// types, and RomM capability probe are all built on the standard library so the
// Phase 0 evidence can be reproduced in any environment without a module proxy.
// Dependencies arrive with Phase 1 (Bridge persistence) and Phase 2 (Host).
