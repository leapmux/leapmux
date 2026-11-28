// Package terminput encodes the input that reaches a terminal's PTY: a paste
// and a key press. The browser's xterm.js encodes them for the modes that the
// program in the terminal enabled, and so does the Worker, through this
// package, for an input that the Control CLI sends.
//
// The key names are the vocabulary of SendInputRequest.keys. The Worker
// parses them to encode them, and the Control CLI parses them to refuse an
// unknown name before it sends anything, so both read one table.
package terminput

import "bytes"

// Modes are the terminal modes that change the bytes of an input. A program
// sets them with DEC private mode sequences.
type Modes struct {
	// ApplicationCursorKeys is DECCKM, DEC private mode 1. An arrow key, home
	// or end with no modifier sends ESC O instead of ESC [ while it is set.
	ApplicationCursorKeys bool
	// BracketedPaste is DEC private mode 2004. A paste arrives between
	// pasteStart and pasteEnd while it is set.
	BracketedPaste bool
}

// The markers of a bracketed paste.
const (
	pasteStart = "\x1b[200~"
	pasteEnd   = "\x1b[201~"
)

// Input is one write to a terminal: Data, then each key of Keys in order.
type Input struct {
	// Data reaches the terminal as it is, unless Paste is set.
	Data []byte
	// Paste sends Data as a paste, as xterm.js sends one (see appendPaste).
	Paste bool
	// Keys are pressed after Data, in order.
	Keys []Key
}

// Encode returns the bytes of in for a terminal in modes m. It returns no
// bytes for an input that holds nothing.
func (in Input) Encode(m Modes) []byte {
	var out []byte
	if in.Paste {
		out = appendPaste(out, in.Data, m.BracketedPaste)
	} else {
		out = append(out, in.Data...)
	}
	for _, k := range in.Keys {
		out = append(out, k.sequence(m)...)
	}
	return out
}

// appendPaste appends text to dst as a paste. Like xterm.js, it converts each
// CRLF and each LF to CR, and it surrounds the text with the markers of a
// bracketed paste when the program enabled them.
//
// It also removes every end marker from the text, which xterm.js does not.
// Otherwise a text that holds one would end its own paste early, and the
// program would read the rest as typed keys, which can run a command.
// Removing a marker can join two halves of another one, so it removes them
// until none remains. A text with nothing left after that sends no paste.
func appendPaste(dst, text []byte, bracketed bool) []byte {
	if len(text) == 0 {
		return dst
	}
	pasteEndBytes := []byte(pasteEnd)
	origLen := len(dst)
	if bracketed {
		dst = append(dst, pasteStart...)
	}
	bodyStart := len(dst)
	for i := 0; i < len(text); i++ {
		b := text[i]
		if b == '\r' && i+1 < len(text) && text[i+1] == '\n' {
			i++
		} else if b == '\n' {
			b = '\r'
		}
		dst = append(dst, b)
		if len(dst)-bodyStart >= len(pasteEndBytes) && bytes.HasSuffix(dst[bodyStart:], pasteEndBytes) {
			dst = dst[:len(dst)-len(pasteEndBytes)]
		}
	}
	if len(dst) == bodyStart {
		return dst[:origLen]
	}
	if bracketed {
		dst = append(dst, pasteEnd...)
	}
	return dst
}
