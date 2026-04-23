// Package version exposes the build-time identity of the running
// binary and a canonical string form used by the startup banner,
// the `leapmux version` CLI, the worker/hub RPC responses, and the
// frontend AboutDialog. Keeping the format in one place prevents
// drift between those surfaces.
package version

import (
	"strings"
	"time"
)

// Value is the build-time version string, set via ldflags or at startup.
var Value = "dev"

// CommitHash is the git commit hash at build time, set via ldflags.
var CommitHash string

// CommitTime is the commit timestamp of the HEAD commit, set via ldflags.
var CommitTime string

// BuildTime is the build timestamp, set via ldflags.
var BuildTime string

// Branch is the git ref (branch name, or tag name when HEAD is on a
// tag) at build time, set via ldflags. Empty when HEAD is detached
// outside any named ref.
var Branch string

// Format returns the canonical single-line identity string, e.g.
//
//	0.0.1-dev · 9c81b87 · feature/foo · Thu, 4/23/2026, 11:45:00 PM KST
//
// Fields are joined with " · " and each is included conditionally:
//
//   - Value is always present (falls back to "dev").
//   - CommitHash is included when set.
//   - Branch is omitted on "main" (the common CI/release case), rendered
//     as "<detached>" when empty but a commit hash is present, and
//     shown verbatim otherwise.
//   - BuildTime is formatted in the local timezone when set.
func Format() string {
	parts := []string{Value}
	if CommitHash != "" {
		parts = append(parts, CommitHash)
	}
	if b := branchDisplay(); b != "" {
		parts = append(parts, b)
	}
	if BuildTime != "" {
		parts = append(parts, FormatLocalTimestamp(BuildTime))
	}
	return strings.Join(parts, " · ")
}

func branchDisplay() string {
	switch Branch {
	case "main":
		return ""
	case "":
		if CommitHash != "" {
			return "<detached>"
		}
		return ""
	default:
		return Branch
	}
}

// FormatLocalTimestamp converts an RFC3339 timestamp to the
// human-readable local form shown in banners and About dialogs. On
// parse failure the input is returned unchanged so we never lose
// information.
func FormatLocalTimestamp(iso string) string {
	if iso == "" {
		return ""
	}
	t, err := time.Parse(time.RFC3339, iso)
	if err != nil {
		return iso
	}
	local := t.Local()
	zone, _ := local.Zone()
	if zone == "" {
		zone = local.Format("-07:00")
	}
	return local.Format("Mon, 1/2/2006, 3:04:05 PM") + " " + zone
}
