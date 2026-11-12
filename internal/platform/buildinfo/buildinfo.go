// Package buildinfo carries link-time build metadata for diagnostics.
package buildinfo

// Build identity, overridden via -ldflags at build time; defaults target
// local builds.
var (
	BuildID   = "unknown"
	Commit    = "unknown"
	BuildTime = "unknown"
)
