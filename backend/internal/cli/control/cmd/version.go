package cmd

import (
	"context"
	"encoding/json"
	"net/http"
	"time"

	"github.com/spf13/cobra"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/hubtransport"
	"github.com/leapmux/leapmux/internal/cli"
	"github.com/leapmux/leapmux/locallisten"
	"github.com/leapmux/leapmux/util/version"
)

// hubVersionTimeout caps the unauthenticated /version probe so a slow
// hub does not stall the CLI when the user just wants to see the
// client identity.
const hubVersionTimeout = 5 * time.Second

func newVersionCommand() *cobra.Command {
	return cli.NewLeaf(cli.Leaf{
		Use:   "version",
		Short: "Print the version of this CLI, and of the hub when one is selected",
		Examples: []cli.Example{
			{Command: "leapmux control version"},
			{Command: "leapmux control version --hub https://hub.example.com"},
		},
		Run: runVersion,
	})
}

// buildInfo is the build of a binary in a result. The CLI's and the hub's
// use the same keys, so a script compares them field by field.
type buildInfo struct {
	Version    string `json:"version"`
	Commit     string `json:"commit,omitempty"`
	CommitTime string `json:"commit_time,omitempty"`
	Branch     string `json:"branch,omitempty"`
	BuildTime  string `json:"build_time,omitempty"`
	Formatted  string `json:"formatted"`
}

// versionResult is the result of `version`.
type versionResult struct {
	CLI buildInfo  `json:"cli"`
	Hub *buildInfo `json:"hub,omitempty"`
	// HubError reports a hub that was selected but did not answer. The
	// command still succeeds, because the CLI's own version is known.
	HubError string `json:"hub_error,omitempty"`
}

// runVersion prints the CLI's build identity, plus the hub's identity when
// a hub is selected: --hub, else the default hub of the stored credentials.
// The hub probe is unauthenticated (the /version endpoint exposes only the
// build banner), so the command works whether or not credentials exist for
// the hub.
func runVersion(inv *cli.Invocation) (any, error) {
	out := versionResult{CLI: cliBuildInfo()}
	hub, err := hubURLFor(inv.Command)
	if err != nil {
		// No hub is selected, which is not a failure of this command.
		return out, nil
	}
	hubBuild, err := fetchHubVersion(inv.Context, hub)
	if err != nil {
		out.HubError = err.Error()
		return out, nil
	}
	out.Hub = &hubBuild
	return out, nil
}

// cliBuildInfo is this binary's build.
func cliBuildInfo() buildInfo {
	return buildInfo{
		Version:   version.Value,
		Commit:    version.CommitHash,
		Branch:    version.Branch,
		BuildTime: version.BuildTime,
		Formatted: version.Format(),
	}
}

// buildInfoOf renders a build that another binary reports. A nil build
// renders as empty values.
func buildInfoOf(b *leapmuxv1.BuildInfo) buildInfo {
	return buildInfo{
		Version:    b.GetVersion(),
		Commit:     b.GetCommitHash(),
		CommitTime: b.GetCommitTime(),
		Branch:     b.GetBranch(),
		BuildTime:  b.GetBuildTime(),
		Formatted:  version.FormatBuild(b),
	}
}

// fetchHubVersion probes the hub's unauthenticated /version endpoint.
// Returns the parsed JSON body on success.
//
// It goes through hubtransport for the same reason every other hub call does:
// a hand-built client cannot dial a `unix:`/`npipe:` hub, so this command used
// to answer "unsupported protocol scheme" against a hub reached over its IPC
// listener.
func fetchHubVersion(ctx context.Context, hubURL string) (buildInfo, error) {
	endpoint, err := hubtransport.New(hubURL)
	if err != nil {
		return buildInfo{}, err
	}
	client := endpoint.UnaryClient(hubVersionTimeout)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, locallisten.JoinPath(endpoint.BaseURL(), "/version"), nil)
	if err != nil {
		return buildInfo{}, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return buildInfo{}, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return buildInfo{}, &hubVersionStatusError{status: resp.Status}
	}
	var body buildInfo
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return buildInfo{}, err
	}
	return body, nil
}

type hubVersionStatusError struct {
	status string
}

func (e *hubVersionStatusError) Error() string {
	return "hub /version returned " + e.status
}
