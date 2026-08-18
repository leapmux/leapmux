package validate

import (
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// passwordWithByte returns an 8 character password whose last character is
// the byte b. A boundary case must read as the code point it pins, and a
// source-level escape sequence hides that code point.
func passwordWithByte(b byte) string {
	return "passwor" + string([]byte{b})
}

func TestValidatePassword(t *testing.T) {
	tests := []struct {
		name     string
		password string
		wantErr  bool
	}{
		{"empty", "", true},
		{"too short (1 char)", "a", true},
		{"too short (7 chars)", "1234567", true},
		{"min length (8 chars)", "12345678", false},
		{"typical password", "my-secure-password", false},
		{"max length (128 chars)", strings.Repeat("a", 128), false},
		{"too long (129 chars)", strings.Repeat("a", 129), true},
		// A password outside printable ASCII is refused whatever its
		// length. Before the character-set rule, `pässwörd` was accepted
		// here and the two length limits counted different units.
		{"non-ASCII characters", "pässwörd", true},
		// The control block and DEL are ASCII, and the rule refuses them
		// anyway: such a character arrives by a paste accident or a
		// terminal control sequence, never by deliberate typing.
		{"NUL (0x00)", passwordWithByte(0x00), true},
		{"tab (0x09)", passwordWithByte(0x09), true},
		{"newline (0x0A)", passwordWithByte(0x0A), true},
		{"0x1F, one below the range", passwordWithByte(0x1F), true},
		{"DEL (0x7F), one above the range", passwordWithByte(0x7F), true},
		// The space is printable, so a passphrase keeps its spaces and
		// neither end of one is trimmed.
		{"space (0x20)", passwordWithByte(0x20), false},
		{"tilde (0x7E)", passwordWithByte(0x7E), false},
		{"a passphrase with spaces", "correct horse battery staple", false},
		{"a leading and a trailing space", " password ", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidatePassword(tt.password)
			if tt.wantErr {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

// TestValidatePasswordAcceptsExactlyThePrintableASCIIBytes walks every byte
// value and pins both edges of the range at once: 0x1F and 0x7F are refused,
// 0x20 and 0x7E are accepted, and no byte above 0x7E passes. Each password is
// 8 bytes long, so only the character-set rule can refuse one.
func TestValidatePasswordAcceptsExactlyThePrintableASCIIBytes(t *testing.T) {
	for b := 0; b <= 0xFF; b++ {
		password := passwordWithByte(byte(b))
		require.Len(t, password, MinPasswordLength, "the probe must clear both length rules")
		err := ValidatePassword(password)
		if b >= 0x20 && b <= 0x7E {
			assert.NoErrorf(t, err, "byte %#02x is printable ASCII and must be accepted", b)
			continue
		}
		require.Errorf(t, err, "byte %#02x is outside printable ASCII and must be refused", b)
		assert.Containsf(t, err.Error(), "printable ASCII",
			"byte %#02x must report the character-set rule", b)
	}
}

// TestValidatePasswordReportsTheCharacterSetBeforeTheLength pins the ORDER
// of the two rules.
//
// A password of 3 CJK characters is both too short and outside the printable
// range. The character set is the actionable complaint: an operator who
// counted 3 characters cannot act on "at least 8 characters" when the hub
// counted 9 bytes. The same holds at the other end, where 200 CJK characters
// are 600 bytes, and for a control character at either length.
func TestValidatePasswordReportsTheCharacterSetBeforeTheLength(t *testing.T) {
	short := ValidatePassword(strings.Repeat("中", 3))
	require.Error(t, short)
	assert.Contains(t, short.Error(), "printable ASCII")
	assert.NotContains(t, short.Error(), "at least")

	long := ValidatePassword(strings.Repeat("中", 200))
	require.Error(t, long)
	assert.Contains(t, long.Error(), "printable ASCII")
	assert.NotContains(t, long.Error(), "at most")

	shortControl := ValidatePassword("ab" + string([]byte{0x01}))
	require.Error(t, shortControl)
	assert.Contains(t, shortControl.Error(), "printable ASCII")
	assert.NotContains(t, shortControl.Error(), "at least")

	longControl := ValidatePassword(strings.Repeat("a"+string([]byte{0x07}), 100))
	require.Error(t, longControl)
	assert.Contains(t, longControl.Error(), "printable ASCII")
	assert.NotContains(t, longControl.Error(), "at most")
}

// TestValidatePasswordCountsBytesAndCharactersAlike states the property the
// character-set rule exists for: an accepted password holds one byte for each
// character, so the hub's byte limit and the browser's code-unit limit are
// the same limit.
func TestValidatePasswordCountsBytesAndCharactersAlike(t *testing.T) {
	for _, password := range []string{
		strings.Repeat("a", MinPasswordLength),
		strings.Repeat("a", MaxPasswordLength),
		"~ !@#$%^&*()_+",
		"my-secure-password",
		"correct horse battery staple",
		" password ",
	} {
		require.NoError(t, ValidatePassword(password))
		assert.Equal(t, len([]rune(password)), len(password),
			"an accepted password must hold one byte for each character")
	}
}

// passwordConformanceFixture is the shared cross-language fixture described
// in testdata/password_policy_conformance.json -- read that file's _readme
// first.
//
// frontend/src/lib/validate.ts hand-mirrors this package's rule so a form
// can refuse a bad password at the field. This side of the fixture pins the
// hub's half: change what the hub accepts without changing validate.ts, and
// the TypeScript suite reading the same file goes red (and the reverse).
// Fields mirror the JSON exactly; `Why` is documentation, not asserted on.
type passwordConformanceFixture struct {
	Cases []struct {
		Password string `json:"password"`
		Repeat   int    `json:"repeat"`
		Valid    bool   `json:"valid"`
		Refusal  string `json:"refusal"`
		Why      string `json:"why"`
	} `json:"cases"`
}

// passwordRefusalMarkers maps a fixture refusal token to the substring this
// package's message carries for that rule. The browser holds the same map
// against its own wording, because the two messages differ by the leading
// capital only.
var passwordRefusalMarkers = map[string]string{
	"too_short":           "at least",
	"too_long":            "at most",
	"not_printable_ascii": "printable ASCII",
}

// TestValidatePasswordConformance runs the shared fixture.
//
// go test runs with CWD = the package dir (backend/util/validate), so the
// repo-level testdata dir -- the one home reachable from both the Go package
// and frontend/src/lib -- is three levels up.
func TestValidatePasswordConformance(t *testing.T) {
	const path = "../../../testdata/password_policy_conformance.json"
	raw, err := os.ReadFile(path)
	require.NoError(t, err, "the conformance fixture is shared with frontend/src/lib/validate.test.ts")

	var fixture passwordConformanceFixture
	require.NoError(t, json.Unmarshal(raw, &fixture))
	// A fixture that silently loads zero cases would make this test pass
	// while asserting nothing -- the one failure mode a shared fixture must
	// not have.
	require.NotEmpty(t, fixture.Cases, "fixture %s loaded no cases", path)

	for _, c := range fixture.Cases {
		repeat := c.Repeat
		if repeat == 0 {
			repeat = 1
		}
		password := strings.Repeat(c.Password, repeat)
		t.Run(c.Why, func(t *testing.T) {
			err := ValidatePassword(password)
			if c.Valid {
				require.Emptyf(t, c.Refusal, "case %q is valid, so its refusal must be empty", c.Why)
				assert.NoErrorf(t, err, "case %q must be accepted", c.Why)
				return
			}
			require.Errorf(t, err, "case %q must be refused", c.Why)
			marker, ok := passwordRefusalMarkers[c.Refusal]
			require.Truef(t, ok, "case %q carries an unknown refusal token %q", c.Why, c.Refusal)
			assert.Containsf(t, err.Error(), marker,
				"case %q must report the %s rule", c.Why, c.Refusal)
		})
	}
}
