package jsonfield

import (
	"bytes"
	"encoding/json"
	"math/big"
	"reflect"
	"strings"
)

// SemanticEqual compares decoded JSON values without rounding numbers. Object
// order and numeric spelling do not matter. Invalid input uses exact bytes.
// An absent value differs from JSON null.
func SemanticEqual(left, right json.RawMessage) bool {
	if !json.Valid(left) || !json.Valid(right) {
		return bytes.Equal(left, right)
	}
	decode := func(raw []byte) any {
		decoder := json.NewDecoder(bytes.NewReader(raw))
		decoder.UseNumber()
		var value any
		if err := decoder.Decode(&value); err != nil {
			return nil
		}
		return normalizeJSONNumbers(value)
	}
	return reflect.DeepEqual(decode(left), decode(right))
}

// semanticNumber stores decimal digits and an exponent without expanding a power
// of ten. Even a very large exponent consumes only its encoded number of digits.
type semanticNumber struct {
	negative         bool
	digits, exponent string
}

func normalizeJSONNumbers(value any) any {
	switch value := value.(type) {
	case json.Number:
		text := string(value)
		negative := strings.HasPrefix(text, "-")
		text = strings.TrimPrefix(text, "-")
		exponent := new(big.Int)
		if index := strings.IndexAny(text, "eE"); index >= 0 {
			exponent.SetString(strings.TrimPrefix(text[index+1:], "+"), 10)
			text = text[:index]
		}
		if index := strings.IndexByte(text, '.'); index >= 0 {
			exponent.Sub(exponent, big.NewInt(int64(len(text)-index-1)))
			text = text[:index] + text[index+1:]
		}
		text = strings.TrimLeft(text, "0")
		if text == "" {
			return semanticNumber{digits: "0", exponent: "0"}
		}
		digits := strings.TrimRight(text, "0")
		exponent.Add(exponent, big.NewInt(int64(len(text)-len(digits))))
		return semanticNumber{negative: negative, digits: digits, exponent: exponent.String()}
	case []any:
		for index, item := range value {
			value[index] = normalizeJSONNumbers(item)
		}
	case map[string]any:
		for key, item := range value {
			value[key] = normalizeJSONNumbers(item)
		}
	}
	return value
}
