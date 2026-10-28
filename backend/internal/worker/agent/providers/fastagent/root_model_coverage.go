package fastagent

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"hash"
	"math"

	"github.com/leapmux/leapmux/generated/contracts"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent"
)

type fastagentCoverageBarrier uint8

const (
	fastagentCoverageNone fastagentCoverageBarrier = iota
	fastagentCoverageRequest
	fastagentCoverageResult
	fastagentCoverageUnknown
)

// Descriptors point into the validated projection without retaining decoded text.
// A rowIndex of -1 records an empty native assistant generation.
type fastagentModelCoverageElement struct {
	rowIndex, messageIndex int
	kind                   agent.AssembledMessageKind
	barrier                fastagentCoverageBarrier
}

type fastagentModelCoverageToken struct {
	kind                 agent.AssembledMessageKind
	barrier              fastagentCoverageBarrier
	length               int64
	digest, prefixDigest [sha256.Size]byte
}

type fastagentModelCoverageBuilder struct {
	tokens                        []fastagentModelCoverageToken
	modelOpen                     bool
	modelKind                     agent.AssembledMessageKind
	modelMessageIndex             int
	modelLength                   int64
	modelHash, prefixHash         hash.Hash
	prefixTokenIndex              int
	prefixLength, prefixRemaining int64
}

func (builder *fastagentModelCoverageBuilder) addModel(kind agent.AssembledMessageKind, text string, messageIndex int) error {
	if builder.modelOpen && (builder.modelKind != kind || builder.modelMessageIndex != messageIndex) {
		if err := builder.flushModel(); err != nil {
			return err
		}
	}
	if !builder.modelOpen {
		builder.modelOpen = true
		builder.modelKind = kind
		builder.modelMessageIndex = messageIndex
		builder.modelLength = 0
		builder.modelHash = sha256.New()
		if len(builder.tokens) == builder.prefixTokenIndex {
			builder.prefixHash = sha256.New()
			builder.prefixRemaining = builder.prefixLength
		}
	}
	if int64(len(text)) > math.MaxInt64-builder.modelLength {
		return errors.New("the Fast Agent model coverage exceeds its byte limit")
	}
	builder.modelLength += int64(len(text))
	// Hash strings through a fixed buffer instead of copying a whole result.
	var buffer [32 << 10]byte
	for len(text) > 0 {
		count := copy(buffer[:], text)
		_, _ = builder.modelHash.Write(buffer[:count])
		if builder.prefixHash != nil && builder.prefixRemaining > 0 {
			prefixCount := min(int64(count), builder.prefixRemaining)
			_, _ = builder.prefixHash.Write(buffer[:prefixCount])
			builder.prefixRemaining -= prefixCount
		}
		text = text[count:]
	}
	return nil
}

func (builder *fastagentModelCoverageBuilder) addBarrier(barrier fastagentCoverageBarrier) error {
	if err := builder.flushModel(); err != nil {
		return err
	}
	if len(builder.tokens) == fastagentRootElementLimit {
		return errors.New("the Fast Agent model coverage exceeds its element limit")
	}
	// Each request and result remains a separate token, including leading and
	// trailing tools. No association between native and live tool IDs is inferred.
	builder.tokens = append(builder.tokens, fastagentModelCoverageToken{barrier: barrier})
	return nil
}

func (builder *fastagentModelCoverageBuilder) flushModel() error {
	if !builder.modelOpen {
		return nil
	}
	if len(builder.tokens) == fastagentRootElementLimit {
		return errors.New("the Fast Agent model coverage exceeds its element limit")
	}
	token := fastagentModelCoverageToken{kind: builder.modelKind, length: builder.modelLength}
	copy(token.digest[:], builder.modelHash.Sum(token.digest[:0]))
	if builder.prefixHash != nil {
		copy(token.prefixDigest[:], builder.prefixHash.Sum(token.prefixDigest[:0]))
	}
	builder.tokens = append(builder.tokens, token)
	builder.modelOpen = false
	builder.modelHash, builder.prefixHash = nil, nil
	return nil
}

func (builder *fastagentModelCoverageBuilder) finish() ([]fastagentModelCoverageToken, error) {
	if err := builder.flushModel(); err != nil {
		return nil, err
	}
	return builder.tokens, nil
}

func decodeCapturedModelText(content agent.MessageContent) (agent.AssembledMessageKind, string, bool, error) {
	metadataKind, _ := agent.MessageMetadata(content)
	var kind agent.AssembledMessageKind
	switch metadataKind {
	case leapmuxv1.AssembledMessageKind_ASSEMBLED_MESSAGE_KIND_TEXT:
		kind = agent.AssembledMessageKindText
	case leapmuxv1.AssembledMessageKind_ASSEMBLED_MESSAGE_KIND_REASONING:
		kind = agent.AssembledMessageKindReasoning
	case leapmuxv1.AssembledMessageKind_ASSEMBLED_MESSAGE_KIND_PLAN:
		return "", "", false, errors.New("the interrupted Fast Agent archive cannot prove captured plan output")
	default:
		return "", "", false, nil
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(content.Original, &fields); err != nil {
		return "", "", false, err
	}
	var text *string
	if err := json.Unmarshal(fields[contracts.AssembledMessageFieldText], &text); err != nil || text == nil {
		return "", "", false, errors.New("the captured Fast Agent model output has no text")
	}
	return kind, *text, true, nil
}

func (group *rootTranscriptGroup) capturedModelCoverage(entries []*rootJournalEntry) ([]fastagentModelCoverageToken, bool, error) {
	if len(entries) > fastagentRootElementLimit {
		return nil, false, errors.New("the captured Fast Agent model coverage exceeds its element limit")
	}
	builder := fastagentModelCoverageBuilder{prefixTokenIndex: -1}
	hasModelText := false
	for index, entry := range entries {
		if entry.kind == rootRecordTurnEnd {
			continue
		}
		record, err := group.readEntry(entry)
		if err != nil {
			return nil, false, err
		}
		if record.source != leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT {
			continue
		}
		if record.span.SpanID != "" {
			barrier := fastagentCoverageRequest
			if record.span.Closing {
				barrier = fastagentCoverageResult
			}
			if err := builder.addBarrier(barrier); err != nil {
				return nil, false, err
			}
			continue
		}
		kind, text, proved, err := decodeCapturedModelText(record.content)
		if err != nil {
			return nil, false, err
		}
		if !proved {
			if err := builder.addBarrier(fastagentCoverageUnknown); err != nil {
				return nil, false, err
			}
			continue
		}
		hasModelText = hasModelText || text != ""
		// Each captured assembled row is its own run. Native blocks may join
		// only within their original assistant message and model kind.
		if err := builder.addModel(kind, text, index); err != nil {
			return nil, false, err
		}
	}
	tokens, err := builder.finish()
	return tokens, hasModelText, err
}

func (projection *fastagentArchiveProjection) recordModelCoverage(rowIndex int, source fastagentArchiveSource) error {
	element := fastagentModelCoverageElement{rowIndex: rowIndex, messageIndex: source.message}
	switch source.field {
	case contracts.FastagentArchiveToolCalls:
		element.barrier = fastagentCoverageRequest
	case contracts.FastagentArchiveToolResults:
		element.barrier = fastagentCoverageResult
	case contracts.FastagentArchiveContent:
		if source.role == contracts.FastagentArchiveAssistantRole {
			element.kind = agent.AssembledMessageKindText
		} else {
			// ToolRunner's user-role context accompanies its result barrier.
			// It neither covers model text nor adds an assistant generation.
			return nil
		}
	case contracts.FastagentArchiveChannels:
		if source.channel == contracts.FastagentArchiveReasoning && source.role == contracts.FastagentArchiveAssistantRole {
			element.kind = agent.AssembledMessageKindReasoning
		} else if source.channel == contracts.FastagentArchiveErrorChannel {
			return nil
		} else if text, err := fastagentArchiveChannel(source.channel); err != nil || text {
			element.barrier = fastagentCoverageUnknown
		} else {
			return nil
		}
	default:
		element.barrier = fastagentCoverageUnknown
	}
	if len(projection.coverage) == fastagentRootElementLimit {
		return errors.New("the native Fast Agent model coverage exceeds its element limit")
	}
	projection.coverage = append(projection.coverage, element)
	return nil
}

func (projection *fastagentArchiveProjection) nativeModelCoverage(prefixTokenIndex int, prefixLength int64) ([]fastagentModelCoverageToken, error) {
	builder := fastagentModelCoverageBuilder{prefixTokenIndex: prefixTokenIndex, prefixLength: prefixLength}
	for _, element := range projection.coverage {
		if element.barrier != fastagentCoverageNone {
			if err := builder.addBarrier(element.barrier); err != nil {
				return nil, err
			}
			continue
		}
		if element.rowIndex == -1 {
			if err := builder.addModel("", "", element.messageIndex); err != nil {
				return nil, err
			}
			continue
		}
		if element.rowIndex < 0 || element.rowIndex >= len(projection.rows) {
			return nil, errors.New("the native Fast Agent model coverage has an invalid row")
		}
		var block struct {
			Text *string `json:"text"`
		}
		if json.Unmarshal(projection.rows[element.rowIndex].content.Original, &block) != nil || block.Text == nil {
			return nil, errors.New("the native Fast Agent model coverage has no text")
		}
		if err := builder.addModel(element.kind, *block.Text, element.messageIndex); err != nil {
			return nil, err
		}
	}
	return builder.finish()
}

func (group *rootTranscriptGroup) validateInterruptedModelCoverage(entries []*rootJournalEntry, projection *fastagentArchiveProjection) error {
	live, hasModelText, err := group.capturedModelCoverage(entries)
	if err != nil {
		return err
	}
	if !hasModelText {
		return nil
	}
	lastModel := -1
	for index, token := range live {
		if token.barrier == fastagentCoverageUnknown {
			return errors.New("the captured Fast Agent output has unproved model chronology")
		}
		if token.barrier == fastagentCoverageNone {
			lastModel = index
		}
	}
	if projection == nil || lastModel < 0 {
		return errors.New("the interrupted Fast Agent archive has no model coverage")
	}
	native, err := projection.nativeModelCoverage(lastModel, live[lastModel].length)
	if err != nil {
		return err
	}
	if len(live) != len(native) {
		return errors.New("the interrupted Fast Agent archive changed the model output sequence")
	}
	for index, captured := range live {
		archived := native[index]
		if captured.barrier != archived.barrier || captured.kind != archived.kind {
			return errors.New("the interrupted Fast Agent archive moved captured model output")
		}
		if captured.barrier != fastagentCoverageNone {
			continue
		}
		if index == lastModel {
			if captured.length > archived.length || captured.digest != archived.prefixDigest {
				return errors.New("the interrupted Fast Agent archive omitted captured model output")
			}
		} else if captured.length != archived.length || captured.digest != archived.digest {
			return errors.New("the interrupted Fast Agent archive changed captured model output")
		}
	}
	return nil
}
