package providerkit

import (
	"io"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestAfterPreamble(t *testing.T) {
	t.Parallel()
	const delimiter = "__LEAPMUX_DELIMITER__"
	assert.Equal(t, "[]\n", string(AfterPreamble([]byte("motd\n"+delimiter+"\n[]\n"), delimiter)))
	assert.Equal(t, "[]", string(AfterPreamble([]byte("[]"), delimiter)), "output with no delimiter stays whole")
	assert.Empty(t, AfterPreamble([]byte("motd\n"+delimiter), delimiter), "a delimiter with no line after it leaves nothing")
	assert.Equal(t, "x", string(AfterPreamble([]byte("x"), "")), "no delimiter to look for keeps the output")
}

func TestLimitedBufferKeepsItsLimit(t *testing.T) {
	t.Parallel()
	buf := LimitedBuffer{Limit: 5}
	n, err := buf.Write([]byte("abc"))
	assert.NoError(t, err)
	assert.Equal(t, 3, n)
	assert.False(t, buf.Truncated)
	n, err = buf.Write([]byte("defg"))
	assert.NoError(t, err)
	assert.Equal(t, 4, n, "the writer never stalls")
	assert.Equal(t, "abcde", buf.String())
	assert.True(t, buf.Truncated)
	n, err = buf.Write([]byte("h"))
	assert.NoError(t, err)
	assert.Equal(t, 1, n)
	assert.Equal(t, []byte("abcde"), buf.Bytes())

	full := LimitedBuffer{Limit: 2}
	_, _ = full.Write([]byte("ab"))
	assert.False(t, full.Truncated, "exactly the limit drops nothing")
	_, _ = full.Write(nil)
	assert.False(t, full.Truncated, "an empty write drops nothing")

	_, isReaderFrom := any(&LimitedBuffer{}).(io.ReaderFrom)
	assert.False(t, isReaderFrom, "io.Copy must go through Write, where the limit applies")
}

// exec fills cmd.Stdout through io.Copy, so the limit must hold on that path.
func TestLimitedBufferAppliesItsLimitThroughCopy(t *testing.T) {
	t.Parallel()
	buf := LimitedBuffer{Limit: 5}
	n, err := io.Copy(&buf, strings.NewReader("0123456789"))
	assert.NoError(t, err)
	assert.Equal(t, int64(10), n, "the copy reads the whole source, so the child never stalls")
	assert.Equal(t, "01234", buf.String())
	assert.True(t, buf.Truncated)
}
