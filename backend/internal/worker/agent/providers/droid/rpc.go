package droid

import (
	"encoding/json"
	"fmt"

	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
)

// requestInput waits for Droid's enqueue reply. A missing reply does not prove rejection.
func (a *Agent) requestInput(id string, params addUserMessageParams) error {
	reply := a.registerReply(id)
	defer a.unregisterReply(id)
	if err := a.writeRequest(id, droidMethodAddUserMessage, params); err != nil {
		// Process.WriteStdin marks a partial write uncertain and preserves a definite failure before the first byte.
		return fmt.Errorf("send Droid input: %w", err)
	}
	response, err := providerkit.AwaitReply(&a.Process, a.Context(), reply, droidMethodAddUserMessage, a.APITimeout())
	if err != nil {
		return fmt.Errorf("%w: Droid did not confirm input delivery: %v", agent.ErrDeliveryUncertain, err)
	}
	if response.Error != nil {
		return response.Error
	}
	return nil
}

// request writes a native request whose reply does not change its caller's result.
func (a *Agent) request(method string, params any) error {
	return a.writeRequest(a.nextRequestID(), method, params)
}

// writeRequest sends one stream-JSON-RPC request with its assigned ID.
func (a *Agent) writeRequest(id, method string, params any) error {
	raw, err := json.Marshal(params)
	if err != nil {
		return err
	}
	env := newDroidEnvelope(droidTypeRequest)
	env.ID = id
	env.Method = method
	env.Params = raw
	line, err := env.Marshal()
	if err != nil {
		return err
	}
	return a.WriteStdin(append(line, '\n'))
}

// nextRequestID issues a JSON-RPC request ID.
func (a *Agent) nextRequestID() string {
	a.Mu.Lock()
	defer a.Mu.Unlock()
	return fmt.Sprintf("leapmux-%d", a.NextTurnSeq())
}

// registerReply adds a one-request inbox before the request reaches Droid.
func (a *Agent) registerReply(id string) chan droidEnvelope {
	a.rpcMu.Lock()
	defer a.rpcMu.Unlock()
	if a.pendingReplies == nil {
		a.pendingReplies = make(map[string]chan droidEnvelope)
	}
	reply := make(chan droidEnvelope, 1)
	a.pendingReplies[id] = reply
	return reply
}

func (a *Agent) unregisterReply(id string) {
	a.rpcMu.Lock()
	delete(a.pendingReplies, id)
	a.rpcMu.Unlock()
}

// settleReply delivers a response only to its matching live request.
func (a *Agent) settleReply(env droidEnvelope) bool {
	a.rpcMu.Lock()
	reply := a.pendingReplies[env.ID]
	delete(a.pendingReplies, env.ID)
	a.rpcMu.Unlock()
	if reply == nil {
		return false
	}
	reply <- env
	return true
}
