// Package jsonfield changes selected JSON values without rewriting unrelated bytes.
package jsonfield

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
)

var (
	ErrMissing = errors.New("the JSON field is missing")
	ErrInvalid = errors.New("the JSON value is invalid")
)

type member struct {
	start, end int
	found      bool
	count      int
}

// locate selects the last matching key, as JSON.parse does for duplicate keys.
func locate(data []byte, key string) (member, error) {
	if !json.Valid(data) {
		return member{}, ErrInvalid
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	token, err := decoder.Token()
	if err != nil || token != json.Delim('{') {
		return member{}, fmt.Errorf("%w: an object is required", ErrInvalid)
	}
	var result member
	for decoder.More() {
		field, err := decoder.Token()
		if err != nil {
			return member{}, err
		}
		var value json.RawMessage
		if err := decoder.Decode(&value); err != nil {
			return member{}, err
		}
		result.count++
		if field == key {
			result.end = int(decoder.InputOffset())
			result.start = result.end - len(value)
			result.found = true
		}
	}
	return result, nil
}

// Get returns an owned copy of a nested object field.
func Get(data []byte, path ...string) (json.RawMessage, error) {
	if len(path) == 0 {
		return nil, fmt.Errorf("%w: the field path is empty", ErrInvalid)
	}
	for _, key := range path {
		field, err := locate(data, key)
		if err != nil {
			return nil, err
		}
		if !field.found {
			return nil, fmt.Errorf("%w: %s", ErrMissing, key)
		}
		data = data[field.start:field.end]
	}
	return bytes.Clone(data), nil
}

// Set replaces or adds the last field in path. All parent objects must exist.
func Set(data, value []byte, path ...string) ([]byte, error) {
	if len(path) == 0 || !json.Valid(value) {
		return nil, ErrInvalid
	}
	field, err := locate(data, path[0])
	if err != nil {
		return nil, err
	}
	if len(path) > 1 {
		if !field.found {
			return nil, fmt.Errorf("%w: %s", ErrMissing, path[0])
		}
		value, err = Set(data[field.start:field.end], value, path[1:]...)
		if err != nil {
			return nil, err
		}
	}
	if field.found {
		return replace(data, field.start, field.end, value), nil
	}
	key, err := json.Marshal(path[0])
	if err != nil {
		return nil, err
	}
	addition := append(key, ':')
	addition = append(addition, value...)
	if field.count > 0 {
		addition = append([]byte{','}, addition...)
	}
	end := closingIndex(data)
	return replace(data, end, end, addition), nil
}

// Append adds one value to a JSON array and preserves the existing array bytes.
func Append(data, value []byte) ([]byte, error) {
	if !json.Valid(data) || !json.Valid(value) {
		return nil, ErrInvalid
	}
	trimmed := bytes.TrimSpace(data)
	if trimmed[0] != '[' {
		return nil, fmt.Errorf("%w: an array is required", ErrInvalid)
	}
	addition := value
	if len(bytes.TrimSpace(trimmed[1:len(trimmed)-1])) > 0 {
		addition = append([]byte{','}, value...)
	}
	end := closingIndex(data)
	return replace(data, end, end, addition), nil
}

func closingIndex(data []byte) int {
	return len(bytes.TrimRight(data, " \t\r\n")) - 1
}

func replace(data []byte, start, end int, value []byte) []byte {
	result := make([]byte, 0, len(data)-(end-start)+len(value))
	result = append(result, data[:start]...)
	result = append(result, value...)
	return append(result, data[end:]...)
}
