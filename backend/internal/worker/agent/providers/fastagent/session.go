package fastagent

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/leapmux/leapmux/generated/contracts"
)

type fastagentRootTool struct {
	id, sessionID, historyFile, timestamp, nativeID, name string
	request, result                                       json.RawMessage
	startedAt                                             time.Time
}

// The model's tool-call ID and the live ACP UUID have no native association. Archive
// rows use their own deterministic identity rather than guessing that association.
func fastagentRootToolID(sessionID, file, timestamp, callID string) string {
	identity, _ := json.Marshal([]string{sessionID, file, timestamp, callID})
	digest := sha256.Sum256(identity)
	return "fastagent-archive-" + hex.EncodeToString(digest[:])
}

func fastagentRootTools(sessionID string, histories []fastagentRootHistory) ([]fastagentRootTool, error) {
	var tools []fastagentRootTool
	identities := make(map[string]struct{})
	for _, history := range histories {
		pending := make(map[string]int)
		completed := make(map[string]struct{})
		for _, message := range history.messages {
			calls, err := fastagentObjectEntries(message.ToolCalls)
			if err != nil {
				return nil, err
			}
			for _, call := range calls {
				var request struct {
					Method string `json:"method"`
					Params struct {
						Name      string          `json:"name"`
						Arguments json.RawMessage `json:"arguments"`
					} `json:"params"`
				}
				if json.Unmarshal(call.Data, &request) != nil || request.Method != "tools/call" || request.Params.Name == "" {
					return nil, errors.New("the fastagent root tool request is invalid")
				}
				if _, exists := pending[call.ID]; exists {
					return nil, errors.New("the fastagent root archive repeats an unfinished tool ID")
				}
				delete(completed, call.ID)
				if request.Params.Name != contracts.FastagentToolBash && request.Params.Name != contracts.FastagentToolProcess {
					pending[call.ID] = -1
					continue
				}
				if message.Role != "assistant" {
					return nil, errors.New("the fastagent root tool request has an invalid role")
				}
				startedAt, err := time.Parse(time.RFC3339Nano, message.Timestamp)
				if err != nil {
					return nil, fmt.Errorf("read fastagent root tool timestamp: %w", err)
				}
				var arguments map[string]json.RawMessage
				if json.Unmarshal(request.Params.Arguments, &arguments) != nil || arguments == nil {
					return nil, errors.New("the fastagent root tool arguments are not an object")
				}
				id := fastagentRootToolID(sessionID, history.file, message.Timestamp, call.ID)
				if _, exists := identities[id]; exists {
					return nil, errors.New("the fastagent root archive repeats a tool identity")
				}
				identities[id] = struct{}{}
				pending[call.ID] = len(tools)
				tools = append(tools, fastagentRootTool{id: id, sessionID: sessionID, historyFile: history.file, timestamp: message.Timestamp, startedAt: startedAt, nativeID: call.ID, name: request.Params.Name, request: call.Data})
			}
			results, err := fastagentObjectEntries(message.ToolResults)
			if err != nil {
				return nil, err
			}
			for _, result := range results {
				index, found := pending[result.ID]
				if !found {
					if _, repeated := completed[result.ID]; repeated {
						return nil, errors.New("the fastagent root archive repeats a completed tool result")
					}
					continue
				}
				delete(pending, result.ID)
				completed[result.ID] = struct{}{}
				if index < 0 {
					continue
				}
				var value map[string]json.RawMessage
				if message.Role != "user" || json.Unmarshal(result.Data, &value) != nil || value == nil {
					return nil, errors.New("the fastagent root tool result is invalid")
				}
				var content []json.RawMessage
				var isError bool
				if json.Unmarshal(value["content"], &content) != nil || content == nil || json.Unmarshal(value["isError"], &isError) != nil {
					return nil, errors.New("the fastagent root tool result lacks content or error status")
				}
				for _, block := range content {
					var item struct {
						Type string `json:"type"`
					}
					if json.Unmarshal(block, &item) != nil || item.Type == "" {
						return nil, errors.New("the fastagent root tool result has an invalid content block")
					}
				}
				tools[index].result = result.Data
			}
		}
	}
	slices.SortStableFunc(tools, func(left, right fastagentRootTool) int { return left.startedAt.Compare(right.startedAt) })
	return tools, nil
}

func (tool fastagentRootTool) updates() ([]json.RawMessage, error) {
	request := fastagentHistoryMessage{Role: "assistant", ToolCalls: json.RawMessage(fmt.Sprintf("{%q:%s}", tool.id, tool.request))}
	updates, err := fastagentHistoryUpdates(request, false, "")
	if err != nil {
		return nil, err
	}
	if len(tool.result) > 0 {
		result := fastagentHistoryMessage{Role: "user", ToolResults: json.RawMessage(fmt.Sprintf("{%q:%s}", tool.id, tool.result))}
		final, err := fastagentHistoryUpdates(result, false, "")
		if err != nil {
			return nil, err
		}
		updates = append(updates, final...)
	}
	for index, raw := range updates {
		var update map[string]json.RawMessage
		if err := json.Unmarshal(raw, &update); err != nil {
			return nil, err
		}
		meta, err := json.Marshal(map[string]any{contracts.FastagentArchiveNamespace: map[string]string{
			contracts.FastagentArchiveSessionID: tool.sessionID, contracts.FastagentArchiveHistoryFile: tool.historyFile,
			contracts.FastagentArchiveTimestamp: tool.timestamp, contracts.FastagentArchiveCallID: tool.nativeID,
			contracts.FastagentArchiveToolName: tool.name,
		}})
		if err != nil {
			return nil, err
		}
		update["_meta"] = meta
		updates[index], err = json.Marshal(update)
		if err != nil {
			return nil, err
		}
	}
	return updates, nil
}

func (tool fastagentRootTool) matchesRecorded(other fastagentRootTool) bool {
	return tool.id == other.id && bytes.Equal(tool.request, other.request) &&
		(len(other.result) == 0 || bytes.Equal(tool.result, other.result))
}
