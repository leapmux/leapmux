package agent

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"time"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
)

const copilotNativeProtocolVersion = 3

// copilotConnection owns native RPC framing and the Copilot process.
type copilotConnection struct {
	jsonrpcBase
	// pendingScanner reads the process stdout. It waits here between the launch and
	// startReading, which is the window the caller uses to adopt the connection.
	pendingScanner *bufio.Scanner
}

// startCopilotConnection launches the CLI and returns BEFORE the reader goroutine
// starts.
//
// The reader reaches its agent through the connection the caller holds, so the caller
// adopts the connection first and calls startReading afterwards. A single function
// that did both would hand the reader a field the caller does not assign until later, and the
// first frame would then read a nil pointer.
//
// verifyNativeProtocol completes the startup. Keep the three calls in that order.
func startCopilotConnection(parent context.Context, opts Options) (*copilotConnection, error) {
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
	connection.pendingScanner = newCopilotScanner(stdout, delimiter, maximum)
	return connection, nil
}

// startReading starts the goroutine that reads the runtime's frames. The caller
// adopted the connection before this call, so the handler finds every field it reads.
func (c *copilotConnection) startReading(handle outputHandler) {
	go c.readOutputLoop(c.pendingScanner, handle)
}

// verifyNativeProtocol confirms that the runtime speaks the protocol this provider
// implements. It stops the process when it does not, because no later request of
// LeapMux's can succeed.
func (c *copilotConnection) verifyNativeProtocol(opts Options) error {
	status, err := c.sendRequest("status.get", json.RawMessage(`{}`), opts.startupTimeout())
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
		c.Stop()
		_ = c.Wait()
		return c.formatStartupError("native protocol initialization", err)
	}
	return nil
}

// requestSession adds the target session without changing the caller's parameters.
func (c *copilotConnection) requestSession(sessionID, method string, values map[string]any, timeout time.Duration) (json.RawMessage, error) {
	if sessionID == "" {
		return nil, fmt.Errorf("the Copilot session ID is empty")
	}
	values = maps.Clone(values)
	if values == nil {
		values = make(map[string]any)
	}
	values["sessionId"] = sessionID
	params, err := json.Marshal(values)
	if err != nil {
		return nil, fmt.Errorf("encode Copilot request: %w", err)
	}
	return c.sendRequest("session."+method, params, timeout)
}
