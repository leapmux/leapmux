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

func (a *Agent) openACPSession(ctx context.Context, resumeID string) (string, error) {
	id, err := a.tryOpenACPSession(ctx, resumeID)
	if providerkit.HasJSONRPCErrorCode(err, -32000) {
		if authErr := a.authenticateSession(ctx, "gemini-api-key"); authErr != nil {
			if platformErr := a.authenticateSession(ctx, "agent-platform"); platformErr != nil {
				return "", errAntigravitySignIn
			}
		}
		id, err = a.tryOpenACPSession(ctx, resumeID)
		if providerkit.HasJSONRPCErrorCode(err, -32000) {
			return "", errAntigravitySignIn
		}
	}
	if err != nil {
		if resumeID != "" {
			return "", providerkit.ResumeFailedError(resumeID, err)
		}
		return "", err
	}
	return id, nil
}

func (a *Agent) tryOpenACPSession(ctx context.Context, resumeID string) (string, error) {
	method, params := "session/new", map[string]any{"cwd": a.workingDir, "mcpServers": []any{}}
	if resumeID != "" {
		method = "session/resume"
		params["sessionId"] = resumeID
	}
	raw, _ := json.Marshal(params)
	result, err := a.SendRequestContext(ctx, method, raw, a.APITimeout())
	if err != nil {
		return "", err
	}
	var reply struct {
		SessionID string `json:"sessionId"`
	}
	if err := json.Unmarshal(result, &reply); err != nil {
		return "", err
	}
	if reply.SessionID == "" {
		return "", fmt.Errorf("%s returned no session id", method)
	}
	return reply.SessionID, nil
}

func (a *Agent) authenticateSession(ctx context.Context, method string) error {
	params, _ := json.Marshal(map[string]any{"methodId": method})
	_, err := a.SendRequestContext(ctx, "authenticate", params, a.APITimeout())
	return err
}

func (a *Agent) closeACPSession(ctx context.Context, sessionID string) error {
	params, _ := json.Marshal(map[string]any{"sessionId": sessionID})
	_, err := a.SendRequestContext(ctx, "session/close", params, a.APITimeout())
	return err
}

func (a *Agent) discoverLSPort(ctx context.Context, timeout time.Duration) error {
	timer := a.clock.NewTimer(timeout, antigravityStreamConnectTimerTag)
	defer timer.Stop(antigravityStreamConnectTimerTag)
	for {
		port, err := a.liveLSPort(ctx)
		if err == nil {
			a.rpc.endpoint, err = providerkit.NewHTTPEndpoint("http://127.0.0.1:"+strconv.Itoa(port), nil)
			return err
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

func (a *Agent) liveLSPort(ctx context.Context) (int, error) {
	content, err := os.ReadFile(a.logPath)
	if err != nil {
		return 0, err
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
			a.rpc.endpoint = endpoint
			return port, nil
		}
		endpoint.Close()
	}
	return 0, fmt.Errorf("no live Antigravity language-server port appears in the log")
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
