package cmd

import (
	"encoding/json"
	"net/http"
	"time"

	"github.com/leapmux/leapmux/internal/cli/remote"
	"github.com/leapmux/leapmux/locallisten"
	"github.com/leapmux/leapmux/util/version"
)

// hubVersionTimeout caps the unauthenticated /version probe so a slow
// hub does not stall the CLI when the user just wants to see the
// client identity.
const hubVersionTimeout = 5 * time.Second

// RunVersion prints the CLI's build identity, plus the hub's identity
// when --hub (or LEAPMUX_HUB) is set. The hub probe is unauthenticated
// (the /version endpoint exposes only the build banner) so the command
// works whether or not credentials exist for the hub.
//
// Output shape (JSON envelope):
//
//	{
//	  "data": {
//	    "cli": {"version": "...", "commit": "...", "branch": "...", "build_time": "...", "formatted": "..."},
//	    "hub": {"version": "...", "formatted": "...", ...}    // only when --hub is set and reachable
//	  }
//	}
//
// When --hub is set but the probe fails (network error, non-200
// response, decode failure), the hub field is omitted and a
// non-fatal "hub_error" key carries a one-line description so
// scripts can surface the partial result without re-running.
func RunVersion(rawCtx any, args []string) error {
	cmd := asCtx(rawCtx)
	var hub string
	fs := flagSet(cmd, &hub)
	if err := parseFlags(fs, args, cmd.Description()); err != nil {
		return err
	}

	out := map[string]any{
		"cli": cliVersionFields(),
	}
	if hub != "" {
		hubVer, err := fetchHubVersion(hub)
		if err != nil {
			out["hub_error"] = err.Error()
		} else {
			out["hub"] = hubVer
		}
	}
	return remote.EmitData(out)
}

// cliVersionFields exposes the same fields the hub /version endpoint
// returns, so the data shape is symmetric on both sides.
func cliVersionFields() map[string]string {
	return map[string]string{
		"version":    version.Value,
		"commit":     version.CommitHash,
		"branch":     version.Branch,
		"build_time": version.BuildTime,
		"formatted":  version.Format(),
	}
}

// fetchHubVersion probes the hub's unauthenticated /version endpoint.
// Returns the parsed JSON body on success.
func fetchHubVersion(hubURL string) (map[string]string, error) {
	client := &http.Client{Timeout: hubVersionTimeout}
	resp, err := client.Get(locallisten.JoinPath(hubURL, "/version"))
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil, &hubVersionStatusError{status: resp.Status}
	}
	var body map[string]string
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return nil, err
	}
	return body, nil
}

type hubVersionStatusError struct {
	status string
}

func (e *hubVersionStatusError) Error() string {
	return "hub /version returned " + e.status
}
