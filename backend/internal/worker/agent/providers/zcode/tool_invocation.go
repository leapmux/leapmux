package zcode

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"github.com/leapmux/leapmux/generated/contracts"
)

// zcodeToolInvocation separates a native call ID from one transcript invocation.
// Native call IDs can repeat in different model responses and concurrent turns.
// The span ID remains stable after a provisional turn receives its native ID.
type zcodeToolInvocation struct {
	spanID       string
	nativeCallID string
	messageID    string
	sessionID    string
	name         string
	input        json.RawMessage
	progress     zcodeToolUpdated
	native       zcodeToolUpdated
	lastFrame    []byte
	order        uint64
	final        bool
	owner        *zcodeTurnState
}

func (p zcodeToolUpdated) spanID() string {
	if p.invocation != nil {
		return p.invocation.spanID
	}
	return p.ToolCallID
}

// findZCodeInvocationLocked refuses to guess when a frame omits a message ID
// shared by several invocations. The caller holds a.Mu.
func (a *Agent) findZCodeInvocationLocked(owner *zcodeTurnState, callID, messageID string) *zcodeToolInvocation {
	if owner == nil {
		return nil
	}
	calls := owner.invocationsByNativeID[callID]
	if messageID != "" {
		if exact := calls[messageID]; exact != nil {
			return exact
		}
		return calls[""]
	}
	var sole *zcodeToolInvocation
	for _, invocation := range calls {
		if invocation.final {
			continue
		}
		if sole != nil {
			return nil
		}
		sole = invocation
	}
	if sole == nil && len(calls) == 1 {
		for _, invocation := range calls {
			return invocation
		}
	}
	return sole
}

func (a *Agent) resolveZCodeInvocationLocked(owner *zcodeTurnState, callID, messageID string, create bool) *zcodeToolInvocation {
	if owner == nil || callID == "" {
		return nil
	}
	if invocation := a.findZCodeInvocationLocked(owner, callID, messageID); invocation != nil {
		if messageID != "" && invocation.messageID == "" {
			delete(owner.invocationsByNativeID[callID], "")
			invocation.messageID = messageID
			owner.invocationsByNativeID[callID][messageID] = invocation
		}
		return invocation
	}
	if !create {
		return nil
	}
	// An ID-less frame cannot select between concurrent model responses.
	if messageID == "" && len(owner.invocationsByNativeID[callID]) > 0 {
		return nil
	}
	if owner.invocationsByNativeID == nil {
		owner.invocationsByNativeID = make(map[string]map[string]*zcodeToolInvocation)
	}
	calls := owner.invocationsByNativeID[callID]
	if calls == nil {
		calls = make(map[string]*zcodeToolInvocation)
		owner.invocationsByNativeID[callID] = calls
	}
	identity := ""
	for _, part := range []string{a.sessionID, messageID, callID} {
		identity += strconv.Itoa(len(part)) + ":" + part
	}
	if messageID == "" {
		identity += fmt.Sprintf(":%d:%d", owner.generation, a.nextToolOrder)
	}
	spanID := "zcode-tool:" + base64.RawURLEncoding.EncodeToString([]byte(identity))
	invocation := &zcodeToolInvocation{spanID: spanID, nativeCallID: callID, messageID: messageID, sessionID: a.sessionID, owner: owner, order: a.nextToolOrder}
	a.nextToolOrder++
	calls[messageID] = invocation
	if a.toolInvocations == nil {
		a.toolInvocations = make(map[string]*zcodeToolInvocation)
	}
	a.toolInvocations[spanID] = invocation
	if owner.toolInvocationIDs == nil {
		owner.toolInvocationIDs = make(map[string]struct{})
	}
	owner.toolInvocationIDs[spanID] = struct{}{}
	return invocation
}

// zcodeSpanForNativeCall is for optional controls that omit turn identity.
// Ambiguity leaves the control answerable without attaching another call's input.
func (a *Agent) zcodeSpanForNativeCall(nativeID string) string {
	a.Mu.Lock()
	defer a.Mu.Unlock()
	var found *zcodeToolInvocation
	for _, owner := range a.turns {
		if invocation := a.findZCodeInvocationLocked(owner, nativeID, ""); invocation != nil {
			if found != nil {
				return ""
			}
			found = invocation
		}
	}
	if found == nil {
		return ""
	}
	return found.spanID
}

// metadata captures the invocation lookup while the caller holds Agent.Mu.
func (invocation *zcodeToolInvocation) metadata(existing []byte) []byte {
	var fields map[string]json.RawMessage
	if len(existing) > 0 {
		_ = json.Unmarshal(existing, &fields)
	}
	if fields == nil {
		fields = make(map[string]json.RawMessage)
	}
	callID, sessionID := invocation.nativeCallID, invocation.sessionID
	if invocation.native.ChildSessionID != "" {
		sessionID = invocation.native.ChildSessionID
	}
	if invocation.native.AgentID != "" {
		prefix := "tool_subagent_" + invocation.native.AgentID + "_"
		callID = strings.TrimPrefix(callID, prefix)
	}
	value, _ := json.Marshal(contracts.ZCodeInvocationMetadata{SpanID: invocation.spanID, CallID: callID, MessageID: invocation.messageID, SessionID: sessionID, ToolName: invocation.name})
	fields[contracts.ZCodeWorkerMetadataInvocation] = value
	encoded, _ := json.Marshal(fields)
	return encoded
}

// zcodeControlOrigin identifies a native child interaction independently of its
// parent stream's qualified tool ID.
type zcodeControlOrigin struct {
	Kind             string `json:"kind"`
	AgentID          string `json:"agentId"`
	ChildSessionID   string `json:"childSessionId"`
	ChildTurnID      string `json:"childTurnId"`
	ParentSessionID  string `json:"parentSessionId"`
	ParentTurnID     string `json:"parentTurnId"`
	ParentToolCallID string `json:"parentToolCallId"`
}

func (a *Agent) resolveZCodeControlSpan(sessionID, turnID, callID string, origin zcodeControlOrigin) string {
	if callID == "" {
		return ""
	}
	a.Mu.Lock()
	defer a.Mu.Unlock()
	if sessionID != "" && sessionID != a.sessionID || origin.ParentSessionID != "" && origin.ParentSessionID != a.sessionID {
		return ""
	}
	child := origin.Kind == "subagent" || origin.ChildSessionID != "" || origin.AgentID != ""
	if child {
		turnID = origin.ParentTurnID
	}
	wireCallID := callID
	if child && origin.AgentID != "" {
		wireCallID = "tool_subagent_" + origin.AgentID + "_" + callID
	}
	var found *zcodeToolInvocation
	for _, owner := range a.turns {
		if turnID != "" && owner.id != turnID {
			continue
		}
		invocation := a.findZCodeInvocationLocked(owner, wireCallID, "")
		if invocation == nil {
			continue
		}
		native := invocation.native
		if child {
			if origin.ChildSessionID != "" && native.ChildSessionID != origin.ChildSessionID {
				continue
			}
			if origin.ParentToolCallID != "" && native.ParentToolCallID != origin.ParentToolCallID {
				continue
			}
		}
		if found != nil {
			return ""
		}
		found = invocation
	}
	if found == nil {
		return ""
	}
	return found.spanID
}
