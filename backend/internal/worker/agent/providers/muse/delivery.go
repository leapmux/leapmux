package muse

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/leapmux/leapmux/internal/worker/agent"
)

type nativeParentInputStatus uint8

const (
	nativeParentInputPending nativeParentInputStatus = iota + 1
	nativeParentInputMatched
	nativeParentInputHuman
	nativeParentInputIgnored
	nativeParentInputInvalid
)

type nativeParentInputTarget struct {
	ParentSessionID string
	ChildSessionID  string
	SubagentID      string
	RunID           string
}

type nativeParentInputMatch struct {
	Status         nativeParentInputStatus
	Original       json.RawMessage
	Records        []json.RawMessage
	IdempotencyKey string
	InboxID        string
	Reason         string
}

type nativeInputEnvelope struct {
	SchemaVersion        int          `json:"schema_version"`
	ID                   string       `json:"id"`
	Sequence             int64        `json:"sequence"`
	Stream               nativeStream `json:"stream"`
	RecordType           string       `json:"record_type"`
	Durability           string       `json:"durability"`
	CausationID          *string      `json:"causation_id"`
	PayloadType          string       `json:"payload_type"`
	PayloadSchemaVersion int          `json:"payload_schema_version"`
	Payload              struct {
		Kind   string          `json:"kind"`
		RunID  string          `json:"run_id"`
		Event  json.RawMessage `json:"event"`
		Record json.RawMessage `json:"record"`
	} `json:"payload"`
}

type nativeInputEvidence struct {
	stored    storedRecord
	envelope  nativeInputEnvelope
	decodeErr error
}

func inputEvidence(stored storedRecord) nativeInputEvidence {
	value := nativeInputEvidence{stored: stored}
	value.decodeErr = json.Unmarshal(stored.raw, &value.envelope)
	return value
}

type nativeInputSource struct {
	Source          string          `json:"source"`
	InputKind       string          `json:"input_kind"`
	CommandID       string          `json:"command_id"`
	ParentSessionID string          `json:"parent_session_id"`
	SubagentID      string          `json:"subagent_id"`
	Caller          json.RawMessage `json:"caller"`
}

type nativeInputDrain struct {
	Kind     string       `json:"kind"`
	ItemID   string       `json:"item_id"`
	Target   nativeStream `json:"drain_target_run_stream"`
	Snapshot *struct {
		Source json.RawMessage `json:"source"`
		Body   json.RawMessage `json:"body"`
	} `json:"delivery_snapshot"`
}

type nativeInputControl struct {
	Kind              string `json:"kind"`
	ParentSessionID   string `json:"parent_session_id"`
	SubagentID        string `json:"subagent_id"`
	AgentPath         string `json:"agent_path"`
	ChildSessionID    string `json:"child_session_id"`
	SubagentSessionID string `json:"subagent_session_id"`
	RunStream         string `json:"run_stream"`
}

type nativeInputReceipt struct {
	evidence nativeInputEvidence
	index    int
	callID   string
	accepted bool
}

func inputOutcome(status nativeParentInputStatus, reason string) nativeParentInputMatch {
	return nativeParentInputMatch{Status: status, Reason: reason}
}

func nativeInputEvidenceFailure(log *nativeLog, evidence nativeInputEvidence) string {
	if evidence.decodeErr != nil {
		return "The native input evidence is malformed."
	}
	value, record := evidence.envelope, evidence.stored.record
	if value.SchemaVersion != 1 || value.RecordType != contracts.MuseParentInputEvent || value.Durability != contracts.MuseParentInputDurable || value.PayloadSchemaVersion <= 0 || value.PayloadType == "" {
		return "The native input evidence has an invalid envelope."
	}
	if value.ID == "" || value.Sequence <= 0 || value.ID != record.ID || value.Sequence != record.Sequence || value.Stream != record.Stream || value.Stream.Kind != contracts.MuseStreamKindSession || value.Stream.ID != log.sessionID {
		return "The native input evidence belongs to another stream or position."
	}
	if log.invalid[record.ID] || log.sequences[record.Sequence] != record.ID {
		return "The native input evidence has a conflicting identity."
	}
	retained, exists := log.records[record.ID]
	if !exists || !bytes.Equal(retained.raw, evidence.stored.raw) {
		return "The native input evidence differs from its retained record."
	}
	return ""
}

// nativeInputString checks the JSON type. The backend does not use the body's
// text to associate records or produce a rendered message.
func nativeInputString(raw json.RawMessage) (string, bool) {
	var value *string
	if len(raw) == 0 || json.Unmarshal(raw, &value) != nil || value == nil {
		return "", false
	}
	return *value, true
}

func nativeInputOptionalString(fields map[string]json.RawMessage, key string) (string, bool, string) {
	raw, present := fields[key]
	if !present {
		return "", false, ""
	}
	value, valid := nativeInputString(raw)
	if !valid {
		return "", true, "A native input argument has an invalid string value."
	}
	return value, true, ""
}

func nativeInputRequiredString(fields map[string]json.RawMessage, key string) (string, string) {
	value, present, failure := nativeInputOptionalString(fields, key)
	if failure != "" {
		return "", failure
	}
	if !present {
		return "", "A required native input argument is absent."
	}
	return value, ""
}

// An absent caller requires independent native provenance. Optional source
// version metadata does not establish or remove caller authority.
func nativeInputCaller(source nativeInputSource, parent string) (bool, string) {
	if len(source.Caller) == 0 {
		return false, ""
	}
	var caller *struct {
		Kind    string `json:"kind"`
		ActorID string `json:"actor_id"`
	}
	if json.Unmarshal(source.Caller, &caller) != nil || caller == nil || strings.TrimSpace(caller.Kind) == "" || strings.TrimSpace(caller.ActorID) == "" {
		return false, "The native input caller is invalid."
	}
	if caller.Kind == contracts.MuseParentInputUser {
		return true, ""
	}
	if caller.ActorID != parent || (caller.Kind != contracts.MuseParentInputParentAgent && caller.Kind != contracts.MuseParentInputChildAgent) {
		return false, "The native input caller is not the owned parent."
	}
	return false, ""
}

func nativeInputHumanIntake(log *nativeLog, source nativeInputSource) (bool, string) {
	matched := false
	for _, stored := range log.records {
		if stored.record.Payload.Kind != contracts.MuseParentInputCommandIntake {
			continue
		}
		evidence := inputEvidence(stored)
		var record struct {
			Kind          string       `json:"kind"`
			CommandID     string       `json:"command_id"`
			SessionStream nativeStream `json:"session_stream"`
			Command       struct {
				Kind       string `json:"kind"`
				Verb       string `json:"verb"`
				SubagentID string `json:"subagent_id"`
			} `json:"command"`
		}
		if json.Unmarshal(evidence.envelope.Payload.Record, &record) != nil || record.Kind != contracts.MuseParentInputReceived || record.CommandID != source.CommandID {
			continue
		}
		if record.SessionStream != (nativeStream{Kind: contracts.MuseStreamKindSession, ID: source.ParentSessionID}) || record.Command.Kind != contracts.MuseParentInputSteering || record.Command.Verb != source.InputKind || record.Command.SubagentID != source.SubagentID {
			return false, "The native human input admission has a conflicting route."
		}
		if failure := nativeInputEvidenceFailure(log, evidence); failure != "" {
			return false, failure
		}
		if evidence.envelope.PayloadType != contracts.MuseParentInputIntakePayloadType {
			return false, "The native human input admission has an invalid type."
		}
		matched = true
	}
	return matched, ""
}

func selectNativeInputControl(log *nativeLog, kind string, matches func(nativeInputControl) bool) (nativeInputEvidence, nativeInputControl, int) {
	var selected nativeInputEvidence
	var control nativeInputControl
	count := 0
	for _, stored := range log.records {
		if stored.record.Payload.Kind != contracts.MuseParentInputSubagentControl {
			continue
		}
		evidence := inputEvidence(stored)
		var candidate nativeInputControl
		if json.Unmarshal(evidence.envelope.Payload.Record, &candidate) != nil || candidate.Kind != kind || !matches(candidate) {
			continue
		}
		count++
		selected, control = evidence, candidate
	}
	return selected, control, count
}

func nativeInputReceipts(log *nativeLog, inboxID, subagentID string) ([]nativeInputReceipt, string) {
	var selected []nativeInputReceipt
	for _, stored := range log.records {
		record := stored.record
		if record.Payload.Kind != contracts.MuseParentInputRun || record.Payload.Event.Kind != contracts.MuseLogEventToolResultBatchCommitted {
			continue
		}
		for _, entry := range record.Payload.Event.Results {
			if entry == nil || entry.Text == nil {
				continue
			}
			var result struct {
				Status       string    `json:"status"`
				SubagentID   string    `json:"subagent_id"`
				EvidenceRefs []*string `json:"evidence_refs"`
			}
			if json.Unmarshal([]byte(*entry.Text), &result) != nil {
				continue
			}
			references := 0
			for _, ref := range result.EvidenceRefs {
				if ref != nil && *ref == inboxID {
					references++
				}
			}
			if references == 0 {
				continue
			}
			evidence := inputEvidence(stored)
			if failure := nativeInputEvidenceFailure(log, evidence); failure != "" {
				return selected, failure
			}
			if references != 1 || (result.Status != contracts.MuseParentInputAccepted && result.Status != contracts.MuseParentInputDuplicate) || result.SubagentID != subagentID || entry.Index == nil || *entry.Index < 0 || entry.CallID == nil || *entry.CallID == "" || record.Payload.RunID == "" || record.Payload.Event.BatchID == "" {
				return selected, "The native input receipt is invalid."
			}
			for _, ref := range result.EvidenceRefs {
				if ref == nil || *ref == "" {
					return selected, "The native input receipt has an invalid evidence reference."
				}
			}
			selected = append(selected, nativeInputReceipt{evidence: evidence, index: *entry.Index, callID: *entry.CallID, accepted: result.Status == contracts.MuseParentInputAccepted})
		}
	}
	return selected, ""
}

func nativeInputArgumentsFailure(raw string, source nativeInputSource, spawn nativeInputControl) string {
	var fields map[string]json.RawMessage
	if json.Unmarshal([]byte(raw), &fields) != nil || fields == nil {
		return "The native parent input arguments are invalid."
	}
	commandID, failure := nativeInputRequiredString(fields, "command_id")
	if failure != "" {
		return failure
	}
	if commandID == "" || commandID != source.CommandID {
		return "The native parent input command does not match its delivery."
	}
	if _, failure := nativeInputRequiredString(fields, "message"); failure != "" {
		return failure
	}
	id, present, failure := nativeInputOptionalString(fields, "subagent_id")
	if failure != "" {
		return failure
	}
	if present && (id == "" || id != source.SubagentID) {
		return "The native parent input selects another subagent."
	}
	path, present, failure := nativeInputOptionalString(fields, "agent_path")
	if failure != "" {
		return failure
	}
	if present && (path == "" || path != spawn.AgentPath) {
		return "The native parent input selects another agent path."
	}
	if _, _, failure := nativeInputOptionalString(fields, "artifact_ref"); failure != "" {
		return failure
	}
	mode, present, failure := nativeInputOptionalString(fields, "mode")
	if failure != "" {
		return failure
	}
	if present {
		switch mode {
		case contracts.MuseParentInputQueue:
			if source.InputKind != contracts.MuseParentInputSendMessage {
				return "The native parent input mode conflicts with its delivery."
			}
		case contracts.MuseParentInputFollowup:
			if source.InputKind != contracts.MuseParentInputFollowupTask {
				return "The native parent input mode conflicts with its delivery."
			}
		default:
			return "The native parent input mode is unsupported."
		}
	}
	if raw, present := fields["interrupt"]; present {
		var interrupt *bool
		if json.Unmarshal(raw, &interrupt) != nil || interrupt == nil {
			return "The native parent input interrupt option is invalid."
		}
	}
	return ""
}

// matchNativeParentInput proves the exact receipt and owned destination.
// The caller holds stateMu or supplies a stable log. No cache mutates here.
func matchNativeParentInput(log *nativeLog, drain storedRecord, target nativeParentInputTarget) nativeParentInputMatch {
	pending := func(reason string) nativeParentInputMatch { return inputOutcome(nativeParentInputPending, reason) }
	invalid := func(reason string) nativeParentInputMatch { return inputOutcome(nativeParentInputInvalid, reason) }
	if log == nil {
		return pending("The native parent log is unavailable.")
	}
	if target.ParentSessionID == "" || target.ChildSessionID == "" || target.SubagentID == "" || target.RunID == "" || log.sessionID != target.ParentSessionID {
		return invalid("The native child input target is invalid.")
	}
	drainEvidence := inputEvidence(drain)
	if failure := nativeInputEvidenceFailure(log, drainEvidence); failure != "" {
		return invalid(failure)
	}
	if drainEvidence.envelope.Payload.Kind != contracts.MuseParentInputRun {
		return inputOutcome(nativeParentInputIgnored, "The native record is not a run input.")
	}
	var event nativeInputDrain
	if json.Unmarshal(drainEvidence.envelope.Payload.Event, &event) != nil {
		return invalid("The native input delivery is malformed.")
	}
	if event.Kind != contracts.MuseLogEventInboxItemDrained {
		return inputOutcome(nativeParentInputIgnored, "The native record is not an input delivery.")
	}
	if event.Snapshot == nil {
		return invalid("The native input delivery has no snapshot.")
	}
	var source nativeInputSource
	if len(event.Snapshot.Source) == 0 || bytes.Equal(bytes.TrimSpace(event.Snapshot.Source), []byte("null")) || json.Unmarshal(event.Snapshot.Source, &source) != nil {
		return invalid("The native input delivery has an invalid source.")
	}
	if source.Source != contracts.MuseParentInputInputSource {
		return inputOutcome(nativeParentInputIgnored, "The native delivery is not a child input.")
	}
	if event.ItemID == "" || drainEvidence.envelope.CausationID != nil && *drainEvidence.envelope.CausationID != event.ItemID || event.Target != (nativeStream{Kind: contracts.MuseParentInputRun, ID: target.RunID}) || source.CommandID == "" || source.ParentSessionID != target.ParentSessionID || source.SubagentID != target.SubagentID {
		return invalid("The native input delivery has a conflicting identity.")
	}
	if _, valid := nativeInputString(event.Snapshot.Body); !valid {
		return invalid("The native input delivery has no string body.")
	}
	if source.InputKind != contracts.MuseParentInputSendMessage && source.InputKind != contracts.MuseParentInputFollowupTask {
		return invalid("The native child input operation is unsupported.")
	}
	human, failure := nativeInputCaller(source, target.ParentSessionID)
	if failure != "" {
		return invalid(failure)
	}
	if human {
		return inputOutcome(nativeParentInputHuman, "The worker owns the human child input.")
	}
	human, failure = nativeInputHumanIntake(log, source)
	if failure != "" {
		return invalid(failure)
	}
	if human {
		if len(source.Caller) != 0 {
			return invalid("The native input has conflicting caller provenance.")
		}
		return inputOutcome(nativeParentInputHuman, "The worker owns the admitted human child input.")
	}
	spawn, spawnControl, count := selectNativeInputControl(log, contracts.MuseParentInputSpawnAccepted, func(control nativeInputControl) bool { return control.SubagentID == target.SubagentID })
	if count == 0 {
		return pending("The native child spawn evidence is unavailable.")
	}
	if count != 1 || spawnControl.ParentSessionID != target.ParentSessionID {
		return invalid("The native child spawn evidence is ambiguous.")
	}
	binding, _, count := selectNativeInputControl(log, contracts.MuseParentInputChildSessionBound, func(control nativeInputControl) bool {
		return control.SubagentID == target.SubagentID && control.ChildSessionID == target.ChildSessionID
	})
	if count == 0 {
		return pending("The native child binding is unavailable.")
	}
	if count != 1 {
		return invalid("The native child binding is ambiguous.")
	}
	attestation, _, count := selectNativeInputControl(log, contracts.MuseParentInputStartAttested, func(control nativeInputControl) bool {
		return control.SubagentID == target.SubagentID && control.SubagentSessionID == target.ChildSessionID && control.RunStream == contracts.MuseParentInputRun+"/"+target.RunID
	})
	if count == 0 {
		return pending("The native child run attestation is unavailable.")
	}
	if count != 1 {
		return invalid("The native child run attestation is ambiguous.")
	}
	for _, evidence := range []nativeInputEvidence{spawn, binding, attestation} {
		if failure := nativeInputEvidenceFailure(log, evidence); failure != "" {
			return invalid(failure)
		}
	}
	receipts, failure := nativeInputReceipts(log, event.ItemID, target.SubagentID)
	if failure != "" {
		return invalid(failure)
	}
	if len(receipts) == 0 {
		return pending("The native parent input receipt is unavailable.")
	}
	var selectedReceipt nativeInputReceipt
	var selectedOrigin nativeInputEvidence
	var canonicalArguments string
	accepted := false
	for _, receipt := range receipts {
		batch := receipt.evidence.stored.record
		var origin nativeInputEvidence
		count := 0
		for _, candidate := range log.records {
			record := candidate.record
			if record.Payload.Kind == contracts.MuseParentInputRun && record.Payload.RunID == batch.Payload.RunID && record.Payload.Event.Kind == contracts.MuseLogEventAssistantToolCallsCommitted && record.Payload.Event.MessageID == batch.Payload.Event.BatchID {
				origin = inputEvidence(candidate)
				count++
			}
		}
		if count == 0 {
			return pending("The native parent input origin is unavailable.")
		}
		if count != 1 {
			return invalid("The native parent input origin is ambiguous.")
		}
		if failure := nativeInputEvidenceFailure(log, origin); failure != "" {
			return invalid(failure)
		}
		calls := origin.stored.record.Payload.Event.Calls
		if receipt.index >= len(calls) {
			return invalid("The native input receipt selects an invalid call index.")
		}
		call := calls[receipt.index]
		if call == nil || call.CallID == nil || call.Name == nil || call.Args == nil || *call.CallID != receipt.callID || *call.Name != contracts.MuseParentInputTool {
			return invalid("The native input receipt does not identify a parent send.")
		}
		callMatches := 0
		for _, candidate := range calls {
			if candidate != nil && candidate.CallID != nil && *candidate.CallID == receipt.callID {
				callMatches++
			}
		}
		if callMatches != 1 {
			return invalid("The native parent call identity is ambiguous.")
		}
		if failure := nativeInputArgumentsFailure(*call.Args, source, spawnControl); failure != "" {
			return invalid(failure)
		}

		if canonicalArguments != "" && !agent.JSONCanonicalEqual([]byte(canonicalArguments), []byte(*call.Args)) {
			return invalid("The native command retry has conflicting arguments.")
		}
		canonicalArguments = *call.Args
		if receipt.accepted && (!accepted || receipt.evidence.stored.record.Sequence < selectedReceipt.evidence.stored.record.Sequence) {
			selectedReceipt, selectedOrigin, accepted = receipt, origin, true
		}
	}
	if !accepted {
		return pending("The native parent input accepted receipt is unavailable.")
	}
	identity := fmt.Sprintf("%q:%q:%q", target.ParentSessionID, target.ChildSessionID, event.ItemID)
	sum := sha256.Sum256([]byte(identity))
	return nativeParentInputMatch{Status: nativeParentInputMatched, Original: slices.Clone(drain.raw), Records: []json.RawMessage{slices.Clone(selectedOrigin.stored.raw), slices.Clone(selectedReceipt.evidence.stored.raw), slices.Clone(spawn.stored.raw), slices.Clone(binding.stored.raw), slices.Clone(attestation.stored.raw)}, IdempotencyKey: fmt.Sprintf("muse:child-input:%x", sum), InboxID: event.ItemID}
}
