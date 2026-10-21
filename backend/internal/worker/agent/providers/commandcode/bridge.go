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

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
)

//go:embed bridge.mjs
var bridgeSource []byte

const compactionWaitLabel = "commandcode-compaction"

type compactionAttempt struct {
	done     chan struct{}
	outcome  string
	failure  error
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
	clock := a.clock
	if clock == nil {
		clock = a.Clock()
	}
	limit := max(a.APITimeout(), 5*time.Minute)
	deadline := clock.Now().Add(limit)
	ctx, cancel := context.WithCancelCause(a.Context())
	defer cancel(nil)
	requestTimer := clock.AfterFunc(limit, func() { cancel(context.DeadlineExceeded) }, compactionWaitLabel, "request")
	defer requestTimer.Stop()
	var reply struct {
		Completed bool `json:"completed"`
	}
	if err := endpoint.Do(ctx, "POST", "/compact", nil, &reply); err != nil {
		var status *providerkit.HTTPStatusError
		var network *net.OpError
		if errors.As(err, &status) || errors.As(err, &network) && network.Op == "dial" {
			a.endAbandonedCompaction(attempt, err)
		} else if !a.IsStopped() && a.Context().Err() == nil {
			// Closing the HTTP request does not cancel cmd.sessions.compact in the native host.
			// Stop unresolved work before the queue can accept another prompt. The reader owns its final row.
			err = errors.Join(err, context.Cause(ctx))
			a.Mu.Lock()
			unresolved := a.manualCompaction == attempt
			if unresolved {
				attempt.failure = err
			}
			a.Mu.Unlock()
			if unresolved {
				a.Stop()
			}
		}
		return err
	}
	if !reply.Completed {
		err := errors.New("the Command Code host did not confirm native compaction")
		a.endAbandonedCompaction(attempt, err)
		return err
	}
	requestTimer.Stop()
	// One deadline covers both phases. The reader closes done after it drains the native completion frame.
	outcome := providerkit.AwaitSignal(ctx, clock, attempt.done, nil, max(0, clock.Until(deadline)), compactionWaitLabel)
	if outcome != providerkit.SignalArrived {
		waitErr := context.DeadlineExceeded
		if outcome == providerkit.SignalCancelled {
			waitErr = context.Cause(ctx)
		}
		a.endAbandonedCompaction(attempt, waitErr)
		return fmt.Errorf("wait for the native Command Code compaction boundary: %w", waitErr)
	}
	a.Mu.Lock()
	compactionOutcome := attempt.outcome
	a.Mu.Unlock()
	if compactionOutcome != contracts.CommandCodeCompactionOutcomeSummarized {
		return fmt.Errorf("the native Command Code compaction did not summarize the context: %s", compactionOutcome)
	}
	return nil
}

func (a *Agent) endAbandonedCompaction(attempt *compactionAttempt, reason error) {
	a.dispatchMu.Lock()
	defer a.dispatchMu.Unlock()
	// During shutdown, the reader can still receive a native end. Its EOF path owns the final outcome.
	if a.IsStopped() || a.Context().Err() != nil {
		return
	}
	a.Mu.Lock()
	if a.manualCompaction != attempt {
		a.Mu.Unlock()
		return
	}
	live := a.compactionLive
	a.compactionLive = false
	a.Mu.Unlock()
	if live {
		providerkit.EndCompaction(a.sink)
		detail := ""
		if reason != nil {
			detail = reason.Error()
		}
		providerkit.PersistCompactionEnded(commandCodeWorkerNotifications{a: a}, providerkit.CompactionEndFailed, detail)
	}
	a.Mu.Lock()
	attempt.outcome = contracts.CommandCodeCompactionOutcomeFailed
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
