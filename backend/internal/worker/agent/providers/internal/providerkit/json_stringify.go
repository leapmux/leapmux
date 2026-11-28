package providerkit

import (
	"bytes"
	"encoding/json"
	"fmt"
)

// JSONStringify encodes value as JavaScript's JSON.stringify does, so text
// that a provider or the browser compares byte for byte matches:
//
//   - It escapes no HTML character. Go's encoder escapes <, > and & by
//     default.
//   - It writes U+2028 and U+2029 as themselves. Go's encoder escapes both
//     even with HTML escaping off.
//   - It ends with no newline.
func JSONStringify(value any) ([]byte, error) {
	var buffer bytes.Buffer
	encoder := json.NewEncoder(&buffer)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(value); err != nil {
		return nil, err
	}
	encoded := bytes.TrimSuffix(buffer.Bytes(), []byte("\n"))
	if !bytes.Contains(encoded, []byte(`\u202`)) {
		return encoded, nil
	}
	output := encoded[:0]
	for index := 0; index < len(encoded); {
		if encoded[index] != '\\' || index+1 == len(encoded) {
			output = append(output, encoded[index])
			index++
			continue
		}
		// An escaped backslash stays as it is, so `\\u2028` is never read
		// as an escape of the separator.
		if index+6 <= len(encoded) && bytes.Equal(encoded[index:index+5], []byte(`\u202`)) && (encoded[index+5] == '8' || encoded[index+5] == '9') {
			output = append(output, 0xe2, 0x80, 0xa8+encoded[index+5]-'8')
			index += 6
			continue
		}
		output = append(output, encoded[index:index+2]...)
		index += 2
	}
	return output, nil
}

// JSONText is JSONStringify for a value that always encodes: a string, a
// number, a boolean, or a list or a map of them. Only a programming error
// makes such a value fail, so JSONText panics on one.
func JSONText(value any) json.RawMessage {
	encoded, err := JSONStringify(value)
	if err != nil {
		panic(fmt.Sprintf("providerkit: encode %T as JSON: %v", value, err))
	}
	return encoded
}
