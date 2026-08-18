package service

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	huboauth "github.com/leapmux/leapmux/internal/hub/oauth"
	"github.com/leapmux/leapmux/util/validate"
)

// The display name an identity provider reports is a MACHINE value: no field
// shows it, and the user cannot correct it. It reaches
// users.display_name through validate.SanitizeDisplayName, which decides its
// fallback from the RAW string -- so a name of one zero width space is not
// "empty" there, fails the sanitize, and refused the whole signup with
// "display name: name must not be empty" while the username fallback sat
// unused in the same call.
//
// cleanProviderDisplayName settles that at the boundary, so both readers of
// the stored row become correct at once: the prefill shows an empty field and
// the fallback branch fires.
func TestCleanProviderDisplayName(t *testing.T) {
	t.Parallel()

	t.Run("keeps an ordinary provider name", func(t *testing.T) {
		got := cleanProviderDisplayName(&huboauth.UserClaims{DisplayName: "Ada Lovelace"})
		assert.Equal(t, "Ada Lovelace", got)
	})

	t.Run("cleans rather than refuses", func(t *testing.T) {
		for _, tc := range []struct{ name, in, want string }{
			{"folds a whitespace run", "Ada   Lovelace", "Ada Lovelace"},
			{"trims both ends", "  Ada Lovelace  ", "Ada Lovelace"},
			{"strips a control character", "Ada\x00Lovelace", "AdaLovelace"},
			{"strips a right-to-left override", "\u202eAda", "Ada"},
			{"keeps the punctuation the name rule keeps", `A"B$C%D\E`, `A"B$C%D\E`},
		} {
			t.Run(tc.name, func(t *testing.T) {
				assert.Equal(t, tc.want, cleanProviderDisplayName(&huboauth.UserClaims{DisplayName: tc.in}))
			})
		}
	})

	// The case that blocked the signup. Each of these is non-empty as a RAW
	// string, so SanitizeDisplayName treated it as present and then failed on
	// it. Cleaning first makes it "", which is what takes the fallback.
	t.Run("maps a name that cleans to nothing onto the empty string", func(t *testing.T) {
		for _, in := range []string{"   ", "\u200b", "\ufeff\u00ad", "\x00\x01", "\u200b\t\u200b"} {
			got := cleanProviderDisplayName(&huboauth.UserClaims{DisplayName: in})
			assert.Emptyf(t, got, "%q must clean to nothing so the caller takes the username fallback", in)
		}
	})

	// A provider name over the byte limit is CUT, not refused. SanitizeName
	// would have failed the signup over a name the user never chose.
	t.Run("cuts an over-long name instead of refusing it", func(t *testing.T) {
		got := cleanProviderDisplayName(&huboauth.UserClaims{DisplayName: strings.Repeat("a", 500)})
		assert.Equal(t, strings.Repeat("a", validate.NameByteLimit), got)

		// A wide script fills the byte limit with fewer characters, which is
		// the case a character count gets wrong.
		got = cleanProviderDisplayName(&huboauth.UserClaims{DisplayName: strings.Repeat("一", 200)})
		assert.Equal(t, strings.Repeat("一", 42), got)
	})

	t.Run("falls back to Name when DisplayName carries nothing usable", func(t *testing.T) {
		assert.Equal(t, "ada", cleanProviderDisplayName(&huboauth.UserClaims{Name: "ada"}))
		assert.Equal(t, "ada", cleanProviderDisplayName(&huboauth.UserClaims{DisplayName: "\u200b", Name: "ada"}))
		assert.Equal(t, "ada", cleanProviderDisplayName(&huboauth.UserClaims{DisplayName: "   ", Name: "  ada  "}))
	})

	t.Run("returns empty when neither claim carries anything usable", func(t *testing.T) {
		assert.Empty(t, cleanProviderDisplayName(&huboauth.UserClaims{}))
		assert.Empty(t, cleanProviderDisplayName(&huboauth.UserClaims{DisplayName: "\u200b", Name: "\ufeff"}))
	})

	// The result is what the later SanitizeDisplayName reads, so it must pass
	// through that rule unchanged. Otherwise the boundary clean would only
	// move the refusal rather than remove it.
	t.Run("its result always satisfies SanitizeDisplayName unchanged", func(t *testing.T) {
		for _, in := range []string{
			"Ada Lovelace", "Ada   Lovelace", "  Ada  ", "\u202eAda",
			strings.Repeat("a", 500), strings.Repeat("一", 200), `A"B$C%D\E`,
		} {
			cleaned := cleanProviderDisplayName(&huboauth.UserClaims{DisplayName: in})
			require.NotEmptyf(t, cleaned, "case %q must survive the clean for this assertion to bite", in)
			got, err := validate.SanitizeDisplayName(cleaned, "fallback")
			require.NoErrorf(t, err, "SanitizeDisplayName must accept the cleaned form of %q", in)
			assert.Equalf(t, cleaned, got, "SanitizeDisplayName must return the cleaned form of %q unchanged", in)
		}
	})
}
