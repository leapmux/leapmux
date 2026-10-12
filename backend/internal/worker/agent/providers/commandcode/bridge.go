package commandcode

import (
	"context"
	"crypto/rand"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"time"

	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
)

//go:embed bridge.mjs
var bridgeSource []byte

type compactionAttempt struct {
	done     chan struct{}
	outcome  string
	finished bool
}

// The agent mutex protects the attempt and closes its notification once.
func (attempt *compactionAttempt) finish() {
	if !attempt.finished {
		attempt.finished = true
		close(attempt.done)
	}
}

func createRuntimeDirectory() (directory, secret string, err error) {
	directory, err = os.MkdirTemp("", "leapmux-commandcode-")
	if err != nil {
		return "", "", fmt.Errorf("create the Command Code runtime directory: %w", err)
	}
	createdDirectory := directory
	defer func() {
		if err != nil {
			err = errors.Join(err, os.RemoveAll(createdDirectory))
		}
	}()
	var token [32]byte
	if _, err = rand.Read(token[:]); err != nil {
		return "", "", fmt.Errorf("create the Command Code bridge credential: %w", err)
	}
	secret = hex.EncodeToString(token[:])
	if err = os.WriteFile(filepath.Join(directory, "bridge.mjs"), bridgeSource, 0o600); err != nil {
		return "", "", fmt.Errorf("write the Command Code bridge: %w", err)
	}
	return directory, secret, nil
}

func (a *Agent) receiveBridge(raw []byte) {
	var ready struct {
		Type      string `json:"type"`
		Port      int    `json:"port"`
		LocalOnly *bool  `json:"localOnly"`
	}
	if json.Unmarshal(raw, &ready) != nil || ready.Type != bridgeFrameType || ready.Port < 1 || ready.Port > 65535 || ready.LocalOnly == nil {
		return
	}
	endpoint, err := providerkit.NewHTTPEndpoint(fmt.Sprintf("http://127.0.0.1:%d", ready.Port), providerkit.BearerAuth(a.bridgeSecret))
	if err != nil {
		slog.Warn("read the Command Code bridge endpoint", "error", err)
		return
	}
	a.Mu.Lock()
	if a.bridge != nil {
		a.Mu.Unlock()
		endpoint.Close()
		return
	}
	a.bridge = endpoint
	a.localOnly = *ready.LocalOnly
	a.Mu.Unlock()
}

// CompactContext calls the native session method through the official mod API.
func (a *Agent) CompactContext() error {
	a.sendMu.Lock()
	defer a.sendMu.Unlock()
	a.Mu.Lock()
	endpoint, busy, stopped := a.bridge, a.operationActiveLocked(), a.StoppedLocked()
	a.Mu.Unlock()
	if stopped {
		return fmt.Errorf("the Command Code agent is stopped")
	}
	if busy {
		return agent.ErrAgentBusy
	}
	if endpoint == nil {
		return fmt.Errorf("the native Command Code compaction bridge is absent")
	}
	attempt := &compactionAttempt{done: make(chan struct{})}
	a.Mu.Lock()
	a.manualCompaction = attempt
	a.compacting = true
	a.Mu.Unlock()
	a.PublishTurnActive()
	// Native compaction runs a full model request before its HTTP reply.
	ctx, cancel := context.WithTimeout(a.Context(), max(a.APITimeout(), 5*time.Minute))
	defer cancel()
	var reply struct {
		Completed bool `json:"completed"`
	}
	if err := endpoint.Do(ctx, "POST", "/compact", nil, &reply); err != nil {
		var status *providerkit.HTTPStatusError
		var network *net.OpError
		if errors.As(err, &status) || errors.As(err, &network) && network.Op == "dial" {
			a.endRejectedCompaction(attempt)
		}
		return err
	}
	if !reply.Completed {
		a.endRejectedCompaction(attempt)
		return fmt.Errorf("the Command Code host did not confirm native compaction")
	}
	select {
	case <-attempt.done:
	case <-ctx.Done():
		return fmt.Errorf("wait for the native Command Code compaction boundary: %w", ctx.Err())
	}
	a.Mu.Lock()
	outcome := attempt.outcome
	a.Mu.Unlock()
	if outcome != "summarized" {
		return fmt.Errorf("the native Command Code compaction did not summarize the context: %s", outcome)
	}
	return nil
}

func (a *Agent) endRejectedCompaction(attempt *compactionAttempt) {
	a.Mu.Lock()
	if a.manualCompaction != attempt {
		a.Mu.Unlock()
		return
	}
	attempt.finish()
	a.manualCompaction = nil
	a.compacting = false
	a.Mu.Unlock()
	a.PublishTurnActive()
}

func (a *Agent) cleanupRuntime() {
	a.cleanupOnce.Do(func() {
		a.Mu.Lock()
		endpoint := a.bridge
		a.Mu.Unlock()
		if endpoint != nil {
			endpoint.Close()
		}
		if a.runtimeDir != "" {
			if err := os.RemoveAll(a.runtimeDir); err != nil {
				slog.Warn("remove the Command Code runtime directory", "error", err)
			}
		}
	})
}
