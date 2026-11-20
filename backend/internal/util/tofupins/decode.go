package tofupins

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"
)

type pinMap map[string]*Entry

// UnmarshalJSON validates every pin before exposing any of them to the store.
func (pins *pinMap) UnmarshalJSON(data []byte) error {
	var rawPins map[string]json.RawMessage
	if err := json.Unmarshal(data, &rawPins); err != nil {
		return err
	}
	if rawPins == nil {
		*pins = nil
		return nil
	}

	decoded := make(pinMap, len(rawPins))
	for workerID, rawPin := range rawPins {
		pin, err := decodePin(workerID, rawPin)
		if err != nil {
			return fmt.Errorf("pin for worker %q: %w", workerID, err)
		}
		decoded[workerID] = pin
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
