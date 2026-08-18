package gitutil

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ValidateBranchName has a browser copy (`validateBranchName` in
// `frontend/src/lib/validate.ts`), and the two must refuse the same names. The
// panel that offers a branch and the worker that creates it read one rule, so
// a disagreement shows the user two answers for one name.
//
// Three of the cases below are where the two copies HAD disagreed: the worker
// accepted `$` and `%`, it counted bytes where the panel counted UTF-16 code
// units, and its control test covered the C1 block where the panel's stopped
// at U+007F.
func TestValidateBranchName(t *testing.T) {
	t.Run("accepts an ordinary name", func(t *testing.T) {
		for _, name := range []string{
			"main",
			"feat/add-dark-mode",
			"release-1.2.3",
			"user/fix_login",
			"기능/한글-브랜치",
		} {
			assert.NoErrorf(t, ValidateBranchName(name), "%q must be accepted", name)
		}
	})

	t.Run("refuses the shell metacharacters the browser copy refuses", func(t *testing.T) {
		// A branch name reaches a shell command line and a file path under
		// `.git/refs/`. git itself accepts both characters, so nothing but
		// this rule keeps the two copies together.
		for _, name := range []string{"feat/$HOME", "feat/100%", "$", "%"} {
			err := ValidateBranchName(name)
			require.Errorf(t, err, "%q must be refused", name)
			assert.Contains(t, err.Error(), "must not contain")
		}
	})

	t.Run("refuses the characters git itself refuses", func(t *testing.T) {
		for _, name := range []string{
			"has space", "til~de", "car^et", "co:lon", "quest?ion",
			"aster*isk", "brack[et", "brack]et", "back\\slash",
		} {
			assert.Errorf(t, ValidateBranchName(name), "%q must be refused", name)
		}
	})

	t.Run("refuses a control character in both blocks", func(t *testing.T) {
		// Go's unicode.IsControl reports the whole Cc category, which is
		// U+0000-U+001F AND U+007F-U+009F. The browser class stopped at
		// U+007F, so a C1 character passed the panel and died here.
		for _, name := range []string{"a\x00b", "a\x1fb", "a\x7fb", "a\u0085b", "a\u009fb"} {
			err := ValidateBranchName(name)
			require.Errorf(t, err, "%q must be refused", name)
			assert.Contains(t, err.Error(), "control characters")
		}
	})

	t.Run("refuses an empty name", func(t *testing.T) {
		err := ValidateBranchName("")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "must not be empty")
	})

	// The limit counts UTF-8 BYTES, because `len` counts bytes, and the
	// message says so. It read "must be at most 256 characters" while it
	// measured bytes, which is a refusal a user cannot act on: 86 CJK
	// characters is 258 bytes and 86 characters.
	t.Run("counts UTF-8 bytes and says bytes", func(t *testing.T) {
		assert.NoError(t, ValidateBranchName(strings.Repeat("a", BranchNameByteLimit)))

		err := ValidateBranchName(strings.Repeat("a", BranchNameByteLimit+1))
		require.Error(t, err)
		assert.Contains(t, err.Error(), "must be at most 256 bytes")

		assert.NoError(t, ValidateBranchName(strings.Repeat("一", 85)),
			"85 CJK characters is 255 bytes, which fits")
		require.Error(t, ValidateBranchName(strings.Repeat("一", 86)),
			"86 CJK characters is 258 bytes, which a character count wrongly accepted")
	})

	t.Run("refuses the prefixes and the suffixes git refuses", func(t *testing.T) {
		for _, name := range []string{"/lead", ".lead", "-lead", "@lead"} {
			assert.Errorf(t, ValidateBranchName(name), "%q must be refused", name)
		}
		for _, name := range []string{"trail/", "trail.", "trail.lock"} {
			assert.Errorf(t, ValidateBranchName(name), "%q must be refused", name)
		}
		for _, name := range []string{"a..b", "a//b", "a/.b"} {
			assert.Errorf(t, ValidateBranchName(name), "%q must be refused", name)
		}
	})
}
