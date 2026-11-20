package cmd

import (
	"context"
	"errors"
	"time"

	"connectrpc.com/connect/v2"
	"github.com/spf13/cobra"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	leapmuxv1connect "github.com/leapmux/leapmux/generated/proto/leapmux/v1/leapmuxv1connect"
	"github.com/leapmux/leapmux/internal/cli"
	"github.com/leapmux/leapmux/internal/cli/control"
	"github.com/leapmux/leapmux/util/version"
)

const versionDiagnosticTimeout = 2 * time.Second

// addHubHints makes every leaf under root add the next step of a control
// command to a call that the hub refused: sign in again after a refused
// credential, and compare the versions after a call that the hub does not
// implement.
//
// The hints belong to this tree and not to cli.FromError, which classifies
// the failures of every command: `leapmux worker register` reaches the hub
// with a registration key, and "auth login" cannot fix a refused key.
func addHubHints(root *cobra.Command) {
	if root.Runnable() && !root.HasSubCommands() {
		run := root.RunE
		root.RunE = func(cmd *cobra.Command, args []string) error {
			err := run(cmd, args)
			if needsVersionDiagnostic(err) && usesDirectHub(cmd) {
				return withPeerVersion(cmd.Context(), err, "Hub", func(ctx context.Context) string {
					hub, lookupErr := hubURLFor(cmd)
					if lookupErr != nil {
						return ""
					}
					return httpHubBuild(ctx, hub)
				})
			}
			return withHubHint(err)
		}
	}
	for _, child := range root.Commands() {
		addHubHints(child)
	}
}

// usesDirectHub covers the command groups that use typed Hub clients in
// place of hubCallUnary. That call already knows the transport of its peer.
func usesDirectHub(cmd *cobra.Command) bool {
	for parent := cmd.Parent(); parent != nil; parent = cmd.Parent() {
		if parent.Name() == "control" {
			return cmd.Name() == "admin" || cmd.Name() == "auth"
		}
		cmd = parent
	}
	return false
}

func needsVersionDiagnostic(err error) bool {
	var classified *cli.Error
	return connect.CodeOf(err) == connect.CodeUnimplemented && !errors.As(err, &classified)
}

// withPeerVersion keeps the RPC failure as the result. The build lookup has
// its own short deadline and can only add information to that result.
func withPeerVersion(ctx context.Context, err error, peer string, lookup func(context.Context) string) error {
	if !needsVersionDiagnostic(err) {
		return err
	}
	out := cli.FromError(err).WithHint("CLI build %s", version.Format())
	build := ""
	if lookup != nil && ctx.Err() == nil {
		probeCtx, cancel := context.WithTimeout(ctx, versionDiagnosticTimeout)
		defer cancel()
		build = lookup(probeCtx)
	}
	if build == "" {
		build = "unavailable"
	}
	return out.WithHint("%s build %s", peer, build).
		WithHint("upgrade the CLI or the %s to a build that supports this command", peer)
}

func httpHubBuild(ctx context.Context, hub string) string {
	build, err := fetchHubVersion(ctx, hub)
	if err != nil {
		return ""
	}
	if build.Formatted != "" {
		return build.Formatted
	}
	return build.Version
}

func withHubVersion(ctx context.Context, c *control.Client, err error) error {
	var ipcError *codedRPCError
	if c.IsWorkerIPC() && errors.As(err, &ipcError) && ipcError.Code == "rpc_failed" {
		return (workerCall{c: c}).withVersion(ctx, "", err)
	}
	return withPeerVersion(ctx, err, "Hub", func(ctx context.Context) string {
		if !c.IsWorkerIPC() {
			return httpHubBuild(ctx, c.HubURL)
		}
		var response leapmuxv1.GetSystemInfoResponse
		if err := hubCallLocalIPC(ctx, c, leapmuxv1connect.SystemServiceGetSystemInfoProcedure, &leapmuxv1.GetSystemInfoRequest{}, &response); err != nil {
			return ""
		}
		return version.FormatBuild(response.GetBuild())
	})
}

// withHubHint returns err with the next step that a control command takes
// after a refused call. An error that a command classified itself keeps its
// own hints.
func withHubHint(err error) error {
	var classified *cli.Error
	var connectErr *connect.Error
	if err == nil || errors.As(err, &classified) || !errors.As(err, &connectErr) {
		return err
	}
	out := cli.FromError(err)
	switch out.Code {
	case cli.CodeNotLoggedIn:
		return out.WithHint(`run "leapmux control auth login --hub <url>" to sign in again`)
	case cli.CodeVersionMismatch:
		return out.WithHint(`run "leapmux control version --hub <url>" to compare the CLI with the hub, and upgrade the older one`)
	default:
		return out
	}
}
