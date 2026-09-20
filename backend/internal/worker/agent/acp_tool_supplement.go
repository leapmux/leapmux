package agent

import (
	"encoding/json"
	"fmt"

	"github.com/leapmux/leapmux/generated/contracts"
)

// acpToolSupplement is the envelope a tool row keeps beside the agent's own frame.
//
// Six providers speak the Agent Client Protocol, and every one of them stores the
// same envelope: the identity of the row it belongs to, the request fields a later
// `tool_call_update` revised, and the payloads LeapMux joined on. The browser plugin
// reads the envelope back, so every key is a contract constant
// (contracts/acp-protocol.json) rather than a literal spelled once in each language.
//
// It stays a MAP rather than a struct because it is open by design: a provider adds
// its own key beside the shared ones -- Cursor stores an extension frame under
// `cursorExtension`, and both Cursor and Reasonix store a native record under
// `rawOutput` -- and a struct would drop those keys the first time a shared helper
// re-encoded the envelope. The methods below are what keep the map from being bare.
type acpToolSupplement map[string]json.RawMessage

// The two key SETS come from the contract, not from a list spelled here. The browser
// derives the same sets with `Object.values()`, so a hand-written copy would check a
// different number of keys once a set grew, and neither side would fail.
//
//   - contracts.ACPSupplementIdentityKeys: the set a supplement must match before it
//     can reach a row. A supplement is stored beside ONE frame, and a row draws the
//     join only when every one of these agrees -- so a retained frame cannot take
//     another call's output, and a frame whose status moved on cannot take a snapshot
//     of the state before it.
//   - contracts.ACPSupplementRequestKeys: the request fields a later
//     `tool_call_update` revises. The protocol lets an agent open a tool call with
//     nothing but an id and fill the title, the kind, the input and the locations
//     afterwards. The row is already stored by then, so the revision lands here and
//     the resolve puts it back on top.

// newACPToolSupplement starts an envelope that identifies the frame it belongs to.
func newACPToolSupplement(original map[string]json.RawMessage) acpToolSupplement {
	supplement := make(acpToolSupplement, len(contracts.ACPSupplementIdentityKeys))
	for _, key := range contracts.ACPSupplementIdentityKeys {
		if value, exists := original[key]; exists {
			supplement[key] = value
		}
	}
	return supplement
}

// identityMatches reports whether this supplement belongs to that frame.
//
// PRESENCE and VALUE must agree on every identity key. A key present on one side
// alone is a mismatch rather than a skip: a frame that states no status and a
// supplement that states one describe two different moments of the same call.
//
// The frame must also IDENTIFY a call. A supplement belongs beside exactly one tool call,
// so an envelope with no id matches nothing -- and without this test two different
// calls that both omitted the id would each take the other's supplement.
func (s acpToolSupplement) identityMatches(original map[string]json.RawMessage) bool {
	var toolCallID string
	if json.Unmarshal(original[contracts.ACPSupplementIdentityToolCallID], &toolCallID) != nil || toolCallID == "" {
		return false
	}
	for _, key := range contracts.ACPSupplementIdentityKeys {
		before, present := original[key]
		after, supplied := s[key]
		if present != supplied {
			return false
		}
		if !present {
			continue
		}
		var first, second string
		if json.Unmarshal(before, &first) != nil || json.Unmarshal(after, &second) != nil || first != second {
			return false
		}
	}
	return true
}

// setProtocol records the state fields the original frame did not carry.
func (s acpToolSupplement) setProtocol(protocol map[string]json.RawMessage) error {
	encoded, err := json.Marshal(protocol)
	if err != nil {
		return fmt.Errorf("encode ACP protocol supplement: %w", err)
	}
	s[contracts.ACPSupplementProtocol] = encoded
	return nil
}

// setTerminals records the output of every terminal this tool call refers to.
func (s acpToolSupplement) setTerminals(terminals map[string]contracts.ACPTerminalResult) error {
	encoded, err := json.Marshal(terminals)
	if err != nil {
		return fmt.Errorf("encode ACP terminal supplement: %w", err)
	}
	s[contracts.ACPSupplementTerminals] = encoded
	return nil
}

// setRawOutput records the native record a provider read out of its own transcript.
func (s acpToolSupplement) setRawOutput(output any) error {
	encoded, err := json.Marshal(output)
	if err != nil {
		return fmt.Errorf("encode ACP raw output supplement: %w", err)
	}
	s[contracts.ACPSupplementRawOutput] = encoded
	return nil
}
