package launch

import (
	"bytes"
	"io"
)

// LimitedBuffer keeps at most Limit bytes and records whether it dropped any.
// It keeps accepting writes past the limit, so a child never stalls on a full
// pipe.
//
// It holds its buffer in a field rather than embedding it. An embedded
// bytes.Buffer would promote ReadFrom. io.Copy prefers ReadFrom to Write,
// so the limit would never apply.
type LimitedBuffer struct {
	buf       bytes.Buffer
	Limit     int
	Truncated bool
}

func (b *LimitedBuffer) Write(data []byte) (int, error) {
	room := b.Limit - b.buf.Len()
	if room <= 0 {
		b.Truncated = b.Truncated || len(data) > 0
		return len(data), nil
	}
	if len(data) > room {
		b.Truncated = true
		_, _ = b.buf.Write(data[:room])
		return len(data), nil
	}
	return b.buf.Write(data)
}

// Bytes returns what the buffer kept.
func (b *LimitedBuffer) Bytes() []byte { return b.buf.Bytes() }

// String returns what the buffer kept.
func (b *LimitedBuffer) String() string { return b.buf.String() }

var _ io.Writer = (*LimitedBuffer)(nil)

// Full reports whether the retained prefix has reached its limit.
func (b *LimitedBuffer) Full() bool { return b.buf.Len() >= b.Limit }
