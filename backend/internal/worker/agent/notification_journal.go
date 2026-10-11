package agent

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"slices"

	"github.com/leapmux/leapmux/generated/contracts"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
)

// NotificationEntry stores one keyed identity under its containing message owner.
type NotificationEntry struct {
	IdempotencyKey string
	Fingerprint    [sha256.Size]byte
}

// NotificationJournalField belongs to Worker storage and is absent from rendering projections.
const NotificationJournalField = "notification_entries"

type encodedNotificationEntry struct {
	IdempotencyKey    string `json:"idempotency_key"`
	FingerprintBase64 string `json:"fingerprint_base64"`
}

func jsonObject(encoded []byte, description string) (map[string]json.RawMessage, error) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(encoded, &fields); err != nil {
		return nil, fmt.Errorf("decode %s: %w", description, err)
	}
	if fields == nil {
		return nil, fmt.Errorf("%s must be a JSON object", description)
	}
	return fields, nil
}

// ValidateIncomingMessageMetadata rejects injection of the stored Worker journal.
func ValidateIncomingMessageMetadata(metadata []byte) error {
	if len(metadata) == 0 {
		return nil
	}
	fields, err := jsonObject(metadata, "the incoming Worker metadata")
	if err != nil {
		return err
	}
	for _, reserved := range []string{NotificationJournalField, NotificationReductionField} {
		if _, present := fields[reserved]; present {
			return fmt.Errorf("the incoming Worker metadata contains the reserved %s field", reserved)
		}
	}
	return nil
}

// DecodeNotificationJournal validates canonical keyed fingerprints without reading provider formats.
func DecodeNotificationJournal(supplement []byte) ([]NotificationEntry, error) {
	parsed, err := ParseStoredMessageSupplement(supplement)
	if err != nil {
		return nil, err
	}
	return parsed.NotificationJournal(), nil
}

func decodeNotificationEntries(journal []byte) ([]NotificationEntry, error) {
	var records []json.RawMessage
	if bytes.Equal(bytes.TrimSpace(journal), []byte("null")) {
		return nil, errors.New("the notification_entries field must be an array")
	}
	if err := json.Unmarshal(journal, &records); err != nil {
		return nil, fmt.Errorf("decode the notification journal: %w", err)
	}
	entries := make([]NotificationEntry, 0, len(records))
	keys := make(map[string]struct{}, len(records))
	for index, record := range records {
		entry, err := decodeNotificationEntry(record)
		if err != nil {
			return nil, fmt.Errorf("decode notification entry %d: %w", index, err)
		}
		if entry.IdempotencyKey != "" {
			if _, repeated := keys[entry.IdempotencyKey]; repeated {
				return nil, fmt.Errorf("the notification journal repeats key %q", entry.IdempotencyKey)
			}
			keys[entry.IdempotencyKey] = struct{}{}
		}
		entries = append(entries, entry)
	}
	return entries, nil
}

func decodeNotificationEntry(raw []byte) (NotificationEntry, error) {
	fields, err := uniqueJSONObject(raw, "the notification identity")
	if err != nil {
		return NotificationEntry{}, err
	}
	for field := range fields {
		if field != "idempotency_key" && field != "fingerprint_base64" {
			return NotificationEntry{}, fmt.Errorf("the notification identity contains unknown field %q", field)
		}
	}
	var key, encoded *string
	if err := json.Unmarshal(fields["idempotency_key"], &key); err != nil || key == nil || *key == "" {
		return NotificationEntry{}, errors.New("the notification identity requires a nonempty idempotency_key")
	}
	if err := json.Unmarshal(fields["fingerprint_base64"], &encoded); err != nil || encoded == nil {
		return NotificationEntry{}, errors.New("the notification identity requires fingerprint_base64")
	}
	decoded, err := base64.StdEncoding.DecodeString(*encoded)
	if err != nil || base64.StdEncoding.EncodeToString(decoded) != *encoded {
		return NotificationEntry{}, errors.New("the notification fingerprint must use canonical padded Base64")
	}
	if len(decoded) != sha256.Size {
		return NotificationEntry{}, errors.New("the notification fingerprint must contain exactly 32 bytes")
	}
	entry := NotificationEntry{IdempotencyKey: *key}
	copy(entry.Fingerprint[:], decoded)
	return entry, nil
}

// NewNotificationEntry fingerprints exact identity inputs without JSON normalization.
// Empty native keys need no stored identity record.
func NewNotificationEntry(provider leapmuxv1.AgentProvider, source leapmuxv1.MessageSource, content MessageContent) (NotificationEntry, error) {
	if err := ValidateMessageCompletion(content); err != nil {
		return NotificationEntry{}, err
	}
	_, completion := MessageMetadata(content)
	if content.IdempotencyKey == "" {
		return NotificationEntry{}, nil
	}
	hash := sha256.New()
	writePart := func(data []byte) {
		var prefix [8]byte
		binary.BigEndian.PutUint64(prefix[:], uint64(len(data)))
		// hash.Hash.Write always returns a nil error.
		_, _ = hash.Write(prefix[:])
		_, _ = hash.Write(data)
	}
	for _, ordinal := range []int32{int32(provider), int32(source), int32(completion)} {
		var encoded [4]byte
		binary.BigEndian.PutUint32(encoded[:], uint32(ordinal))
		writePart(encoded[:])
	}
	writePart(content.Original)
	writePart(content.Supplemental)
	writePart(content.Metadata)
	entry := NotificationEntry{IdempotencyKey: content.IdempotencyKey}
	copy(entry.Fingerprint[:], hash.Sum(nil))
	return entry, nil
}

// AppendNotificationJournal keeps prior rendering fields and adds one canonical entry.
func AppendNotificationJournal(previous []byte, incoming MessageContent, entry NotificationEntry) ([]byte, error) {
	if err := ValidateIncomingMessageMetadata(incoming.Metadata); err != nil {
		return nil, err
	}
	parsed, err := ParseStoredMessageSupplement(previous)
	if err != nil {
		return nil, err
	}
	entries := parsed.NotificationJournal()
	if entry.IdempotencyKey != "" {
		for _, prior := range entries {
			if prior.IdempotencyKey == entry.IdempotencyKey {
				return nil, fmt.Errorf("the notification journal repeats key %q", entry.IdempotencyKey)
			}
		}
		entries = append(entries, entry)
	}
	encoded := make([]encodedNotificationEntry, 0, len(entries))
	for _, item := range entries {
		encoded = append(encoded, encodedNotificationEntry{IdempotencyKey: item.IdempotencyKey,
			FingerprintBase64: base64.StdEncoding.EncodeToString(item.Fingerprint[:])})
	}
	fields := maps.Clone(parsed.fields)
	metadata := maps.Clone(parsed.metadata)
	if len(incoming.Metadata) > 0 {
		added, err := jsonObject(incoming.Metadata, "the incoming Worker metadata")
		if err != nil {
			return nil, err
		}
		for key, value := range added {
			metadata[key] = value
		}
	}
	if len(incoming.Supplemental) > 0 {
		provider, err := mergeProviderSupplement(fields[contracts.MessageSupplementFieldProvider], incoming.Supplemental)
		if err != nil {
			return nil, err
		}
		fields[contracts.MessageSupplementFieldProvider] = provider
	}
	journal, err := json.Marshal(encoded)
	if err != nil {
		return nil, err
	}
	if len(entries) == 0 {
		delete(metadata, NotificationJournalField)
	} else {
		metadata[NotificationJournalField] = journal
	}
	fields[contracts.MessageSupplementFieldMetadata], err = json.Marshal(metadata)
	if err != nil {
		return nil, err
	}
	return json.Marshal(fields)
}

func mergeProviderSupplement(previous, incoming []byte) ([]byte, error) {
	if !json.Valid(incoming) {
		return nil, errors.New("the incoming provider supplement must be valid JSON")
	}
	if len(previous) == 0 {
		return slices.Clone(incoming), nil
	}
	var before, added map[string]json.RawMessage
	if json.Unmarshal(previous, &before) != nil || before == nil || json.Unmarshal(incoming, &added) != nil || added == nil {
		return slices.Clone(incoming), nil
	}
	for key, value := range added {
		before[key] = value
	}
	return json.Marshal(before)
}

// ReplaceMessageSupplementProvider preserves the stored journal and all unrelated envelope fields.
func ReplaceMessageSupplementProvider(previous, provider []byte, notificationMessageCount int) ([]byte, error) {
	parsed, err := ParseStoredMessageSupplement(previous)
	if err != nil {
		return nil, err
	}
	return parsed.ReplaceProvider(provider, notificationMessageCount)
}
