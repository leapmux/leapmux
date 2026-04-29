package agent

import (
	"encoding/json"
	"fmt"
	"strconv"
	"time"
)

// piResponseEnvelope is the success/error/data shape of a {type:"response"}
// line emitted by Pi. The response interceptor has already filtered by type
// and routed by id, so this struct only carries the fields awaitResponse
// needs to interpret the result.
type piResponseEnvelope struct {
	Success bool            `json:"success"`
	Data    json.RawMessage `json:"data"`
	Error   string          `json:"error"`
}

// sendPiCommand writes a {id, type:method, ...payload} JSONL command and
// blocks until the matching response arrives, the process exits, or the
// context/timeout fires.
//
// Returns the response's raw `data` field on success. On a server-side
// failure (success:false), returns an error containing the server's `error`
// string. Wire-level failures (write/timeout) wrap the underlying error.
func (a *PiAgent) sendPiCommand(method string, payload map[string]any, timeout time.Duration) (json.RawMessage, error) {
	id := "leapmux-" + strconv.FormatInt(a.nextReqID.Add(1), 10)

	envelope := make(map[string]any, len(payload)+2)
	for k, v := range payload {
		envelope[k] = v
	}
	envelope["id"] = id
	envelope["type"] = method

	data, err := json.Marshal(envelope)
	if err != nil {
		return nil, fmt.Errorf("marshal %s: %w", method, err)
	}
	data = append(data, '\n')

	ch, release := a.register(id)
	defer release()

	a.mu.Lock()
	stopped := a.stopped
	a.mu.Unlock()
	if stopped {
		return nil, fmt.Errorf("agent is stopped")
	}

	if err := a.writeStdin(data); err != nil {
		return nil, fmt.Errorf("write %s: %w", method, err)
	}

	respLine, err := a.awaitResponse(ch, method, timeout)
	if err != nil {
		return nil, err
	}

	var env piResponseEnvelope
	if err := json.Unmarshal(respLine, &env); err != nil {
		return nil, fmt.Errorf("parse %s response: %w", method, err)
	}
	if !env.Success {
		if env.Error != "" {
			return nil, fmt.Errorf("%s failed: %s", method, env.Error)
		}
		return nil, fmt.Errorf("%s failed", method)
	}
	return env.Data, nil
}

// handlePiResponse is the readOutput interceptor that routes {type:"response"}
// lines to the matching pending channel. Returns true when the line was
// consumed, false to forward it to the regular output handler.
func (a *PiAgent) handlePiResponse(line *parsedLine) bool {
	if line.Type != PiEventResponse {
		return false
	}
	// Pi response ids are opaque strings — fall back to the parsedLine helper
	// which handles both string and number forms.
	id := line.IDString()
	if id == "" {
		return false
	}
	return a.deliver(id, line.Raw)
}

func (a *PiAgent) handleOutput(line *parsedLine) {
	handlePiOutput(a, line)
}

// HandleOutput parses a single JSONL line and dispatches it. Used by tests
// and out-of-band feed paths; the production read loop calls handleOutput
// directly via readOutput.
func (a *PiAgent) HandleOutput(content []byte) {
	handlePiOutput(a, parseLine(content))
}
