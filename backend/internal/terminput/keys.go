package terminput

import (
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
)

// Key is one key press: a key and the modifiers held with it. Only ParseKey
// builds a Key, so every Key holds a combination that has an encoding. The
// zero Key encodes as no bytes.
type Key struct {
	// name is the canonical name, which ParseKey reads back as this Key.
	name string
	// normal is the bytes that the key sends in the normal cursor key mode.
	normal string
	// application is the bytes that the key sends in the application cursor
	// key mode. It differs from normal only for an unmodified cursor key.
	application string
}

// String returns the canonical name of k: the modifiers in the order ctrl,
// alt, shift, then the key, in lower case except for a character key.
func (k Key) String() string { return k.name }

// sequence returns the bytes that k sends to a terminal in modes m.
func (k Key) sequence(m Modes) string {
	if m.ApplicationCursorKeys {
		return k.application
	}
	return k.normal
}

// modifier is a key held with another key. The values are the bits that
// xterm adds to 1 for the modifier parameter of a key sequence, such as the 5
// of ESC [1;5D for ctrl-left.
type modifier uint8

const (
	modShift modifier = 1 << iota
	modAlt
	modCtrl
)

// modifierNames lists each modifier with its name, in the order of a
// canonical key name.
var modifierNames = []struct {
	mod  modifier
	name string
}{
	{modCtrl, "ctrl"},
	{modAlt, "alt"},
	{modShift, "shift"},
}

// keyKind is how a key encodes.
type keyKind uint8

const (
	// kindByte is a key that sends one byte: enter, tab, escape, backspace,
	// space, and a printable character.
	kindByte keyKind = iota + 1
	// kindCursor is an arrow key, home or end. It sends ESC [ <final>, or
	// ESC O <final> in the application cursor key mode.
	kindCursor
	// kindSS3 is f1 to f4. It sends ESC O <final> in either mode.
	kindSS3
	// kindTilde is insert, delete, pageup, pagedown, and f5 to f12. It sends
	// ESC [ <number> ~.
	kindTilde
)

// namedKey is a key that a name selects.
type namedKey struct {
	name string
	kind keyKind
	// code is the byte that a kindByte key sends, or the final byte of the
	// sequence of a kindCursor or kindSS3 key.
	code byte
	// number is the number before the "~" of a kindTilde key.
	number int
}

// namedKeys lists every key that a name selects, with the bytes that xterm.js
// sends for it. A modifier on a cursor, editing or function key adds xterm's
// modifier parameter, which both modes send the same way.
var namedKeys = []namedKey{
	{name: "enter", kind: kindByte, code: '\r'},
	{name: "tab", kind: kindByte, code: '\t'},
	{name: "escape", kind: kindByte, code: 0x1b},
	{name: "backspace", kind: kindByte, code: 0x7f},
	{name: "space", kind: kindByte, code: ' '},
	{name: "up", kind: kindCursor, code: 'A'},
	{name: "down", kind: kindCursor, code: 'B'},
	{name: "right", kind: kindCursor, code: 'C'},
	{name: "left", kind: kindCursor, code: 'D'},
	{name: "home", kind: kindCursor, code: 'H'},
	{name: "end", kind: kindCursor, code: 'F'},
	{name: "insert", kind: kindTilde, number: 2},
	{name: "delete", kind: kindTilde, number: 3},
	{name: "pageup", kind: kindTilde, number: 5},
	{name: "pagedown", kind: kindTilde, number: 6},
	{name: "f1", kind: kindSS3, code: 'P'},
	{name: "f2", kind: kindSS3, code: 'Q'},
	{name: "f3", kind: kindSS3, code: 'R'},
	{name: "f4", kind: kindSS3, code: 'S'},
	{name: "f5", kind: kindTilde, number: 15},
	{name: "f6", kind: kindTilde, number: 17},
	{name: "f7", kind: kindTilde, number: 18},
	{name: "f8", kind: kindTilde, number: 19},
	{name: "f9", kind: kindTilde, number: 20},
	{name: "f10", kind: kindTilde, number: 21},
	{name: "f11", kind: kindTilde, number: 23},
	{name: "f12", kind: kindTilde, number: 24},
}

// keyAliases maps a second name of a key to its name in namedKeys.
var keyAliases = map[string]string{
	"esc": "escape",
}

// backtab is the sequence of shift-tab.
const backtab = "\x1b[Z"

// errEmptyKey is the error of an empty key name.
var errEmptyKey = errors.New("a key name is empty")

// ParseKeys parses each name with ParseKey. It fails on the first name that
// ParseKey refuses, so a caller sends either every key or none.
func ParseKeys(names []string) ([]Key, error) {
	if len(names) == 0 {
		return nil, nil
	}
	keys := make([]Key, 0, len(names))
	for _, name := range names {
		k, err := ParseKey(name)
		if err != nil {
			return nil, err
		}
		keys = append(keys, k)
	}
	return keys, nil
}

// ParseKey reads a key name: a key, after any of the modifiers "ctrl-",
// "alt-" and "shift-". A key is a name of namedKeys, an alias of keyAliases,
// or one printable ASCII character. The modifiers and the names are
// case-insensitive, and a character stands for itself, so "alt-X" sends an
// upper case X.
//
// It refuses a modifier that a terminal cannot send with the key, such as
// ctrl-enter, rather than send the key without it.
func ParseKey(name string) (Key, error) {
	if name == "" {
		return Key{}, errEmptyKey
	}
	rest := name
	var mods modifier
	for {
		mod, after, ok := cutModifier(rest)
		if !ok {
			break
		}
		if mods&mod != 0 {
			return Key{}, fmt.Errorf("key %q holds %s twice", name, modifierName(mod))
		}
		mods |= mod
		rest = after
	}
	if len(rest) == 1 {
		return parseCharacter(name, rest[0], mods)
	}
	base, ok := lookupNamedKey(rest)
	if !ok {
		return Key{}, fmt.Errorf("unknown key %q", name)
	}
	switch base.kind {
	case kindByte:
		return parseByteKey(name, base, mods)
	case kindCursor:
		normal := "\x1b[" + string(base.code)
		application := "\x1bO" + string(base.code)
		if mods != 0 {
			normal = modifiedSequence("1", mods, base.code)
			application = normal
		}
		return Key{name: canonicalName(mods, base.name), normal: normal, application: application}, nil
	case kindSS3:
		seq := "\x1bO" + string(base.code)
		if mods != 0 {
			seq = modifiedSequence("1", mods, base.code)
		}
		return Key{name: canonicalName(mods, base.name), normal: seq, application: seq}, nil
	case kindTilde:
		number := strconv.Itoa(base.number)
		seq := "\x1b[" + number + "~"
		if mods != 0 {
			seq = modifiedSequence(number, mods, '~')
		}
		return Key{name: canonicalName(mods, base.name), normal: seq, application: seq}, nil
	}
	// namedKeys gives every entry one of the kinds above.
	panic(fmt.Sprintf("terminput: key %q has no kind", base.name))
}

// cutModifier removes a leading "ctrl-", "alt-" or "shift-" from name. It
// keeps the prefix when nothing follows it, so "ctrl-" reads as a key name,
// which ParseKey then refuses.
func cutModifier(name string) (modifier, string, bool) {
	for _, m := range modifierNames {
		prefix := m.name + "-"
		if len(name) > len(prefix) && strings.EqualFold(name[:len(prefix)], prefix) {
			return m.mod, name[len(prefix):], true
		}
	}
	return 0, name, false
}

func modifierName(mod modifier) string {
	for _, m := range modifierNames {
		if m.mod == mod {
			return m.name
		}
	}
	return strconv.Itoa(int(mod))
}

// lookupNamedKey finds the key of a name or an alias, in any case.
func lookupNamedKey(name string) (namedKey, bool) {
	lower := strings.ToLower(name)
	if target, ok := keyAliases[lower]; ok {
		lower = target
	}
	for _, k := range namedKeys {
		if k.name == lower {
			return k, true
		}
	}
	return namedKey{}, false
}

// canonicalName joins the modifiers of mods, in the order of modifierNames,
// with base.
func canonicalName(mods modifier, base string) string {
	var sb strings.Builder
	for _, m := range modifierNames {
		if mods&m.mod != 0 {
			sb.WriteString(m.name)
			sb.WriteByte('-')
		}
	}
	sb.WriteString(base)
	return sb.String()
}

// modifiedSequence returns xterm's sequence of a key held with mods:
// ESC [ <number> ; <1 + mods> <final>.
func modifiedSequence(number string, mods modifier, final byte) string {
	return "\x1b[" + number + ";" + strconv.Itoa(1+int(mods)) + string(final)
}

// parseCharacter reads a key that is one character: c itself, or the control
// byte of c with ctrl. It refuses a character outside printable ASCII and a
// space, which has a name of its own.
func parseCharacter(name string, c byte, mods modifier) (Key, error) {
	if c <= ' ' || c > '~' {
		return Key{}, fmt.Errorf("unknown key %q: a character key is one printable ASCII character, and a space is \"space\"", name)
	}
	base := string(c)
	if mods&modCtrl != 0 && isASCIILetter(c) {
		// ctrl sends the same byte for either case of a letter, so the
		// canonical name keeps one of them.
		base = strings.ToLower(base)
	}
	return byteKey(name, namedKey{name: base, kind: kindByte, code: c}, mods)
}

// parseByteKey reads a named key that sends one byte. Shift-tab is the one
// key of them that shift changes, and it sends ESC [Z with no other modifier.
func parseByteKey(name string, base namedKey, mods modifier) (Key, error) {
	if base.code == '\t' && mods&modShift != 0 {
		if mods != modShift {
			return Key{}, fmt.Errorf("key %q: shift-tab takes no other modifier", name)
		}
		return Key{name: canonicalName(mods, base.name), normal: backtab, application: backtab}, nil
	}
	return byteKey(name, base, mods)
}

// byteKey encodes a key that sends one byte, held with mods. Ctrl replaces
// the byte with its control byte, and alt then sends ESC before the byte, as
// xterm.js does for a key that sends one byte.
func byteKey(name string, base namedKey, mods modifier) (Key, error) {
	if mods&modShift != 0 {
		if isASCIILetter(base.code) {
			return Key{}, fmt.Errorf("key %q: shift applies only to tab and to the cursor, editing and function keys; give a capital letter as the character itself, such as %q", name, strings.ToUpper(base.name))
		}
		return Key{}, fmt.Errorf("key %q: shift applies only to tab and to the cursor, editing and function keys", name)
	}
	code := base.code
	if mods&modCtrl != 0 {
		c, ok := controlByte(code)
		if !ok {
			return Key{}, fmt.Errorf("key %q: ctrl applies only to a letter, to one of @ [ \\ ] ^ _ ?, to space and backspace, and to the cursor, editing and function keys", name)
		}
		code = c
	}
	seq := string(code)
	if mods&modAlt != 0 {
		seq = "\x1b" + seq
	}
	return Key{name: canonicalName(mods, base.name), normal: seq, application: seq}, nil
}

// controlByte returns the byte that ctrl sends with the key that sends c: the
// caret notation of the C0 controls (ctrl-a is 0x01, ctrl-[ is ESC), NUL for
// space, and BS for backspace (which sends DEL), as xterm.js sends them.
func controlByte(c byte) (byte, bool) {
	switch {
	case isASCIILetter(c):
		return c & 0x1f, true
	case c == ' ', c == '@':
		return 0x00, true
	case c >= '[' && c <= '_':
		// [ \ ] ^ _ are ESC, FS, GS, RS and US.
		return c & 0x1f, true
	case c == '?':
		return 0x7f, true
	case c == 0x7f:
		return 0x08, true
	}
	return 0, false
}

func isASCIILetter(c byte) bool {
	return (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z')
}

// Vocabulary states the key names that ParseKey accepts, in one sentence for
// a help text. It reads the names from namedKeys and keyAliases, so the help
// cannot drift from the parser.
func Vocabulary() string {
	names := make([]string, 0, len(namedKeys))
	for i := 0; i < len(namedKeys); i++ {
		name := namedKeys[i].name
		// A run of function keys reads as "f1 to f12".
		if isFunctionKey(name) {
			last := i
			for last+1 < len(namedKeys) && isFunctionKey(namedKeys[last+1].name) {
				last++
			}
			if last > i {
				name += " to " + namedKeys[last].name
				i = last
			}
		}
		for _, alias := range aliasesOf(namedKeys[i].name) {
			name += " or " + alias
		}
		names = append(names, name)
	}
	return strings.Join(names, ", ") + ", or one printable ASCII character," +
		" after any of ctrl-, alt- and shift-, such as ctrl-c, alt-b, shift-tab or ctrl-left;" +
		" the names are case-insensitive, and a character keeps its case"
}

// aliasesOf returns the aliases of the key name, in sorted order.
func aliasesOf(name string) []string {
	var out []string
	for alias, target := range keyAliases {
		if target == name {
			out = append(out, alias)
		}
	}
	slices.Sort(out)
	return out
}

// isFunctionKey reports whether name is f1, f2 and so on.
func isFunctionKey(name string) bool {
	if len(name) < 2 || name[0] != 'f' {
		return false
	}
	_, err := strconv.Atoi(name[1:])
	return err == nil
}
