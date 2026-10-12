package muse

import (
	"encoding/json"
	"fmt"
	"io"
	"time"

	"github.com/google/uuid"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
)

// connection owns one MSP process. An agent host and a read-only query use the same transport.
type connection struct {
	providerkit.JSONRPCProcess
	outputDone chan struct{}
	handshake  initializeResult
	stdout     io.ReadCloser
	stderr     io.ReadCloser
}

func commandParams(params map[string]any) (map[string]any, string, error) {
	id, err := uuid.NewV7()
	if err != nil {
		return nil, "", fmt.Errorf("create the Muse command ID: %w", err)
	}
	out := make(map[string]any, len(params)+1)
	for key, value := range params {
		out[key] = value
	}
	out["commandId"] = id.String()
	return out, id.String(), nil
}

// request returns the native reply. Its optional observer runs before later stream events.
func (c *connection) request(method string, params any, timeout time.Duration, observe func(json.RawMessage, error)) (json.RawMessage, error) {
	raw, err := json.Marshal(params)
	if err != nil {
		return nil, fmt.Errorf("encode the Muse %s request: %w", method, err)
	}
	return c.SendRequestObserved(method, raw, timeout, observe)
}

// command verifies the echoed idempotency ID of an admission acknowledgement.
// Session start and resume return a session instead. Their callers use request.
func (c *connection) command(method string, params map[string]any, timeout time.Duration, observe func(json.RawMessage, error)) (json.RawMessage, error) {
	payload, commandID, err := commandParams(params)
	if err != nil {
		return nil, err
	}
	validate := func(raw json.RawMessage, nativeErr error) error {
		if nativeErr != nil {
			return nativeErr
		}
		var reply struct {
			CommandID string `json:"commandId"`
			Status    string `json:"status"`
		}
		if err := json.Unmarshal(raw, &reply); err != nil {
			return fmt.Errorf("%w: decode the Muse %s acknowledgement: %v", agent.ErrDeliveryUncertain, method, err)
		}
		validStatus := reply.Status == "accepted" || method == methodSessionCompact && reply.Status == "noop"
		if reply.CommandID != commandID || !validStatus {
			return fmt.Errorf("%w: Muse returned an invalid %s acknowledgement", agent.ErrDeliveryUncertain, method)
		}
		return nil
	}
	var observedErr error
	data, err := c.request(method, payload, timeout, func(raw json.RawMessage, nativeErr error) {
		observedErr = validate(raw, nativeErr)
		if observe != nil {
			observe(raw, observedErr)
		}
	})
	if err != nil {
		return nil, err
	}
	if observedErr != nil {
		return nil, observedErr
	}
	return data, nil
}

func (c *connection) close() error {
	c.Stop()
	err := c.Wait()
	if c.outputDone != nil {
		<-c.outputDone
	}
	return err
}

// wait uses the connection receiver. Agent.Wait must not enter itself through promotion.
func (c *connection) wait() error {
	err := c.Wait()
	if c.outputDone != nil {
		<-c.outputDone
	}
	return err
}
