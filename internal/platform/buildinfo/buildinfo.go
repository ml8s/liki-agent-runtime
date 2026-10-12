// Package buildinfo carries link-time build metadata for diagnostics.
package buildinfo

// Build metadata, overridden via -ldflags at build time; defaults target local
// builds.
var (
	Version   = "local"
	Commit    = "unknown"
	BuildTime = "unknown"
)
