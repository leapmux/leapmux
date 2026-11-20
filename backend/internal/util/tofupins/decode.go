package tofupins

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"
	"strings"
	"time"
)

type pinMap map[string]*Entry

// UnmarshalJSON validates every pin before exposing any of them to the store.
func (pins *pinMap) UnmarshalJSON(data []byte) error {
	if bytes.Equal(bytes.TrimSpace(data), []byte("null")) {
		*pins = nil
		return nil
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	start, err := decoder.Token()
	if err != nil {
		return err
	}
	if start != json.Delim('{') {
		return errors.New("pins must be an object")
	}
	type rawPinEntry struct {
		workerID string
		pin      json.RawMessage
	}
	seen := make(map[string]struct{})
	rawPins := make([]rawPinEntry, 0)
	for decoder.More() {
		key, err := decoder.Token()
		if err != nil {
			return err
		}
		workerID, ok := key.(string)
		if !ok {
			return errors.New("pin worker id must be a string")
		}
		if _, exists := seen[workerID]; exists {
			return fmt.Errorf("duplicate pin for worker %q", workerID)
		}
		seen[workerID] = struct{}{}
		var rawPin json.RawMessage
		if err := decoder.Decode(&rawPin); err != nil {
			return err
		}
		rawPins = append(rawPins, rawPinEntry{workerID: workerID, pin: rawPin})
	}
	decoded := make(pinMap, len(rawPins))
	for _, raw := range rawPins {
		pin, err := decodePin(raw.workerID, raw.pin)
		if err != nil {
			return fmt.Errorf("pin for worker %q: %w", raw.workerID, err)
		}
		decoded[raw.workerID] = pin
	}
	end, err := decoder.Token()
	if err != nil {
		return err
	}
	if end != json.Delim('}') {
		return errors.New("pins object is not closed")
	}
	if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("unexpected data after pins object")
		}
		return err
	}
	*pins = decoded
	return nil
}

// unreadablePinError lets Resolve describe the presented key when the
// requested Worker's pinned fingerprint cannot be calculated.
type unreadablePinError struct {
	workerID string
	cause    error
}

func (e *unreadablePinError) Error() string { return e.cause.Error() }
func (e *unreadablePinError) Unwrap() error { return e.cause }

func decodePin(workerID string, data json.RawMessage) (*Entry, error) {
	var fields *pinFields[*string, *string]
	if err := json.Unmarshal(data, &fields); err != nil {
		return nil, err
	}
	if fields == nil {
		return nil, errors.New("pin must be an object")
	}

	var entry Entry
	for _, key := range []struct {
		field   string
		encoded *string
		value   *string
	}{
		{"x25519_public_key", fields.X25519PublicKey, &entry.X25519PublicKey},
		{"mlkem_public_key", fields.MlkemPublicKey, &entry.MlkemPublicKey},
		{"slhdsa_public_key", fields.SlhdsaPublicKey, &entry.SlhdsaPublicKey},
	} {
		if key.encoded == nil {
			return nil, fmt.Errorf("%s must be a nonnull string", key.field)
		}
		if _, err := decodeBase64Key(*key.encoded); err != nil {
			return nil, &unreadablePinError{
				workerID: workerID,
				cause:    fmt.Errorf("%s must be standard Base64: %w", key.field, err),
			}
		}
		*key.value = *key.encoded
	}

	var err error
	entry.FirstSeenAt, err = decodeTimestamp("first_seen_at", fields.FirstSeenAt)
	if err != nil {
		return nil, err
	}
	entry.LastUsedAt, err = decodeTimestamp("last_used_at", fields.LastUsedAt)
	if err != nil {
		return nil, err
	}
	return &entry, nil
}

func decodeBase64Key(encoded string) ([]byte, error) {
	// Even the strict standard decoder ignores carriage returns and line feeds.
	if strings.ContainsAny(encoded, "\r\n") {
		return nil, errors.New("encoded key contains a line break")
	}
	return base64.StdEncoding.Strict().DecodeString(encoded)
}

// time.Parse checks the calendar and clock values but accepts some forms that
// RFC 3339 disallows, including a single-digit hour and a comma for fractions.
var timestampPattern = regexp.MustCompile(`^[0-9]{4}-[0-9]{2}-[0-9]{2}[Tt][0-9]{2}:[0-9]{2}:[0-9]{2}(\.[0-9]+)?([Zz]|[+-]([01][0-9]|2[0-3]):[0-5][0-9])$`)

func decodeTimestamp(field string, value *string) (time.Time, error) {
	if value == nil || !timestampPattern.MatchString(*value) {
		return time.Time{}, fmt.Errorf("%s must be an RFC 3339 timestamp", field)
	}
	timestamp, err := time.Parse(time.RFC3339Nano, strings.ToUpper(*value))
	if err != nil {
		return time.Time{}, fmt.Errorf("%s must be an RFC 3339 timestamp: %w", field, err)
	}
	return timestamp, nil
}
