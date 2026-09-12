package agent

import (
	"context"
	"encoding/json"
	"fmt"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
)

const copilotNativeProtocolVersion = 3

// copilotConnection owns native RPC framing and the Copilot process.
type copilotConnection struct {
	jsonrpcBase
}

func startCopilotConnection(parent context.Context, opts Options, handle outputHandler) (*copilotConnection, error) {
	ctx, cancel := context.WithCancel(parent)
	launch, err := resolveProviderLaunch(ctx, opts.Shell, opts.LoginShell, leapmuxv1.AgentProvider_AGENT_PROVIDER_GITHUB_COPILOT)
	if err != nil {
		cancel()
		return nil, err
	}
	cmd, delimiter, prefix := buildShellWrappedCommand(ctx, shellWrapSpec{
		Shell: opts.Shell, LoginShell: opts.LoginShell, Launch: launch, WorkingDir: opts.WorkingDir,
		BaseArgs: []string{"--server", "--stdio", "--no-remote", "--no-remote-export"},
	})
	cmd.Env = FinalizeAgentEnv(cmd.Environ(), opts)
	stdin, stdout, stderr, err := setupProcessPipes(cmd, cancel)
	if err != nil {
		return nil, err
	}
	connection := &copilotConnection{jsonrpcBase: jsonrpcBase{
		processBase:  newProcessBase(opts, "copilot", cmd, stdin, ctx, cancel, delimiter, prefix),
		frameMessage: frameCopilotJSON,
	}}
	if err := connection.startCmd(cmd, cancel); err != nil {
		_ = stdin.Close()
		_ = stdout.Close()
		_ = stderr.Close()
		return nil, err
	}
	connection.drainStderr(stderr)
	stdoutMu.Lock()
	maximum := stdoutConfiguredMax
	stdoutMu.Unlock()
	go connection.readOutputLoop(newCopilotScanner(stdout, delimiter, maximum), handle)

	status, err := connection.sendRequest("status.get", json.RawMessage(`{}`), opts.startupTimeout())
	if err == nil {
		var version struct {
			ProtocolVersion int `json:"protocolVersion"`
		}
		if parseErr := json.Unmarshal(status, &version); parseErr != nil {
			err = fmt.Errorf("decode Copilot protocol version: %w", parseErr)
		} else if version.ProtocolVersion != copilotNativeProtocolVersion {
			err = fmt.Errorf("copilot protocol %d is unsupported; expected protocol %d", version.ProtocolVersion, copilotNativeProtocolVersion)
		}
	}
	if err != nil {
		connection.Stop()
		_ = connection.Wait()
		return nil, connection.formatStartupError("native protocol initialization", err)
	}
	return connection, nil
}
