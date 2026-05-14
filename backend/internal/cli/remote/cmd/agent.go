package cmd

import (
	"context"
	"errors"
	"flag"
	"io"
	"os"
	"strings"

	"google.golang.org/protobuf/proto"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/cli/remote"
	"github.com/leapmux/leapmux/internal/cli/remote/resolve"
	"github.com/leapmux/leapmux/internal/util/agentlabels"
)

// RunAgentSend forwards a user message to the agent.
func RunAgentSend(rawCtx any, args []string) error {
	var message string
	var stdinFlag bool
	return withResolvedAgent(rawCtx, args, agentScaffoldOpts{
		setup: func(fs *flag.FlagSet) {
			fs.StringVar(&message, "message", "", "user message")
			fs.BoolVar(&stdinFlag, "stdin", false, "read message from stdin")
		},
		validate: func() error {
			if message == "" && stdinFlag {
				buf, err := io.ReadAll(os.Stdin)
				if err != nil {
					return remote.EmitErrorWith("stdin_read_failed", err)
				}
				message = string(buf)
			}
			if message == "" {
				return remote.EmitError("invalid_request", "--message or --stdin is required")
			}
			return nil
		},
		body: func(ctx context.Context, c *remote.Client, workerID, agentID, _ string) error {
			if err := callInnerRPC(ctx, c, workerID, "SendAgentMessage", &leapmuxv1.SendAgentMessageRequest{AgentId: agentID, Content: message}, nil); err != nil {
				return err
			}
			return remote.EmitData(map[string]string{"agent_id": agentID})
		},
	})
}

func RunAgentInterrupt(rawCtx any, args []string) error {
	var reason string
	return withResolvedAgent(rawCtx, args, agentScaffoldOpts{
		setup: func(fs *flag.FlagSet) {
			fs.StringVar(&reason, "reason", "", "audit reason")
		},
		body: func(ctx context.Context, c *remote.Client, workerID, agentID, _ string) error {
			if err := callInnerRPC(ctx, c, workerID, "InterruptAgent", &leapmuxv1.InterruptAgentRequest{AgentId: agentID, Reason: reason}, nil); err != nil {
				return err
			}
			return remote.EmitData(map[string]string{"agent_id": agentID})
		},
	})
}

// RunAgentGet returns the worker-side agent record (settings, status,
// available models). Resolution mirrors `agent send`: --worker-id wins,
// then GetTab on the hub. Implementation reuses ListAgents with a
// single tab id rather than introducing a new GetAgent RPC, since the
// worker already filters by id and we only ever want one row.
func RunAgentGet(rawCtx any, args []string) error {
	return withResolvedAgent(rawCtx, args, agentScaffoldOpts{
		body: func(ctx context.Context, c *remote.Client, workerID, agentID, _ string) error {
			var resp leapmuxv1.ListAgentsResponse
			if err := callInnerRPC(ctx, c, workerID, "ListAgents", &leapmuxv1.ListAgentsRequest{TabIds: []string{agentID}}, &resp); err != nil {
				return err
			}
			for _, a := range resp.GetAgents() {
				if a.GetId() == agentID {
					return remote.EmitData(agentInfoToMap(a))
				}
			}
			return remote.EmitError("not_found", "agent not found or not accessible: "+agentID)
		},
	})
}

// RunAgentProviders lists the agent providers a worker supports.
// Worker selection accepts any universal entity input (--tab-id /
// --worker-id / --workspace-id / ...) via the resolver.
func RunAgentProviders(rawCtx any, args []string) error {
	cmd := asCtx(rawCtx)
	var hub string
	var in resolve.Inputs
	fs := flagSet(cmd, &hub)
	resolve.BindEntityFlags(fs, &in, resolve.FlagOptions{
		HideOrg:      true,
		HideUser:     true,
		FixedTabType: leapmuxv1.TabType_TAB_TYPE_AGENT,
	})
	if err := parseFlags(fs, args, cmd.Description()); err != nil {
		return err
	}
	return resolveAndEmit(hub, resolve.Need{WorkerID: true}, in, func(ctx context.Context, c *remote.Client, got resolve.Resolved) error {
		if err := maybePreflightWorker(ctx, c, got.WorkerID); err != nil {
			return err
		}
		var resp leapmuxv1.ListAvailableProvidersResponse
		if err := callInnerRPC(ctx, c, got.WorkerID, "ListAvailableProviders", &leapmuxv1.ListAvailableProvidersRequest{}, &resp); err != nil {
			return err
		}
		// Each row carries the canonical display name ("Claude Code") plus
		// every alias `--provider` accepts for that enum value, in a
		// deterministic order (canonical first, remainder sorted). The
		// bare int ordinals from the default proto Go -> JSON path are
		// useless to a human; emitting the alias list lets scripts pick
		// a stable form and lets interactive callers see what they can
		// type without grepping the source.
		providers := resp.GetProviders()
		rows := make([]map[string]any, 0, len(providers))
		for _, p := range providers {
			rows = append(rows, map[string]any{
				"name":    agentProviderName(p),
				"aliases": agentlabels.AliasesFor(p),
			})
		}
		return remote.EmitData(rows)
	})
}

// permissionModeApplier matches `callInnerRPCBest`'s signature so
// applyPermissionMode can be unit-tested without standing up a real
// E2EE channel or local-IPC socket.
type permissionModeApplier func(ctx context.Context, c *remote.Client, workerID, method string, in proto.Message, out proto.Message) error

// applyPermissionMode fires `UpdateAgentSettings` to set just the
// permission-mode field. Returns an empty string on success or when
// `mode` is empty (caller didn't pass --permission-mode); returns the
// error's message on failure so the caller can fold it into the
// `agent open` JSON envelope alongside the agent payload.
//
// We deliberately do not roll the agent back when this fails — the
// agent is already running with provider defaults, which is more
// useful than a force-closed tab whose creation just succeeded. The
// caller surfaces the error so scripts can decide whether to retry
// (the apply is idempotent — UpdateAgentSettings with the same
// permission_mode value is safe to re-run).
func applyPermissionMode(ctx context.Context, c *remote.Client, workerID, agentID, mode string, call permissionModeApplier) string {
	if mode == "" {
		return ""
	}
	req := &leapmuxv1.UpdateAgentSettingsRequest{
		AgentId: agentID,
		Settings: &leapmuxv1.AgentSettings{
			PermissionMode: mode,
		},
	}
	if err := call(ctx, c, workerID, "UpdateAgentSettings", req, nil); err != nil {
		return err.Error()
	}
	return ""
}

// providerListerFn matches `callInnerRPCBest`'s signature so
// resolveProvider can be unit-tested without standing up a real E2EE
// channel or local-IPC socket.
type providerListerFn func(ctx context.Context, c *remote.Client, workerID, method string, in proto.Message, out proto.Message) error

// resolveProvider turns the user-supplied --provider value (possibly
// pre-filled from $LEAPMUX_REMOTE_AGENT_PROVIDER) into an
// AgentProvider enum, querying the worker's installed providers when
// the caller passed no value at all.
//
// Three rejection paths, each surfaced as a typed envelope error so
// scripts can branch on `code`:
//
//   - raw != "" and not a known alias → "invalid_request" (lists every
//     alias the parser accepts; the typo is local, no need for an RPC).
//   - raw == "" and the worker reports zero installed providers →
//     "no_providers_installed" with a fix-it hint.
//   - raw == "" and the worker reports more than one →
//     "ambiguous_provider" with the display names of what's installed.
//
// The exactly-one case is the silent success path: a worker with a
// single provider needs no `--provider` flag at all, matching the
// frontend's first-launch UX where the picker auto-selects when only
// one option is available.
func resolveProvider(ctx context.Context, c *remote.Client, workerID, raw string, list providerListerFn) (leapmuxv1.AgentProvider, error) {
	if raw != "" {
		if p, ok := agentlabels.ParseProvider(raw); ok {
			return p, nil
		}
		return 0, remote.EmitError(
			"invalid_request",
			"unknown --provider: "+raw+"; allowed aliases: "+strings.Join(allProviderAliases(), ", "),
		)
	}
	var resp leapmuxv1.ListAvailableProvidersResponse
	if err := list(ctx, c, workerID, "ListAvailableProviders", &leapmuxv1.ListAvailableProvidersRequest{}, &resp); err != nil {
		var coded *codedRPCError
		if errors.As(err, &coded) {
			return 0, remote.EmitErrorWith(coded.Code, coded.Cause)
		}
		return 0, remote.EmitErrorWith("rpc_failed", err)
	}
	switch providers := resp.GetProviders(); len(providers) {
	case 0:
		return 0, remote.EmitError(
			"no_providers_installed",
			"worker has no agent providers installed; install one or pass --provider explicitly",
		)
	case 1:
		return providers[0], nil
	default:
		names := make([]string, 0, len(providers))
		for _, p := range providers {
			names = append(names, agentlabels.DisplayName(p))
		}
		return 0, remote.EmitError(
			"ambiguous_provider",
			"worker supports multiple providers ("+strings.Join(names, ", ")+"); pass --provider to disambiguate",
		)
	}
}

// allProviderAliases returns every alias ParseProvider accepts,
// grouped by provider and ordered deterministically. Used in the
// "unknown --provider" error so the user sees the full set of valid
// inputs without needing to run `agent providers` against a worker.
func allProviderAliases() []string {
	var out []string
	for _, p := range agentlabels.AllProviders() {
		out = append(out, agentlabels.AliasesFor(p)...)
	}
	return out
}
