package fastagent

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"slices"

	"github.com/leapmux/leapmux/generated/contracts"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent"
)

type fastagentHistoryReceipt struct {
	actor, file string
	digests     [][sha256.Size]byte
}
type fastagentRootReceipt struct {
	sessionID, activeActor, activeFile string
	histories                          map[string]fastagentHistoryReceipt
}
type fastagentArchiveSource struct {
	captureID, sessionID, actor, file string
	message                           int
	role, field, channel              string
	index                             int
	callID, toolName, spanID          string
	timestamp                         *string
	request                           json.RawMessage
	noSpan                            bool
}
type fastagentArchiveRow struct {
	source      leapmuxv1.MessageSource
	content     agent.MessageContent
	span        agent.SpanInfo
	open, close bool
}
type fastagentArchiveProjection struct {
	rows     []fastagentArchiveRow
	receipt  *fastagentRootReceipt
	coverage []fastagentModelCoverageElement
}
type fastagentRootMessage struct {
	role                     string
	content                  []json.RawMessage
	calls, results, channels []fastagentToolEntry
	stopReason               string
	timestamp                *string
}
type fastagentPendingCall struct {
	raw          json.RawMessage
	name, spanID string
	noSpan       bool
}

// Decode ordered collections without sorting native object members.
func fastagentArchiveArray(raw json.RawMessage) ([]json.RawMessage, error) {
	d := json.NewDecoder(bytes.NewReader(raw))
	token, err := d.Token()
	if err != nil || token != json.Delim('[') {
		return nil, errors.New("the Fast Agent collection is not an array")
	}
	out := make([]json.RawMessage, 0)
	for d.More() {
		if len(out) == fastagentRootElementLimit {
			return nil, errors.New("the Fast Agent collection exceeds its element limit")
		}
		var value json.RawMessage
		if err := d.Decode(&value); err != nil {
			return nil, err
		}
		out = append(out, value)
	}
	token, err = d.Token()
	if err != nil || token != json.Delim(']') {
		return nil, errors.New("the Fast Agent array has no end")
	}
	if _, err := d.Token(); err != io.EOF {
		return nil, errors.New("the Fast Agent array has trailing data")
	}
	return out, nil
}

func fastagentArchiveEntries(raw json.RawMessage) ([]fastagentToolEntry, error) {
	if len(raw) == 0 || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return nil, nil
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	token, err := d.Token()
	if err != nil || token != json.Delim('{') {
		return nil, errors.New("the Fast Agent collection is not an object")
	}
	out := make([]fastagentToolEntry, 0)
	seen := make(map[string]bool)
	for d.More() {
		if len(out) == fastagentRootElementLimit {
			return nil, errors.New("the Fast Agent collection exceeds its element limit")
		}
		token, err := d.Token()
		if err != nil {
			return nil, err
		}
		key, ok := token.(string)
		if !ok || key == "" || seen[key] {
			return nil, errors.New("the Fast Agent object has an invalid or repeated key")
		}
		seen[key] = true
		var value json.RawMessage
		if err := d.Decode(&value); err != nil {
			return nil, err
		}
		out = append(out, fastagentToolEntry{ID: key, Data: value})
	}
	token, err = d.Token()
	if err != nil || token != json.Delim('}') {
		return nil, errors.New("the Fast Agent object has no end")
	}
	if _, err := d.Token(); err != io.EOF {
		return nil, errors.New("the Fast Agent object has trailing data")
	}
	return out, nil
}

func fastagentContentBlocks(raw json.RawMessage) ([]json.RawMessage, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	blocks, err := fastagentArchiveArray(raw)
	if err != nil {
		return nil, err
	}
	for _, block := range blocks {
		var item struct {
			Type string `json:"type"`
		}
		if json.Unmarshal(block, &item) != nil || item.Type == "" {
			return nil, errors.New("the Fast Agent content block has no native type")
		}
	}
	return blocks, nil
}

func decodeFastagentRootMessage(raw json.RawMessage) (fastagentRootMessage, error) {
	var out fastagentRootMessage
	fields, err := fastagentArchiveEntries(raw)
	if err != nil || fields == nil {
		return out, errors.New("the Fast Agent message is not an object")
	}
	for _, field := range fields {
		switch field.ID {
		case "role":
			err = json.Unmarshal(field.Data, &out.role)
		case "content":
			out.content, err = fastagentContentBlocks(field.Data)
		case "tool_calls":
			out.calls, err = fastagentArchiveEntries(field.Data)
		case "tool_results":
			out.results, err = fastagentArchiveEntries(field.Data)
		case "channels":
			out.channels, err = fastagentArchiveEntries(field.Data)
		case "stop_reason":
			var value *string
			err = json.Unmarshal(field.Data, &value)
			if value != nil {
				out.stopReason = *value
			}
		case "timestamp":
			err = json.Unmarshal(field.Data, &out.timestamp)
		}
		if err != nil {
			return out, err
		}
	}
	if out.role != contracts.FastagentArchiveAssistantRole && out.role != contracts.FastagentArchiveUserRole {
		return out, errors.New("the Fast Agent message role is unsupported")
	}
	if out.role == contracts.FastagentArchiveAssistantRole && len(out.results) > 0 || out.role == contracts.FastagentArchiveUserRole && len(out.calls) > 0 {
		return out, errors.New("the Fast Agent tool collection has a conflicting role")
	}
	for _, channel := range out.channels {
		if _, err := fastagentContentBlocks(channel.Data); err != nil {
			return out, err
		}
	}
	return out, nil
}

func fastagentMessageDigest(raw json.RawMessage) ([sha256.Size]byte, error) {
	if !json.Valid(raw) {
		return [sha256.Size]byte{}, errors.New("the Fast Agent message is invalid JSON")
	}
	// Whitespace is irrelevant. Member order and numeric bytes remain native.
	// Hash source runs directly so a large result needs no compacted copy.
	hash := sha256.New()
	start := 0
	inString, escaped := false, false
	for index, character := range raw {
		if inString {
			if escaped {
				escaped = false
			} else if character == '\\' {
				escaped = true
			} else if character == '"' {
				inString = false
			}
			continue
		}
		if character == '"' {
			inString = true
			continue
		}
		if character == ' ' || character == '\t' || character == '\r' || character == '\n' {
			if start < index {
				_, _ = hash.Write(raw[start:index])
			}
			start = index + 1
		}
	}
	_, _ = hash.Write(raw[start:])
	return [sha256.Size]byte(hash.Sum(nil)), nil
}

func captureFastagentRootReceipt(archive *fastagentRootArchive) (*fastagentRootReceipt, error) {
	if archive == nil || archive.sessionID == "" || archive.activeActor == "" {
		return nil, errors.New("the Fast Agent archive has no active owner")
	}
	out := &fastagentRootReceipt{sessionID: archive.sessionID, activeActor: archive.activeActor, histories: make(map[string]fastagentHistoryReceipt)}
	for _, history := range archive.histories {
		if history.actor == "" || !validFastagentHistoryFile(history.file) {
			return nil, errors.New("the Fast Agent history owner is invalid")
		}
		if _, exists := out.histories[history.file]; exists {
			return nil, errors.New("the Fast Agent history has multiple owners")
		}
		digests := make([][sha256.Size]byte, len(history.messages))
		for index, raw := range history.messages {
			digest, err := fastagentMessageDigest(raw)
			if err != nil {
				return nil, err
			}
			digests[index] = digest
		}
		out.histories[history.file] = fastagentHistoryReceipt{actor: history.actor, file: history.file, digests: digests}
		if history.actor == out.activeActor {
			if out.activeFile != "" {
				return nil, errors.New("the Fast Agent actor has multiple histories")
			}
			out.activeFile = history.file
		}
	}
	if out.activeFile == "" {
		return nil, errors.New("the Fast Agent active actor has no history")
	}
	return out, nil
}

// Only a positively fresh session and its confirmed mode authorize an empty receipt.
func freshFastagentRootReceipt(sessionID, actor string) (*fastagentRootReceipt, error) {
	if !safeFastagentID(sessionID) || actor == "" {
		return nil, errors.New("the fresh Fast Agent archive owner is invalid")
	}
	return &fastagentRootReceipt{sessionID: sessionID, activeActor: actor, histories: make(map[string]fastagentHistoryReceipt)}, nil
}

func fastagentArchiveID(source fastagentArchiveSource, prefix string) string {
	value := fmt.Sprintf("%q:%q:%q:%q:%d:%q:%q:%d:%q", source.captureID, source.sessionID, source.actor, source.file, source.message, source.field, source.channel, source.index, source.callID)
	digest := sha256.Sum256([]byte(value))
	return fmt.Sprintf("%s%x", prefix, digest)
}

func makeFastagentArchiveRow(raw json.RawMessage, source fastagentArchiveSource) (fastagentArchiveRow, error) {
	meta := map[string]any{
		contracts.FastagentArchiveFormatKey: contracts.FastagentArchiveFormat, contracts.FastagentArchiveCaptureID: source.captureID,
		contracts.FastagentArchiveSessionID: source.sessionID, contracts.FastagentArchiveActor: source.actor,
		contracts.FastagentArchiveHistoryFile: source.file, contracts.FastagentArchiveMessageIndex: source.message,
		contracts.FastagentArchiveRole: source.role, contracts.FastagentArchiveField: source.field,
	}
	if source.index >= 0 {
		meta[contracts.FastagentArchiveIndex] = source.index
	}
	if source.channel != "" {
		meta[contracts.FastagentArchiveChannel] = source.channel
	}
	if source.callID != "" {
		meta[contracts.FastagentArchiveCallID] = source.callID
		meta[contracts.FastagentArchiveToolName] = source.toolName
		meta[contracts.FastagentArchiveSpanID] = source.spanID
	}
	if source.timestamp != nil {
		meta[contracts.FastagentArchiveTimestamp] = *source.timestamp
	}
	if len(source.request) > 0 {
		meta[contracts.FastagentArchiveRequest] = source.request
	}
	supplement, err := json.Marshal(map[string]any{contracts.FastagentArchiveNamespace: meta})
	if err != nil {
		return fastagentArchiveRow{}, err
	}
	row := fastagentArchiveRow{source: leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, content: agent.MessageContent{Original: slices.Clone(raw), Supplemental: supplement, AgentSessionID: source.sessionID, IdempotencyKey: fastagentArchiveID(source, "fastagent:archive:")}}
	// The sole human dispatch is omitted after its exact input proof. Later
	// native user-role content belongs to a tool-result context, not a human.
	if source.callID != "" {
		row.span = agent.SpanInfo{SpanID: source.spanID, SpanType: source.toolName, Closing: source.field == contracts.FastagentArchiveToolResults, NoSpan: source.noSpan}
		row.open, row.close = source.field == contracts.FastagentArchiveToolCalls, row.span.Closing
	}
	return row, nil
}

func fastagentNativeFinalStop(reason string) bool {
	switch reason {
	case "endTurn", "stopSequence", "maxTokens", "pause", "error", "cancelled", "timeout", "safety", "maxIterations":
		return true
	default:
		return false
	}
}

func decodeFastagentArchiveRequest(raw json.RawMessage) (string, error) {
	var request struct {
		Method string `json:"method"`
		Params struct {
			Name      string          `json:"name"`
			Arguments json.RawMessage `json:"arguments"`
		} `json:"params"`
	}
	if json.Unmarshal(raw, &request) != nil || request.Method != "tools/call" || request.Params.Name == "" {
		return "", errors.New("the Fast Agent native request is invalid")
	}
	args := bytes.TrimSpace(request.Params.Arguments)
	if len(args) > 0 && !bytes.Equal(args, []byte("null")) {
		if _, err := fastagentArchiveEntries(args); err != nil {
			return "", errors.New("the Fast Agent native arguments are invalid")
		}
	}
	return request.Params.Name, nil
}

func validateFastagentArchiveResult(raw json.RawMessage) error {
	fields, err := fastagentArchiveEntries(raw)
	if err != nil || fields == nil {
		return errors.New("the Fast Agent native result is invalid")
	}
	content := false
	for _, field := range fields {
		switch field.ID {
		case "content":
			content = true
			if _, err := fastagentContentBlocks(field.Data); err != nil {
				return err
			}
		case "isError":
			var value *bool
			if json.Unmarshal(field.Data, &value) != nil || value == nil {
				return errors.New("the Fast Agent result error status is invalid")
			}
		}
	}
	if !content {
		return errors.New("the Fast Agent result has no content array")
	}
	return nil
}

func validateFastagentTextBlock(raw json.RawMessage) error {
	var block struct {
		Type string  `json:"type"`
		Text *string `json:"text"`
	}
	if json.Unmarshal(raw, &block) != nil || block.Type != "text" || block.Text == nil {
		return errors.New("the Fast Agent body block has no supported text representation")
	}
	return nil
}

func fastagentArchiveChannel(channel string) (bool, error) {
	switch channel {
	case contracts.FastagentArchiveReasoning, contracts.FastagentArchiveErrorChannel:
		return true, nil
	case contracts.FastagentArchiveTimingChannel, contracts.FastagentArchiveToolTimingChannel, contracts.FastagentArchiveToolMetadataChannel, contracts.FastagentArchiveUsageChannel, contracts.FastagentArchiveShellProcessMetadataChannel:
		return false, nil
	default:
		return false, fmt.Errorf("the Fast Agent archive channel %q is unsupported", channel)
	}
}

// Validate the whole suffix before any archive row reaches the output queue.
func projectFastagentRootArchive(before *fastagentRootReceipt, after *fastagentRootArchive, captureID string, expectedInput [32]byte, completion agent.MessageCompletion) (*fastagentArchiveProjection, error) {
	if before == nil || after == nil || captureID == "" || before.sessionID != after.sessionID || before.activeActor != after.activeActor {
		return nil, errors.New("the Fast Agent archive owner changed")
	}
	receipt, err := captureFastagentRootReceipt(after)
	if err != nil {
		return nil, err
	}
	seen := make(map[string]bool)
	var selected *fastagentRootHistory
	start := 0
	for index := range after.histories {
		history := &after.histories[index]
		seen[history.file] = true
		prior, exists := before.histories[history.file]
		next := receipt.histories[history.file]
		offset := 0
		if exists {
			offset = len(prior.digests)
			if prior.actor != next.actor || len(next.digests) < offset || !slices.Equal(prior.digests, next.digests[:offset]) {
				return nil, errors.New("the Fast Agent history owner or prefix changed")
			}
		} else if history.actor != before.activeActor && len(history.messages) > 0 {
			return nil, errors.New("an unselected Fast Agent history gained messages")
		}
		if history.actor == before.activeActor {
			if selected != nil || before.activeFile != "" && before.activeFile != history.file {
				return nil, errors.New("the Fast Agent active history changed")
			}
			selected, start = history, offset
		} else if len(history.messages) != offset {
			return nil, errors.New("multiple Fast Agent histories changed without a proved order")
		}
	}
	for file := range before.histories {
		if !seen[file] {
			return nil, errors.New("a Fast Agent history disappeared")
		}
	}
	if selected == nil || start == len(selected.messages) {
		return nil, errors.New("the Fast Agent archive has no complete current suffix")
	}
	out := &fastagentArchiveProjection{receipt: receipt}
	pending := make(map[string]fastagentPendingCall)
	inputs := 0
	var final fastagentRootMessage
	modelMessageIndex := -1
	appendRow := func(raw json.RawMessage, source fastagentArchiveSource) error {
		if len(out.rows) == fastagentRootElementLimit {
			return errors.New("the Fast Agent projection exceeds its row limit")
		}
		row, err := makeFastagentArchiveRow(raw, source)
		if err != nil {
			return err
		}
		out.rows = append(out.rows, row)
		if completion == agent.MessageCompletionInterrupted {
			if err := out.recordModelCoverage(len(out.rows)-1, source); err != nil {
				return err
			}
			if source.role == contracts.FastagentArchiveAssistantRole && (source.field == contracts.FastagentArchiveContent || source.field == contracts.FastagentArchiveChannels && source.channel == contracts.FastagentArchiveReasoning) {
				modelMessageIndex = source.message
			}
		}
		return nil
	}
	for index := start; index < len(selected.messages); index++ {
		message, err := decodeFastagentRootMessage(selected.messages[index])
		if err != nil {
			return nil, err
		}
		final = message
		workerInput := message.role == contracts.FastagentArchiveUserRole && len(message.results) == 0
		if workerInput {
			digest, err := fastagentInputDigest(message.content, false)
			if err != nil || digest != expectedInput {
				return nil, errors.New("the Fast Agent archive input does not match this dispatch")
			}
			inputs++
			if index != start || inputs != 1 {
				return nil, errors.New("the Fast Agent suffix has an unproved input boundary")
			}
		} else if index == start {
			return nil, errors.New("the Fast Agent suffix does not begin with its current input")
		}
		base := fastagentArchiveSource{captureID: captureID, sessionID: after.sessionID, actor: selected.actor, file: selected.file, message: index, role: message.role, index: -1, timestamp: message.timestamp}
		content := func() error {
			if workerInput {
				return nil
			}
			for n, raw := range message.content {
				if err := validateFastagentTextBlock(raw); err != nil {
					return err
				}
				source := base
				source.field, source.index = contracts.FastagentArchiveContent, n
				if err := appendRow(raw, source); err != nil {
					return err
				}
			}
			return nil
		}
		channels := func(reasoning bool) error {
			for _, channel := range message.channels {
				if (channel.ID == contracts.FastagentArchiveReasoning) != reasoning {
					continue
				}
				text, err := fastagentArchiveChannel(channel.ID)
				if err != nil {
					return err
				}
				if reasoning && message.role != contracts.FastagentArchiveAssistantRole {
					return errors.New("the Fast Agent text channel has a conflicting role")
				}
				blocks, err := fastagentContentBlocks(channel.Data)
				if err != nil {
					return err
				}
				for n, raw := range blocks {
					if text {
						if err := validateFastagentTextBlock(raw); err != nil {
							return err
						}
					}
					source := base
					source.field, source.channel, source.index = contracts.FastagentArchiveChannels, channel.ID, n
					if err := appendRow(raw, source); err != nil {
						return err
					}
				}
			}
			return nil
		}
		calls := func() error {
			if completion == agent.MessageCompletionInterrupted && modelMessageIndex != index {
				if len(out.coverage) == fastagentRootElementLimit {
					return errors.New("the native Fast Agent model coverage exceeds its element limit")
				}
				// Cancellation can append an empty assistant after streamed text.
				// Keep that generation separate from the preceding assistant.
				out.coverage = append(out.coverage, fastagentModelCoverageElement{rowIndex: -1, messageIndex: index})
			}
			for _, call := range message.calls {
				if _, exists := pending[call.ID]; exists {
					return errors.New("the Fast Agent archive repeats an unfinished call")
				}
				name, err := decodeFastagentArchiveRequest(call.Data)
				if err != nil {
					return err
				}
				source := base
				source.field, source.callID, source.toolName = contracts.FastagentArchiveToolCalls, call.ID, name
				var request struct {
					Params struct {
						Arguments json.RawMessage `json:"arguments"`
					} `json:"params"`
				}
				if err := json.Unmarshal(call.Data, &request); err != nil {
					return err
				}
				_, source.noSpan = fastagentSubagentRequest(name, request.Params.Arguments)
				source.spanID = fastagentArchiveID(source, "fastagent-tool-")
				if err := appendRow(call.Data, source); err != nil {
					return err
				}
				pending[call.ID] = fastagentPendingCall{raw: call.Data, name: name, spanID: source.spanID, noSpan: source.noSpan}
			}
			return nil
		}
		results := func() error {
			for _, result := range message.results {
				call, exists := pending[result.ID]
				if !exists {
					return errors.New("the Fast Agent result has no pending native request")
				}
				if err := validateFastagentArchiveResult(result.Data); err != nil {
					return err
				}
				source := base
				source.field, source.callID, source.toolName = contracts.FastagentArchiveToolResults, result.ID, call.name
				source.spanID, source.request = call.spanID, call.raw
				source.noSpan = call.noSpan
				if err := appendRow(result.Data, source); err != nil {
					return err
				}
				if completion == agent.MessageCompletionInterrupted && fastagentInterruptedResult(result.Data) {
					out.rows[len(out.rows)-1].content.Completion = agent.MessageCompletionInterrupted
				}
				delete(pending, result.ID)
			}
			return nil
		}
		steps := []func() error{results, content, func() error { return channels(true) }, func() error { return channels(false) }}
		if message.role == contracts.FastagentArchiveAssistantRole {
			steps = []func() error{func() error { return channels(true) }, content, func() error { return channels(false) }, calls}
		}
		for _, step := range steps {
			if err := step(); err != nil {
				return nil, err
			}
		}
	}
	// Cancellation persists a reconciled tool-result checkpoint before the ACP
	// acknowledgement. It has no final assistant response. Only that native
	// acknowledgement permits this otherwise incomplete-looking suffix.
	finalAssistant := final.role == contracts.FastagentArchiveAssistantRole && fastagentNativeFinalStop(final.stopReason)
	canceledCheckpoint := completion == agent.MessageCompletionInterrupted && final.role == contracts.FastagentArchiveUserRole && len(final.results) > 0
	if inputs != 1 || len(pending) > 0 || (!finalAssistant && !canceledCheckpoint) {
		return nil, errors.New("the Fast Agent archive suffix is incomplete")
	}
	return out, nil
}

// Native ToolRunner._build_interrupted_tool_result writes this exact error.
func fastagentInterruptedResult(raw json.RawMessage) bool {
	var result struct {
		IsError bool `json:"isError"`
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
	}
	return json.Unmarshal(raw, &result) == nil && result.IsError && len(result.Content) == 1 && result.Content[0].Type == "text" && result.Content[0].Text == "**The user interrupted this tool call**"
}
