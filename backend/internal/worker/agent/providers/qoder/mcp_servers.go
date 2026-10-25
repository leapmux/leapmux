package qoder

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"
	"time"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
)

// mcpServerStatusTimeout limits an mcp_status request. Qoder answers it from
// memory, so the limit only permits a loaded machine.
const mcpServerStatusTimeout = 30 * time.Second

// mcpServerRestartTimeout limits an mcp_set_servers request. Qoder restarts
// every MCP server before it answers, so the limit permits a slow server start.
const mcpServerRestartTimeout = 60 * time.Second

// qoderMcpServer is one server of an mcp_status reply.
//
// Config is the native server configuration, minus `toolOverrides`. It can
// hold secrets in `env` or `headers`, so no log line and no stored row may
// contain it.
type qoderMcpServer struct {
	Name   string            `json:"name"`
	Status string            `json:"status"`
	Config json.RawMessage   `json:"config"`
	Tools  []json.RawMessage `json:"tools"`
}

type qoderMcpStatusReply struct {
	Servers []qoderMcpServer `json:"servers"`
}

// qoderMcpSetServersReply lists the servers that Qoder refused to register,
// with the reason for each.
type qoderMcpSetServersReply struct {
	Errors map[string]json.RawMessage `json:"errors"`
}

// qoderMcpServerConfig is the part of a server configuration that the
// selection reads. LeapMux sends the configuration back unchanged.
type qoderMcpServerConfig struct {
	Scope  string          `json:"scope"`
	Type   string          `json:"type"`
	Plugin json.RawMessage `json:"plugin"`
}

// registerMcpServersForForms lets the configured MCP servers of the session
// ask for input forms.
//
// Qoder CLI 1.1.78 gives an elicitation handler only to a dynamic server,
// which a host registers through mcp_set_servers. A server from a settings
// file or from --mcp-config gets none, so its `elicitation/create` request
// fails with -32601. This step registers each admitted configured server
// again, with the configuration that Qoder reports. Qoder then marks the
// server as dynamic, and the dynamic entry replaces the configured entry of
// the same name in the session.
//
// The user accepted these costs:
//   - Qoder keeps its pooled connection to a user-scope server, so that
//     server runs twice.
//   - The dynamic copy hides a later edit of the server's settings until the
//     agent restarts.
//   - The copy reports the workspace, not the settings directory, as its MCP
//     root.
//
// mcp_status also lists the servers that the session skipped: an untrusted
// folder, an allow or exclude list, or a disabled server. A user-scope server
// keeps its pooled client in that case, so its status can still read
// "connected". Only an active server of the session lists tools, so a server
// qualifies only when it is connected AND lists a tool. A dynamic server skips
// Qoder's folder-trust check, so this rule must not weaken.
//
// A copy that does not connect is removed again, which restores Qoder's own
// entry for that server. The returned error gives server names only.
//
// Qoder stores the dynamic set before it restarts the servers, so a refused
// mcp_set_servers can still leave a copy that did not connect. The status
// check therefore follows a refusal too. It does not follow a timeout or a
// process exit, because the restart can still run then.
func (a *Agent) registerMcpServersForForms() error {
	servers, err := a.mcpServerStatus()
	if err != nil {
		return fmt.Errorf("read the MCP server status: %w", err)
	}
	selected := qoderFormServers(servers)
	if len(selected) == 0 {
		return nil
	}
	var problems []error
	rejected, err := a.setMcpServers(selected)
	if err != nil {
		registration := fmt.Errorf("register the MCP servers %s: %w", qoderServerNames(selected), err)
		var refusal *qoderControlRefusal
		if !errors.As(err, &refusal) {
			return registration
		}
		problems = append(problems, registration)
	}
	after, err := a.mcpServerStatus()
	if err != nil {
		return errors.Join(append(problems, fmt.Errorf("verify the registered MCP servers: %w", err))...)
	}
	if len(rejected) > 0 {
		problems = append(problems, fmt.Errorf("qoder refused to register the MCP servers %s", strings.Join(rejected, ", ")))
	}
	kept, failed := qoderVerifiedFormServers(selected, after)
	if len(failed) > 0 {
		if _, err := a.setMcpServers(kept); err != nil {
			return errors.Join(append(problems, fmt.Errorf("restore the MCP servers %s: %w", strings.Join(failed, ", "), err))...)
		}
		problems = append(problems, fmt.Errorf("the registered copies of the MCP servers %s did not connect, so Qoder serves those servers without input forms", strings.Join(failed, ", ")))
	}
	return errors.Join(problems...)
}

// qoderFormServers selects the configured servers to register again, keyed by
// name. It returns nil when no server qualifies.
//
// It also returns nil when a dynamic server exists already, because another
// host owns the dynamic set and mcp_set_servers replaces that whole set.
func qoderFormServers(servers []qoderMcpServer) map[string]json.RawMessage {
	selected := make(map[string]json.RawMessage)
	for _, server := range servers {
		config, ok := qoderMcpConfig(server.Config)
		if !ok {
			continue
		}
		if config.Scope == qoderMcpScopeDynamic {
			return nil
		}
		if server.Name == "" || !qoderMcpServerActive(server) || !qoderMcpConfiguredScope(config.Scope) {
			continue
		}
		// Qoder starts a plugin server with its plugin, and an SDK server
		// belongs to the SDK host. mcp_set_servers cannot register either one.
		if providerkit.RawPresent(config.Plugin) || config.Type == qoderMcpTypeSDK {
			continue
		}
		selected[server.Name] = server.Config
	}
	if len(selected) == 0 {
		return nil
	}
	return selected
}

// qoderVerifiedFormServers splits the registered servers by the status after
// the registration.
//
// kept holds each server that is dynamic and active now. failed lists each
// server that became dynamic but is not active. A server that did not become
// dynamic kept its own entry, so it is in neither result. kept is never nil,
// because mcp_set_servers refuses a null server set.
func qoderVerifiedFormServers(selected map[string]json.RawMessage, after []qoderMcpServer) (kept map[string]json.RawMessage, failed []string) {
	kept = make(map[string]json.RawMessage)
	for _, server := range after {
		configured, ok := selected[server.Name]
		if !ok {
			continue
		}
		config, ok := qoderMcpConfig(server.Config)
		if !ok || config.Scope != qoderMcpScopeDynamic {
			continue
		}
		if qoderMcpServerActive(server) {
			kept[server.Name] = configured
		} else {
			failed = append(failed, server.Name)
		}
	}
	slices.Sort(failed)
	return kept, slices.Compact(failed)
}

// qoderMcpServerActive reports whether the session uses the server: it is
// connected, and the session lists at least one of its tools.
func qoderMcpServerActive(server qoderMcpServer) bool {
	return server.Status == qoderMcpStatusConnected && len(server.Tools) > 0
}

func qoderMcpConfiguredScope(scope string) bool {
	switch scope {
	case qoderMcpScopeUser, qoderMcpScopeProject, qoderMcpScopeLocal, qoderMcpScopeFlag:
		return true
	}
	return false
}

// qoderMcpConfig reads a server configuration, which must be a JSON object.
func qoderMcpConfig(raw json.RawMessage) (qoderMcpServerConfig, bool) {
	var config qoderMcpServerConfig
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || trimmed[0] != '{' || json.Unmarshal(trimmed, &config) != nil {
		return qoderMcpServerConfig{}, false
	}
	return config, true
}

func qoderServerNames(servers map[string]json.RawMessage) string {
	return strings.Join(slices.Sorted(maps.Keys(servers)), ", ")
}

// mcpServerStatus reads Qoder's MCP servers for the session.
func (a *Agent) mcpServerStatus() ([]qoderMcpServer, error) {
	body, err := json.Marshal(map[string]any{"subtype": contracts.QoderControlRequestSubtypeMcpStatus})
	if err != nil {
		return nil, err
	}
	result, err := a.sendControlAndWait(string(body), mcpServerStatusTimeout)
	if err != nil {
		return nil, err
	}
	var reply qoderMcpStatusReply
	if err := json.Unmarshal(result.RawResponse, &reply); err != nil {
		return nil, fmt.Errorf("decode the Qoder mcp_status reply: %w", err)
	}
	return reply.Servers, nil
}

// setMcpServers replaces Qoder's dynamic server set and returns the names of
// the servers that Qoder refused. servers must not be nil.
func (a *Agent) setMcpServers(servers map[string]json.RawMessage) (rejected []string, err error) {
	body, err := json.Marshal(map[string]any{
		"subtype": contracts.QoderControlRequestSubtypeMcpSetServers,
		"servers": servers,
	})
	if err != nil {
		return nil, err
	}
	result, err := a.sendControlAndWait(string(body), mcpServerRestartTimeout)
	if err != nil {
		return nil, err
	}
	if !providerkit.RawPresent(result.RawResponse) {
		return nil, nil
	}
	var reply qoderMcpSetServersReply
	if err := json.Unmarshal(result.RawResponse, &reply); err != nil {
		return nil, fmt.Errorf("decode the Qoder mcp_set_servers reply: %w", err)
	}
	return slices.Sorted(maps.Keys(reply.Errors)), nil
}
