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
	"regexp"
	"strings"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/leapmux/leapmux/internal/hub/settings"
	"github.com/leapmux/leapmux/internal/util/ptrconv"
	"github.com/leapmux/leapmux/util/validate"
)

// FontFamilyValue is one font family setting: the enable switch plus the
// ordered stack of family names. The two halves form one coherent unit —
// overriding the toggle and the list independently gives incoherent
// states — so both tiers (account blob and browser override) treat the
// whole object as the override unit.
// ThemeValue is the whole appearance choice: which palette, and which variant
// of it. One value rather than two keys because they are one choice, presented
// by one control under one scope chip and one Reset.
//
// Name is validated as a SLUG and nothing more, deliberately. The palettes are
// TypeScript modules under frontend/src/styles/themes/, which the hub cannot
// see; a Go copy of their ids would be a second authority that drifts the first
// time someone adds a palette file. The client's own themeById() answers for a
// name its build does not carry, so an unknown name costs a fallback to the
// default palette -- not a refused write that would stop a NEWER client from
// storing a theme this hub has never heard of.
type ThemeValue struct {
	Name    string        `json:"name"`
	Mode    string        `json:"mode"`
	Variant *ThemeVariant `json:"variant,omitempty"`
}

// ThemeVariant pins which look of a palette each polarity wears: Catppuccin
// publishes four flavours, of which one is light and three are dark, so a
// palette name and a light/dark mode together no longer name one look.
//
// BOTH HALVES ARE STORED although the client shows one picker at a time. A
// preference set to follow the system has to answer for both, because the OS
// flips at dusk and the client must already know which dark variant to paint.
//
// An empty half means "that theme's default variant", which is the palette it
// shipped before variants existed. The names are slugs the CLIENT owns, for the
// same reason the palette name is -- the variants are TypeScript modules the hub
// cannot see -- so the hub checks only their shape.
type ThemeVariant struct {
	Light string `json:"light,omitempty"`
	Dark  string `json:"dark,omitempty"`
}

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
// supplies none: users.prefs is MEDIUMTEXT in MySQL and effectively
// unlimited in SQLite and Postgres, so the DIALECT no longer sets a
// ceiling and the only other one is MaxPrefsBytes for the whole
// document, plus the 4 MiB request limit for the whole call. The browser
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
		return fmt.Errorf("must be one of %s (got %q)", strings.Join(names, ", "), echoLimit(v))
	}
}

// maxEchoedValue caps what a validation error may quote back.
//
// It is generous next to every legal value here -- the longest is a theme
// variant id -- and small next to what a client can send.
const maxEchoedValue = 64

// echoLimit cuts v to what an error message may carry.
//
// EVERY validator that formats a rejected value with %q reads this, because the
// cap has to hold for the whole class rather than for the fields somebody
// remembered. `Registry.ApplyPartial` runs the per-key validator BEFORE it
// measures the document against MaxPrefsBytes, so nothing upstream bounds the
// value: a 4 MiB string reached %q, which expands an unprintable byte roughly
// fourfold, and built a ~16 MiB error in the RPC response AND in the log line.
// validateTheme and validateTerminalTheme capped their `Name` half for exactly
// this reason and then handed the uncapped `Mode` half to validateEnum first.
//
// The marker is the ellipsis, so a reader can tell a cut value from a short
// one and does not chase a difference that is not in the stored data.
func echoLimit(v string) string {
	if len(v) <= maxEchoedValue {
		return v
	}
	return validate.TruncateToBytes(v, maxEchoedValue) + "..."
}

// DefaultThemeName is the palette every fallback resolves to. It must equal
// DEFAULT_THEME_ID in frontend/src/styles/themes/index.ts, which
// TestThemeDefaultMatchesTheClientCatalogue pins.
const DefaultThemeName = "default"

// MaxThemeNameLength caps the stored palette name. The product bound on the
// whole document is MaxPrefsBytes, which registry.ApplyPartial enforces, so
// every string an account can set needs a cap of its OWN -- one oversized field
// would otherwise refuse every later write to any other key in the blob.
//
// The bound is the product's and no longer the storage's: this branch moved
// MySQL's `prefs` column to MEDIUMTEXT precisely so no dialect exports a
// ceiling the product never chose.
const MaxThemeNameLength = 64

// themeNamePattern is the slug shape a palette id takes: lowercase, digits, and
// single interior hyphens. It matches the id rule the client's theme catalogue
// test enforces, so a name this accepts is a name a client could carry.
var themeNamePattern = regexp.MustCompile(`^[a-z][a-z0-9]*(?:-[a-z0-9]+)*$`)

// validateTheme checks the mode against its advertised enum and the palette
// name against a slug shape.
//
// The asymmetry is deliberate, and it is the point of the ThemeValue comment:
// the mode is a closed set this package owns, so it gets the same exact-match
// rule every other enum key gets. The palette catalogue lives in the client, so
// the hub checks only that the name is SHAPED like an id. An empty name is
// accepted and means "the default palette", which is what an older client that
// writes only a mode produces.
func validateTheme(v ThemeValue) error {
	if err := validateEnum(themeModeEnumValues)(v.Mode); err != nil {
		return fmt.Errorf("mode: %w", err)
	}
	if err := validateThemeVariant(v.Variant); err != nil {
		return err
	}
	// The sentinel is REFUSED in this slot. `match-ui` means "follow the UI
	// theme", so it can mean nothing for the UI theme itself -- but it happens to
	// match themeNamePattern, so the shape check alone accepted it. The client
	// then rejects it and substitutes the default, leaving the user's appearance
	// dialog showing the default palette beside a "Customized" badge with no way
	// out but Reset: a stored value with no meaning, which is exactly what this
	// package exists to keep out.
	if v.Name == MatchUI {
		return fmt.Errorf("invalid theme name %q: the sentinel means \"follow the UI theme\" and cannot name the UI theme itself", MatchUI)
	}
	return validateThemeName(v.Name)
}

// validateThemeVariant checks the SHAPE of each half, and nothing more. The
// variant catalogue lives in the client for the reason the ThemeValue comment
// gives, so a name this accepts is a name a client could carry; one it does not
// carry resolves to that theme's default rather than failing.
func validateThemeVariant(v *ThemeVariant) error {
	if v == nil {
		return nil
	}
	for _, half := range []struct {
		field string
		name  string
	}{{"light", v.Light}, {"dark", v.Dark}} {
		if err := validateThemeName(half.name); err != nil {
			return fmt.Errorf("variant.%s: %w", half.field, err)
		}
	}
	return nil
}

// validateThemeName checks the SHAPE of a palette name, shared by both theme
// keys. An empty name is accepted and means "the default palette", which is what
// an older client that writes only a mode produces.
func validateThemeName(name string) error {
	if name == "" {
		return nil
	}
	if len(name) > MaxThemeNameLength {
		return fmt.Errorf("theme name too long: %d bytes (max %d)", len(name), MaxThemeNameLength)
	}
	if !themeNamePattern.MatchString(name) {
		return fmt.Errorf("invalid theme name %q: must be lowercase letters, digits and single hyphens", name)
	}
	return nil
}

// MaxPrefsBytes caps the WHOLE serialized settings document.
//
// Every per-key cap bounds one field and none of them can see the aggregate:
// the declared caps sum to roughly 170 KB, and one key reaches that alone. The
// check lives at Registry.ApplyPartial, the single write path.
const MaxPrefsBytes = 256 << 10

// MatchUI ties the terminal theme, and the syntax theme, to the UI theme. It is
// stored as a VALUE rather than as an absence, because "follow the app" has to
// survive the app's theme changing -- recording a copy of whatever the UI held
// at the time would freeze the terminal to that palette the moment the user
// switched.
//
// It must equal MATCH_UI in frontend/src/styles/themes/types.ts, which
// TestTerminalThemeSentinelMatchesTheClient pins.
const MatchUI = "match-ui"

// validateTerminalTheme accepts everything validateTheme does, plus the MatchUI
// sentinel -- in BOTH halves together, or in neither. Shared with `syntax_theme`,
// which has the same shape and the same sentinel meaning; a second copy would be
// a second place for the rule to drift.
//
// Together, because "follow the app" is ONE decision and the client states it as
// one switch. A mixed document is not a third setting a user can describe: it is
// a document no client produces, so the hub refuses it here rather than storing
// a state whose meaning the two sides would have to agree on separately.
//
// It refuses rather than rewrites, for the reason validateFontFamily gives: a
// silent normalization hands the client back something it did not ask for.
func validateTerminalTheme(v ThemeValue) error {
	// Through the same derived-from-the-catalogue validator the UI mode uses, so
	// the accepted set is stated once -- in terminalThemeModeEnumValues -- rather
	// than as a special case for the sentinel here.
	if err := validateEnum(terminalThemeModeEnumValues)(v.Mode); err != nil {
		return fmt.Errorf("mode: %w", err)
	}
	if err := validateThemeVariant(v.Variant); err != nil {
		return err
	}
	// The name is capped BEFORE it reaches a %q. The sentinel check below formats
	// it into an error, and %q expands an unprintable byte roughly fourfold, so a
	// 4 MiB name arriving with a mismatched mode would build a ~16 MiB error
	// string in the response and in the log line -- the failure validateFontFamily
	// states at its own guard. The sentinel is shorter than the cap, so testing
	// the length first refuses nothing that the sentinel branch would accept.
	if v.Name != MatchUI {
		if err := validateThemeName(v.Name); err != nil {
			return err
		}
	}
	if (v.Name == MatchUI) != (v.Mode == MatchUI) {
		return fmt.Errorf(
			"name and mode must both be %q or neither (got name %q, mode %q)",
			MatchUI, v.Name, v.Mode)
	}
	// A row that follows the app carries no variant of its own. The client's
	// parse returns the bare sentinel and discards any variant beside it, so
	// accepting one stores a document no client can produce and none can read
	// back -- and a user who later pins the row off the sentinel would surface a
	// variant they never chose, or lose it silently, depending on which side
	// answered first.
	if v.Name == MatchUI && v.Variant != nil {
		return fmt.Errorf("variant: a theme that follows the UI (%q) carries no variant of its own", MatchUI)
	}
	return nil
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
	themeModeEnumValues = []settings.EnumValue{
		{Value: "system"},
		{Value: "light"},
		{Value: "dark"},
	}
	// The terminal's mode accepts everything the UI's does, plus the sentinel
	// that ties it to the UI's RESOLVED mode.
	terminalThemeModeEnumValues = append(
		[]settings.EnumValue{{Value: MatchUI}}, themeModeEnumValues...)
	diffViewEnumValues = []settings.EnumValue{
		{Value: "unified"},
		{Value: "split"},
	}
	turnEndSoundEnumValues = []settings.EnumValue{
		{Value: "none"},
		{Value: "ding-dong"},
	}
	// The Desktop keys are the ONE family whose tokens come from
	// contracts/desktop.json rather than from a literal here, because a THIRD
	// language spells them: the Rust shell matches them out of the
	// set_desktop_behavior payload to decide what a close, a minimize and a
	// login launch do. `unified`, `none` and the rest stay literals for the
	// opposite reason -- only this package and the browser read those, and the
	// browser reads them off the wire rather than restating them.
	trayOnCloseEnumValues = []settings.EnumValue{
		{Value: contracts.TrayOnCloseTray},
		{Value: contracts.TrayOnCloseQuit},
	}
	trayOnMinimizeEnumValues = []settings.EnumValue{
		{Value: contracts.TrayOnMinimizeTray},
		{Value: contracts.TrayOnMinimizeTaskbar},
	}
	startMinimizedEnumValues = []settings.EnumValue{
		{Value: contracts.StartMinimizedWindow},
		{Value: contracts.StartMinimizedMinimized},
	}
)

// dropStaleVariant clears a variant the MERGE carried over from a palette the
// document no longer names.
//
// A variant id names one look of ONE palette, so it stops meaning anything the
// moment the name beside it changes. The client already states this -- the
// chooser commits `variant: undefined` with every palette pick and with the
// return to the sentinel -- but `JSON.stringify` OMITS an undefined field, so
// the partial document says nothing about the variant and `ApplyPartial` keeps
// the stored one.
//
// This REWRITES where the validators refuse, and the two rules agree rather
// than conflict: refusing is right for a value a client SENT, and this residue
// is a value no client sent. Without it the sentinel becomes unreachable --
// once a user picks a variant, every later attempt to put that row back on
// "Match UI" merges the old variant back in and the validator refuses the write
// for the life of the account.
//
// `specified` distinguishes "the client cleared it" from "the client did not
// mention it", so a bare mode change keeps the variant it never spoke about.
func dropStaleVariant(prev, next ThemeValue, specified map[string]bool) ThemeValue {
	if specified["variant"] || next.Name == prev.Name {
		return next
	}
	next.Variant = nil
	return next
}

// The declared account-scope keys. Names are the JSON property names
// inside the users.prefs blob.
var (
	KeyTheme = settings.NewKey[ThemeValue]("theme").
			WithDefault(ThemeValue{Name: DefaultThemeName, Mode: "system"}).
			WithNormalize(dropStaleVariant).
			WithValidate(validateTheme).
			WithUI(settings.UIMeta{
			Category: "appearance",
			Title:    "Theme",
			Summary:  "color palette, variant and light/dark mode",
			Fields: []settings.Field{{
				Name: "", Kind: settings.FieldCustom,
				CustomID: "theme",
			}},
		})

	KeyTerminalTheme = settings.NewKey[ThemeValue]("terminal_theme").
				WithDefault(ThemeValue{Name: MatchUI, Mode: MatchUI}).
				WithNormalize(dropStaleVariant).
				WithValidate(validateTerminalTheme).
				WithUI(settings.UIMeta{
			Category: "appearance",
			Title:    "Terminal theme",
			Summary:  "color palette, variant and light/dark mode for terminal tabs",
			Fields: []settings.Field{{
				Name: "", Kind: settings.FieldCustom,
				CustomID: "terminalTheme",
			}},
		})

	KeySyntaxTheme = settings.NewKey[ThemeValue]("syntax_theme").
			WithDefault(ThemeValue{Name: MatchUI, Mode: MatchUI}).
			WithNormalize(dropStaleVariant).
			WithValidate(validateTerminalTheme).
			WithUI(settings.UIMeta{
			Category: "appearance",
			Title:    "Syntax theme",
			Summary:  "color palette, variant and light/dark mode for highlighted code",
			Fields: []settings.Field{{
				Name: "", Kind: settings.FieldCustom,
				CustomID: "syntaxTheme",
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

	// --- Desktop ---
	//
	// Five settings the desktop shell applies and a browser ignores. The hub
	// declares them all the same, because the account tier follows the user to
	// whatever they sign in on; the CLIENT hides the rows outside the desktop
	// app.
	//
	// The Titles and Summaries here are PLATFORM-NEUTRAL and say "tray or menu
	// bar". They are the CLI's `settings get` output, and the hub cannot know
	// the operating system of the client that reads them. The dialog supplies
	// its own macOS wording; see the browser registry.
	KeyTrayEnabled = settings.NewKey[bool]("tray_enabled").
			WithDefault(false).
			WithUI(settings.UIMeta{
			Category: "desktop",
			Title:    "Tray icon",
			Summary:  "show a LeapMux icon in the system tray or the menu bar",
			Fields:   []settings.Field{{Name: "", Kind: settings.FieldBool}},
		})

	KeyTrayOnClose = settings.NewKey[string]("tray_on_close").
			WithDefault(contracts.TrayOnCloseTray).
			WithValidate(validateEnum(trayOnCloseEnumValues)).
			WithUI(settings.UIMeta{
			Category: "desktop",
			Title:    "Window close action",
			Summary:  "what the desktop app does when you close the last window",
			Fields: []settings.Field{{
				Name: "", Kind: settings.FieldEnum,
				EnumValues: trayOnCloseEnumValues,
			}},
		})

	KeyTrayOnMinimize = settings.NewKey[string]("tray_on_minimize").
				WithDefault(contracts.TrayOnMinimizeTaskbar).
				WithValidate(validateEnum(trayOnMinimizeEnumValues)).
				WithUI(settings.UIMeta{
			Category: "desktop",
			Title:    "Window minimize action",
			Summary:  "what the desktop app does when you minimize a window",
			Fields: []settings.Field{{
				Name: "", Kind: settings.FieldEnum,
				EnumValues: trayOnMinimizeEnumValues,
			}},
		})

	KeyStartOnLogin = settings.NewKey[bool]("start_on_login").
			WithDefault(false).
			WithUI(settings.UIMeta{
			Category: "desktop",
			Title:    "Start at login",
			Summary:  "start the desktop app when you sign in to the computer",
			Fields:   []settings.Field{{Name: "", Kind: settings.FieldBool}},
		})

	KeyStartMinimized = settings.NewKey[string]("start_minimized").
				WithDefault(contracts.StartMinimizedWindow).
				WithValidate(validateEnum(startMinimizedEnumValues)).
				WithUI(settings.UIMeta{
			Category: "desktop",
			Title:    "Window state at login",
			Summary:  "whether a login launch shows a window; the login launch only",
			Fields: []settings.Field{{
				Name: "", Kind: settings.FieldEnum,
				EnumValues: startMinimizedEnumValues,
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
		KeySyntaxTheme,
		KeyUIFonts,
		KeyMonoFonts,
		KeyDiffView,
		KeyTurnEndSound,
		KeyTurnEndSoundVolume,
		KeyTrayEnabled,
		KeyTrayOnClose,
		KeyTrayOnMinimize,
		KeyStartOnLogin,
		KeyStartMinimized,
		KeyDebugLogging,
		KeyKeybindings,
	}
}
