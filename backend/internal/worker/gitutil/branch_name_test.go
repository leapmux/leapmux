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
// Every case below is checked against REAL git (2.55.0) with
// `git check-ref-format`, because that is the only authority for a ref name. A
// rule that refuses more than git makes an existing branch that `for-each-ref`
// lists impossible to act on; a rule that refuses less pushes the error down
// into git, where the user gets a raw message instead of a validation one.
//
// Exactly ONE refusal goes beyond git on purpose: a leading `-`, because the
// name reaches git as a positional argument and its option parser would read
// that as a flag.
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

	t.Run("accepts the characters git accepts, including $ and %", func(t *testing.T) {
		// Verified against real git: `git check-ref-format --branch 'release-100%'`
		// and `--branch 'env-$STAGE'` both exit 0, and both names check out.
		// Nothing in this package reaches a shell -- `NewGitCmd` builds an argv
		// directly -- so refusing them bought no safety and made an existing
		// branch that `for-each-ref` lists impossible to check out, push or
		// delete from inside LeapMux.
		for _, name := range []string{"feat/$HOME", "feat/100%", "release-100%", "env-$STAGE"} {
			assert.NoErrorf(t, ValidateBranchName(name), "%q must be accepted", name)
		}
	})

	t.Run("refuses the characters git itself refuses", func(t *testing.T) {
		for _, name := range []string{
			"has space", "til~de", "car^et", "co:lon", "quest?ion",
			"aster*isk", "brack[et", "back\\slash",
		} {
			assert.Errorf(t, ValidateBranchName(name), "%q must be refused", name)
		}
	})

	t.Run("refuses a control character in both blocks", func(t *testing.T) {
		// git refuses the ASCII controls and DEL. The C1 block U+0080-U+009F is
		// NOT a git rule -- `git check-ref-format` exits 0 for it and `git branch`
		// creates the ref -- so it is asserted as ACCEPTED below.
		for _, name := range []string{"a\x00b", "a\x1fb", "a\x7fb"} {
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
		// `-lead` is the one deliberate extra: git accepts it, and this rule does
		// not, because the name becomes a positional argument to git.
		for _, name := range []string{"/lead", ".lead", "-lead"} {
			assert.Errorf(t, ValidateBranchName(name), "%q must be refused", name)
		}
		for _, name := range []string{"trail/", "trail."} {
			assert.Errorf(t, ValidateBranchName(name), "%q must be refused", name)
		}
		for _, name := range []string{"a..b", "a//b", "a/.b"} {
			assert.Errorf(t, ValidateBranchName(name), "%q must be refused", name)
		}
	})

	// Each of these git ACCEPTS (verified: `git check-ref-format` exits 0 and
	// `git branch` creates the ref, which `for-each-ref` then lists). Refusing
	// them offered the branch in the picker and then refused every checkout,
	// push and delete of it -- the same defect `$` and `%` had.
	t.Run("accepts the names git accepts", func(t *testing.T) {
		for _, name := range []string{
			"brack]et",   // git forbids `[`, not `]`
			"a\u0085b",   // the C1 block is not a git rule
			"a\u009fb",   //
			"@lead",      // only the bare `@` is refused
			"@/a",        //
			"feat/100%",  // already repaired; pinned here beside its siblings
			"env-$STAGE", //
		} {
			assert.NoErrorf(t, ValidateBranchName(name), "%q must be accepted", name)
		}
	})

	// Each of these git REFUSES, and this rule used to accept -- so the error
	// surfaced later, from git itself, with a message the user cannot act on.
	t.Run("refuses the names git refuses", func(t *testing.T) {
		for _, name := range []string{
			"@",           // the single-character refname
			"feature@{1}", // `@{` is reflog syntax
			"a@{0}",       //
			"at@{",        //
			"a.lock/b",    // `.lock` on ANY component, not just the last
			"x/a.lock/b",  //
			"trail.lock",  //
		} {
			assert.Errorf(t, ValidateBranchName(name), "%q must be refused", name)
		}
	})
}
