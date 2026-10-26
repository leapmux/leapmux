package zcode

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"math/big"
	"regexp"
	"strconv"
	"strings"
)

// zcodeJSONStringify encodes the arrays and ordered records used in ZCode's
// credential keys and account scopes. Native JSON.stringify leaves U+2028 and
// U+2029 literal, while Go escapes them even with HTML escaping disabled.
func zcodeJSONStringify(value any) string {
	var out strings.Builder
	encoder := json.NewEncoder(&out)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(value); err != nil {
		panic(fmt.Sprintf("zcodeJSONStringify: %v", err))
	}
	encoded := strings.TrimSuffix(out.String(), "\n")
	if !strings.Contains(encoded, `\u2028`) && !strings.Contains(encoded, `\u2029`) {
		return encoded
	}
	var native strings.Builder
	native.Grow(len(encoded))
	for i := 0; i < len(encoded); {
		if encoded[i] != '\\' {
			native.WriteByte(encoded[i])
			i++
			continue
		}
		// The encoder guarantees a complete JSON escape. Consume it together
		// so a literal backslash followed by "u2028" remains literal text.
		end := i + 2
		if encoded[i+1] == 'u' {
			end = i + 6
		}
		switch encoded[i:end] {
		case `\u2028`:
			native.WriteString("\u2028")
		case `\u2029`:
			native.WriteString("\u2029")
		default:
			native.WriteString(encoded[i:end])
		}
		i = end
	}
	return native.String()
}

// zcodeEncodeURIComponent preserves the ASCII characters that JavaScript keeps,
// and percent-encodes every other UTF-8 byte in uppercase hexadecimal.
func zcodeEncodeURIComponent(s string) string {
	const hex = "0123456789ABCDEF"
	var out strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		if ('a' <= c && c <= 'z') || ('A' <= c && c <= 'Z') || ('0' <= c && c <= '9') || strings.IndexByte("-_.!~*'()", c) >= 0 {
			out.WriteByte(c)
			continue
		}
		out.WriteByte('%')
		out.WriteByte(hex[c>>4])
		out.WriteByte(hex[c&0x0f])
	}
	return out.String()
}

// zcodeTrimJSString uses ECMAScript whitespace and line terminators. Go's
// Unicode whitespace includes U+0085 and excludes U+FEFF; JavaScript does the reverse.
const zcodeJSWhitespaceChars = "\t\n\v\f\r \u00A0\u1680\u2000\u2001\u2002\u2003\u2004\u2005\u2006\u2007\u2008\u2009\u200A\u2028\u2029\u202F\u205F\u3000\uFEFF"

func zcodeTrimJSString(value string) string { return strings.Trim(value, zcodeJSWhitespaceChars) }

func zcodeIsJSWhitespace(r rune) bool {
	return strings.ContainsRune(zcodeJSWhitespaceChars, r)
}

// zcodeJSONNumberLiteral accepts only a JSON number. JSON whitespace stays
// separate from JavaScript string whitespace, which permits additional characters.
func zcodeJSONNumberLiteral(raw json.RawMessage) (float64, bool) {
	text := bytes.Trim(raw, " \t\r\n")
	if len(text) == 0 || (text[0] != '-' && (text[0] < '0' || text[0] > '9')) {
		return 0, false
	}
	var value float64
	if json.Unmarshal(text, &value) != nil {
		return 0, false
	}
	return value, true
}

var zcodeJSDecimal = regexp.MustCompile(`^[+-]?(?:[0-9]+(?:\.[0-9]*)?|\.[0-9]+)(?:[eE][+-]?[0-9]+)?$`)

// zcodeJSNumber converts the primitive JSON values that billing times use.
// Arrays and objects are not billing times and return ok=false.
func zcodeJSNumber(raw json.RawMessage) (float64, bool) {
	text := bytes.Trim(raw, " \t\r\n")
	switch {
	case len(text) == 0:
		return 0, false
	case bytes.Equal(text, []byte("null")), bytes.Equal(text, []byte("false")):
		return 0, true
	case bytes.Equal(text, []byte("true")):
		return 1, true
	case text[0] == '"':
		var value string
		if json.Unmarshal(text, &value) != nil {
			return 0, false
		}
		return zcodeJSStringNumber(value)
	default:
		return zcodeJSONNumberLiteral(text)
	}
}

func zcodeJSStringNumber(value string) (float64, bool) {
	value = zcodeTrimJSString(value)
	switch value {
	case "":
		return 0, true
	case "Infinity", "+Infinity":
		return math.Inf(1), true
	case "-Infinity":
		return math.Inf(-1), true
	}
	if len(value) > 2 && value[0] == '0' {
		base := 0
		switch value[1] {
		case 'x', 'X':
			base = 16
		case 'o', 'O':
			base = 8
		case 'b', 'B':
			base = 2
		}
		if base != 0 {
			for _, c := range value[2:] {
				digit := -1
				switch {
				case c >= '0' && c <= '9':
					digit = int(c - '0')
				case c >= 'a' && c <= 'f':
					digit = int(c-'a') + 10
				case c >= 'A' && c <= 'F':
					digit = int(c-'A') + 10
				}
				if digit < 0 || digit >= base {
					return 0, false
				}
			}
			integer, ok := new(big.Int).SetString(value[2:], base)
			if !ok {
				return 0, false
			}
			number, _ := integer.Float64()
			return number, true
		}
	}
	if !zcodeJSDecimal.MatchString(value) {
		return 0, false
	}
	number, err := strconv.ParseFloat(value, 64)
	if err != nil && !errors.Is(err, strconv.ErrRange) {
		return 0, false
	}
	return number, !math.IsNaN(number)
}

// zcodeOptionalJSNumber distinguishes a missing, null, or empty time from zero.
func zcodeOptionalJSNumber(raw json.RawMessage) (float64, bool) {
	text := bytes.Trim(raw, " \t\r\n")
	if len(text) == 0 || bytes.Equal(text, []byte("null")) || bytes.Equal(text, []byte(`""`)) {
		return 0, false
	}
	return zcodeJSNumber(text)
}
