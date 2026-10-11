package agent

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"slices"

	"github.com/leapmux/leapmux/generated/contracts"
)

// NotificationReductionField stores Worker-only reduction facts.
const NotificationReductionField = "notification_reduction"

// ErrInvalidNotificationStorage identifies a corrupt Worker private schema.
var ErrInvalidNotificationStorage = errors.New("the stored notification metadata is invalid")

// NotificationReductionState retains facts that consolidated content cannot supply.
// ProviderSlots identifies provider groups by their one-based content indices.
// CancelledSettings retains the first values of settings that returned to those values.
type NotificationReductionState struct {
	ProviderSlots     map[string]int    `json:"provider_slots,omitempty"`
	CancelledSettings map[string]string `json:"cancelled_settings,omitempty"`
}

func (state NotificationReductionState) Clone() NotificationReductionState {
	return NotificationReductionState{ProviderSlots: maps.Clone(state.ProviderSlots), CancelledSettings: maps.Clone(state.CancelledSettings)}
}

// ParsedMessageSupplement retains one immutable parse of a stored supplement.
// Count validation uses the caller's Worker notification wrapper, without provider parsing.
type ParsedMessageSupplement struct {
	original         []byte
	fields           map[string]json.RawMessage
	metadata         map[string]json.RawMessage
	entries          []NotificationEntry
	reduction        NotificationReductionState
	journalPresent   bool
	reductionPresent bool
}

// ParseStoredMessageSupplement validates both private schemas once.
func ParseStoredMessageSupplement(supplement []byte) (*ParsedMessageSupplement, error) {
	original := slices.Clone(supplement)
	fields, metadata, err := storedSupplementObjects(original)
	if err != nil {
		return nil, err
	}
	parsed := &ParsedMessageSupplement{original: original, fields: fields, metadata: metadata}
	if raw, present := metadata[NotificationJournalField]; present {
		parsed.journalPresent = true
		parsed.entries, err = decodeNotificationEntries(raw)
		if err != nil {
			return nil, fmt.Errorf("%w: %w", ErrInvalidNotificationStorage, err)
		}
	}
	if raw, present := metadata[NotificationReductionField]; present {
		parsed.reductionPresent = true
		parsed.reduction, err = decodeNotificationReduction(raw)
		if err != nil {
			return nil, fmt.Errorf("%w: %w", ErrInvalidNotificationStorage, err)
		}
	}
	return parsed, nil
}

func (supplement *ParsedMessageSupplement) HasNotificationReduction() bool {
	return supplement != nil && supplement.reductionPresent
}

func (supplement *ParsedMessageSupplement) NotificationJournal() []NotificationEntry {
	if supplement == nil {
		return nil
	}
	return slices.Clone(supplement.entries)
}

// MessageContent clones each source without parsing the envelope again.
func (supplement *ParsedMessageSupplement) MessageContent(original []byte) MessageContent {
	content := MessageContent{Original: slices.Clone(original)}
	if supplement != nil {
		content.Supplemental = slices.Clone(supplement.fields[contracts.MessageSupplementFieldProvider])
		content.Metadata = slices.Clone(supplement.fields[contracts.MessageSupplementFieldMetadata])
	}
	return content
}

func (supplement *ParsedMessageSupplement) NotificationReduction(messageCount int) (NotificationReductionState, error) {
	if supplement == nil {
		return NotificationReductionState{}, errors.New("the stored supplement has no parsed result")
	}
	if supplement.reductionPresent {
		if err := validateNotificationReductionCount(supplement.reduction, messageCount); err != nil {
			return NotificationReductionState{}, err
		}
	}
	return supplement.reduction.Clone(), nil
}

func validateNotificationReductionCount(state NotificationReductionState, messageCount int) error {
	if messageCount < 0 {
		return errors.New("the notification message count must not be negative")
	}
	for group, index := range state.ProviderSlots {
		if index > messageCount {
			return fmt.Errorf("the notification provider slot %q has an invalid content index", group)
		}
	}
	return nil
}

// Project removes private metadata and preserves ordinary received bytes exactly.
func (supplement *ParsedMessageSupplement) Project(messageCount int) ([]byte, error) {
	if _, err := supplement.NotificationReduction(messageCount); err != nil {
		return nil, err
	}
	if !supplement.journalPresent && !supplement.reductionPresent {
		return slices.Clone(supplement.original), nil
	}
	fields := maps.Clone(supplement.fields)
	metadata := maps.Clone(supplement.metadata)
	delete(metadata, NotificationJournalField)
	delete(metadata, NotificationReductionField)
	if len(metadata) == 0 {
		delete(fields, contracts.MessageSupplementFieldMetadata)
	} else {
		encoded, err := json.Marshal(metadata)
		if err != nil {
			return nil, err
		}
		fields[contracts.MessageSupplementFieldMetadata] = encoded
	}
	if len(fields) == 0 {
		return nil, nil
	}
	return json.Marshal(fields)
}

// ReplaceProvider preserves both private fields and every unrelated envelope field.
func (supplement *ParsedMessageSupplement) ReplaceProvider(provider []byte, messageCount int) ([]byte, error) {
	if _, err := supplement.NotificationReduction(messageCount); err != nil {
		return nil, err
	}
	fields := maps.Clone(supplement.fields)
	if len(provider) == 0 {
		delete(fields, contracts.MessageSupplementFieldProvider)
	} else {
		if !json.Valid(provider) {
			return nil, errors.New("the provider supplement must be valid JSON")
		}
		fields[contracts.MessageSupplementFieldProvider] = slices.Clone(provider)
	}
	if len(fields) == 0 {
		return nil, nil
	}
	return json.Marshal(fields)
}

type jsonObjectField struct {
	key   string
	value json.RawMessage
}

// orderedJSONObject retains every key occurrence, including escaped duplicate keys.
func orderedJSONObject(raw []byte, description string) ([]jsonObjectField, error) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	first, err := decoder.Token()
	if err != nil || first != json.Delim('{') {
		return nil, fmt.Errorf("%s must be a JSON object", description)
	}
	var result []jsonObjectField
	for decoder.More() {
		token, err := decoder.Token()
		if err != nil {
			return nil, fmt.Errorf("decode %s: %w", description, err)
		}
		key, ok := token.(string)
		if !ok {
			return nil, fmt.Errorf("%s contains an invalid key", description)
		}
		var value json.RawMessage
		if err := decoder.Decode(&value); err != nil {
			return nil, fmt.Errorf("decode %s: %w", description, err)
		}
		result = append(result, jsonObjectField{key: key, value: value})
	}
	last, err := decoder.Token()
	if err != nil || last != json.Delim('}') {
		return nil, fmt.Errorf("%s has an invalid closing token", description)
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return nil, fmt.Errorf("%s contains trailing data", description)
	}
	return result, nil
}

// storedSupplementObjects rejects private state that an ordinary map parse could hide.
func storedSupplementObjects(raw []byte) (map[string]json.RawMessage, map[string]json.RawMessage, error) {
	fields := make(map[string]json.RawMessage)
	metadata := make(map[string]json.RawMessage)
	if len(raw) == 0 {
		return fields, metadata, nil
	}
	entries, err := orderedJSONObject(raw, "the message supplement")
	if err != nil {
		return nil, nil, err
	}
	metadataCount := 0
	privatePresent := false
	var metadataErr error
	for _, entry := range entries {
		fields[entry.key] = entry.value
		if entry.key != contracts.MessageSupplementFieldMetadata {
			continue
		}
		metadataCount++
		members, err := orderedJSONObject(entry.value, "the Worker metadata")
		metadataErr = err
		metadata = make(map[string]json.RawMessage, len(members))
		for _, member := range members {
			private := member.key == NotificationJournalField || member.key == NotificationReductionField
			if private {
				privatePresent = true
				if _, repeated := metadata[member.key]; repeated {
					return nil, nil, fmt.Errorf("%w: the Worker metadata repeats private field %q", ErrInvalidNotificationStorage, member.key)
				}
			}
			metadata[member.key] = member.value
		}
	}
	if privatePresent && metadataCount > 1 {
		return nil, nil, fmt.Errorf("%w: the message supplement repeats metadata that contains private state", ErrInvalidNotificationStorage)
	}
	if metadataErr != nil {
		return nil, nil, metadataErr
	}
	return fields, metadata, nil
}

// uniqueJSONObject rejects duplicate Worker-owned keys without normalizing retained values.
func uniqueJSONObject(raw []byte, description string) (map[string]json.RawMessage, error) {
	entries, err := orderedJSONObject(raw, description)
	if err != nil {
		return nil, err
	}
	result := make(map[string]json.RawMessage, len(entries))
	for _, entry := range entries {
		if _, found := result[entry.key]; found {
			return nil, fmt.Errorf("%s repeats key %q", description, entry.key)
		}
		result[entry.key] = entry.value
	}
	return result, nil
}

// DecodeNotificationReduction validates the single stored reduction schema.
func DecodeNotificationReduction(supplement []byte, messageCount int) (NotificationReductionState, error) {
	parsed, err := ParseStoredMessageSupplement(supplement)
	if err != nil {
		return NotificationReductionState{}, err
	}
	return parsed.NotificationReduction(messageCount)
}

func decodeNotificationReduction(raw []byte) (NotificationReductionState, error) {
	fields, err := uniqueJSONObject(raw, "the notification reduction state")
	if err != nil {
		return NotificationReductionState{}, err
	}
	state := NotificationReductionState{}
	for key, value := range fields {
		switch key {
		case "provider_slots":
			slots, err := uniqueJSONObject(value, "the notification provider slots")
			if err != nil {
				return NotificationReductionState{}, err
			}
			state.ProviderSlots = make(map[string]int, len(slots))
			owners := make(map[int]string, len(slots))
			for group, encoded := range slots {
				var index *int
				if err := json.Unmarshal(encoded, &index); err != nil || index == nil || *index <= 0 {
					return NotificationReductionState{}, fmt.Errorf("the notification provider slot %q has an invalid content index", group)
				}
				if previous, found := owners[*index]; found {
					return NotificationReductionState{}, fmt.Errorf("the notification provider slots %q and %q claim one payload", previous, group)
				}
				owners[*index] = group
				state.ProviderSlots[group] = *index
			}
		case "cancelled_settings":
			settings, err := uniqueJSONObject(value, "the canceled notification settings")
			if err != nil {
				return NotificationReductionState{}, err
			}
			state.CancelledSettings = make(map[string]string, len(settings))
			for setting, encoded := range settings {
				var original *string
				if err := json.Unmarshal(encoded, &original); err != nil || original == nil {
					return NotificationReductionState{}, fmt.Errorf("the canceled notification setting %q must contain a string", setting)
				}
				state.CancelledSettings[setting] = *original
			}
		default:
			return NotificationReductionState{}, fmt.Errorf("the notification reduction state contains unknown field %q", key)
		}
	}
	return state, nil
}

// WithNotificationReduction replaces only the Worker reduction state.
func WithNotificationReduction(supplement []byte, state NotificationReductionState, messageCount int) ([]byte, error) {
	encoded, err := json.Marshal(state)
	if err != nil {
		return nil, err
	}
	validated, err := decodeNotificationReduction(encoded)
	if err != nil {
		return nil, err
	}
	if err := validateNotificationReductionCount(validated, messageCount); err != nil {
		return nil, err
	}
	parsed, err := ParseStoredMessageSupplement(supplement)
	if err != nil {
		return nil, err
	}
	fields := maps.Clone(parsed.fields)
	metadata := maps.Clone(parsed.metadata)
	if len(state.ProviderSlots) == 0 && len(state.CancelledSettings) == 0 {
		delete(metadata, NotificationReductionField)
	} else {
		metadata[NotificationReductionField] = encoded
	}
	if len(metadata) == 0 {
		delete(fields, contracts.MessageSupplementFieldMetadata)
	} else {
		fields[contracts.MessageSupplementFieldMetadata], err = json.Marshal(metadata)
		if err != nil {
			return nil, err
		}
	}
	if len(fields) == 0 {
		return nil, nil
	}
	return json.Marshal(fields)
}

// ProjectMessageSupplement removes only the two Worker storage fields.
func ProjectMessageSupplement(supplement []byte, messageCount int) ([]byte, error) {
	parsed, err := ParseStoredMessageSupplement(supplement)
	if err != nil {
		return nil, err
	}
	return parsed.Project(messageCount)
}
