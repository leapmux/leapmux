// Package usersettings declares the account-scope settings stored in the
// users.prefs JSON blob, one declared key per setting — the user-scope
// counterpart of internal/hub/settings. The declaration model (typed
// Key[T] handles with defaults, validators, and a UIMeta field schema) is
// shared with the instance scope; the storage strategy is not. Instance
// settings are one row per key in hub_settings with a process-wide
// snapshot cache, while user settings are one key inside a per-user JSON
// blob decoded per request. What the two scopes share is exactly what a
// per-user row cannot carry: partial merges, per-key validation, and a
// declared UI schema — so a bad stored value degrades that key to its
// default instead of zeroing the whole document, and two devices editing
// different keys never overwrite each other.
package usersettings

import (
	"fmt"
	"strings"

	"github.com/leapmux/leapmux/internal/hub/settings"
	"github.com/leapmux/leapmux/internal/util/ptrconv"
	"github.com/leapmux/leapmux/util/validate"
)

// FontFamilyValue is one font family setting: the enable switch plus the
// ordered stack of family names. The two halves form one coherent unit —
// overriding the toggle and the list independently gives incoherent
// states — so both tiers (account blob and browser override) treat the
// whole object as the override unit.
type FontFamilyValue struct {
	Enabled bool     `json:"enabled"`
	Fonts   []string `json:"fonts,omitempty"`
}

// KeybindingOverride is one custom keybinding entry. The shape mirrors
// the client's override document verbatim (tinykeys combo, command id,
// optional when-expression); an empty key unbinds the command.
type KeybindingOverride struct {
	Key     string `json:"key"`
	Command string `json:"command"`
	When    string `json:"when,omitempty"`
}

// The list caps of the account-scope keys. The keybinding pair moved
// from the old whole-blob RPC path: at most 200 overrides, at most 256
// characters per field. MaxFonts caps each font stack.
//
// Every list an account can grow needs a cap here, because the storage
// supplies none: users.prefs is TEXT in MySQL (65,535 bytes) and
// effectively unlimited in SQLite and Postgres, so an uncapped list
// accepts a different maximum per dialect and the only other ceiling is
// the 4 MiB request limit the hub sets for the whole call. The browser
// tier mirrors each cap (see MAX_STRING_LIST_ITEMS and
// MAX_KEYBINDING_OVERRIDES in the frontend controls); the hub is the
// authority, and the browser must never be the stricter of the two.
const (
	MaxKeybindings      = 200
	MaxKeybindingLength = 256
	MaxFonts            = 32
)

// validateEnum REFUSES any value the key's own UIMeta does not advertise.
//
// A declared enum is the write-path rule, not only a rendering hint. Without
// this, `theme` accepted any slug-shaped string: the hub stored it, the
// client's own parse then refused it and fell back to the default, and the
// dialog showed the default beside a "Customized" badge with no way out but
// Reset. The value the hub keeps and the value a client can display must be
// the same set.
//
// The allowed set comes from the field declaration itself, so a value added
// to the enum is accepted the moment it is declared and no second list can
// drift from it. `settings.TestEnumDeclarationsAreSingleSourced` already
// pins the other direction — every advertised value must pass the
// validator.
func validateEnum(allowed []settings.EnumValue) func(string) error {
	names := make([]string, 0, len(allowed))
	for _, ev := range allowed {
		names = append(names, ev.Value)
	}
	return func(v string) error {
		for _, name := range names {
			if v == name {
				return nil
			}
		}
		return fmt.Errorf("must be one of %s (got %q)", strings.Join(names, ", "), v)
	}
}

// validateFontFamily REFUSES a stack longer than MaxFonts, and any family
// name that `validate.SanitizeName` does not return unchanged.
//
// The list cap comes first, for the reason validateKeybindings caps its own
// list: a per-name limit caps one entry and says nothing about how many
// entries the list holds, and this key is one an account can grow without
// limit. The per-name BYTE cap comes second, and it comes before the two
// `%q` verbs below, which each expand an unprintable byte roughly fourfold:
// without it, one 4 MiB name (the hub's whole request cap) turns into a
// 16 MiB error string in an uncapped Connect response and in a log line.
//
// This function refuses rather than rewrites, although the settings framework
// does supply a rewrite hook (Key.WithNormalize, which ApplyPartial runs).
// Refusing is a decision and not a limit: a session ID and a font name are
// both values a client SENT, and a silent rewrite of either gives the client
// back something it did not ask for. A tab title takes the other answer,
// because it has no client to report to.
//
// The error reports the SANITIZED FORM rather than a list of causes.
// SanitizeName rewrites as well as strips, so a list of causes cannot stay
// correct: `Fira Code` with a no-break space is refused, and a no-break
// space is neither a control character, nor an invisible format character,
// nor a repeated space. `%q` escapes both strings, so the user reads
// `"Fira Code"` against `"Fira Code"` and can see the difference that
// the screen hides.
//
// The rule no longer refuses a quote, a backslash, a `$` or a `%`, and the
// CSS stays safe: `buildFontFamily` in frontend/src/lib/fontStack.ts escapes
// a quote and a backslash where the name is interpolated into the
// `font-family` value. That escape is the guard worth having, because it
// holds for whatever the store holds — including the hand-edited
// localStorage document that never reaches this function at all. A second
// character ban here only duplicated it, in two languages that drift apart.
// A font named `Fira$Code` matches no installed family, and that is all the
// harm it does.
func validateFontFamily(v FontFamilyValue) error {
	if len(v.Fonts) > MaxFonts {
		return fmt.Errorf("too many font names: %d (max %d)", len(v.Fonts), MaxFonts)
	}
	for i, name := range v.Fonts {
		if len(name) > validate.NameByteLimit {
			return fmt.Errorf("invalid font name at index %d: must be at most %d bytes (got %d)", i, validate.NameByteLimit, len(name))
		}
		sanitized, err := validate.SanitizeName(name)
		if err != nil {
			return fmt.Errorf("invalid font name %q: %w", name, err)
		}
		if sanitized != name {
			return fmt.Errorf("invalid font name %q: must be its cleaned form %q", name, sanitized)
		}
	}
	return nil
}

// validateKeybindings ports the old custom-keybindings JSON validation
// onto the key that owns it.
func validateKeybindings(v []KeybindingOverride) error {
	if len(v) > MaxKeybindings {
		return fmt.Errorf("too many keybinding overrides: %d (max %d)", len(v), MaxKeybindings)
	}
	for i, e := range v {
		if e.Command == "" {
			return fmt.Errorf("keybinding %d: command is required", i)
		}
		if len(e.Key) > MaxKeybindingLength {
			return fmt.Errorf("keybinding %d: key too long (%d > %d)", i, len(e.Key), MaxKeybindingLength)
		}
		if len(e.Command) > MaxKeybindingLength {
			return fmt.Errorf("keybinding %d: command too long (%d > %d)", i, len(e.Command), MaxKeybindingLength)
		}
		if len(e.When) > MaxKeybindingLength {
			return fmt.Errorf("keybinding %d: when too long (%d > %d)", i, len(e.When), MaxKeybindingLength)
		}
	}
	return nil
}

// The enum catalogues of the account-scope scalar keys. Each one is the
// single source for both halves of its key: the advertised set in UIMeta
// and the write-path validator derived from it, so the two cannot drift.
var (
	themeEnumValues = []settings.EnumValue{
		{Value: "dark"},
		{Value: "light"},
		{Value: "system"},
	}
	terminalThemeEnumValues = []settings.EnumValue{
		{Value: "match-ui"},
		{Value: "dark"},
		{Value: "light"},
	}
	diffViewEnumValues = []settings.EnumValue{
		{Value: "unified"},
		{Value: "split"},
	}
	turnEndSoundEnumValues = []settings.EnumValue{
		{Value: "none"},
		{Value: "ding-dong"},
	}
)

// The declared account-scope keys. Names are the JSON property names
// inside the users.prefs blob.
var (
	KeyTheme = settings.NewKey[string]("theme").
			WithDefault("system").
			WithValidate(validateEnum(themeEnumValues)).
			WithUI(settings.UIMeta{
			Category: "appearance",
			Title:    "Theme",
			Summary:  "overall light and dark palette",
			Fields: []settings.Field{{
				Name: "", Kind: settings.FieldEnum,
				EnumValues: themeEnumValues,
			}},
		})

	KeyTerminalTheme = settings.NewKey[string]("terminal_theme").
				WithDefault("match-ui").
				WithValidate(validateEnum(terminalThemeEnumValues)).
				WithUI(settings.UIMeta{
			Category: "appearance",
			Title:    "Terminal theme",
			Summary:  "color scheme for terminal tabs",
			Fields: []settings.Field{{
				Name: "", Kind: settings.FieldEnum,
				EnumValues: terminalThemeEnumValues,
			}},
		})

	KeyUIFonts = settings.NewKey[FontFamilyValue]("ui_fonts").
			WithDefault(FontFamilyValue{}).
			WithValidate(validateFontFamily).
			WithUI(settings.UIMeta{
			Category: "appearance",
			Title:    "UI fonts",
			Summary:  "custom UI font stack, in priority order",
			Fields: []settings.Field{
				{Name: "enabled", Kind: settings.FieldBool},
				{Name: "fonts", Kind: settings.FieldStringList},
			},
		})

	KeyMonoFonts = settings.NewKey[FontFamilyValue]("mono_fonts").
			WithDefault(FontFamilyValue{}).
			WithValidate(validateFontFamily).
			WithUI(settings.UIMeta{
			Category: "appearance",
			Title:    "Monospace fonts",
			Summary:  "custom monospace font stack, in priority order",
			Fields: []settings.Field{
				{Name: "enabled", Kind: settings.FieldBool},
				{Name: "fonts", Kind: settings.FieldStringList},
			},
		})

	KeyDiffView = settings.NewKey[string]("diff_view").
			WithDefault("unified").
			WithValidate(validateEnum(diffViewEnumValues)).
			WithUI(settings.UIMeta{
			Category: "appearance",
			Title:    "Diff view",
			Summary:  "how file diffs render in chat and the file viewer",
			Fields: []settings.Field{{
				Name: "", Kind: settings.FieldEnum,
				EnumValues: diffViewEnumValues,
			}},
		})

	KeyTurnEndSound = settings.NewKey[string]("turn_end_sound").
			WithDefault("ding-dong").
			WithValidate(validateEnum(turnEndSoundEnumValues)).
			WithUI(settings.UIMeta{
			Category: "notifications",
			Title:    "Turn-end sound",
			Summary:  "sound played when an agent turn finishes",
			Fields: []settings.Field{{
				Name: "", Kind: settings.FieldEnum,
				EnumValues: turnEndSoundEnumValues,
			}},
		})

	KeyTurnEndSoundVolume = settings.NewKey[int64]("turn_end_sound_volume").
				WithDefault(100).
				WithValidate(func(v int64) error {
			if v < 0 || v > 100 {
				return fmt.Errorf("turn-end volume must be between 0 and 100 (got %d)", v)
			}
			return nil
		}).
		WithUI(settings.UIMeta{
			Category: "notifications",
			Title:    "Turn-end volume",
			Summary:  "playback volume for the turn-end sound",
			Fields: []settings.Field{{
				Name: "", Kind: settings.FieldInt,
				Min: ptrconv.Ptr[int64](0), Max: ptrconv.Ptr[int64](100), Unit: "percent",
			}},
		})

	KeyDebugLogging = settings.NewKey[bool]("debug_logging").
			WithDefault(false).
			WithUI(settings.UIMeta{
			Category: "advanced",
			Title:    "Debug logging",
			Summary:  "verbose client-side diagnostic logging",
			Fields:   []settings.Field{{Name: "", Kind: settings.FieldBool}},
		})

	KeyKeybindings = settings.NewKey[[]KeybindingOverride]("keybindings").
			WithDefault([]KeybindingOverride{}).
			WithValidate(validateKeybindings).
			WithUI(settings.UIMeta{
			Category: "shortcuts",
			Title:    "Keyboard shortcuts",
			Summary:  "per-command keybinding overrides",
			Fields: []settings.Field{{
				Name: "", Kind: settings.FieldCustom,
				CustomID: "keybindings",
			}},
		})
)

// descriptors lists the account-scope keys in declaration order.
func descriptors() []settings.Descriptor {
	return []settings.Descriptor{
		KeyTheme,
		KeyTerminalTheme,
		KeyUIFonts,
		KeyMonoFonts,
		KeyDiffView,
		KeyTurnEndSound,
		KeyTurnEndSoundVolume,
		KeyDebugLogging,
		KeyKeybindings,
	}
}
