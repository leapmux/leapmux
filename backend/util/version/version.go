package version

// Value is the build-time version string, set via ldflags or at startup.
var Value = "dev"

// CommitHash is the git commit hash at build time, set via ldflags.
var CommitHash string

// CommitTime is the commit timestamp of the HEAD commit, set via ldflags.
var CommitTime string

// BuildTime is the build timestamp, set via ldflags.
var BuildTime string
