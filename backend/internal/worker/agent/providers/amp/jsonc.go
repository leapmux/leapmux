package amp

import (
	"bytes"
	"errors"
	"strings"
)

// jsoncToJSON converts the JSON-with-comments text that Amp accepts in its
// settings file into plain JSON. Amp parses the file with a JSONC reader that
// allows `//` and `/* */` comments and a trailing comma before `}` or `]`, so a
// file the user wrote for Amp can hold all three.
//
// It removes each comment and each trailing comma outside a string and changes
// nothing else. A comment that never closes is an error, because the rest of
// the file would be read as a comment.
func jsoncToJSON(text []byte) ([]byte, error) {
	withoutComments, err := stripJSONComments(text)
	if err != nil {
		return nil, err
	}
	return stripTrailingCommas(withoutComments), nil
}

// stripJSONComments removes every comment outside a string. A comment becomes a
// single space, so two tokens it separated stay separated.
func stripJSONComments(text []byte) ([]byte, error) {
	var out strings.Builder
	out.Grow(len(text))
	inString := false
	for i := 0; i < len(text); i++ {
		c := text[i]
		if inString {
			out.WriteByte(c)
			switch c {
			case '\\':
				if i+1 < len(text) {
					i++
					out.WriteByte(text[i])
				}
			case '"':
				inString = false
			}
			continue
		}
		switch {
		case c == '"':
			inString = true
			out.WriteByte(c)
		case c == '/' && i+1 < len(text) && text[i+1] == '/':
			for i < len(text) && text[i] != '\n' {
				i++
			}
			out.WriteByte(' ')
			if i < len(text) {
				out.WriteByte('\n')
			}
		case c == '/' && i+1 < len(text) && text[i+1] == '*':
			// bytes.Index reads the text in place. A conversion to a string
			// here would copy the rest of the file for each comment.
			end := bytes.Index(text[i+2:], []byte("*/"))
			if end < 0 {
				return nil, errors.New("a block comment does not close")
			}
			i += 2 + end + 1
			out.WriteByte(' ')
		default:
			out.WriteByte(c)
		}
	}
	return []byte(out.String()), nil
}

// stripTrailingCommas removes a trailing comma outside a string: a comma that
// follows a value, when only whitespace separates it from the `}` or `]` that
// follows. A comma right after `{`, `[`, `,` or `:` is not a trailing comma, and
// Amp's parser refuses it, so it stays and the JSON reader refuses it too. The
// text holds no comment by now.
func stripTrailingCommas(text []byte) []byte {
	out := make([]byte, 0, len(text))
	inString := false
	for i := 0; i < len(text); i++ {
		c := text[i]
		if inString {
			out = append(out, c)
			switch c {
			case '\\':
				if i+1 < len(text) {
					i++
					out = append(out, text[i])
				}
			case '"':
				inString = false
			}
			continue
		}
		if c == '"' {
			inString = true
			out = append(out, c)
			continue
		}
		if c == ',' && endsValue(out) {
			next := i + 1
			for next < len(text) && isJSONWhitespace(text[next]) {
				next++
			}
			if next < len(text) && (text[next] == '}' || text[next] == ']') {
				continue
			}
		}
		out = append(out, c)
	}
	return out
}

// endsValue reports whether the last byte of out outside whitespace ends a
// value, which a trailing comma must follow. Each call reads back over the
// whitespace since the last token alone, so the work stays linear in the text.
func endsValue(out []byte) bool {
	for i := len(out) - 1; i >= 0; i-- {
		if isJSONWhitespace(out[i]) {
			continue
		}
		switch out[i] {
		case '{', '[', ',', ':':
			return false
		default:
			return true
		}
	}
	return false
}

func isJSONWhitespace(c byte) bool {
	return c == ' ' || c == '\t' || c == '\n' || c == '\r'
}
