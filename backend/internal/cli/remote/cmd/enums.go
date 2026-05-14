package cmd

import (
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/util/agentlabels"
)

// Enums emitted by protobuf default to integer ordinals through
// encoding/json, which is fine on the wire and terrible for a human
// reading a CLI envelope. These helpers project each enum to the form
// the rest of the product already uses:
//
//   - TabType        -> "agent" / "terminal" / "file" (matches the
//                       --type flag values and $LEAPMUX_REMOTE_TAB_TYPE)
//   - AgentProvider  -> agentlabels.DisplayName, e.g. "Claude Code"
//                       (the canonical display name the frontend
//                       picker shows; parseProvider already accepts it)
//   - AgentStatus    -> lowercase short label ("active", "starting",
//                       "startup_failed", ...)
//   - TerminalStatus -> same shape ("starting", "ready", ...)
//
// Unspecified / unknown enum values collapse to the empty string so
// callers don't have to special-case zero-valued fields when they
// build an output map.

func tabTypeName(t leapmuxv1.TabType) string {
	switch t {
	case leapmuxv1.TabType_TAB_TYPE_AGENT:
		return "agent"
	case leapmuxv1.TabType_TAB_TYPE_TERMINAL:
		return "terminal"
	case leapmuxv1.TabType_TAB_TYPE_FILE:
		return "file"
	default:
		return ""
	}
}

func agentProviderName(p leapmuxv1.AgentProvider) string {
	if p == leapmuxv1.AgentProvider_AGENT_PROVIDER_UNSPECIFIED {
		return ""
	}
	return agentlabels.DisplayName(p)
}

func messageSourceName(s leapmuxv1.MessageSource) string {
	switch s {
	case leapmuxv1.MessageSource_MESSAGE_SOURCE_USER:
		return "user"
	case leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT:
		return "agent"
	case leapmuxv1.MessageSource_MESSAGE_SOURCE_LEAPMUX:
		return "leapmux"
	default:
		return ""
	}
}

func agentStatusName(s leapmuxv1.AgentStatus) string {
	switch s {
	case leapmuxv1.AgentStatus_AGENT_STATUS_ACTIVE:
		return "active"
	case leapmuxv1.AgentStatus_AGENT_STATUS_INACTIVE:
		return "inactive"
	case leapmuxv1.AgentStatus_AGENT_STATUS_STARTING:
		return "starting"
	case leapmuxv1.AgentStatus_AGENT_STATUS_STARTUP_FAILED:
		return "startup_failed"
	default:
		return ""
	}
}

func terminalStatusName(s leapmuxv1.TerminalStatus) string {
	switch s {
	case leapmuxv1.TerminalStatus_TERMINAL_STATUS_STARTING:
		return "starting"
	case leapmuxv1.TerminalStatus_TERMINAL_STATUS_READY:
		return "ready"
	case leapmuxv1.TerminalStatus_TERMINAL_STATUS_STARTUP_FAILED:
		return "startup_failed"
	case leapmuxv1.TerminalStatus_TERMINAL_STATUS_DISCONNECTED:
		return "disconnected"
	case leapmuxv1.TerminalStatus_TERMINAL_STATUS_EXITED:
		return "exited"
	default:
		return ""
	}
}

// parseEnumFlag looks up `value` in `mapping` and returns the matched
// enum + ok. The error path is the caller's: empty `value` is treated
// as "not provided" and returns (zero, false) so caller can apply its
// own default.
//
// Centralises the "constrained --flag string → typed enum + emit
// invalid_request on miss" pattern that previously open-coded a per-
// flag switch for every CLI verb that wanted a typed enum.
func parseEnumFlag[T comparable](value string, mapping map[string]T) (T, bool) {
	var zero T
	if value == "" {
		return zero, false
	}
	v, ok := mapping[value]
	return v, ok
}

// workspaceTabToMap projects a WorkspaceTab into a JSON-friendly map
// with tab_type rendered as a human-readable string. Used by `tab
// list` / `tab get` so the envelope shows "agent" instead of `1`.
func workspaceTabToMap(t *leapmuxv1.WorkspaceTab) map[string]any {
	return map[string]any{
		"tab_id":       t.GetTabId(),
		"tab_type":     tabTypeName(t.GetTabType()),
		"tile_id":      t.GetTileId(),
		"worker_id":    t.GetWorkerId(),
		"workspace_id": t.GetWorkspaceId(),
		"position":     t.GetPosition(),
	}
}

// workspaceTabsToList runs every row in `tabs` through workspaceTabToMap.
// Returns a non-nil zero-length slice when the input is empty, so the
// JSON envelope renders `"tabs": []` rather than `"tabs": null`.
func workspaceTabsToList(tabs []*leapmuxv1.WorkspaceTab) []map[string]any {
	out := make([]map[string]any, 0, len(tabs))
	for _, t := range tabs {
		out = append(out, workspaceTabToMap(t))
	}
	return out
}

// agentInfoToMap projects an AgentInfo into a JSON-friendly map with
// status / agent_provider rendered as strings. The proto carries a
// number of provider-specific nested messages (AvailableModel,
// AvailableOptionGroup, AgentGitStatus); those still go through the
// default encoding/json path because their fields are strings/bools
// already -- only the top-level enum fields need projection.
func agentInfoToMap(a *leapmuxv1.AgentInfo) map[string]any {
	return map[string]any{
		"id":                      a.GetId(),
		"workspace_id":            a.GetWorkspaceId(),
		"title":                   a.GetTitle(),
		"model":                   a.GetModel(),
		"status":                  agentStatusName(a.GetStatus()),
		"agent_provider":          agentProviderName(a.GetAgentProvider()),
		"created_at":              a.GetCreatedAt(),
		"closed_at":               a.GetClosedAt(),
		"agent_session_id":        a.GetAgentSessionId(),
		"permission_mode":         a.GetPermissionMode(),
		"effort":                  a.GetEffort(),
		"worker_id":               a.GetWorkerId(),
		"worker_name":             a.GetWorkerName(),
		"working_dir":             a.GetWorkingDir(),
		"git_status":              a.GetGitStatus(),
		"home_dir":                a.GetHomeDir(),
		"available_models":        a.GetAvailableModels(),
		"available_option_groups": a.GetAvailableOptionGroups(),
		"extra_settings":          a.GetExtraSettings(),
		"startup_error":           a.GetStartupError(),
		"startup_message":         a.GetStartupMessage(),
	}
}
