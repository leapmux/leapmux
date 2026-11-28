package store

import (
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/internal/util/id"
)

func TestParseIDPrefix(t *testing.T) {
	t.Run("empty and whitespace return inactive", func(t *testing.T) {
		for _, in := range []string{"", "   ", "\t\n"} {
			f, ok, err := ParseIDPrefix(in)
			require.NoError(t, err)
			assert.False(t, ok)
			assert.Zero(t, f)
		}
	})

	t.Run("invalid UTF-8 returns ErrInvalidArgument", func(t *testing.T) {
		_, ok, err := ParseIDPrefix("abc\xff")
		require.ErrorIs(t, err, ErrInvalidArgument)
		assert.False(t, ok)
	})

	t.Run("full generated length uses exact match", func(t *testing.T) {
		full := id.Generate()
		f, ok, err := ParseIDPrefix("  " + full + " ")
		require.NoError(t, err)
		require.True(t, ok)
		assert.True(t, f.Exact)
		assert.Equal(t, full, f.Raw)
		assert.Equal(t, full, f.Lower)
		assert.Empty(t, f.Upper)
		assert.False(t, f.HasUpper)
	})

	t.Run("longer than generated length also uses exact match", func(t *testing.T) {
		longID := id.Generate() + "extra"
		f, ok, err := ParseIDPrefix(longID)
		require.NoError(t, err)
		require.True(t, ok)
		assert.True(t, f.Exact)
		assert.Equal(t, longID, f.Lower)
	})

	t.Run("short ASCII prefix computes exact half-open range", func(t *testing.T) {
		f, ok, err := ParseIDPrefix("ab9Z")
		require.NoError(t, err)
		require.True(t, ok)
		assert.False(t, f.Exact)
		assert.True(t, f.HasUpper)
		assert.Equal(t, "ab9Z", f.Lower)
		assert.Equal(t, "ab9[", f.Upper)
		assert.True(t, utf8.ValidString(f.Upper))
		assert.True(t, "ab9Z" >= f.Lower && "ab9Z" < f.Upper)
		assert.True(t, "ab9Z0000" >= f.Lower && "ab9Z0000" < f.Upper)
		assert.False(t, "ab9[" >= f.Lower && "ab9[" < f.Upper)
		assert.False(t, "ab9z" >= f.Lower && "ab9z" < f.Upper, "case-sensitive byte order")
	})

	t.Run("non-ASCII boundary and surrogate skip stay valid UTF-8", func(t *testing.T) {
		f, ok, err := ParseIDPrefix("ÿ") // U+00FF (0xC3 0xBF) -> U+0100 (0xC4 0x80)
		require.NoError(t, err)
		require.True(t, ok)
		assert.True(t, f.HasUpper)
		assert.Equal(t, "\u0100", f.Upper)
		assert.True(t, utf8.ValidString(f.Upper))
		assert.True(t, "ÿtail" >= f.Lower && "ÿtail" < f.Upper)
		assert.False(t, "\u0100" >= f.Lower && "\u0100" < f.Upper)

		// U+D7FF skips UTF-16 surrogates straight to U+E000.
		f2, ok, err := ParseIDPrefix("\uD7FF")
		require.NoError(t, err)
		require.True(t, ok)
		assert.True(t, f2.HasUpper)
		assert.Equal(t, "\uE000", f2.Upper)
		assert.True(t, utf8.ValidString(f2.Upper))

		// Trailing MaxRune carries to the preceding rune.
		f3, ok, err := ParseIDPrefix("a" + string(utf8.MaxRune))
		require.NoError(t, err)
		require.True(t, ok)
		assert.True(t, f3.HasUpper)
		assert.Equal(t, "b", f3.Upper)

		// All MaxRune has no upper bound.
		f4, ok, err := ParseIDPrefix(strings.Repeat(string(utf8.MaxRune), 2))
		require.NoError(t, err)
		require.True(t, ok)
		assert.False(t, f4.HasUpper)
		assert.Empty(t, f4.Upper)
	})

	t.Run("CursorAfter handles exact and range bounds", func(t *testing.T) {
		exact, _, err := ParseIDPrefix(id.Generate())
		require.NoError(t, err)
		gt, empty := exact.CursorAfter(nil)
		assert.Empty(t, gt)
		assert.False(t, empty)
		_, empty = exact.CursorAfter(&Cursor{ID: exact.Lower})
		assert.True(t, empty)

		pref, _, err := ParseIDPrefix("abcd")
		require.NoError(t, err)
		gt, empty = pref.CursorAfter(&Cursor{ID: "abcd1"})
		assert.Equal(t, "abcd1", gt)
		assert.False(t, empty)
		_, empty = pref.CursorAfter(&Cursor{ID: pref.Upper})
		assert.True(t, empty)
	})
}
