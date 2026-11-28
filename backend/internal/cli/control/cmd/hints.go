package cmd

import (
	"context"
	"errors"
	"fmt"
	"time"

	"connectrpc.com/connect/v2"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	leapmuxv1connect "github.com/leapmux/leapmux/generated/proto/leapmux/v1/leapmuxv1connect"
	"github.com/leapmux/leapmux/internal/cli"
	"github.com/leapmux/leapmux/internal/cli/control"
	"github.com/leapmux/leapmux/locallisten"
	"github.com/leapmux/leapmux/util/version"
)

const versionDiagnosticTimeout = 2 * time.Second

// hubHintError attaches CLI hints to an error chain without eagerly converting
// it into a *cli.Error, so cli.FromError classifies the underlying error once
// and preserves wrappers such as *codedRPCError and *retryReceiptError.
type hubHintError struct {
	cause error
	hints []string
}

var _ cli.HintProvider = (*hubHintError)(nil)

func (e *hubHintError) Error() string      { return e.cause.Error() }
func (e *hubHintError) Unwrap() error      { return e.cause }
func (e *hubHintError) CLIHints() []string { return e.hints }

// stripHubHintError removes any transport-added *hubHintError in err's chain
// (including one wrapped by Connect's client stream) so a caller that builds
// its own *cli.Error with a command-specific hint does not duplicate the
// generic transport hint.
func stripHubHintError(err error) error {
	var hinted *hubHintError
	if errors.As(err, &hinted) {
		return hinted.cause
	}
	return err
}

// decoratePeerError attaches transport-owned version diagnostics and Hub hints
// to ConnectRPC and WebSocket errors returned by p.
//
// For *control.HubClient, CodeUnimplemented triggers a Hub /version diagnostic
// probe and CodeUnauthenticated attaches a Hub-scoped login hint.
//
// For *control.WorkerIPCClient, CodeUnimplemented is left for the RPC-layer
// wrappers (withHubVersion / workerCall.withVersion) so they can distinguish a
// local Worker IPC refusal from an in-band Hub or Worker refusal without
// recursing into GetWorkerSystemInfo. CodeUnauthenticated still receives the
// local login hint immediately.
func decoratePeerError(ctx context.Context, p control.Peer, err error) error {
	if err == nil {
		return nil
	}
	switch p.(type) {
	case *control.HubClient:
		if needsVersionDiagnostic(err) {
			return withHubVersion(ctx, p, err)
		}
		return withHubHintForPeer(p, err)
	case *control.WorkerIPCClient:
		if connect.CodeOf(err) == connect.CodeUnauthenticated {
			return withHubHintForPeer(p, err)
		}
		return err
	default:
		return withHubHintForPeer(p, err)
	}
}

func needsVersionDiagnostic(err error) bool {
	var classified *cli.Error
	var hinted *hubHintError
	return connect.CodeOf(err) == connect.CodeUnimplemented && !errors.As(err, &classified) && !errors.As(err, &hinted)
}

// withPeerVersion keeps the RPC failure as the result. The build lookup has
// its own short deadline and can only add information to that result.
func withPeerVersion(ctx context.Context, err error, peer string, lookup func(context.Context) string) error {
	if !needsVersionDiagnostic(err) {
		return err
	}
	build := ""
	if lookup != nil && ctx.Err() == nil {
		probeCtx, cancel := context.WithTimeout(ctx, versionDiagnosticTimeout)
		defer cancel()
		build = lookup(probeCtx)
	}
	if build == "" {
		build = "unavailable"
	}
	return &hubHintError{
		cause: err,
		hints: []string{
			fmt.Sprintf("CLI build %s", version.Format()),
			fmt.Sprintf("%s build %s", peer, build),
			fmt.Sprintf("upgrade the CLI or the %s to a build that supports this command", peer),
		},
	}
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

func withHubVersion(ctx context.Context, p control.Peer, err error) error {
	if !needsVersionDiagnostic(err) {
		return withHubHintForPeer(p, err)
	}
	var ipcError *codedRPCError
	if ipc, ok := p.(*control.WorkerIPCClient); ok && errors.As(err, &ipcError) && ipcError.Code == "rpc_failed" {
		return (workerCall{ipc: ipc}).withVersion(ctx, "", err)
	}
	return withPeerVersion(ctx, err, "Hub", func(ctx context.Context) string {
		switch c := p.(type) {
		case *control.HubClient:
			return httpHubBuild(ctx, c.HubURL)
		case *control.WorkerIPCClient:
			var response leapmuxv1.GetSystemInfoResponse
			if err := hubCallLocalIPC(ctx, c, leapmuxv1connect.SystemServiceGetSystemInfoProcedure, &leapmuxv1.GetSystemInfoRequest{}, &response); err != nil {
				return ""
			}
			return version.FormatBuild(response.GetBuild())
		default:
			return ""
		}
	})
}

func needsHubHint(err error) bool {
	var classified *cli.Error
	var hinted *hubHintError
	var connectErr *connect.Error
	if err == nil || errors.As(err, &classified) || errors.As(err, &hinted) || !errors.As(err, &connectErr) {
		return false
	}
	code := cli.FromError(err).Code
	return code == cli.CodeNotLoggedIn || code == cli.CodeVersionMismatch
}

func attachHubHint(err error, hub string, isLocal bool) error {
	switch cli.FromError(err).Code {
	case cli.CodeNotLoggedIn:
		if isLocal {
			return &hubHintError{cause: err, hints: []string{`run "leapmux auth login --hub <url>" to sign in again`}}
		}
		return &hubHintError{cause: err, hints: []string{fmt.Sprintf(`run "leapmux auth login --hub %s" to sign in again`, hub)}}
	case cli.CodeVersionMismatch:
		if isLocal && hub == "<url>" {
			return &hubHintError{cause: err, hints: []string{`run "leapmux version" to compare the CLI with the hub, and upgrade the older one`}}
		}
		return &hubHintError{cause: err, hints: []string{fmt.Sprintf(`run "leapmux version --hub %s" to compare the CLI with the hub, and upgrade the older one`, hub)}}
	default:
		return err
	}
}

// withHubHintForPeer attaches Hub login or version hints using the peer that
// executed the call, avoiding command-flag or environment re-derivation.
func withHubHintForPeer(p control.Peer, err error) error {
	if !needsHubHint(err) {
		return err
	}
	hub := "<url>"
	isLocal := false
	switch c := p.(type) {
	case *control.HubClient:
		if c != nil && c.HubURL != "" {
			hub = c.HubURL
			isLocal = locallisten.IsLocal(c.HubURL)
		}
	case *control.WorkerIPCClient:
		isLocal = true
	}
	return attachHubHint(err, hub, isLocal)
}
