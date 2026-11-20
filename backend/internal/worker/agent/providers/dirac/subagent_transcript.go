package dirac

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/leapmux/leapmux/generated/contracts"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/sessionstore"
)

type diracToolDeclaration struct {
	Type string `json:"type"`
	ID   string `json:"id"`
	Name string `json:"name"`
}

func diracTranscriptRecordKey(record contracts.DiracTranscriptRecord) (string, error) {
	if record.SessionID == "" || record.ParentSessionID == "" || record.TaskletID == "" || record.SessionID == record.ParentSessionID {
		return "", errors.New("the Dirac transcript record has no distinct native owner")
	}
	if record.Type == contracts.DiracRecordKindMessage {
		message, err := decodeDiracMarkdown(record.Message)
		if err != nil || record.Role != "" || len(record.Declaration) != 0 || len(record.Result) != 0 {
			return "", errors.New("the Dirac presentation record has invalid message fields")
		}
		if _, err := diracMessageCompletion(record); err != nil {
			return "", err
		}
		raw, _ := json.Marshal([]string{record.Type, record.SessionID, record.ParentSessionID, record.TaskletID, message.ID})
		digest := sha256.Sum256(raw)
		return "dirac-record:" + hex.EncodeToString(digest[:]), nil
	}
	var declaration diracToolDeclaration
	if err := json.Unmarshal(record.Declaration, &declaration); err != nil || declaration.Type != contracts.DiracNativeBlockToolUse || declaration.ID == "" || declaration.Name == "" {
		return "", errors.New("the Dirac tool record has no native declaration identity")
	}
	if record.Type != contracts.DiracRecordKindTool || len(record.Message) != 0 || len(record.RequestContext) != 0 || len(record.TaskletOutcome) != 0 ||
		(record.Role != contracts.DiracRecordRoleRequest && record.Role != contracts.DiracRecordRoleResult && record.Role != contracts.DiracRecordRoleRetained) {
		return "", errors.New("the Dirac tool record has no complete native owner")
	}
	if record.Role != contracts.DiracRecordRoleResult && len(record.Result) != 0 {
		return "", errors.New("the Dirac declaration record contains a later result")
	}
	if record.Role == contracts.DiracRecordRoleResult && len(record.Result) == 0 {
		return "", errors.New("the Dirac result record has no native result block")
	}
	if len(record.Result) != 0 {
		var result struct {
			Type string `json:"type"`
			ID   string `json:"tool_use_id"`
		}
		if err := json.Unmarshal(record.Result, &result); err != nil || result.Type != contracts.DiracNativeBlockToolResult || result.ID != declaration.ID {
			return "", errors.New("the Dirac tool result belongs to another declaration")
		}
	}
	raw, _ := json.Marshal([]string{record.Type, record.SessionID, record.ParentSessionID, record.TaskletID, record.Role, declaration.ID})
	digest := sha256.Sum256(raw)
	return "dirac-record:" + hex.EncodeToString(digest[:]), nil
}

func readDiracTaskletHistory(profile string, expected diracTaskletRecord) (api, presentation []json.RawMessage, err error) {
	if !safeDiracComponent(expected.RootTaskID) || !validDiracTaskletID(expected.ID) {
		return nil, nil, errors.New("the Dirac tasklet history has an invalid owner")
	}
	root, err := sessionstore.OpenArchiveRoot(profile)
	if err != nil {
		return nil, nil, err
	}
	defer func() {
		if closeErr := root.Close(); closeErr != nil {
			api, presentation, err = nil, nil, errors.Join(err, closeErr)
		}
	}()
	chain, err := sessionstore.OpenCheckedArchiveDirectoryChain(root, "data", "tasks", expected.RootTaskID, "tasklets", expected.ID)
	if err != nil {
		return nil, nil, err
	}
	defer func() {
		if closeErr := chain.Close(); closeErr != nil {
			api, presentation, err = nil, nil, errors.Join(err, closeErr)
		}
	}()
	validateOwner := func() error {
		raw, err := readDiracRegularFile(chain.Root(), "tasklet.json", diracTaskHistoryLimit)
		if err != nil {
			return err
		}
		current, err := decodeDiracTaskletRecord(raw, expected.RootTaskID, expected.ID)
		if err != nil {
			return err
		}
		if current.ConversationID != expected.ConversationID {
			return errors.New("the native Dirac tasklet conversation changed during history recovery")
		}
		return nil
	}
	if err := validateOwner(); err != nil {
		return nil, nil, err
	}
	files, err := readDiracAPIHistoryFiles(chain.Root())
	if err != nil {
		return nil, nil, err
	}
	if err := validateOwner(); err != nil {
		return nil, nil, err
	}
	api, err = decodeDiracAPIHistory(files.snapshot, files.baseline, files.operations)
	if err != nil {
		return nil, nil, err
	}
	presentation, err = readDiracPresentation(chain.Root())
	if err != nil {
		return nil, nil, err
	}
	if err := validateOwner(); err != nil {
		return nil, nil, err
	}
	return api, presentation, nil
}

func diracTaskletToolRecords(parentSessionID string, record diracTaskletRecord, messages []json.RawMessage) ([]contracts.DiracTranscriptRecord, error) {
	declarations := make(map[string]json.RawMessage)
	for _, message := range messages {
		fields, blocks, err := diracAPIMessage(message)
		if err != nil {
			return nil, err
		}
		role, _ := diracStoredString(fields, "role")
		if role != "assistant" {
			continue
		}
		for _, block := range blocks {
			var declaration diracToolDeclaration
			if err := json.Unmarshal(block, &declaration); err != nil {
				return nil, err
			}
			if declaration.Type != contracts.DiracNativeBlockToolUse {
				continue
			}
			if declaration.ID == "" || declaration.Name == "" {
				return nil, errors.New("the native Dirac tool declaration is incomplete")
			}
			if _, duplicate := declarations[declaration.ID]; duplicate {
				return nil, errors.New("the native Dirac history repeats a tool declaration ID")
			}
			declarations[declaration.ID] = block
		}
	}
	result := make([]contracts.DiracTranscriptRecord, 0)
	results := make(map[string]struct{})
	for _, message := range messages {
		fields, blocks, err := diracAPIMessage(message)
		if err != nil {
			return nil, err
		}
		role, _ := diracStoredString(fields, "role")
		for _, block := range blocks {
			var head struct {
				Type   string `json:"type"`
				ID     string `json:"id"`
				ToolID string `json:"tool_use_id"`
			}
			if err := json.Unmarshal(block, &head); err != nil {
				return nil, err
			}
			item := contracts.DiracTranscriptRecord{Type: contracts.DiracRecordKindTool, SessionID: record.ConversationID, ParentSessionID: parentSessionID, TaskletID: record.ID}
			switch {
			case role == "assistant" && head.Type == contracts.DiracNativeBlockToolUse:
				item.Role, item.Declaration = contracts.DiracRecordRoleRequest, block
			case role == "user" && head.Type == contracts.DiracNativeBlockToolResult:
				declaration, exists := declarations[head.ToolID]
				if !exists {
					return nil, errors.New("the native Dirac result has no owning declaration")
				}
				if _, duplicate := results[head.ToolID]; duplicate {
					return nil, errors.New("the native Dirac history repeats a tool result ID")
				}
				results[head.ToolID] = struct{}{}
				item.Role, item.Declaration, item.Result = contracts.DiracRecordRoleResult, declaration, block
			default:
				continue
			}
			if _, err := diracTranscriptRecordKey(item); err != nil {
				return nil, err
			}
			result = append(result, item)
		}
	}
	if record.Outcome != nil {
		for _, request := range result {
			if request.Role != contracts.DiracRecordRoleRequest {
				continue
			}
			var declaration diracToolDeclaration
			if err := json.Unmarshal(request.Declaration, &declaration); err != nil {
				return nil, err
			}
			if _, complete := results[declaration.ID]; complete {
				continue
			}
			request.Role = contracts.DiracRecordRoleRetained
			result = append(result, request)
		}
	}
	return result, nil
}

type diracTaskletTranscript struct {
	agent.ProviderServices
	queue       *agent.OutputQueue
	scope       agent.TranscriptScope
	lifecycleID string
}

func captureDiracTaskletTranscript(services agent.ProviderServices, sessionID string) (*diracTaskletTranscript, error) {
	direct, valid := agent.DirectTranscript(services).(agent.ProviderServices)
	if !valid {
		return nil, errors.New("the Dirac child has no provider transcript services")
	}
	writer := &diracTaskletTranscript{ProviderServices: direct, queue: agent.OutputQueueFor(services)}
	anchor := agent.CaptureTranscript(writer, agent.MessageContent{AgentSessionID: sessionID}, agent.SpanInfo{NoSpan: true})
	scope, err := writer.TranscriptScopeFor(anchor)
	if err != nil {
		return nil, err
	}
	writer.scope = scope
	return writer, nil
}

func (writer *diracTaskletTranscript) OutputQueue() *agent.OutputQueue { return writer.queue }

func (writer *diracTaskletTranscript) PersistMessage(source leapmuxv1.MessageSource, content agent.MessageContent, span agent.SpanInfo) error {
	var record contracts.DiracTranscriptRecord
	if err := json.Unmarshal(content.Original, &record); err != nil {
		return err
	}
	key, err := diracTranscriptRecordKey(record)
	if err != nil {
		return err
	}
	if content.AgentSessionID != record.SessionID || content.IdempotencyKey != key || source != leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT {
		return errors.New("the captured Dirac tool record has another destination or key")
	}
	stored, err := writer.ReadMessageByIdempotencyKey(record.SessionID, key)
	if err != nil {
		return err
	}
	if stored != nil {
		if record.Type == contracts.DiracRecordKindMessage {
			return writer.updateMessage(stored, record, content, span)
		}
		if !bytes.Equal(stored.Content.Original, content.Original) {
			return errors.New("the immutable Dirac tool identity contains different native data")
		}
		content.WriteReceipt.RecordStoredMessage(stored.Seq)
		content.WriteReceipt.RecordStoredWrite(false)
		return nil
	}
	return writer.ProviderServices.PersistMessage(source, content, span)
}

func (writer *diracTaskletTranscript) publish(records []contracts.DiracTranscriptRecord) error {
	for _, record := range records {
		key, err := diracTranscriptRecordKey(record)
		if err != nil {
			return err
		}
		raw, err := json.Marshal(record)
		if err != nil {
			return err
		}
		if record.Type == contracts.DiracRecordKindMessage {
			completion, err := diracMessageCompletion(record)
			if err != nil {
				return err
			}
			captured, err := writer.scope.CaptureTranscript(agent.MessageContent{Original: raw, AgentSessionID: record.SessionID, IdempotencyKey: key, Completion: completion}, agent.SpanInfo{NoSpan: true})
			if err != nil {
				return err
			}
			if err := captured.PersistMessage(leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT); err != nil {
				return err
			}
			continue
		}
		var declaration diracToolDeclaration
		if err := json.Unmarshal(record.Declaration, &declaration); err != nil {
			return err
		}
		request := record
		request.Role, request.Result = contracts.DiracRecordRoleRequest, nil
		spanID, err := diracTranscriptRecordKey(request)
		if err != nil {
			return err
		}
		span := agent.SpanInfo{SpanID: spanID, SpanType: declaration.Name, Closing: record.Role != contracts.DiracRecordRoleRequest}
		span.SpanColor = writer.scope.ReserveSpanColor(spanID, "")
		var completion agent.MessageCompletion
		if record.Role == contracts.DiracRecordRoleRetained {
			completion = agent.MessageCompletionFinished
		}
		if record.Role == contracts.DiracRecordRoleResult {
			completion = agent.MessageCompletionComplete
			var result struct {
				IsError bool `json:"is_error"`
			}
			if err := json.Unmarshal(record.Result, &result); err != nil {
				return err
			}
			if result.IsError {
				completion = agent.MessageCompletionError
			}
		}
		captured, err := writer.scope.CaptureTranscript(agent.MessageContent{Original: raw, AgentSessionID: record.SessionID, IdempotencyKey: key, Completion: completion}, span)
		if err != nil {
			return err
		}
		if err := captured.PersistMessage(leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT); err != nil {
			return fmt.Errorf("publish the native Dirac tool: %w", err)
		}
	}
	return nil
}
