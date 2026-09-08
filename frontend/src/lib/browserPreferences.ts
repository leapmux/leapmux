/**
 * The consolidated browser-preference document: its shape, and the three
 * accessors that read and write it.
 *
 * A MODULE OF ITS OWN, beside `~/lib/browserStorage` rather than inside it. The
 * gateway answers "where is this key stored, and under whose account"; this
 * answers "what is in the browser-preference document". Keeping the interface
 * and its accessors adjacent is the point -- they were a thousand lines apart in
 * one file, so a field added to one half had no visible partner in the other.
 *
 * `updateBrowserPref` and the batch state the two rules that go with the
 * document. Stating them here rather than at each caller is what keeps a writer
 * from drifting from the reader beside it.
 *
 * THIS MODULE AND THE GATEWAY IMPORT EACH OTHER, deliberately.
 * `setStorageAccount` has to refuse an account move while a batch is open, and
 * that guard is a direct call to `browserPrefBatchOpen` rather than a
 * registration this module performs on load. A registry would make a
 * CORRECTNESS guard depend on this module having been evaluated -- and only leaf
 * consumers import it, so the gateway could move the namespace with the guard
 * silently absent. Both sides are hoisted function declarations, so the cycle
 * resolves at call time.
 */
import {
  KEY_BROWSER_PREFS,
  localStorageGet,
  localStorageSet,
} from './browserStorage'

export type EnterKeyMode = 'enter-sends' | 'cmd-enter-sends'
export type TerminalRendererPreference = 'auto' | 'webgl' | 'canvas'

/**
 * Browser-level preferences stored as a single JSON object.
 * Fields that are undefined mean "use account default."
 * Dual-tier keys override the matching account setting from
 * UserService; browser-only keys have no account half.
 */
/**
 * The stored shape of one appearance preference, as it sits in localStorage.
 *
 * Every field is optional and every field is a bare `string`, because this is
 * UNTRUSTED: it is whatever a previous build, or a hand-edited storage entry,
 * left behind. `parseThemeValue` / `parseTerminalThemeValue` in `~/lib/themeStore`
 * validate it into the `ThemeValue` / `TerminalThemeValue` the app actually
 * uses, so this type deliberately is NOT those -- naming it separately is what
 * keeps a validated value and a stored one from being confused.
 *
 * Stated once because all three appearance surfaces store the same document; it
 * was written out inline three times, so a fourth field had three places to be
 * added and two of them could be forgotten.
 */
export interface StoredThemeDocument {
  name?: string
  mode?: string
  variant?: { light?: string, dark?: string }
}

export interface BrowserPreferences {
  /**
   * Whole-object browser override of the account `theme` tier
   * ({name, mode}). Absent means "use the account value". The palette name and
   * its light/dark mode override together because they are one appearance
   * choice, presented by one control under one scope chip. `variant` pins which
   * look of that palette each polarity wears; see ~/styles/themes/types.ts.
   */
  theme?: StoredThemeDocument
  /**
   * Whole-object browser override of the account `terminal_theme` tier
   * ({name, mode}). The `match-ui` sentinel fills both halves or neither; see
   * ~/styles/themes/types.ts.
   */
  terminalTheme?: StoredThemeDocument
  /**
   * Whole-object browser override of the account `syntax_theme` tier
   * ({name, mode}). Same shape and same `match-ui` sentinel as
   * {@link terminalTheme}.
   */
  syntaxTheme?: StoredThemeDocument
  diffView?: string
  turnEndSound?: string
  turnEndSoundVolume?: number
  /**
   * Quake-terminal panel geometry and motion, per device.
   *
   * `quakeOrientation` is a bare `string` for the same reason {@link diffView}
   * is: this is untrusted storage, and the parse in PreferencesContext is what
   * narrows it. That matters more here than elsewhere -- all four values reach a
   * CSS custom property, so a hand-edited entry must never survive to the style
   * attribute.
   */
  quakeOrientation?: string
  quakeSizePercent?: number
  quakeAnimationMs?: number
  quakeBackgroundOpacity?: number
  debugLogging?: boolean
  expandAgentThoughts?: boolean
  showHiddenMessages?: boolean
  enterKeyMode?: EnterKeyMode
  terminalRenderer?: TerminalRendererPreference
  /**
   * Whole-object browser override of the account `ui_fonts` tier
   * ({enabled, fonts}). Absent means "use the account value"; the whole
   * object is the override unit because overriding the toggle and the list
   * independently gives incoherent states.
   */
  uiFontOverride?: { enabled: boolean, fonts: string[] }
  /**
   * Whole-object browser override of the account `mono_fonts` tier. Same
   * contract as {@link uiFontOverride}.
   */
  monoFontOverride?: { enabled: boolean, fonts: string[] }
  /**
   * Whether to reveal the saved file in the OS file manager (Finder /
   * Explorer / Files) after a successful download. Only applies in
   * desktop mode; ignored in the browser. Defaults to true — set to
   * `false` explicitly to opt out.
   */
  revealAfterDownload?: boolean
  /** Desktop/browser terminal OSC notifications (OSC 9 / 777 / 99). Default off. */
  terminalOsNotifications?: boolean
  /**
   * Device overrides of the five Desktop account keys. Absent means "use the
   * account value", like every other dual tier, and they ride inside this same
   * consolidated document so `LOCAL_KEY_SPECS` needs no entry of its own.
   *
   * FIVE SCALARS, not one object: the user makes five choices under five scope
   * chips, so an object would make an override of any one of them drag the
   * other four onto the device tier. The enums are typed as bare `string` for
   * the reason {@link diffView} is -- this is untrusted storage, and the parse
   * in PreferencesContext is what narrows it.
   */
  trayEnabled?: boolean
  trayOnClose?: string
  trayOnMinimize?: string
  startOnLogin?: boolean
  startMinimized?: string
  /**
   * Whether the composer status bar (branch/model/effort/mode +
   * rate-limit/context chips) is shown beneath the input box. Default on;
   * toggled from the composer's `[+]` menu.
   */
  showComposerStatusBar?: boolean
}

/** Load the consolidated browser preferences. */
export function loadBrowserPrefs(): BrowserPreferences {
  return localStorageGet<BrowserPreferences>(KEY_BROWSER_PREFS) ?? {}
}

/** Any value a browser preference field can hold. */
export type BrowserPrefValue = NonNullable<BrowserPreferences[keyof BrowserPreferences]>

/**
 * The document every browser-preference write shares while a batch is open, or
 * null while each write owns its own read and write.
 */
let batchedPrefs: BrowserPreferences | null = null

/**
 * Whether a batch is open, for `setStorageAccount`'s guard.
 *
 * A function rather than a direct read of `batchedPrefs`, because the guard sits
 * above this declaration: the account namespace is the earlier concept and
 * reads better first, and a hoisted function keeps that order without the
 * use-before-define a bare reference would be.
 */
export function browserPrefBatchOpen(): boolean {
  return batchedPrefs !== null
}

/**
 * Update a single field in the consolidated browser preferences.
 *
 * `undefined` DELETES the field, which is what "use the account default" means
 * on disk -- storing a null instead would read back as a device override that
 * pins the value to nothing.
 *
 * This lives beside the key and the interface rather than in
 * PreferencesContext, because the document's SHAPE is this module's to state:
 * the interface above, the field-deletion rule here and the batch below are one
 * contract, and a writer that restated any part of it elsewhere could drift
 * from the reader beside it.
 */
export function updateBrowserPref(key: keyof BrowserPreferences, value: BrowserPrefValue | undefined): void {
  const prefs = batchedPrefs ?? loadBrowserPrefs()
  if (value === undefined) {
    delete prefs[key]
  }
  else {
    (prefs as Record<string, unknown>)[key] = value
  }
  // The batch owns the write while one is open. Storing here as well would
  // defeat it and publish a half-applied document to the other tabs.
  if (batchedPrefs === null)
    localStorageSet(KEY_BROWSER_PREFS, prefs)
}

/**
 * Run `body` with every browser-preference write applied to ONE document,
 * stored once at the end.
 *
 * "Reset all browser overrides" clears every browser override, and each one is
 * otherwise a full read, parse, serialize and write of the whole document. One
 * write is also one `storage` event for the other tabs rather than one per field.
 *
 * Both guards are required. The `finally` closes the batch even when a write
 * inside `body` throws; without it every later write in the page would
 * accumulate into a document that nothing stores. The re-entrancy check holds
 * the same invariant from the other side: a nested call must not adopt a second
 * document and store it over the outer one.
 */
export function batchBrowserPrefWrites(body: () => void): void {
  if (batchedPrefs !== null) {
    body()
    return
  }
  batchedPrefs = loadBrowserPrefs()
  try {
    body()
  }
  finally {
    const written = batchedPrefs
    batchedPrefs = null
    localStorageSet(KEY_BROWSER_PREFS, written)
  }
}
