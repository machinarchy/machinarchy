// Package buildinfo provides access to build-time information embedded in the binary.
package buildinfo

// These values are overridden by linker flags during builds.
var (
	Version   = "dev"
	Commit    = "unknown"
	BuildTime = "unknown"
)
