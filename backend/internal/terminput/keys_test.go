package terminput

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var (
	normalModes      = Modes{}
	applicationModes = Modes{ApplicationCursorKeys: true}
)

// TestParseKey_EncodesEveryNamedKey pins the bytes of each key without a
// modifier in both cursor key modes. The expected bytes are the ones that
// xterm.js sends for the same key press.
func TestParseKey_EncodesEveryNamedKey(t *testing.T) {
	cases := map[string]struct{ normal, application string }{
		"enter":     {"\r", "\r"},
		"tab":       {"\t", "\t"},
		"escape":    {"\x1b", "\x1b"},
		"esc":       {"\x1b", "\x1b"},
		"backspace": {"\x7f", "\x7f"},
		"space":     {" ", " "},
		"up":        {"\x1b[A", "\x1bOA"},
		"down":      {"\x1b[B", "\x1bOB"},
		"right":     {"\x1b[C", "\x1bOC"},
		"left":      {"\x1b[D", "\x1bOD"},
		"home":      {"\x1b[H", "\x1bOH"},
		"end":       {"\x1b[F", "\x1bOF"},
		"insert":    {"\x1b[2~", "\x1b[2~"},
		"delete":    {"\x1b[3~", "\x1b[3~"},
		"pageup":    {"\x1b[5~", "\x1b[5~"},
		"pagedown":  {"\x1b[6~", "\x1b[6~"},
		"f1":        {"\x1bOP", "\x1bOP"},
		"f2":        {"\x1bOQ", "\x1bOQ"},
		"f3":        {"\x1bOR", "\x1bOR"},
		"f4":        {"\x1bOS", "\x1bOS"},
		"f5":        {"\x1b[15~", "\x1b[15~"},
		"f6":        {"\x1b[17~", "\x1b[17~"},
		"f7":        {"\x1b[18~", "\x1b[18~"},
		"f8":        {"\x1b[19~", "\x1b[19~"},
		"f9":        {"\x1b[20~", "\x1b[20~"},
		"f10":       {"\x1b[21~", "\x1b[21~"},
		"f11":       {"\x1b[23~", "\x1b[23~"},
		"f12":       {"\x1b[24~", "\x1b[24~"},
	}
	for _, k := range namedKeys {
		assert.Containsf(t, cases, k.name, "the table pins no bytes for %q", k.name)
	}
	for alias := range keyAliases {
		assert.Containsf(t, cases, alias, "the table pins no bytes for the alias %q", alias)
	}
	for name, want := range cases {
		t.Run(name, func(t *testing.T) {
			k, err := ParseKey(name)
			require.NoError(t, err)
			assert.Equal(t, want.normal, k.sequence(normalModes), "normal cursor key mode")
			assert.Equal(t, want.application, k.sequence(applicationModes), "application cursor key mode")
		})
	}
}

// A modifier on a cursor, editing or function key adds xterm's modifier
// parameter, 1 + shift(1) + alt(2) + ctrl(4), and the application cursor key
// mode no longer changes the sequence.
func TestParseKey_EncodesModifiersOnSequenceKeys(t *testing.T) {
	for name, want := range map[string]string{
		"shift-up":            "\x1b[1;2A",
		"alt-left":            "\x1b[1;3D",
		"alt-shift-right":     "\x1b[1;4C",
		"ctrl-left":           "\x1b[1;5D",
		"ctrl-shift-down":     "\x1b[1;6B",
		"ctrl-alt-home":       "\x1b[1;7H",
		"ctrl-alt-shift-end":  "\x1b[1;8F",
		"shift-f1":            "\x1b[1;2P",
		"ctrl-f4":             "\x1b[1;5S",
		"ctrl-delete":         "\x1b[3;5~",
		"shift-pageup":        "\x1b[5;2~",
		"alt-insert":          "\x1b[2;3~",
		"ctrl-shift-pagedown": "\x1b[6;6~",
		"shift-f5":            "\x1b[15;2~",
		"ctrl-alt-f12":        "\x1b[24;7~",
	} {
		t.Run(name, func(t *testing.T) {
			k, err := ParseKey(name)
			require.NoError(t, err)
			assert.Equal(t, want, k.sequence(normalModes))
			assert.Equal(t, want, k.sequence(applicationModes), "a modified key ignores the cursor key mode")
		})
	}
}

// A key that sends one byte takes ctrl as its control byte and alt as an ESC
// before it, as xterm.js sends them.
func TestParseKey_EncodesModifiersOnByteKeys(t *testing.T) {
	for name, want := range map[string]string{
		"a":                  "a",
		"Z":                  "Z",
		"~":                  "~",
		"-":                  "-",
		"ctrl-a":             "\x01",
		"ctrl-c":             "\x03",
		"ctrl-z":             "\x1a",
		"ctrl-C":             "\x03",
		"ctrl-@":             "\x00",
		"ctrl-[":             "\x1b",
		`ctrl-\`:             "\x1c",
		"ctrl-]":             "\x1d",
		"ctrl-^":             "\x1e",
		"ctrl-_":             "\x1f",
		"ctrl-?":             "\x7f",
		"ctrl-space":         "\x00",
		"ctrl-backspace":     "\x08",
		"alt-b":              "\x1bb",
		"alt-B":              "\x1bB",
		"alt--":              "\x1b-",
		"alt-.":              "\x1b.",
		"alt-enter":          "\x1b\r",
		"alt-escape":         "\x1b\x1b",
		"alt-backspace":      "\x1b\x7f",
		"alt-space":          "\x1b ",
		"alt-tab":            "\x1b\t",
		"ctrl-alt-x":         "\x1b\x18",
		"ctrl-alt-space":     "\x1b\x00",
		"ctrl-alt-backspace": "\x1b\x08",
		"shift-tab":          "\x1b[Z",
	} {
		t.Run(name, func(t *testing.T) {
			k, err := ParseKey(name)
			require.NoError(t, err)
			assert.Equal(t, want, k.sequence(normalModes))
			assert.Equal(t, want, k.sequence(applicationModes))
		})
	}
}

// The names and the modifiers are case-insensitive, the modifiers may come in
// any order, and String gives one canonical name that parses back to the same
// key.
func TestParseKey_CanonicalName(t *testing.T) {
	for name, want := range map[string]string{
		"ENTER":           "enter",
		"Esc":             "escape",
		"PageUp":          "pageup",
		"F12":             "f12",
		"Ctrl-C":          "ctrl-c",
		"CTRL-c":          "ctrl-c",
		"ctrl-C":          "ctrl-c",
		"Shift-Tab":       "shift-tab",
		"shift-ctrl-up":   "ctrl-shift-up",
		"Alt-Ctrl-Delete": "ctrl-alt-delete",
		"alt-X":           "alt-X",
		"ctrl-alt-X":      "ctrl-alt-x",
		"Q":               "Q",
		"ctrl-SPACE":      "ctrl-space",
	} {
		t.Run(name, func(t *testing.T) {
			k, err := ParseKey(name)
			require.NoError(t, err)
			assert.Equal(t, want, k.String())
			again, err := ParseKey(k.String())
			require.NoError(t, err)
			assert.Equal(t, k, again, "the canonical name parses back to the same key")
		})
	}
}

// ParseKey refuses a name it cannot encode, and identifies the key in the error.
func TestParseKey_Refusals(t *testing.T) {
	for name, want := range map[string]string{
		"":                "is empty",
		"ctl-c":           `unknown key "ctl-c"`,
		"enterr":          `unknown key "enterr"`,
		"f13":             `unknown key "f13"`,
		"f0":              `unknown key "f0"`,
		"ctrl":            `unknown key "ctrl"`,
		"ctrl-":           `unknown key "ctrl-"`,
		"ctrl-f-1":        `unknown key "ctrl-f-1"`,
		"--":              `unknown key "--"`,
		" ":               "printable ASCII",
		" enter":          `unknown key " enter"`,
		"\t":              "printable ASCII",
		"é":               `unknown key "é"`,
		"ctrl-é":          `unknown key "ctrl-é"`,
		"\x7f":            "printable ASCII",
		"ctrl-ctrl-c":     "holds ctrl twice",
		"Alt-alt-x":       "holds alt twice",
		"shift-a":         "shift applies only to tab",
		"shift-A":         `give a capital letter as the character itself, such as "A"`,
		"shift-enter":     "shift applies only to tab",
		"shift-space":     "shift applies only to tab",
		"ctrl-shift-x":    `such as "X"`,
		"alt-shift-tab":   "shift-tab takes no other modifier",
		"ctrl-shift-tab":  "shift-tab takes no other modifier",
		"ctrl-enter":      "ctrl applies only to a letter",
		"ctrl-tab":        "ctrl applies only to a letter",
		"ctrl-escape":     "ctrl applies only to a letter",
		"ctrl-1":          "ctrl applies only to a letter",
		"ctrl--":          "ctrl applies only to a letter",
		"ctrl-alt-enter":  "ctrl applies only to a letter",
		"ctrl-alt-shift-": `unknown key "ctrl-alt-shift-"`,
	} {
		t.Run(name, func(t *testing.T) {
			_, err := ParseKey(name)
			require.Error(t, err)
			assert.Contains(t, err.Error(), want)
		})
	}
}

// ParseKeys parses every name or none, so a caller never sends half of a
// list.
func TestParseKeys(t *testing.T) {
	keys, err := ParseKeys([]string{"Enter", "ctrl-c", "up"})
	require.NoError(t, err)
	require.Len(t, keys, 3)
	assert.Equal(t, []string{"enter", "ctrl-c", "up"}, []string{keys[0].String(), keys[1].String(), keys[2].String()})

	keys, err = ParseKeys([]string{"enter", "nope", "up"})
	require.Error(t, err)
	assert.Nil(t, keys)
	assert.Contains(t, err.Error(), `"nope"`)

	keys, err = ParseKeys(nil)
	require.NoError(t, err)
	assert.Empty(t, keys)
	keys, err = ParseKeys([]string{})
	require.NoError(t, err)
	assert.Empty(t, keys)
}

// The zero Key, which no parse returns, encodes as nothing.
func TestKey_ZeroValueSendsNothing(t *testing.T) {
	var k Key
	assert.Empty(t, k.sequence(normalModes))
	assert.Empty(t, k.sequence(applicationModes))
	assert.Empty(t, k.String())
}

// Vocabulary states every name and alias that ParseKey accepts, so the help
// text that the CLI prints from it cannot leave one out.
func TestVocabulary_StatesEveryName(t *testing.T) {
	text := Vocabulary()
	for _, k := range namedKeys {
		if isFunctionKey(k.name) {
			continue
		}
		assert.Containsf(t, text, k.name, "the vocabulary leaves out %q", k.name)
	}
	assert.Contains(t, text, "f1 to f12", "the function keys read as one range")
	for alias := range keyAliases {
		assert.Contains(t, text, alias)
	}
	for _, mod := range modifierNames {
		assert.Contains(t, text, mod.name+"-")
	}
	assert.False(t, strings.HasSuffix(text, "."), "a caller ends the sentence")
	assert.Equal(t, text, Vocabulary(), "the sentence is the same on every call")
}

func TestIsFunctionKey(t *testing.T) {
	for name, want := range map[string]bool{"f1": true, "f12": true, "f": false, "fx": false, "end": false, "": false} {
		assert.Equalf(t, want, isFunctionKey(name), "%q", name)
	}
}

// The advice to type a capital letter fits only a letter.
func TestParseKey_ShiftOnALetterSuggestsTheCapital(t *testing.T) {
	_, err := ParseKey("shift-enter")
	require.Error(t, err)
	assert.NotContains(t, err.Error(), "capital")
	_, err = ParseKey("shift-1")
	require.Error(t, err)
	assert.NotContains(t, err.Error(), "capital")
}

func TestAppendPaste_LinearStripsDeeplyNestedEndMarkersAndNormalizesNewlines(t *testing.T) {
	t.Parallel()
	const depth = 2000
	var nested strings.Builder
	for range depth {
		nested.WriteString("\x1b[20")
	}
	for range depth {
		nested.WriteString("1~")
	}
	out := Input{Data: []byte(nested.String()), Paste: true}.Encode(Modes{BracketedPaste: true})
	assert.Empty(t, out, "nested paste end markers that collapse to empty must emit nothing")

	mixed := "a\r\nb\nc" + nested.String() + "d"
	out = Input{Data: []byte(mixed), Paste: true}.Encode(Modes{BracketedPaste: true})
	assert.Equal(t, []byte(pasteStart+"a\rb\rcd"+pasteEnd), out)
}
