import type { Accessor, ParentComponent } from 'solid-js'
import type { SettingDescriptor, SettingValue } from '~/generated/proto/leapmux/v1/settings_pb'
import type { BrowserPreferences, BrowserPrefValue, EnterKeyMode, TerminalRendererPreference } from '~/lib/browserStorage'
import type { UserKeybindingOverride } from '~/lib/shortcuts/types'
import type { TerminalThemeValue, ThemeValue } from '~/styles/themes'
import { createEffect, createSignal, onCleanup, onMount, useContext } from 'solid-js'
import { userClient } from '~/api/clients'
import { NAME_BYTE_LIMIT } from '~/generated/contracts/validate'
import {
  batchBrowserPrefWrites,
  hasStorageAccount,
  KEY_BROWSER_PREFS,
  KEY_DIRECTORY_SELECTOR_SHOW_HIDDEN,
  KEY_PREFERRED_EDITOR,
  loadBrowserPrefs,
  localStorageGet,
  localStorageRemove,
  localStorageSet,
  onStorageAccountChange,
  storedKeyFor,
  updateBrowserPref,
} from '~/lib/browserStorage'
import { createStableContext } from '~/lib/createStableContext'
import { formatErrorMessage } from '~/lib/errors'
import { buildFontFamily, DEFAULT_MONO_FONT_FAMILY } from '~/lib/fontStack'
import { createKeyedQueue } from '~/lib/keyedQueue'
import { createKeyedSeq } from '~/lib/keyedSeq'
import { createLogger, setDebugEnabled } from '~/lib/logger'
import { parseSettingJson } from '~/lib/settingJson'
import { getTerminalRendererPreference } from '~/lib/terminal'
import { applyTheme, DEFAULT_TERMINAL_THEME_VALUE, DEFAULT_THEME_VALUE, parseTerminalThemeValue, parseThemeValue } from '~/lib/themeStore'
import { sanitizeName } from '~/lib/validate'

const log = createLogger('PreferencesContext')

/**
 * A failed account write that also records whether it was still the newest
 * write for its key when it failed.
 *
 * A superseded failure must not roll the control back: the value it would
 * restore predates a write the user has since made.
 */
class SupersededAwareError extends Error {
  readonly newest: boolean
  constructor(message: string, newest: boolean) {
    super(message)
    this.name = 'SupersededAwareError'
    this.newest = newest
  }
}

export type DiffViewPreference = 'unified' | 'split'
export type TurnEndSoundPreference = 'none' | 'ding-dong'

/**
 * One font-family tier: the enable switch plus the ordered stack. Mirrors the
 * backend's `FontFamilyValue` (usersettings keys `ui_fonts` / `mono_fonts`)
 * exactly — the whole object is the override unit on both tiers, because
 * overriding the toggle and the list independently gives incoherent states.
 */
export interface FontTier {
  enabled: boolean
  fonts: string[]
}

export interface PreferencesState {
  /** Resolved theme preference (localStorage override → account default → hardcoded default). */
  theme: () => ThemeValue
  /** Resolved terminal theme preference. */
  terminalTheme: () => TerminalThemeValue
  syntaxTheme: () => TerminalThemeValue
  /** Resolved UI font tier (browser override → account). */
  uiFonts: () => FontTier
  /** Resolved mono font tier (browser override → account). */
  monoFonts: () => FontTier
  /** CSS font-family for monospace contexts. Uses custom fonts if enabled, else default. */
  monoFontFamily: () => string
  /** CSS font-family for UI contexts. Only returns a value if custom fonts are enabled and non-empty. */
  uiFontFamily: () => string | undefined
  /** Resolved diff view preference. */
  diffView: () => DiffViewPreference
  /** Resolved turn end sound preference. */
  turnEndSound: () => TurnEndSoundPreference
  /** Resolved turn end sound volume (0-100). */
  turnEndSoundVolume: () => number
  /** Resolved debug logging preference. */
  debugLogging: () => boolean
  /** Whether thinking/reasoning bubbles should start expanded. */
  expandAgentThoughts: () => boolean
  setExpandAgentThoughts: (value: boolean) => void
  /** Whether hidden messages are shown in the chat view (developer feature). */
  showHiddenMessages: () => boolean
  setShowHiddenMessages: (value: boolean) => void
  /**
   * Whether to reveal the saved file in the OS file manager after a
   * successful download (desktop only).
   */
  revealAfterDownload: () => boolean
  setRevealAfterDownload: (value: boolean) => void
  /** Resolved enter key mode. */
  enterKeyMode: () => EnterKeyMode
  setEnterKeyMode: (value: EnterKeyMode | null) => void
  /**
   * Whether the composer status bar (branch/model/effort/mode +
   * rate-limit/context chips) is shown beneath the input box. Default on.
   */
  showComposerStatusBar: () => boolean
  setShowComposerStatusBar: (value: boolean) => void
  /** Custom keybinding overrides (account-level, stored in Hub DB). */
  customKeybindings: () => UserKeybindingOverride[]
  setCustomKeybindings: (value: UserKeybindingOverride[]) => Promise<void>
  /** Terminal renderer preference (browser-only). */
  terminalRenderer: () => TerminalRendererPreference
  setTerminalRenderer: (value: TerminalRendererPreference | null) => void
  /** Preferred external editor id (browser-only, desktop). */
  preferredEditorId: () => string | undefined
  setPreferredEditorId: (id: string | undefined) => void
  /** Whether the directory picker shows hidden files (browser-only, default on). */
  directoryPickerShowHidden: () => boolean
  setDirectoryPickerShowHidden: (value: boolean) => void

  /** Browser-level terminal OSC desktop notifications (default off). */
  terminalOsNotifications: () => boolean
  setTerminalOsNotifications: (value: boolean) => void

  /**
   * Every dual-tier preference, addressed by name.
   *
   * ONE member per key instead of one per TIER OPERATION (`browserX`,
   * `setBrowserX`, `accountX`, `setAccountX`, `resetX`, plus the resolved
   * accessor above). They are never useful apart — the settings registry
   * reads the resolved value, decides which tier a write lands on from
   * `browser()`, seeds an override from the other tier, and resets the
   * account tier by its own key — so naming them individually made a new
   * key cost an interface line, a provider line and a binding argument
   * EACH, every one of which could be wrong on its own.
   *
   * The RESOLVED accessors stay named above (`theme()`, `monoFonts()`,
   * …). They have consumers all over the app that care about the value and
   * nothing about its tiers.
   */
  dual: {
    theme: DualPreference<ThemeValue>
    terminalTheme: DualPreference<TerminalThemeValue>
    syntaxTheme: DualPreference<TerminalThemeValue>
    uiFonts: DualPreference<FontTier>
    monoFonts: DualPreference<FontTier>
    diffView: DualPreference<DiffViewPreference>
    turnEndSound: DualPreference<TurnEndSoundPreference>
    turnEndSoundVolume: DualPreference<number>
    debugLogging: DualPreference<boolean>
  }

  /** Which account keys carry a stored (customized) value, by proto key. */
  accountCustomized: () => Record<string, boolean>

  /**
   * The hub's SCHEMA for every account key, from the newest load that
   * succeeded. EMPTY until one does.
   *
   * The hub declares each account key's category, control kind, enum values
   * and numeric bounds (`usersettings/keys.go`), and the settings registry
   * joins its own presentation onto this rather than restating any of it.
   * So an account row cannot exist before the reply that describes it: a
   * failed load renders those rows not at all, which `accountLoadError`
   * beneath is what states to the user.
   *
   * The reply is kept here, and not in a second store, because this context
   * already fetches it once for the values and hands the result to every
   * reader. A second fetch is a second thing that can fail on its own.
   */
  accountDescriptors: () => SettingDescriptor[]

  /**
   * Run `body` with every browser-preference write it makes applied to ONE
   * consolidated document, stored once when it returns.
   *
   * For a caller that writes many preferences at once ("Reset all browser
   * overrides"). Each write is otherwise a full read, parse, serialize and
   * write of the whole document, and one `storage` event per field for the
   * other tabs.
   */
  batchBrowserPrefWrites: (body: () => void) => void

  /**
   * Merge a partial JSON document onto ONE account setting, server-first:
   * the server's effective value is applied to the signals on success.
   * REJECTS when the server refuses, so the row that made the write can
   * state the reason inline.
   */
  updateUserSetting: (key: string, partial: unknown) => Promise<void>
  /** Remove one account setting's stored value, returning it to its default. */
  resetUserSetting: (key: string) => Promise<void>
  /** The most recent account-settings load failure, or null. */
  accountLoadError: () => string | null
  /** Reload account settings from Hub. */
  reload: () => Promise<void>
  /**
   * Return every tier to its built-in default, for a page with no signed-in
   * account.
   *
   * A sign-out is client-side, so this provider and everything under it stay
   * mounted. Both tiers still hold the values of the account that LEFT -- the
   * device tier from its stored document, the account tier from its hub reply --
   * and `PreferencesApplier` keeps painting them. Without this the sign-in page
   * renders in the departed account's palette and fonts, which is the leak that
   * scoping the keys exists to close, in the window between sign-out and the
   * next sign-in.
   *
   * `usePreferencesForIdentity` calls it on the transition to no identity. The
   * SEEDING half needs no call: the provider subscribes to
   * `onStorageAccountChange`, so an account arriving re-reads the device tier on
   * its own.
   */
  resetForSignOut: () => void
}

const PreferencesContext = createStableContext<PreferencesState>('context/PreferencesContext')

/** Accept one of a closed set of string values, and refuse anything else. */
function oneOf<T extends string>(...allowed: T[]): (raw: unknown) => T | undefined {
  return raw => (allowed.includes(raw as T) ? raw as T : undefined)
}

/**
 * Accept one font tier, normalized to exactly its two fields, so a stored
 * document cannot carry an extra key into the signal. Anything else is
 * refused, at both tiers.
 */
function parseFontTier(raw: unknown): FontTier | undefined {
  if (typeof raw !== 'object' || raw === null)
    return undefined
  const tier = raw as { enabled?: unknown, fonts?: unknown }
  if (typeof tier.enabled !== 'boolean')
    return undefined
  // `fonts` may be ABSENT, and an absent list is an EMPTY list. The Go
  // field is `json:"fonts,omitempty"`, so the hub's own document for an
  // enabled tier with no families is `{"enabled":true}` — and that is the
  // mandatory FIRST state, because the stack row stays hidden until the
  // tier is on. Refusing it turned the toggle back off on the next load,
  // under a "Customized" badge over a value the row never showed.
  // `protoRegistry` states the same omitempty rule for the numeric zeros.
  if (tier.fonts !== undefined && !Array.isArray(tier.fonts))
    return undefined
  const stored: unknown[] = tier.fonts ?? []
  // Every element must be a string the HUB would also store, for two
  // separate reasons. `buildFontFamily` calls `.replace` on each one, so a
  // non-string element throws a TypeError inside the synchronous font
  // accessors and breaks the whole reactive computation that renders the
  // UI and terminal font family. And one parse guards BOTH tiers, so a
  // name the hub's `validateFontFamily` refuses must not reach the screen
  // from a hand-edited localStorage document either.
  const fonts: string[] = []
  for (const name of stored) {
    if (typeof name !== 'string' || !isStorableFontName(name))
      return undefined
    fonts.push(name)
  }
  return { enabled: tier.enabled, fonts }
}

/**
 * Whether the hub would store this font name unchanged.
 *
 * `usersettings.validateFontFamily` runs `validate.SanitizeName` and
 * refuses the name when the sanitized form differs — no control
 * character, no invisible format character, no repeated space, no leading
 * or trailing space, non-empty, at most 128 UTF-8 bytes. `sanitizeName` is
 * that same rule on this side, so the two are asserted equal here rather
 * than re-typed as a fourth character class.
 *
 * Neither side refuses a quote, a backslash, a `$` or a `%` any more. The
 * guard against a name that ends a CSS declaration is the escape in
 * {@link buildFontFamily}, which holds for whatever the store holds —
 * including the hand-edited document this function reads, which never
 * passes through the hub's validator at all.
 */
function isStorableFontName(name: string): boolean {
  // The length guard runs FIRST, and it is what bounds the work on this path.
  // `parseFontTier` calls this for every entry of the localStorage document on
  // every mount, and a hand-edited document can carry a string of any size.
  // `sanitizeName` below runs three regex passes over the whole value, and the
  // Go copy stops appending at 129 bytes where this one has no such stop.
  //
  // The guard cannot reject a storable name: `String.length` counts UTF-16
  // units, which is never MORE than the UTF-8 byte count, so a name over the
  // limit here is over the limit in bytes too. A name that the clean would
  // SHRINK under the limit is not storable either, because the test below
  // demands the cleaned form equal the input.
  if (name.length > NAME_BYTE_LIMIT)
    return false
  const { value, error } = sanitizeName(name)
  return error === null && value === name
}

/**
 * One setting the hub stores against the account.
 *
 * A key is declared ONCE, as a call to one of the two factories below. The
 * signal, the parse that guards a stored value, the optimistic write, and
 * the dispatch of a server reply all come from that single declaration —
 * before, each key was written out in four places that could disagree.
 */
interface AccountSetting<T> {
  /** The `usersettings` key on the wire. */
  protoKey: string
  /** The hub's stored value, or the built-in default until it answers. */
  account: Accessor<T>
  /**
   * Write the account value, server-first: apply it at once so the control
   * feels local, then let the hub's effective value replace it. REJECTS
   * when the hub refuses, after restoring the pre-write value, so the row
   * that made the write states the reason inline.
   */
  setAccount: (value: T) => Promise<void>
  /** Whether the hub holds a stored value for this key. */
  customized: Accessor<boolean>
  /** Remove the stored value, returning this key to its built-in default. */
  reset: () => Promise<void>
  /**
   * Apply one server document onto the account signal, and report whether
   * it was applied.
   *
   * A document the parse REFUSES leaves the signal at what it holds, so
   * the caller must not then record the key as customized: the badge and
   * the Reset button would sit over a value the row does not show.
   */
  applyServer: (raw: unknown) => boolean
  /**
   * Return the account signal to its built-in default, with no hub call.
   *
   * For a page that has no account to answer for. `reset` is the other
   * direction: it asks the hub to REMOVE the stored value, which needs a
   * signed-in session that a signed-out page does not have.
   */
  clear: () => void
}

/**
 * An account setting that a browser override can shadow. The override
 * wins while it is set; null means "use the account value".
 */
interface DualSetting<T> extends AccountSetting<T> {
  browser: Accessor<T | null>
  setBrowser: (value: T | null) => void
  /** browser() ?? account(). */
  resolved: Accessor<T>
}

/**
 * One dual-tier preference, as a consumer addresses it.
 *
 * The members travel TOGETHER because a caller that has one almost always
 * needs the rest: the settings registry reads the resolved value, decides
 * which tier a write lands on from `browser()`, seeds an override from
 * the other tier, and states whether the account tier holds a stored value
 * it can remove. Naming them individually on the context meant one
 * interface member, one provider line and one binding argument EACH, all
 * of which a new key had to get right separately — and a `bind` that
 * wired one key's browser tier to another key's account tier compiled
 * cleanly.
 *
 * `applyServer` stays off this type: it is how the context talks to the
 * wire, not how a consumer edits a value. `protoKey` travels WITH the
 * preference, because a consumer that resets the account tier has to name
 * the key it removes — and re-stating that key at each binding is how a
 * row for one key came to reset another.
 */
export interface DualPreference<T> {
  /** The `usersettings` key this preference's account tier is stored under. */
  protoKey: string
  /** browser() ?? account(). */
  resolved: Accessor<T>
  /** The device-local override, or null while the account value applies. */
  browser: Accessor<T | null>
  setBrowser: (value: T | null) => void
  account: Accessor<T>
  /** REJECTS when the hub refuses, after restoring the pre-write value. */
  setAccount: (value: T) => Promise<void>
  /** Whether the hub holds a stored value for this key. */
  customized: Accessor<boolean>
  /** Remove the stored value, returning this key to its built-in default. */
  reset: () => Promise<void>
}

interface AccountSettingOptions<T> {
  protoKey: string
  /** The value in force before the hub answers. */
  fallback: T
  /**
   * Accept one stored value, or return undefined to refuse it and keep
   * what the signal holds.
   */
  parse: (raw: unknown) => T | undefined
}

interface DualSettingOptions<T> extends AccountSettingOptions<T> {
  /** The field of the consolidated browser-preferences document. */
  browserPrefKey: keyof BrowserPreferences
  /** Runs after a browser write, with the newly resolved value. */
  onBrowserWrite?: (resolved: T) => void
}

export const PreferencesProvider: ParentComponent = (props) => {
  /** Which account keys carry a stored value, by proto key. */
  const [accountCustomized, setAccountCustomized] = createSignal<Record<string, boolean>>({})

  /**
   * Every device-tier signal, and how it re-reads and re-applies itself.
   *
   * THE DEVICE TIER CANNOT BE READ AT MOUNT. This provider sits above the
   * Router and renders unconditionally, so it mounts while the auth bootstrap
   * is still in flight; every stored preference is scoped to an account, and a
   * read before one resolves throws. So each signal starts at its own default,
   * and the entry it registers here is how it catches up.
   *
   * ONE registry rather than one array per purpose. Seeding an account,
   * following another tab and returning to the defaults are three passes over
   * the same set, and a second array is a set a signal can be missing from: the
   * cross-tab pass covered the nine dual settings alone, so a diff view or an
   * Enter-key mode changed next door did not follow, and neither did the two
   * preferences that live in a key of their own.
   */
  interface DeviceTierEntry {
    /**
     * The LOGICAL storage name a write to this signal changes. Most share the
     * consolidated document; two have a key of their own.
     */
    storageName: string
    /**
     * Re-read the signal. `null` means "no account" and takes the built-in
     * default -- an entry that reads its own key MUST honour it, because the
     * namespace still points at the account that left.
     */
    seed: (prefs: BrowserPreferences | null) => void
    /** Re-apply the resolved value, for a setting that paints something. */
    apply?: () => void
  }

  const deviceTier: DeviceTierEntry[] = []

  /** Re-read every device-tier signal from the account now in force. */
  const reseedBrowserTier = () => {
    const prefs = loadBrowserPrefs()
    for (const entry of deviceTier)
      entry.seed(prefs)
  }

  /**
   * Follow the device-tier writes another tab made under `stored`.
   *
   * `stored` is the key AS STORED, so the match carries the account: another
   * account's document changing next door says nothing about this one. A null
   * key is a whole-store clear, which every entry has to answer for.
   */
  const syncFromOtherTab = (stored: string | null) => {
    // No account, no document to read: every account-scoped read throws, and
    // nothing another tab wrote can belong to a page that has no identity.
    if (!hasStorageAccount())
      return
    const prefs = loadBrowserPrefs()
    for (const entry of deviceTier) {
      if (stored !== null && storedKeyFor(entry.storageName) !== stored)
        continue
      entry.seed(prefs)
      // The applier, WITHOUT the write that a `set` performs. Echoing the value
      // back to storage would raise a `storage` event in the tab that wrote it,
      // and the two tabs would write to each other for as long as both are open.
      entry.apply?.()
    }
  }

  /**
   * One browser-only boolean with a fixed default.
   *
   * The document stores only the value that DIFFERS from the default, so a
   * preference left alone costs no bytes and a changed default reaches
   * every browser that never touched it.
   */
  function createBrowserToggle(key: keyof BrowserPreferences, defaultOn: boolean) {
    const [value, setSignal] = createSignal(defaultOn)
    deviceTier.push({
      storageName: KEY_BROWSER_PREFS,
      seed: (prefs) => {
        const stored = prefs?.[key]
        setSignal(typeof stored === 'boolean' ? stored : defaultOn)
      },
    })
    const set = (next: boolean) => {
      setSignal(next)
      updateBrowserPref(key, next === defaultOn ? undefined : next)
    }
    return [value, set] as const
  }

  /**
   * One browser-only boolean that lives in its OWN localStorage key.
   *
   * Its sibling above holds the same two rules for a field of the
   * consolidated document, and the rules are what matter. Store only the
   * value that DIFFERS from the default, so the default at its sentinel is
   * a DELETED key: storing `true` for a default-on preference is an
   * override in the opposite direction, and it stops a changed default
   * from reaching the browsers that never touched it. Read the key back
   * through a type test, because the stored document is editable by hand
   * and survives a downgrade: an accessor that returned the string
   * `"true"` read as truthy everywhere except the toggle bound to it,
   * which compares against `true` and rendered OFF.
   */
  function createOwnKeyToggle(storageKey: string, defaultOn: boolean) {
    const [value, setSignal] = createSignal(defaultOn)
    deviceTier.push({
      storageName: storageKey,
      // Reads its OWN key rather than the consolidated document, so it ignores
      // the argument the other entries use -- except for the null, which means
      // there is no account whose key it may read.
      seed: (prefs) => {
        const stored = prefs === null ? undefined : localStorageGet<unknown>(storageKey)
        setSignal(typeof stored === 'boolean' ? stored : defaultOn)
      },
    })
    const set = (next: boolean) => {
      setSignal(next)
      if (next === defaultOn)
        localStorageRemove(storageKey)
      else
        localStorageSet(storageKey, next)
    }
    return [value, set] as const
  }

  // --- Browser-only preferences ---
  const [expandAgentThoughts, setExpandAgentThoughts] = createBrowserToggle('expandAgentThoughts', true)
  const [showHiddenMessages, setShowHiddenMessages] = createBrowserToggle('showHiddenMessages', false)
  const [revealAfterDownload, setRevealAfterDownload] = createBrowserToggle('revealAfterDownload', true)
  const [terminalOsNotifications, setTerminalOsNotifications] = createBrowserToggle('terminalOsNotifications', false)
  const [showComposerStatusBar, setShowComposerStatusBar] = createBrowserToggle('showComposerStatusBar', true)

  // A stored value is UNTRUSTED. `localStorageGet` casts rather than
  // checks, and the document it reads is editable by hand and outlives the
  // value set it was written against. An unrecognised mode reaches the
  // composer, whose menu compares against 'cmd-enter-sends' and renders
  // neither entry as checked, so the first click appears to do nothing.
  const [enterKeyMode, setEnterKeyModeSignal] = createSignal<EnterKeyMode>('cmd-enter-sends')
  deviceTier.push({
    storageName: KEY_BROWSER_PREFS,
    seed: prefs => setEnterKeyModeSignal(
      oneOf<EnterKeyMode>('enter-sends', 'cmd-enter-sends')(prefs?.enterKeyMode) ?? 'cmd-enter-sends',
    ),
  })
  const setEnterKeyMode = (value: EnterKeyMode | null) => {
    setEnterKeyModeSignal(value ?? 'cmd-enter-sends')
    updateBrowserPref('enterKeyMode', value ?? undefined)
  }

  // `~/lib/terminal` reads this same field with its own guard, so the
  // guard is imported rather than re-typed: two readers of one field that
  // disagree is the whole defect. A stored 'x' resolved to 'auto' for the
  // terminal and to 'x' for the settings row, whose enum control then held
  // a value absent from its three options.
  const [terminalRenderer, setTerminalRendererSignal] = createSignal<TerminalRendererPreference>('auto')
  deviceTier.push({
    storageName: KEY_BROWSER_PREFS,
    seed: prefs => setTerminalRendererSignal(getTerminalRendererPreference(prefs ?? {})),
  })
  const setTerminalRenderer = (value: TerminalRendererPreference | null) => {
    setTerminalRendererSignal(value ?? 'auto')
    updateBrowserPref('terminalRenderer', value ?? undefined)
  }

  const [preferredEditorId, setPreferredEditorIdSignal] = createSignal<string | undefined>(undefined)
  deviceTier.push({
    storageName: KEY_PREFERRED_EDITOR,
    seed: prefs => setPreferredEditorIdSignal(
      prefs === null ? undefined : localStorageGet<string>(KEY_PREFERRED_EDITOR),
    ),
  })
  const setPreferredEditorId = (id: string | undefined) => {
    setPreferredEditorIdSignal(id)
    if (id === undefined)
      localStorageRemove(KEY_PREFERRED_EDITOR)
    else
      localStorageSet(KEY_PREFERRED_EDITOR, id)
  }

  const [directoryPickerShowHidden, setDirectoryPickerShowHidden]
    = createOwnKeyToggle(KEY_DIRECTORY_SELECTOR_SHOW_HIDDEN, true)

  /**
   * The most recent account-settings load failure, or null.
   *
   * The surface that renders account rows has to state it, and only this
   * context can know. It fetches the reply once and hands it to every
   * reader, so a reader has no request of its own that could fail.
   */
  const [accountLoadError, setAccountLoadError] = createSignal<string | null>(null)

  /**
   * The hub's account-key schema, from the newest load that succeeded.
   *
   * A FAILED load leaves the previous schema in place rather than clearing
   * it. The keys the hub declares do not change while a page is open, so
   * dropping them on a transient failure would empty the dialog's account
   * rows for a reload that changes nothing.
   */
  const [accountDescriptors, setAccountDescriptors] = createSignal<SettingDescriptor[]>([])

  /**
   * The newest write issued per key.
   *
   * Every response applies the server's effective value onto the signal, so
   * two writes to one key that complete out of order would leave the OLDER
   * value on screen — two fast clicks on a toggle, and it snaps back. The
   * sequence is per key, not global: writes to different keys are
   * independent and must not cancel each other. Same guard the stores use
   * for `load`; the write path simply never had one.
   *
   * EVERY applier passes through it, the list reply included. A write is
   * not the only thing that can carry a stale value: `reload` reads every
   * key at once, so a list reply that lands after a faster write reply
   * would restore the pre-write value for the key the user just changed.
   */
  const writeSeq = createKeyedSeq()

  /**
   * The write REQUESTS in flight per key, so the next one for a key is
   * issued only after the previous one settles.
   *
   * The sequence above decides which ANSWER is applied. It cannot decide
   * which REQUEST the hub commits first, and `mutateUserPrefs` merges the
   * partial under a row lock, so the request that commits LAST is the one
   * the hub keeps. Two fast clicks on "+" in a font stack could leave the
   * hub holding the one-font document while the screen showed two.
   */
  const writeQueue = createKeyedQueue()

  /**
   * The newest LOAD, which is a separate rule from the write sequence.
   *
   * Two reloads with no write between them carry identical per-key write
   * stamps, so those stamps cannot separate them and both replies apply in
   * arrival order. This counter is unkeyed: a load reads the whole account
   * at once, so there is one subject to be newest for.
   */
  const loadSeq = createKeyedSeq()

  /**
   * Read every account key from the hub and apply the values the caller has
   * not written since the read was issued.
   *
   * The reply is stamped against the per-key write sequence taken BEFORE the
   * request: a key whose sequence moved while the list was in flight has a
   * newer answer already on screen, so this reply is stale for that key
   * alone. The other keys still apply, because the list is the only source
   * for a key the user never touched.
   */
  const reload = async () => {
    const mySeq = loadSeq.next()
    const issuedAt = writeSeq.snapshot()
    let resp
    try {
      resp = await userClient.listUserSettings({})
    }
    catch (err) {
      // RECORD, then rethrow. The failure has to be recorded HERE, in the
      // function that owns the invariant, so `accountLoadError` is true for
      // every caller of the exported `reload` — not only for the one call
      // site that remembers to catch it. Only while this load is still the
      // newest, though: a later load that already succeeded cleared the
      // record, and nothing on the page clears it a second time.
      if (loadSeq.isNewest(undefined, mySeq))
        setAccountLoadError(formatErrorMessage(err, 'Failed to load account settings'))
      throw err
    }
    if (!loadSeq.isNewest(undefined, mySeq))
      return
    setAccountLoadError(null)
    // The SCHEMA, kept beside the values it describes. The settings
    // registry joins its presentation onto these descriptors instead of
    // restating each key's category, control kind, enum values and bounds,
    // so discarding them here left the dialog with nothing to render an
    // account row from.
    setAccountDescriptors(resp.descriptors)
    for (const value of resp.values) {
      if (!writeSeq.isNewest(value.key, issuedAt.get(value.key) ?? 0))
        continue
      // applyAccountValue records the customized flag for the key it
      // applies, so a skipped key keeps the flag its own newer reply set.
      applyAccountValue(value.key, value)
    }
  }

  /**
   * Server-first per-key write. On success the server's effective value
   * (the source of truth after validation and defaults) replaces the
   * optimistic one.
   *
   * REJECTS on failure. The row that made the write is the one place the
   * user is looking, and `SettingRow` already renders a rejected `set`
   * inline; swallowing the rejection left the control reverting with no
   * stated reason and only a toast that had already gone.
   */
  const updateUserSetting = async (key: string, partial: unknown): Promise<void> => {
    // Both the document and the sequence are fixed at ISSUE time, before
    // the request waits its turn: the sequence so it matches the order the
    // user clicked in, and the JSON so the queued request sends what the
    // caller asked for rather than whatever `partial` holds by then.
    const partialJson = JSON.stringify(partial)
    const seq = writeSeq.next(key)
    try {
      const resp = await writeQueue.run(key, () => userClient.updateUserSetting({ key, partialJson }))
      // A superseded response is still a SUCCESS — it just no longer
      // describes what the user last asked for, so it must not be applied.
      if (resp.value && writeSeq.isNewest(key, seq))
        applyAccountValue(key, resp.value)
    }
    catch (err) {
      // LOG, never toast. The rejection travels to the row that made the
      // write and `SettingRow` renders it under the control, so a toast put
      // the same text on screen twice — and it fired before the superseded
      // flag was even computed, so a superseded write that this path and
      // `writeAccount` both go out of their way to suppress toasted anyway.
      log.warn('account setting write failed', { key }, err)
      throw new SupersededAwareError(formatErrorMessage(err, 'Failed to save setting'), writeSeq.isNewest(key, seq))
    }
  }

  const resetUserSetting = async (key: string): Promise<void> => {
    const seq = writeSeq.next(key)
    try {
      const resp = await writeQueue.run(key, () => userClient.resetUserSetting({ key }))
      if (resp.value && writeSeq.isNewest(key, seq))
        applyAccountValue(key, resp.value)
    }
    catch (err) {
      log.warn('account setting reset failed', { key }, err)
      throw new SupersededAwareError(formatErrorMessage(err, 'Failed to reset setting'), writeSeq.isNewest(key, seq))
    }
  }

  /**
   * One account write: apply the value optimistically (the control should
   * feel local), hand it to the server, and restore the pre-write value if
   * the server refuses. The rejection travels on so the row can state it.
   */
  function writeAccount<T>(
    key: string,
    value: T,
    read: () => T,
    write: (v: T) => void,
  ): Promise<void> {
    const prev = read()
    write(value)
    return updateUserSetting(key, value).catch((err: unknown) => {
      // Roll back only while this write is still the newest for its key.
      // `prev` was captured before this write; a newer write has since
      // replaced it, and restoring `prev` would revert the user's LATER
      // selection out from under its own in-flight request.
      if (!(err instanceof SupersededAwareError) || err.newest)
        write(prev)
      throw err
    })
  }

  function createAccountSetting<T>(opts: AccountSettingOptions<T>): AccountSetting<T> {
    const [account, setSignal] = createSignal<T>(opts.fallback)
    // The functional form, so a value that is itself callable would be
    // stored rather than invoked as an updater.
    const write = (value: T) => setSignal(() => value)
    return {
      protoKey: opts.protoKey,
      account,
      setAccount: (value: T) => writeAccount(opts.protoKey, value, account, write),
      customized: () => accountCustomized()[opts.protoKey] === true,
      reset: () => resetUserSetting(opts.protoKey),
      applyServer: (raw: unknown) => {
        const parsed = opts.parse(raw)
        if (parsed === undefined)
          return false
        write(parsed)
        return true
      },
      clear: () => write(opts.fallback),
    }
  }

  function createDualSetting<T extends BrowserPrefValue>(opts: DualSettingOptions<T>): DualSetting<T> {
    const base = createAccountSetting<T>(opts)
    // `null` means "this device has no opinion, use the account value" -- the
    // same thing an absent field means on disk. Starting here rather than at a
    // parsed default is what lets the account tier win before any device
    // override is read.
    // A VALUE comparator, not the default reference one. Every device-tier
    // parse builds a fresh object, so an unchanged field still reads as a new
    // value: one unrelated preference written in another tab would notify all
    // nine dual settings and repaint the whole palette. The values all come
    // from JSON and each parse builds a fixed key order, so serializing is an
    // exact comparison here.
    const [browser, setSignal] = createSignal<T | null>(null, {
      equals: (a, b) => a === b || JSON.stringify(a) === JSON.stringify(b),
    })
    const resolved = () => browser() ?? base.account()
    deviceTier.push({
      storageName: KEY_BROWSER_PREFS,
      // The stored browser value passes the SAME parse as a server document,
      // so a corrupt localStorage entry cannot put a value on screen that the
      // hub would refuse.
      seed: prefs => setSignal(() => (prefs === null ? null : opts.parse(prefs[opts.browserPrefKey]) ?? null)),
      // Runs from the `storage` handler and the sign-out reset, never a tracked
      // scope. It re-applies the value WITHOUT the write that `setBrowser`
      // performs: echoing the value back to storage would raise a `storage`
      // event in the tab that wrote it, and the two tabs would write to each
      // other for as long as both are open.
      apply: () => opts.onBrowserWrite?.(resolved()),
    })
    const setBrowser = (value: T | null) => {
      setSignal(() => value)
      updateBrowserPref(opts.browserPrefKey, value ?? undefined)
      opts.onBrowserWrite?.(resolved())
    }
    return { ...base, browser, setBrowser, resolved }
  }

  /**
   * Every account-backed setting that ALSO has a browser tier, declared
   * once.
   *
   * The dialog, the resolution order, the browser override, and the server
   * dispatch all read this one record, so a new dual key is one entry here
   * and nothing else. An account-only key is declared beneath it.
   */
  const dualSettings = {
    // The palette AND its light/dark mode, as one value: they are one
    // appearance choice, so they share one scope chip and one Reset. Splitting
    // them into two keys would let a device override the palette while the
    // account still decides the mode, which no control in the app can express.
    theme: createDualSetting<ThemeValue>({
      protoKey: 'theme',
      browserPrefKey: 'theme',
      fallback: DEFAULT_THEME_VALUE,
      parse: parseThemeValue,
      // ~/lib/themeStore owns the live palette, so tell it at once rather than
      // wait for the next render pass.
      onBrowserWrite: applyTheme,
    }),
    // The terminal's palette AND mode, which move together: `match-ui` fills
    // both halves or neither. One key for the same reason `theme` is one key:
    // it is one appearance choice with one scope chip, and the control states
    // it as one entry in one palette list. See ~/styles/themes/types.ts.
    terminalTheme: createDualSetting<TerminalThemeValue>({
      protoKey: 'terminal_theme',
      browserPrefKey: 'terminalTheme',
      fallback: DEFAULT_TERMINAL_THEME_VALUE,
      parse: parseTerminalThemeValue,
    }),
    // Highlighted code, the third appearance surface. Same shape and same
    // sentinel as the terminal: it is a different surface with different
    // habits, and following the app is only the default.
    syntaxTheme: createDualSetting<TerminalThemeValue>({
      protoKey: 'syntax_theme',
      browserPrefKey: 'syntaxTheme',
      fallback: DEFAULT_TERMINAL_THEME_VALUE,
      parse: parseTerminalThemeValue,
    }),
    // The font overrides are whole-object tiers: null means "use the
    // account value" and deletes the key; any object is a full override.
    uiFonts: createDualSetting<FontTier>({
      protoKey: 'ui_fonts',
      browserPrefKey: 'uiFontOverride',
      fallback: { enabled: false, fonts: [] },
      parse: parseFontTier,
    }),
    monoFonts: createDualSetting<FontTier>({
      protoKey: 'mono_fonts',
      browserPrefKey: 'monoFontOverride',
      fallback: { enabled: false, fonts: [] },
      parse: parseFontTier,
    }),
    diffView: createDualSetting<DiffViewPreference>({
      protoKey: 'diff_view',
      browserPrefKey: 'diffView',
      fallback: 'unified',
      parse: oneOf('unified', 'split'),
    }),
    turnEndSound: createDualSetting<TurnEndSoundPreference>({
      protoKey: 'turn_end_sound',
      browserPrefKey: 'turnEndSound',
      fallback: 'ding-dong',
      parse: oneOf('none', 'ding-dong'),
    }),
    turnEndSoundVolume: createDualSetting<number>({
      protoKey: 'turn_end_sound_volume',
      browserPrefKey: 'turnEndSoundVolume',
      fallback: 100,
      // The hub refuses `v < 0 || v > 100` (usersettings/keys.go), and the
      // consumer needs the same bound for its own reason: `useTurnEnd`
      // assigns `volume / 100` to an HTMLAudioElement, and a value outside
      // 0..1 throws IndexSizeError SYNCHRONOUSLY, after the rate limiter
      // already recorded the play. No sound, and no turn-end event.
      // `Number.isFinite` is required, because `typeof NaN === 'number'`.
      parse: raw => (typeof raw === 'number' && Number.isFinite(raw) && raw >= 0 && raw <= 100 ? raw : undefined),
    }),
    debugLogging: createDualSetting<boolean>({
      protoKey: 'debug_logging',
      browserPrefKey: 'debugLogging',
      fallback: false,
      parse: raw => (typeof raw === 'boolean' ? raw : undefined),
    }),
  }

  // Keybindings live at the account tier only: an override that follows
  // the person is the whole point, so there is no browser half. Declared
  // BESIDE the record rather than inside it, because `dualSettings` is
  // what the context publishes as `dual`, and every member of that record
  // must carry the browser tier its consumers address.
  const keybindings = createAccountSetting<UserKeybindingOverride[]>({
    protoKey: 'keybindings',
    fallback: [],
    // A malformed document is an EMPTY override set, not "keep what is
    // on screen": a stored value the client cannot read must not leave
    // stale bindings in force.
    parse: raw => (Array.isArray(raw) ? raw as UserKeybindingOverride[] : []),
  })

  const settingsByProtoKey = new Map(
    [...Object.values(dualSettings), keybindings].map(setting => [setting.protoKey, setting] as const),
  )

  /**
   * Apply one SettingValue's effective JSON onto its account signal.
   *
   * A key that no setting declares is reported, never dropped in silence:
   * the hub declares the account keys, so an unlisted one means this file
   * fell behind `usersettings/keys.go`, and the row would otherwise show a
   * "Customized" badge over a value no signal holds.
   *
   * Declared as a function, not a const, so that hoisting resolves a loop:
   * the write path above calls this, and the settings that this reads are
   * themselves built on that write path.
   */
  function applyAccountValue(key: string, value: SettingValue) {
    const setting = settingsByProtoKey.get(key)
    if (!setting) {
      log.warn('account setting has no local signal; ignoring', { key })
      return
    }
    // The flag follows the APPLY. A document the parse refuses leaves the
    // signal alone, so recording the key as customized would put a badge
    // and a Reset button over a stored value that no row is showing.
    if (!setting.applyServer(parseSettingJson(value.effectiveJson))) {
      log.warn('account setting value refused by the local parse; leaving the customized flag alone', { key })
      return
    }
    setAccountCustomized(prev => ({ ...prev, [key]: value.customized }))
  }

  // --- Resolved values (browser override → account default → hardcoded) ---
  const uiFonts = dualSettings.uiFonts.resolved
  const monoFonts = dualSettings.monoFonts.resolved

  const monoFontFamily = () => {
    const tier = monoFonts()
    if (!tier.enabled || tier.fonts.length === 0) {
      return DEFAULT_MONO_FONT_FAMILY
    }
    return `${buildFontFamily(tier.fonts)}, ${DEFAULT_MONO_FONT_FAMILY}`
  }

  const uiFontFamily = () => {
    const tier = uiFonts()
    if (!tier.enabled || tier.fonts.length === 0) {
      return undefined
    }
    return buildFontFamily(tier.fonts)
  }

  /**
   * Return every tier to its built-in default. See `resetForSignOut`.
   *
   * Both tiers, because both hold the departing account's values: the device
   * tier from its stored document, the account tier from its hub reply. The
   * account half calls `clear` rather than `reset` -- there is no session left
   * to ask the hub to remove anything.
   *
   * THE ACCOUNT TIER GOES FIRST. A device entry's applier paints
   * `browser() ?? account()`, so applying while the account tier still holds the
   * departing values would paint those values -- corrected on the next flush by
   * `PreferencesApplier`, but a repaint to the wrong palette and back all the
   * same.
   */
  const resetForSignOut = () => {
    for (const setting of settingsByProtoKey.values())
      setting.clear()
    setAccountCustomized({})
    for (const entry of deviceTier) {
      entry.seed(null)
      entry.apply?.()
    }
  }
  // `accountLoadError` and `accountDescriptors` deliberately survive. Neither
  // is a VALUE of the account that left: the error is the record of the load
  // that failed, which the preferences dialog renders and retries, and the
  // descriptors are the hub's schema, which does not change with the identity.
  // The mount load of a page nobody is signed in to answers Unauthenticated,
  // and clearing its record here would erase the one thing that reports it.

  // Seed from the account in force, and re-seed each time it moves.
  //
  // The subscription is what makes an unseeded device tier impossible: it fires
  // from inside `setStorageAccount`, which `AuthContext` calls SYNCHRONOUSLY as
  // it writes the identity, so the signals are correct before the render effect
  // that mounts the authenticated tree can read them. An effect-driven seed runs
  // after that render, and every consumer that reads once at mount would take
  // the built-in default.
  //
  // The eager call covers the account that resolved BEFORE this provider
  // mounted: the error boundary's reset remounts this subtree, and component
  // tests mount it directly, where waiting for a move that will never come
  // would leave every device-tier signal at its default.
  //
  // Placed after every signal is created, because that is what fills
  // `deviceTier`.
  if (hasStorageAccount())
    reseedBrowserTier()
  onCleanup(onStorageAccountChange(reseedBrowserTier))

  createEffect(() => {
    setDebugEnabled(dualSettings.debugLogging.resolved())
  })

  // Follow a device-tier write made in another tab.
  //
  // The event says only WHICH key changed; the value is read back through
  // `loadBrowserPrefs`, so the reader unwraps the `{ v, e }` TTL envelope
  // exactly as every other read does. `event.newValue` carries the raw envelope,
  // so a field read straight off it is always `undefined`.
  //
  // It matches the key AS STORED, which carries the account: another account's
  // document changing in another tab says nothing about this one. Before an
  // identity resolves `storedKeyFor` answers null, which matches no event -- the
  // right answer, and the reason it does not throw here.
  //
  // A null `event.key` is a whole-store `clear()` next door, and it names no
  // key, so every entry answers for it. Dropping it left the signals showing
  // values whose document was gone, and the next write in this tab merged onto
  // an empty one and silently discarded them.
  onMount(() => {
    const onStorage = (event: StorageEvent) => syncFromOtherTab(event.key)
    window.addEventListener('storage', onStorage)
    onCleanup(() => window.removeEventListener('storage', onStorage))
  })

  onMount(() => {
    reload().catch((err) => {
      // The provider mounts before the session is guaranteed, so a failed
      // load here is ORDINARY: it keeps the defaults and the next reload()
      // converges. Debug level, not warn — an expected outcome on the mount
      // path must not write to the console on every unauthenticated page
      // view.
      //
      // `reload` already recorded it in `accountLoadError`, and the record is
      // what the dialog reads: the store there reads this already-fetched
      // reply rather than asking again, so it never sees an error of its
      // own. Every account setting keeps its built-in default until a load
      // succeeds, and `accountDescriptors` stays empty, so the dialog builds
      // no account ROW either and says why where the rows would have been.
      //
      // This is the only load this provider issues on its own. Two callers
      // ask again, and each owns a different failure:
      // `usePreferencesForIdentity` seeds the device tier and reloads the
      // account tier for every resolved identity, which covers both the
      // ordinary page load and the user who signs in through the form after
      // this attempt answered Unauthenticated; `PreferencesDialog` retries on
      // open, which covers a failure with no identity change behind it (an
      // unreachable hub, a timeout, a 500).
      log.debug('account settings load failed; using defaults until the next reload', err)
    })
  })

  return (
    <PreferencesContext.Provider value={{
      // The RESOLVED value of each dual preference, which is what the app
      // at large reads. Its tiers travel together under `dual` below, so a
      // new key adds one entry there rather than one line per tier here.
      theme: dualSettings.theme.resolved,
      terminalTheme: dualSettings.terminalTheme.resolved,
      syntaxTheme: dualSettings.syntaxTheme.resolved,
      uiFonts,
      monoFonts,
      diffView: dualSettings.diffView.resolved,
      turnEndSound: dualSettings.turnEndSound.resolved,
      turnEndSoundVolume: dualSettings.turnEndSoundVolume.resolved,
      debugLogging: dualSettings.debugLogging.resolved,

      // Derived from the same record the settings are built from, so a
      // tier this literal could forget to wire cannot exist.
      dual: dualSettings,

      customKeybindings: keybindings.account,
      setCustomKeybindings: keybindings.setAccount,

      monoFontFamily,
      uiFontFamily,

      expandAgentThoughts,
      setExpandAgentThoughts,
      showHiddenMessages,
      setShowHiddenMessages,
      revealAfterDownload,
      setRevealAfterDownload,
      enterKeyMode,
      setEnterKeyMode,
      terminalRenderer,
      setTerminalRenderer,
      preferredEditorId,
      setPreferredEditorId,
      directoryPickerShowHidden,
      setDirectoryPickerShowHidden,
      terminalOsNotifications,
      setTerminalOsNotifications,
      showComposerStatusBar,
      setShowComposerStatusBar,

      accountCustomized,
      accountDescriptors,
      accountLoadError,
      updateUserSetting,
      resetUserSetting,
      reload,
      resetForSignOut,
      batchBrowserPrefWrites,
    }}
    >
      {props.children}
    </PreferencesContext.Provider>
  )
}

/**
 * The preferences state. Throws outside a `PreferencesProvider`.
 *
 * There is deliberately no optional variant. One existed for a control that
 * rendered on both sides of the provider boundary -- the desktop launcher's
 * theme picker -- and that control is gone: every stored preference is scoped
 * to an account, so a surface rendering before the identity resolves has no
 * preferences to show. Outside the provider is now a bug, and it fails loudly.
 */
export function usePreferences(): PreferencesState {
  const ctx = useContext(PreferencesContext)
  if (!ctx) {
    throw new Error('usePreferences must be used within PreferencesProvider')
  }
  return ctx
}
