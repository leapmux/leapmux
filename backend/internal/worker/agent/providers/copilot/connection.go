package copilot

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"time"

	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/internal/launch"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
)

const copilotNativeProtocolVersion = 3

// copilotConnection owns native RPC framing and the Copilot process.
type copilotConnection struct {
	providerkit.JSONRPCProcess
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
func startCopilotConnection(parent context.Context, opts agent.Options) (*copilotConnection, error) {
	ctx, cancel := context.WithCancel(parent)
	launchSpec, err := providerkit.ResolveLaunch(ctx, opts, Registration())
	if err != nil {
		cancel()
		return nil, err
	}
	cmd, delimiter, prefix := launch.Wrap(ctx, launch.WrapSpec{
		Shell: opts.Shell, LoginShell: opts.LoginShell, Launch: launchSpec, WorkingDir: opts.WorkingDir,
		BaseArgs: []string{"--server", "--stdio", "--no-remote", "--no-remote-export"},
	})
	cmd.Env = providerkit.FinalizeAgentEnv(cmd.Environ(), opts)
	stdin, stdout, stderr, err := providerkit.SetupProcessPipes(cmd, cancel)
	if err != nil {
		return nil, err
	}
	connection := &copilotConnection{JSONRPCProcess: providerkit.JSONRPCProcess{
		Process:      providerkit.NewProcess(opts, "copilot", cmd, stdin, ctx, cancel, delimiter, prefix),
		FrameMessage: frameCopilotJSON,
	}}
	if err := connection.StartCmd(cmd, cancel); err != nil {
		_ = stdin.Close()
		_ = stdout.Close()
		_ = stderr.Close()
		return nil, err
	}
	connection.DrainStderr(stderr)
	connection.pendingScanner = newCopilotScanner(stdout, delimiter, agent.ConfiguredMaxMessageSize())
	return connection, nil
}

// startReading starts the goroutine that reads the runtime's frames. The caller
// adopted the connection before this call, so the handler finds every field it reads.
func (c *copilotConnection) startReading(handle providerkit.LineHandler) {
	go c.ReadOutputLoop(c.pendingScanner, handle)
}

// verifyNativeProtocol confirms that the runtime speaks the protocol this provider
// implements. It stops the process when it does not, because no later request of
// LeapMux's can succeed.
func (c *copilotConnection) verifyNativeProtocol(opts agent.Options) error {
	status, err := c.SendRequest("status.get", json.RawMessage(`{}`), opts.EffectiveStartupTimeout())
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
		return c.FormatStartupError("native protocol initialization", err)
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
	return c.SendRequest("session."+method, params, timeout)
}
