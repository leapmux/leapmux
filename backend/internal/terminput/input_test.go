package terminput

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func mustKeys(t *testing.T, names ...string) []Key {
	t.Helper()
	keys, err := ParseKeys(names)
	require.NoError(t, err)
	return keys
}

// Data reaches the terminal first and as it is, then each key in order, in
// the cursor key mode of the terminal.
func TestInputEncode_DataThenKeys(t *testing.T) {
	in := Input{Data: []byte("vim\x1b[201~\n"), Keys: mustKeys(t, "enter", "up", "ctrl-c")}
	assert.Equal(t, "vim\x1b[201~\n\r\x1b[A\x03", string(in.Encode(Modes{})),
		"a text that is no paste keeps every byte, the end marker included")
	assert.Equal(t, "vim\x1b[201~\n\r\x1bOA\x03", string(in.Encode(Modes{ApplicationCursorKeys: true})))
	assert.Equal(t, "vim\x1b[201~\n\r\x1b[A\x03", string(in.Encode(Modes{BracketedPaste: true})),
		"bracketed paste changes only a paste")
}

func TestInputEncode_NothingSendsNoBytes(t *testing.T) {
	assert.Empty(t, Input{}.Encode(Modes{}))
	assert.Empty(t, Input{Paste: true}.Encode(Modes{BracketedPaste: true}), "an empty paste sends no markers")
	assert.Empty(t, Input{Data: []byte{}, Keys: []Key{}}.Encode(Modes{}))
}

// A paste is surrounded by the markers only while the program enabled
// bracketed paste, and its line endings become CR, as xterm.js sends them.
func TestInputEncode_Paste(t *testing.T) {
	in := Input{Data: []byte("echo one\necho two\r\necho three\r"), Paste: true, Keys: mustKeys(t, "enter")}
	assert.Equal(t, "\x1b[200~echo one\recho two\recho three\r\x1b[201~\r", string(in.Encode(Modes{BracketedPaste: true})))
	assert.Equal(t, "echo one\recho two\recho three\r\r", string(in.Encode(Modes{})))
}

// A paste cannot end itself early: every end marker in the text goes, also
// one that the removal of another marker builds, in either mode.
func TestInputEncode_PasteRemovesEveryEndMarker(t *testing.T) {
	for name, tc := range map[string]struct {
		text string
		want string
	}{
		"one marker":          {"a\x1b[201~b", "ab"},
		"two markers":         {"\x1b[201~a\x1b[201~", "a"},
		"a nested marker":     {"x\x1b[20\x1b[201~1~y", "xy"},
		"a deeply nested one": {"\x1b[2\x1b[20\x1b[201~1~01~", ""},
		"a start marker stays": {
			"\x1b[200~inner", "\x1b[200~inner",
		},
		"a marker split by a line ending": {"\x1b[201\n~", "\x1b[201\r~"},
	} {
		t.Run(name, func(t *testing.T) {
			bracketed := Input{Data: []byte(tc.text), Paste: true}.Encode(Modes{BracketedPaste: true})
			plain := Input{Data: []byte(tc.text), Paste: true}.Encode(Modes{})
			assert.Equal(t, tc.want, string(plain))
			if tc.want == "" {
				assert.Empty(t, bracketed, "a paste with nothing left sends no markers")
				return
			}
			assert.Equal(t, pasteStart+tc.want+pasteEnd, string(bracketed))
		})
	}
}

// Encode never changes the caller's data, though a paste rewrites a copy.
func TestInputEncode_KeepsTheCallersData(t *testing.T) {
	data := []byte("a\nb\x1b[201~")
	Input{Data: data, Paste: true}.Encode(Modes{BracketedPaste: true})
	assert.Equal(t, "a\nb\x1b[201~", string(data))
}

// A large paste encodes whole, with one pair of markers.
func TestInputEncode_LargePaste(t *testing.T) {
	text := make([]byte, 1<<20)
	for i := range text {
		text[i] = 'x'
	}
	got := Input{Data: text, Paste: true}.Encode(Modes{BracketedPaste: true})
	require.Len(t, got, len(text)+len(pasteStart)+len(pasteEnd))
	assert.Equal(t, pasteStart, string(got[:len(pasteStart)]))
	assert.Equal(t, pasteEnd, string(got[len(got)-len(pasteEnd):]))
}
