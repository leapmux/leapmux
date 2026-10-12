package commandcode

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
)

func (a *Agent) request(method string, params any, timeout time.Duration) (json.RawMessage, error) {
	var raw json.RawMessage
	if params != nil {
		var err error
		raw, err = json.Marshal(params)
		if err != nil {
			return nil, fmt.Errorf("encode the Command Code request: %w", err)
		}
	}
	return a.SendRequest(method, raw, timeout)
}

func (a *Agent) HandleOutput(content []byte) {
	a.handleOutput(providerkit.ParseLine(content))
}

func (a *Agent) handleOutput(line *providerkit.ParsedLine) {
	a.dispatchMu.Lock()
	defer a.dispatchMu.Unlock()
	if a.IsDiscardingOutput() || line == nil {
		return
	}
	if line.Method != "" {
		a.handleMethod(line)
		return
	}
	if line.Type == bridgeFrameType {
		a.receiveBridge(line.Raw)
		return
	}
	a.handleEvent(line.Raw)
}
