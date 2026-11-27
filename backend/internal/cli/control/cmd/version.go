package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"sync"
	"time"

	"github.com/spf13/cobra"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	leapmuxv1connect "github.com/leapmux/leapmux/generated/proto/leapmux/v1/leapmuxv1connect"
	"github.com/leapmux/leapmux/hubtransport"
	"github.com/leapmux/leapmux/internal/cli"
	"github.com/leapmux/leapmux/internal/cli/control"
	"github.com/leapmux/leapmux/internal/worker/controlenv"
	"github.com/leapmux/leapmux/locallisten"
	"github.com/leapmux/leapmux/util/version"
)

// hubVersionTimeout caps the unauthenticated /version probe so a slow
// hub does not stall the CLI when the user just wants to see the
// client identity.
const hubVersionTimeout = 5 * time.Second

func newVersionCommandWithOverride(override func() string) *cobra.Command {
	return cli.NewLeaf(cli.Leaf{
		Use:              "version",
		Short:            "Print the version of this CLI, and of the hub and worker when connected",
		QuietUnsupported: true,
		Examples: []cli.Example{
			{Command: "leapmux version"},
			{Command: "leapmux version --hub https://hub.example.com"},
		},
		Run: func(inv *cli.Invocation) (any, error) {
			return runVersionWithOverride(inv, override)
		},
	})
}

// RenderText prints a concise standalone version line when no Hub or Worker
// is connected, or aligned cli/hub/worker lines when connected.
func (r versionResult) RenderText(w io.Writer) error {
	if r.Hub == nil && r.HubError == "" && r.Worker == nil && r.WorkerError == "" {
		_, err := fmt.Fprintln(w, cli.SanitizeSingleLine(r.CLI.Formatted))
		return err
	}
	if _, err := fmt.Fprintf(w, "cli:    %s\n", cli.SanitizeSingleLine(r.CLI.Formatted)); err != nil {
		return err
	}
	if r.Hub != nil {
		if _, err := fmt.Fprintf(w, "hub:    %s\n", cli.SanitizeSingleLine(r.Hub.Formatted)); err != nil {
			return err
		}
	} else if r.HubError != "" {
		if _, err := fmt.Fprintf(w, "hub:    error (%s)\n", cli.SanitizeSingleLine(r.HubError)); err != nil {
			return err
		}
	}
	if r.Worker != nil {
		if _, err := fmt.Fprintf(w, "worker: %s\n", cli.SanitizeSingleLine(r.Worker.Formatted)); err != nil {
			return err
		}
	} else if r.WorkerError != "" {
		if _, err := fmt.Fprintf(w, "worker: error (%s)\n", cli.SanitizeSingleLine(r.WorkerError)); err != nil {
			return err
		}
	}
	return nil
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
	HubError    string     `json:"hub_error,omitempty"`
	Worker      *buildInfo `json:"worker,omitempty"`
	WorkerError string     `json:"worker_error,omitempty"`
}

// runVersionWithOverride prints the CLI's build identity, plus the hub's (and
// worker's when inside a tab) build identity.
func runVersionWithOverride(inv *cli.Invocation, override func() string) (any, error) {
	cliBuild := cliBuildInfo()
	if override != nil {
		if v := override(); v != "" && v != version.Format() {
			cliBuild = buildInfo{Version: v, Formatted: v}
		}
	}
	out := versionResult{CLI: cliBuild}
	if explicitHub(inv.Command) == "" && os.Getenv(controlenv.SocketVariable) != "" {
		c, err := control.NewLocalClient(os.Getenv(controlenv.SocketVariable), os.Getenv(controlenv.TokenVariable))
		if err != nil {
			out.HubError = err.Error()
			return out, nil
		}
		var (
			sysResp    leapmuxv1.GetSystemInfoResponse
			sysErr     error
			workerResp leapmuxv1.GetWorkerSystemInfoResponse
			workerErr  error
			wg         sync.WaitGroup
		)
		wg.Add(2)
		go func() {
			defer wg.Done()
			hubCtx, cancel := context.WithTimeout(inv.Context, hubVersionTimeout)
			defer cancel()
			sysErr = hubCallLocalIPC(hubCtx, c, leapmuxv1connect.SystemServiceGetSystemInfoProcedure, &leapmuxv1.GetSystemInfoRequest{}, &sysResp)
		}()
		go func() {
			defer wg.Done()
			workerCtx, cancel := context.WithTimeout(inv.Context, hubVersionTimeout)
			defer cancel()
			workerErr = callInnerRPC(workerCtx, c, "", leapmuxv1connect.WorkerSystemServiceGetWorkerSystemInfoProcedure, &leapmuxv1.GetWorkerSystemInfoRequest{}, &workerResp)
		}()
		wg.Wait()
		if sysErr != nil {
			out.HubError = sysErr.Error()
		} else if sysResp.GetBuild() != nil {
			b := buildInfoOf(sysResp.GetBuild())
			out.Hub = &b
		}
		if workerErr != nil {
			out.WorkerError = workerErr.Error()
		} else if workerResp.GetBuild() != nil {
			b := buildInfoOf(workerResp.GetBuild())
			out.Worker = &b
		}
		return out, nil
	}
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
	return buildInfoOf(version.BuildInfoProto())
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
	defer endpoint.CloseIdleConnections()
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
		return buildInfo{}, fmt.Errorf("hub /version returned %s", resp.Status)
	}
	var body buildInfo
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return buildInfo{}, err
	}
	return body, nil
}
