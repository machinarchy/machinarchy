// Package buildinfo provides access to build-time information embedded in the binary.
package buildinfo

// These values are overridden by linker flags during builds.
var (
	Version   = "dev"
	Commit    = "unknown"
	BuildTime = "unknown"
)

// API generations. The controller serves, and the CLI speaks, every generation
// from APIMin to APIMax; raise APIMax for an additive break-free change only
// when the CLI also learns it, and APIMin when an old shape is dropped.
const (
	APIMin = 1
	APIMax = 1
)
