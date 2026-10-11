package providerkit

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io"
)

// SSEEvent is one server-sent event.
type SSEEvent struct {
	// Event is the `event:` field. It is empty for an event that states none,
	// which the standard reads as the type "message".
	Event string
	// Data is the event's `data:` lines, joined with a newline.
	Data []byte
	// ID is the `id:` field, or empty.
	ID string
}

// ErrSSEEventTooLarge reports an event whose data exceeds the reader's limit.
var ErrSSEEventTooLarge = errors.New("server-sent event exceeds the size limit")

// ReadSSE reads a text/event-stream body and hands each complete event to
// handle, in order, until the body ends or fails.
//
// It follows the WHATWG event-stream rules that a local agent server uses:
//
//   - A blank line dispatches the event. An event with no data line is not
//     dispatched.
//   - One event's data may span several `data:` lines, joined with a newline.
//   - A single space after the colon is not part of the value.
//   - A line that starts with a colon is a comment (a keep-alive), and a field
//     this reader does not know (`retry:`) is ignored.
//   - An event that the body ends in the middle of is DISCARDED. A stream cut
//     mid-event carries a truncated payload, and handing that on would make a
//     reader act on half a JSON document. A caller that reconnects must restate
//     its state from the server, which each caller in this module does.
//
// maxEventBytes limits one line and the joined data of one event. A larger
// event fails the read with ErrSSEEventTooLarge rather than grow without limit.
//
// handle runs on the reader goroutine. It must not block for long: the server
// cannot write past a full connection buffer.
func ReadSSE(body io.Reader, maxEventBytes int, handle func(SSEEvent)) error {
	if maxEventBytes <= 0 {
		return fmt.Errorf("ReadSSE: maxEventBytes must be positive, got %d", maxEventBytes)
	}
	scanner := bufio.NewScanner(body)
	scanner.Buffer(make([]byte, 0, min(64<<10, maxEventBytes)), maxEventBytes)

	var event SSEEvent
	var data []byte
	hasData := false
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			if hasData {
				event.Data = data
				handle(event)
			}
			event, data, hasData = SSEEvent{}, nil, false
			continue
		}
		if line[0] == ':' {
			continue
		}
		field, value, found := bytes.Cut(line, []byte(":"))
		if found {
			value = bytes.TrimPrefix(value, []byte(" "))
		} else {
			// A line with no colon is a field with an empty value.
			value = nil
		}
		switch string(field) {
		case "data":
			if hasData {
				data = append(data, '\n')
			}
			if len(data)+len(value) > maxEventBytes {
				return ErrSSEEventTooLarge
			}
			data = append(data, value...)
			hasData = true
		case "event":
			event.Event = string(value)
		case "id":
			// The standard ignores an id that holds a NUL.
			if !bytes.ContainsRune(value, 0) {
				event.ID = string(value)
			}
		}
	}
	if err := scanner.Err(); err != nil {
		if errors.Is(err, bufio.ErrTooLong) {
			return ErrSSEEventTooLarge
		}
		return err
	}
	return nil
}
