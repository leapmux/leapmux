package antigravity

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
)

var antigravityPortPattern = regexp.MustCompile(`Language server listening on random port at ([0-9]+) for HTTP$`)

var errAntigravitySignIn = errors.New("antigravity CLI is not signed in. Run agy on this machine and sign in, then start the agent again")

func (a *Agent) initializeACP(ctx context.Context) error {
	params, _ := json.Marshal(map[string]any{
		"protocolVersion": 1, "clientCapabilities": map[string]any{},
		"clientInfo": map[string]any{"name": "leapmux", "version": "0"},
	})
	result, err := a.SendRequestContext(ctx, "initialize", params, a.APITimeout())
	if err != nil {
		return err
	}
	var reply struct {
		AgentInfo struct {
			Version string `json:"version"`
		} `json:"agentInfo"`
	}
	if err := json.Unmarshal(result, &reply); err != nil {
		return err
	}
	a.version = reply.AgentInfo.Version
	return nil
}

// session/list starts the language server and checks authentication. Both
// session/new and session/resume start an ACP stream that requests permission
// for the same steps as the native stream. Keep cascade lifecycle on the
// language server so the native stream owns each permission decision.
func (a *Agent) bootstrapBackend(ctx context.Context) error {
	params, _ := json.Marshal(map[string]string{"cwd": a.workingDir})
	_, err := a.SendRequestContext(ctx, "session/list", params, a.APITimeout())
	if !providerkit.HasJSONRPCErrorCode(err, -32000) {
		return err
	}
	if authErr := a.authenticateSession(ctx, "gemini-api-key"); authErr != nil {
		if platformErr := a.authenticateSession(ctx, "agent-platform"); platformErr != nil {
			return errAntigravitySignIn
		}
	}
	_, err = a.SendRequestContext(ctx, "session/list", params, a.APITimeout())
	if providerkit.HasJSONRPCErrorCode(err, -32000) {
		return errAntigravitySignIn
	}
	return err
}

func (a *Agent) authenticateSession(ctx context.Context, method string) error {
	params, _ := json.Marshal(map[string]any{"methodId": method})
	_, err := a.SendRequestContext(ctx, "authenticate", params, a.APITimeout())
	return err
}

func (a *Agent) discoverLSPort(ctx context.Context, timeout time.Duration) error {
	timer := a.clock.NewTimer(timeout, antigravityStreamConnectTimerTag)
	defer timer.Stop(antigravityStreamConnectTimerTag)
	for {
		endpoint, err := a.liveLSEndpoint(ctx)
		if err == nil {
			a.rpc.endpoint = endpoint
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-a.ProcessDone():
			return providerkit.ErrServerExited
		case <-timer.C:
			return fmt.Errorf("the Antigravity language server did not listen within %s: %w", timeout, err)
		case <-a.clock.NewTimer(25*time.Millisecond, antigravityStreamReconnectTimerTag).C:
		}
	}
}

func (a *Agent) liveLSEndpoint(ctx context.Context) (*providerkit.HTTPEndpoint, error) {
	content, err := os.ReadFile(a.logPath)
	if err != nil {
		return nil, err
	}
	lines := strings.Split(strings.TrimSpace(string(content)), "\n")
	for index := len(lines) - 1; index >= 0; index-- {
		match := antigravityPortPattern.FindStringSubmatch(strings.TrimSpace(lines[index]))
		if match == nil {
			continue
		}
		port, err := strconv.Atoi(match[1])
		if err != nil || port < 1 || port > 65535 {
			continue
		}
		endpoint, err := providerkit.NewHTTPEndpoint("http://127.0.0.1:"+strconv.Itoa(port), nil)
		if err != nil {
			continue
		}
		rpc := antigravityRPC{endpoint: endpoint, timeout: a.rpc.timeout}
		if rpc.status(ctx) == nil {
			return endpoint, nil
		}
		endpoint.Close()
	}
	return nil, fmt.Errorf("no live Antigravity language-server port appears in the log")
}

func lastLogLines(path string, limit int) string {
	content, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	lines := strings.Split(strings.TrimSpace(string(content)), "\n")
	if len(lines) > limit {
		lines = lines[len(lines)-limit:]
	}
	return strings.Join(lines, "\n")
}
