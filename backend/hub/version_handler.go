package hub

import (
	"encoding/json"
	"net/http"

	"github.com/leapmux/leapmux/util/version"
)

// versionHandler serves the unauthenticated /version endpoint.
//
// The endpoint is intentionally not behind a session/bearer check:
// build identity is non-secret (it appears in the startup banner and
// is reported back to authenticated users via AuthService.Validate),
// and `leapmux remote version` benefits from being able to compare
// CLI and hub versions before login (so first-time setup can detect
// a hub/CLI mismatch).
//
// The response shape mirrors `version.Format()` plus its component
// fields so callers can decide whether to render the formatted line
// verbatim or compose their own.
func versionHandler(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]string{
		"version":    version.Value,
		"commit":     version.CommitHash,
		"branch":     version.Branch,
		"build_time": version.BuildTime,
		"formatted":  version.Format(),
	})
}
