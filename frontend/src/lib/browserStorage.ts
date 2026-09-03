/**
 * Centralized browser storage management.
 *
 * Callers pass a LOGICAL key name (`'key-pins'`, `'worker-info:w-1'`). This
 * module owns the physical layout and composes the whole stored key, so no call
 * site can build one by hand and no constant can drift from what is actually
 * stored.
 *
 * EVERY KEY IS SCOPED TO ONE ACCOUNT, because two accounts sharing a browser
 * profile must not share stored state. The scope is part of the stored key:
 *
 *   - `account` -> `leapmux:u:<userId>:<name>`, and an access with no account
 *     set THROWS. There is no silent fallback: a value written before the
 *     identity resolves has no correct owner, and guessing one is how one
 *     user's preferences end up on another user's screen.
 *   - `device` -> `leapmux:<name>`, for the two relay sequence marks that fence
 *     a process-wide sidecar and therefore CANNOT be partitioned. See
 *     `LOCAL_KEY_SPECS`.
 *
 * Every key is registered in `LOCAL_KEY_SPECS` or `SESSION_KEY_SPECS` with its
 * match type, its scope and its TTL. An unregistered key throws, so a missed
 * registration fails loudly instead of disappearing on the next sweep.
 *
 * Every value is wrapped as `{ v: T, e: number }` with an expiration
 * timestamp; reads unwrap and may refresh the timestamp on access.
 * Long-lived preferences use a 1-year TTL plus the refresh-on-read
 * mechanism, so opening the app at any point in a year keeps them
 * alive; total inactivity for a year is the only way they expire.
 *
 * `runCleanup` sweeps both stores on a timer, deleting any `leapmux:`-family
 * key that is unregistered, that carries a scope its registration does not
 * allow, or whose wrapper is missing/malformed/expired. It keeps OTHER
 * accounts' keys, which is the whole point of scoping them.
 *
 * A module that MIRRORS an account-scoped key in memory subscribes to
 * `onStorageAccountChange`, so the mirror moves with the namespace instead of
 * serving the previous account's copy.
 */
import { createLogger } from './logger'

const log = createLogger('browserStorage')

// ---------------------------------------------------------------------------
// Types
// ---------------------------------------------------------------------------

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

// ---------------------------------------------------------------------------
// Key registry
// ---------------------------------------------------------------------------

// Logical key names. They carry NO `leapmux:` prefix and no account segment:
// this module composes the stored key (see `storedKeyFor`). A constant that
// spelled out the stored form would be a second, drifting statement of a layout
// only this module can apply -- and the account segment is not knowable here.

/** Long-lived localStorage singletons (exact-match in the key registry). */
export const KEY_BROWSER_PREFS = 'browser-prefs'
export const KEY_MRU_AGENT_PROVIDERS = 'mru-agent-providers'
export const KEY_KEY_PINS = 'key-pins'
export const KEY_DIRECTORY_SELECTOR_SHOW_HIDDEN = 'directory-selector-show-hidden'
export const KEY_PREFERRED_EDITOR = 'preferred-editor'
export const KEY_ACTIVE_WORKSPACE = 'active-workspace'
export const KEY_USER_EVENTS_RELAY_SEQ = 'user-events-relay-seq'
export const KEY_CHANNEL_RELAY_SEQ = 'channel-relay-seq'

/** Dynamic key prefixes — single source of truth for all consumers. */
export const PREFIX_EDITOR_DRAFT = 'editor-draft:'
export const PREFIX_EDITOR_MIN_HEIGHT = 'editor-min-height:'
export const PREFIX_AGENT_SESSION = 'agent-session:'
export const PREFIX_ASK_STATE = 'ask-state:'
export const PREFIX_WORKER_INFO = 'worker-info:'
export const PREFIX_LOCAL_MESSAGES = 'local-messages:'
export const PREFIX_FILES_SHOW_HIDDEN = 'files-show-hidden:'
export const PREFIX_FILES_SORT_ORDER = 'files-sort-order:'
export const PREFIX_WORKSPACE_GIT_MODE = 'workspace-git-mode:'
export const PREFIX_CHAT_ROW_HEIGHTS = 'chat-row-heights:'

/** sessionStorage dynamic key prefixes. */
export const PREFIX_FILE_SCROLL = 'fileScroll:'
export const PREFIX_ACTIVE_TAB = 'activeTab:'
export const PREFIX_TILE_ACTIVE_TABS = 'tileActiveTabs:'
export const PREFIX_FOCUSED_TILE = 'focusedTile:'
export const PREFIX_SIDEBAR = 'sidebar:'
export const PREFIX_TAB_TREE = 'tabTree:'
export const PREFIX_DIRECTORY_TREE = 'directoryTree:'
/** Singleton sessionStorage keys (exact-match in the key registry). */
export const KEY_CLI_PATH_CHECKED = 'cli-path-checked'

/**
 * The address a user typed into the account email field but has not sent yet.
 *
 * It exists for ONE journey. An account with no password and no passkey can
 * only elevate at its identity provider, and that option is a full-document
 * navigation out of the app and back. Without this the user typed the new
 * address, was asked to verify, came back to an empty field, and had to type
 * it again -- on the one account shape that has no other way to verify.
 *
 * sessionStorage, not localStorage: an unsent address is the tab's business,
 * and it must not reappear in a window the user opens tomorrow. The TTL is a
 * backstop for a tab that survives the round trip and is then abandoned; the
 * field clears itself on a successful send, which is the ordinary end.
 */
export const KEY_EMAIL_CHANGE_DRAFT = 'email-change-draft'

export const KEY_EXPANDED_WORKSPACES = 'expandedWorkspaces'
export const KEY_CLIENT_ID = 'client-id'
/** Per-tab MRU stamp map (`Record<tabId, number>`), single blob. See tabMetadata.store. */
export const KEY_TAB_MRU = 'tab-mru'

const DAY_MS = 24 * 60 * 60 * 1000
const HOUR_MS = 60 * 60 * 1000
const MINUTE_MS = 60 * 1000
const REFRESH_THRESHOLD_MS = 3 * HOUR_MS
const CLEANUP_INTERVAL_MS = HOUR_MS
const YEAR_MS = 365 * DAY_MS
/** How long the first sweep waits for an idle window before it runs anyway. */
const IDLE_SWEEP_TIMEOUT_MS = 5000

/**
 * Which account namespace a key is stored under.
 *
 * `account` is the answer for anything a user can be said to own. `device` is
 * the narrow exception: state that fences a resource SHARED by every account on
 * the origin, and which therefore breaks when it is partitioned.
 */
export type StorageScope = 'account' | 'device'

/**
 * One key's registration: how its name is matched, whose namespace it lives in,
 * and how long a value survives without being touched.
 *
 * `scope` is deliberately not optional. `satisfies Record<string, KeySpec>`
 * below turns a new key that omits it into a COMPILE error, so the account
 * question is answered when the key is added rather than discovered later by
 * whoever finds their preferences on someone else's screen.
 */
export interface KeySpec {
  /**
   * `exact` matches the whole logical name; `prefix` matches any name that
   * starts with it.
   *
   * Kept as a field rather than as two tables so scope and TTL sit beside it,
   * but it carries the same rule the two tables did: a singleton must never
   * inherit a TTL because some prefix happens to be its leading substring, so
   * the exact entries are consulted first and never prefix-match.
   */
  readonly match: 'exact' | 'prefix'
  readonly scope: StorageScope
  readonly ttlMs: number
}

/**
 * Every localStorage key, by logical name.
 *
 * The `account` entries are the ordinary case and need no argument: a
 * preference, a cache or a piece of trust state belongs to the user who made
 * it, and a second account on this browser must neither read it nor overwrite
 * it.
 *
 * The two `device` entries are the exception, and it is a narrow one. Both are
 * high-water marks for relay ids that the Go sidecar compares through a
 * strictly-greater owner fence, and that sidecar is PROCESS-WIDE: it outlives a
 * webview reload and it serves every account on the origin. `persistedSeq`
 * spells out the failure -- the mark is the only shared state, so two
 * independent counters mint ids that both pass the fence, and one process's
 * close then tears down a relay another process already adopted, wedging the
 * channel until an app restart. Partitioning them per account rebuilds exactly
 * that. They hold no user data: an id sequence says nothing about who is signed
 * in.
 */
export const LOCAL_KEY_SPECS = {
  // User-level preferences and trust state -- values that should outlive
  // ordinary idle gaps but still self-clean if the app goes unopened for a
  // year. The on-read refresh in `readDynamic` pushes the expiration forward on
  // every access, so a user who opens the app at any point during the year
  // keeps these forever; a year of total inactivity expires them.
  [KEY_BROWSER_PREFS]: { match: 'exact', scope: 'account', ttlMs: YEAR_MS },
  [KEY_MRU_AGENT_PROVIDERS]: { match: 'exact', scope: 'account', ttlMs: YEAR_MS },
  [KEY_KEY_PINS]: { match: 'exact', scope: 'account', ttlMs: YEAR_MS },
  [KEY_DIRECTORY_SELECTOR_SHOW_HIDDEN]: { match: 'exact', scope: 'account', ttlMs: YEAR_MS },
  [KEY_PREFERRED_EDITOR]: { match: 'exact', scope: 'account', ttlMs: YEAR_MS },
  // The workspace this account was last on. A year rather than the days its
  // templated table-mates get: this is a preference, not a cache -- it is the
  // only record of where the app should reopen, since the URL no longer carries
  // the workspace id.
  [KEY_ACTIVE_WORKSPACE]: { match: 'exact', scope: 'account', ttlMs: YEAR_MS },
  // High-water mark for the desktop userevents relay ids (see useUserEvents).
  // Device-scoped: see the note above the table.
  [KEY_USER_EVENTS_RELAY_SEQ]: { match: 'exact', scope: 'device', ttlMs: YEAR_MS },
  // High-water mark for the desktop channel relay ids (see relayClaim). Same
  // reason, same sidecar fence.
  [KEY_CHANNEL_RELAY_SEQ]: { match: 'exact', scope: 'device', ttlMs: YEAR_MS },

  [PREFIX_EDITOR_DRAFT]: { match: 'prefix', scope: 'account', ttlMs: 7 * DAY_MS },
  [PREFIX_EDITOR_MIN_HEIGHT]: { match: 'prefix', scope: 'account', ttlMs: 7 * DAY_MS },
  [PREFIX_AGENT_SESSION]: { match: 'prefix', scope: 'account', ttlMs: 7 * DAY_MS },
  [PREFIX_ASK_STATE]: { match: 'prefix', scope: 'account', ttlMs: 1 * DAY_MS },
  [PREFIX_WORKER_INFO]: { match: 'prefix', scope: 'account', ttlMs: 7 * DAY_MS },
  [PREFIX_LOCAL_MESSAGES]: { match: 'prefix', scope: 'account', ttlMs: 7 * DAY_MS },
  [PREFIX_FILES_SHOW_HIDDEN]: { match: 'prefix', scope: 'account', ttlMs: 7 * DAY_MS },
  [PREFIX_FILES_SORT_ORDER]: { match: 'prefix', scope: 'account', ttlMs: 7 * DAY_MS },
  // The git mode a repository was last started with, keyed by
  // `<workerId>:<gitToplevel>` (see `gitModeStickyKey`). The TTL is the only
  // thing limiting growth -- there is one entry per repository the user ever
  // starts a workspace in -- and `readDynamic` refreshes it on read, so a
  // repository in weekly use never expires while an abandoned one does.
  [PREFIX_WORKSPACE_GIT_MODE]: { match: 'prefix', scope: 'account', ttlMs: 7 * DAY_MS },
  // Measured chat-row heights (see chatRowHeightPersistence). A warm-start
  // cache: stale entries are harmless (each row's key digest must match its
  // live heightKey to hydrate), so the TTL only limits storage growth.
  [PREFIX_CHAT_ROW_HEIGHTS]: { match: 'prefix', scope: 'account', ttlMs: 7 * DAY_MS },
} as const satisfies Record<string, KeySpec>

/**
 * Every sessionStorage key, by logical name.
 *
 * sessionStorage normally clears on tab close, but PWAs and "restore tabs on
 * restart" can keep it alive across sessions — capping retention limits the key
 * set without depending on tab-close cleanup.
 *
 * Per-workspace UI state (active tab, tile active tabs, focused tile, sidebar
 * layout, tab-tree group collapse, directory-tree expansion) is restored by
 * `restoreTabSelection` on page refresh. Without registration the on-load sweep
 * wipes these and the restore path falls back to "activate the first tab" /
 * "the first workspace". 30 days lets a user return after a long break and
 * still land on their last tab.
 *
 * Every entry is `account`. A tab outlives a sign-out, so a second account
 * signing in to the same tab would otherwise inherit the first account's tab
 * pointers, sidebar layout and CRDT client identity.
 */
export const SESSION_KEY_SPECS = {
  // The set of expanded workspaces in the sidebar tree. Matches the 30-day
  // lifetime of the per-workspace UI snapshot. Its sibling "which workspace is
  // active" is deliberately NOT here: that one has to survive a tab close, so
  // it lives in localStorage under `KEY_ACTIVE_WORKSPACE`.
  [KEY_EXPANDED_WORKSPACES]: { match: 'exact', scope: 'account', ttlMs: 30 * DAY_MS },
  // Per-session CRDT client identity. Long-lived so a refresh keeps the same
  // id; the TTL limits retention if the tab survives for weeks without being
  // closed. Account-scoped because `checkpointStore` keys its records by
  // `[userId, clientId]` -- a second account resuming the first account's
  // client id would claim checkpoints that are not its own.
  [KEY_CLIENT_ID]: { match: 'exact', scope: 'account', ttlMs: 30 * DAY_MS },
  // One-shot gate for the macOS "install leapmux on PATH" prompt. At most once
  // per session; the TTL is a backstop in case sessionStorage is preserved
  // across sessions.
  [KEY_CLI_PATH_CHECKED]: { match: 'exact', scope: 'account', ttlMs: 1 * DAY_MS },
  // An unsent email address, kept only long enough to survive the OAuth
  // round trip that the elevation prompt sends this account shape on. Half an
  // hour covers a provider that asks the user to sign in again; anything the
  // user still has not sent by then, they are no longer in the middle of.
  [KEY_EMAIL_CHANGE_DRAFT]: { match: 'exact', scope: 'account', ttlMs: 30 * MINUTE_MS },
  // Per-tab MRU stamp map. A single JSON blob keyed by globally-unique tab id
  // (no workspace dimension, so it is an exact singleton rather than a templated
  // prefix family). 30 days matches the sibling tab-pointer keys so a user who
  // returns after a long break still lands on the tab they touched last.
  [KEY_TAB_MRU]: { match: 'exact', scope: 'account', ttlMs: 30 * DAY_MS },

  [PREFIX_FILE_SCROLL]: { match: 'prefix', scope: 'account', ttlMs: 1 * DAY_MS },
  [PREFIX_ACTIVE_TAB]: { match: 'prefix', scope: 'account', ttlMs: 30 * DAY_MS },
  [PREFIX_TILE_ACTIVE_TABS]: { match: 'prefix', scope: 'account', ttlMs: 30 * DAY_MS },
  [PREFIX_FOCUSED_TILE]: { match: 'prefix', scope: 'account', ttlMs: 30 * DAY_MS },
  [PREFIX_SIDEBAR]: { match: 'prefix', scope: 'account', ttlMs: 30 * DAY_MS },
  [PREFIX_TAB_TREE]: { match: 'prefix', scope: 'account', ttlMs: 30 * DAY_MS },
  [PREFIX_DIRECTORY_TREE]: { match: 'prefix', scope: 'account', ttlMs: 30 * DAY_MS },
} as const satisfies Record<string, KeySpec>

// ---------------------------------------------------------------------------
// Key helpers
// ---------------------------------------------------------------------------

/** The namespace every stored key carries, and the marker for the scoped family. */
const NAMESPACE = 'leapmux:'
const ACCOUNT_SEGMENT = 'u:'

/**
 * The namespace of a build that predates the key registry.
 *
 * Nothing writes it. The sweep still recognises it, because a browser that ran
 * such a build holds keys that no other code path will ever name, and therefore
 * never deletes.
 */
const RETIRED_NAMESPACE = 'leapmux-'

/**
 * Split each table once, so a lookup is a Map hit plus a short prefix scan.
 *
 * The index carries the two names an error message needs -- the store it
 * describes and the table to register a key in. They are properties of the
 * table, so an index and its labels cannot be paired incorrectly.
 */
function indexSpecs(specs: Record<string, KeySpec>, store: string, table: string) {
  const exact = new Map<string, KeySpec>()
  const prefixes: Array<{ name: string, spec: KeySpec }> = []
  for (const [name, spec] of Object.entries(specs)) {
    if (spec.match === 'exact')
      exact.set(name, spec)
    else
      prefixes.push({ name, spec })
  }
  return { exact, prefixes, store, table }
}

const LOCAL_INDEX = indexSpecs(LOCAL_KEY_SPECS, 'localStorage', 'LOCAL_KEY_SPECS')
const SESSION_INDEX = indexSpecs(SESSION_KEY_SPECS, 'sessionStorage', 'SESSION_KEY_SPECS')

type SpecIndex = ReturnType<typeof indexSpecs>

/**
 * The registration for a logical name, or null when nothing registers it.
 *
 * Exact entries are consulted FIRST and never prefix-match, so a singleton
 * cannot inherit a TTL because some prefix happens to be its leading substring.
 */
function specFor(name: string, index: SpecIndex): KeySpec | null {
  const exact = index.exact.get(name)
  if (exact !== undefined)
    return exact
  for (const { name: prefix, spec } of index.prefixes) {
    if (name.startsWith(prefix))
      return spec
  }
  return null
}

/** Returns the TTL in ms for a registered localStorage name, or null if unknown. */
export function getTtlForKey(name: string): number | null {
  return specFor(name, LOCAL_INDEX)?.ttlMs ?? null
}

// ---------------------------------------------------------------------------
// The account namespace
// ---------------------------------------------------------------------------

/** The account every `account`-scoped key resolves under, or null before sign-in. */
let storageAccount: string | null = null

/**
 * The stored key for `name` under `userId`.
 *
 * The id is PERCENT-ENCODED, so the ':' that ends the account segment cannot
 * occur inside it and the parse back is unambiguous for ANY id the hub mints.
 * The alternative -- assert that ids are drawn from `[A-Za-z0-9]`, as
 * `internal/util/id` draws them today -- restates a backend property in the
 * frontend and turns the day it changes into a throw at sign-in, which
 * `AuthContext` can only report as a failed bootstrap. Encoding is the identity
 * function over that alphabet, so the stored keys are the same either way.
 *
 * Exported for the tests and the E2E helpers, which have to name a key for an
 * account other than the signed-in one. Production never does -- `storedKeyFor`
 * answers that, and the accessors do it themselves.
 */
export function accountStorageKey(userId: string, name: string): string {
  return `${NAMESPACE}${ACCOUNT_SEGMENT}${encodeURIComponent(userId)}:${name}`
}

/** The prefix every key of `userId` carries. For a caller that matches a whole namespace. */
export function accountStorageKeyPrefix(userId: string): string {
  return accountStorageKey(userId, '')
}

/** The stored key for a `device`-scoped `name`. */
function deviceStorageKey(name: string): string {
  return `${NAMESPACE}${name}`
}

/** Listeners that `setStorageAccount` notifies after it moves the namespace. */
const accountListeners = new Set<() => void>()

/**
 * Run `listener` each time the account namespace moves. Returns the unsubscribe.
 *
 * For a cache that MIRRORS an account-scoped key in memory. The mirror belongs
 * to the account it was read for, so it has to be dropped or re-read when the
 * namespace moves; a subscription is what makes that automatic rather than a
 * step each such cache has to remember.
 *
 * `setStorageAccount` calls the listeners synchronously, which is the property
 * a subscriber depends on: the namespace and every mirror of it move in the same
 * step, before the signal that carries the identity notifies, so no render effect
 * can observe one without the other.
 */
export function onStorageAccountChange(listener: () => void): () => void {
  accountListeners.add(listener)
  return () => accountListeners.delete(listener)
}

/**
 * Point every `account`-scoped key at `userId`'s namespace.
 *
 * Call this SYNCHRONOUSLY as the identity is written, before the signal that
 * carries it notifies. A render effect runs ahead of a user effect in the same
 * flush, so a subscriber can mount and read storage while an effect-based call
 * is still queued -- and it would read the previous account's values.
 *
 * IT NEVER RETURNS TO NULL, which is why it takes no null. Signing out tears
 * down the authenticated tree, and the writes that teardown makes -- a draft
 * flush, a layout snapshot -- belong to the account that is LEAVING. Clearing
 * the namespace would make each of them throw on the way out. A reader that
 * needs "is anyone signed in RIGHT NOW" must ask the identity, not this module:
 * `hasStorageAccount` answers whether a namespace exists to write into, which
 * stays true after a sign-out.
 */
export function setStorageAccount(userId: string): void {
  if (userId === '')
    throw new Error('Invalid storage account id: the id must not be empty.')
  // An unchanged id is not a move, so it neither disturbs an open batch nor
  // needs to do anything. `refreshUser` replaces the User object on every call.
  if (userId === storageAccount)
    return
  // A batch holds ONE account's document in memory and stores it in a `finally`.
  // Moving the namespace underneath it would write the outgoing account's whole
  // document into the incoming account's key.
  if (browserPrefBatchOpen())
    throw new Error('Cannot change the storage account while a browser-preference batch is open.')
  storageAccount = userId
  for (const listener of accountListeners)
    listener()
}

/** Whether an account namespace is available, i.e. whether an identity resolved. */
export function hasStorageAccount(): boolean {
  return storageAccount !== null
}

/**
 * Drop the account namespace and every listener. FOR TESTS ONLY.
 *
 * Production has no path back to "no account": see `setStorageAccount`.
 */
export function resetStorageAccountForTests(): void {
  storageAccount = null
  accountListeners.clear()
}

/**
 * The stored key `spec` puts `name` at, or null while an `account`-scoped name
 * has no account to resolve under.
 *
 * The one statement of the layout decision, so `storedKeyFor` and `resolveKey`
 * cannot answer it differently. They differ only in what they do with the null.
 */
function composeKey(name: string, spec: KeySpec): string | null {
  if (spec.scope === 'device')
    return deviceStorageKey(name)
  return storageAccount === null ? null : accountStorageKey(storageAccount, name)
}

/**
 * The key `name` is stored under right now, or null while no account is set.
 *
 * For a caller that must COMPARE against a stored key it did not write, such as
 * the cross-tab `storage` listener: a null answer correctly matches no event,
 * where a throw would take down an event handler that fires on any tab's write.
 *
 * An UNREGISTERED name throws, as it does everywhere else in this module. Null
 * would be indistinguishable from "no account yet" at the one caller, so a
 * misspelled name would disable cross-tab sync in silence -- which is the exact
 * failure this listener already shipped with once.
 */
export function storedKeyFor(name: string): string | null {
  const spec = specFor(name, LOCAL_INDEX) ?? specFor(name, SESSION_INDEX)
  if (spec === null) {
    throw new Error(
      `Unknown storage key: "${name}". Register it in browserStorage.ts `
      + `(LOCAL_KEY_SPECS or SESSION_KEY_SPECS).`,
    )
  }
  return composeKey(name, spec)
}

/**
 * Where `name` is stored, and how long its value lives.
 *
 * Throws for an unregistered name, and for an `account`-scoped name while no
 * account is set. Both are programming errors that must not degrade quietly:
 * the first would write a key the sweep deletes on the next load, and the second
 * would write one account's value where no account can own it.
 */
function resolveKey(name: string, index: SpecIndex): { key: string, ttl: number } {
  const spec = specFor(name, index)
  if (spec === null) {
    throw new Error(
      `Unknown ${index.store} key: "${name}". Register it in browserStorage.ts (${index.table}).`,
    )
  }
  const key = composeKey(name, spec)
  if (key === null) {
    throw new Error(
      `No storage account is set for account-scoped key "${name}". Call setStorageAccount() `
      + `once the identity resolves, or register the key as scope: 'device' in browserStorage.ts.`,
    )
  }
  return { key, ttl: spec.ttlMs }
}

// ---------------------------------------------------------------------------
// Reading a key back off the wire (the sweep)
// ---------------------------------------------------------------------------

/**
 * Split a key AS STORED into the scope it was written under and the logical
 * name it was written for, or null for anything this module could not have
 * written.
 *
 * The id segment is percent-encoded (see `accountStorageKey`), so the first ':'
 * ends it whatever the id holds. The segment is only VALIDATED here, never used:
 * the sweep judges a key by its name and scope, and which account owns it is
 * deliberately not part of that decision. A truncated segment, or one carrying a
 * malformed escape, returns null and the sweep treats the key as unknown.
 */
function parseStoredKey(stored: string): { scope: StorageScope, name: string } | null {
  if (!stored.startsWith(NAMESPACE))
    return null
  const rest = stored.slice(NAMESPACE.length)
  if (!rest.startsWith(ACCOUNT_SEGMENT))
    return { scope: 'device', name: rest }
  const body = rest.slice(ACCOUNT_SEGMENT.length)
  const separator = body.indexOf(':')
  if (separator <= 0)
    return null
  try {
    decodeURIComponent(body.slice(0, separator))
  }
  catch {
    // A malformed escape ("%ZZ") is not something this module wrote.
    return null
  }
  return { scope: 'account', name: body.slice(separator + 1) }
}

/**
 * The TTL for a key as it sits in storage, or null when nothing registers it
 * under the scope it carries.
 *
 * Deliberately independent of the current account: the sweep runs before any
 * identity resolves and must judge every account's keys, keeping the ones that
 * are merely someone else's. A scope MISMATCH is unknown rather than a fallback
 * — that is what retires a flat key left by an earlier build, and what stops a
 * scoped copy of a device key from masquerading as registered.
 */
function ttlForStoredKey(stored: string, index: SpecIndex): number | null {
  const parsed = parseStoredKey(stored)
  if (parsed === null)
    return null
  const spec = specFor(parsed.name, index)
  if (spec === null || spec.scope !== parsed.scope)
    return null
  return spec.ttlMs
}

export function getTtlForStoredKey(stored: string): number | null {
  return ttlForStoredKey(stored, LOCAL_INDEX)
}

export function getSessionTtlForStoredKey(stored: string): number | null {
  return ttlForStoredKey(stored, SESSION_INDEX)
}

/** Whether the sweep should KEEP `stored` when its wrapper is fresh. */
function isRegisteredLocalKey(stored: string): boolean {
  return ttlForStoredKey(stored, LOCAL_INDEX) !== null
}

/** Whether the sweep should KEEP `stored` when its wrapper is fresh. */
function isRegisteredSessionKey(stored: string): boolean {
  return ttlForStoredKey(stored, SESSION_INDEX) !== null
}

/** Type guard: checks if a parsed value has the wrapped format { v, e }. */
export function isWrappedValue(raw: unknown): raw is { v: unknown, e: number } {
  return (
    typeof raw === 'object'
    && raw !== null
    && !Array.isArray(raw)
    && 'v' in raw
    && 'e' in raw
    && typeof (raw as Record<string, unknown>).e === 'number'
  )
}

/**
 * Check if a wrapped value's expiration should be refreshed on read.
 * Returns true if the expiration was last refreshed more than 3 hours ago
 * (i.e. the remaining lifetime is shorter than TTL minus 3 hours).
 */
export function shouldRefreshExpiration(e: number, ttlMs: number): boolean {
  return e < Date.now() + ttlMs - REFRESH_THRESHOLD_MS
}

// ---------------------------------------------------------------------------
// Safe localStorage wrappers
// ---------------------------------------------------------------------------

/**
 * Read and unwrap a dynamic key's value, handling expiration and refresh.
 * Returns the unwrapped value, or undefined if missing/expired/malformed.
 */
function readDynamic(storage: Storage, key: string, ttl: number): unknown | undefined {
  const raw = storage.getItem(key)
  if (raw === null)
    return undefined

  const parsed = JSON.parse(raw)
  if (!isWrappedValue(parsed))
    return undefined

  if (parsed.e <= Date.now()) {
    storage.removeItem(key)
    return undefined
  }

  if (shouldRefreshExpiration(parsed.e, ttl)) {
    // In a `try` OF ITS OWN. The refresh is a best-effort extension of a value
    // that is already read and parsed, and every caller wraps this whole
    // function in a catch that answers `undefined`. A refused write -- the
    // origin quota, which per-account partitioning reaches sooner -- would
    // therefore discard a value that is still on disk, and every device
    // preference would revert to its default while the document was intact.
    try {
      parsed.e = Date.now() + ttl
      storage.setItem(key, JSON.stringify(parsed))
    }
    catch { /* the value stands; it simply expires on its original schedule */ }
  }

  return parsed.v
}

/** Write a value wrapped with a TTL expiration to `storage`. */
function writeWrapped(storage: Storage, key: string, value: unknown, ttl: number): void {
  storage.setItem(key, JSON.stringify({ v: value, e: Date.now() + ttl }))
}

/**
 * Report a write that failed, and continue.
 *
 * A failed write is still not an error a caller can act on -- a draft, a layout
 * snapshot or a key pin has nowhere else to go -- so it stays swallowed. But a
 * REFUSAL must not be silent: the usual cause is the origin quota, every key is
 * partitioned per account, and the symptom a user reports is "my preferences
 * stop saving" with nothing anywhere to point at the cause.
 *
 * A `ReferenceError` is the other case and it is not a refusal: it means the
 * environment has no such global at all, which is true of Node -- server-side
 * rendering, and the E2E harness that drives the channel code outside a browser.
 * That is an expected property of where the code runs, so it stays at debug and
 * does not put a warning on the console for every write.
 */
function reportWriteFailure(store: string, name: string, err: unknown): void {
  const message = `${store} write failed for "${name}"; the value is not persisted`
  if (err instanceof ReferenceError)
    log.debug(message, err)
  else
    log.warn(message, err)
}

/** Read and unwrap a value from localStorage. Returns undefined on missing/expired/malformed. */
export function localStorageGet<T>(name: string): T | undefined {
  const { key, ttl } = resolveKey(name, LOCAL_INDEX)
  try {
    return readDynamic(localStorage, key, ttl) as T | undefined
  }
  catch { /* ignore parse errors */ }
  return undefined
}

/** Stringify and write a value to localStorage wrapped with a TTL. Write errors are logged, not thrown. */
export function localStorageSet(name: string, value: unknown): void {
  const { key, ttl } = resolveKey(name, LOCAL_INDEX)
  try {
    writeWrapped(localStorage, key, value, ttl)
  }
  catch (err) {
    reportWriteFailure('localStorage', name, err)
  }
}

/**
 * Remove a key from localStorage. Silently ignores errors.
 *
 * It validates the name, unlike the version that took a whole stored key:
 * composing the stored key requires the registration that says which namespace
 * the name lives in, so an unregistered name has no key to remove and a
 * silent no-op would leave the caller believing it deleted something.
 */
export function localStorageRemove(name: string): void {
  const { key } = resolveKey(name, LOCAL_INDEX)
  try {
    localStorage.removeItem(key)
  }
  catch { /* ignore errors */ }
}

/**
 * Clear localStorage. For tests only: production code removes keys by name
 * (`localStorageRemove`); a wholesale clear is a test-fixture reset, which
 * still routes through this module so no call site touches localStorage
 * directly. `sessionStorageClearForTests` is its twin -- a fixture resets both
 * stores, so a module that forwards one and not the other pushes the caller
 * straight back to a raw `clear()`.
 */
export function localStorageClearForTests(): void {
  try {
    localStorage.clear()
  }
  catch { /* ignore errors */ }
}

/** Load the consolidated browser preferences from localStorage. */
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
function browserPrefBatchOpen(): boolean {
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
 * "Reset all browser overrides" clears seventeen fields, and each one is
 * otherwise a full read, parse, serialize and write of the whole document. One
 * write is also one `storage` event for the other tabs rather than seventeen.
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

// ---------------------------------------------------------------------------
// Safe sessionStorage wrappers
// ---------------------------------------------------------------------------

/** Read and unwrap a value from sessionStorage. Returns undefined on missing/expired/malformed. */
export function sessionStorageGet<T>(name: string): T | undefined {
  const { key, ttl } = resolveKey(name, SESSION_INDEX)
  try {
    return readDynamic(sessionStorage, key, ttl) as T | undefined
  }
  catch { /* ignore parse errors */ }
  return undefined
}

/** Stringify and write a value to sessionStorage wrapped with a TTL. Write errors are logged, not thrown. */
export function sessionStorageSet(name: string, value: unknown): void {
  const { key, ttl } = resolveKey(name, SESSION_INDEX)
  try {
    writeWrapped(sessionStorage, key, value, ttl)
  }
  catch (err) {
    reportWriteFailure('sessionStorage', name, err)
  }
}

/**
 * Cheap existence check: true iff the key has any value in sessionStorage.
 * Skips the wrapper parse / TTL refresh that `sessionStorageGet` performs —
 * use this when callers only need "did anything write here?".
 */
export function sessionStorageHas(name: string): boolean {
  const { key } = resolveKey(name, SESSION_INDEX)
  try {
    return sessionStorage.getItem(key) !== null
  }
  catch { /* ignore access errors */ }
  return false
}

/** Remove a key from sessionStorage. Silently ignores errors. See `localStorageRemove`. */
export function sessionStorageRemove(name: string): void {
  const { key } = resolveKey(name, SESSION_INDEX)
  try {
    sessionStorage.removeItem(key)
  }
  catch { /* ignore errors */ }
}

/** Clear sessionStorage. For tests only. See `localStorageClearForTests`. */
export function sessionStorageClearForTests(): void {
  try {
    sessionStorage.clear()
  }
  catch { /* ignore errors */ }
}

// ---------------------------------------------------------------------------
// Cleanup
// ---------------------------------------------------------------------------

function sweepStorage(
  storage: Storage,
  isRegistered: (key: string) => boolean,
): void {
  const now = Date.now()
  const keysToDelete: string[] = []
  for (let i = 0; i < storage.length; i++) {
    const key = storage.key(i)
    if (!key)
      continue
    if (!key.startsWith(NAMESPACE) && !key.startsWith(RETIRED_NAMESPACE))
      continue
    if (isRegistered(key)) {
      try {
        const raw = storage.getItem(key)
        if (raw !== null) {
          const parsed = JSON.parse(raw)
          if (isWrappedValue(parsed) && parsed.e > now)
            continue
        }
      }
      catch { /* parse error → treat as stale */ }
    }
    keysToDelete.push(key)
  }
  for (const key of keysToDelete) {
    try {
      storage.removeItem(key)
    }
    catch { /* ignore removal errors */ }
  }
}

/**
 * Scan localStorage and sessionStorage and delete every `leapmux:`-family key
 * that is unregistered, that carries a scope its registration does not allow,
 * or whose wrapper is missing / malformed / expired.
 *
 * It judges a key by the key itself, never by the current account, because it
 * runs before any identity resolves and it has to answer for EVERY account's
 * keys. Another account's fresh key is registered and unexpired, so it is kept;
 * that is the whole point of scoping. Another account's expired key still goes,
 * so a TTL is not something an unused account can dodge.
 *
 * A flat key left by an earlier build resolves to the `device` scope, does not
 * match the `account` scope its name is registered under, and is therefore
 * unknown -- which is how the move to scoped keys retires the old ones without
 * a migration step.
 */
export function runCleanup(): void {
  sweepStorage(localStorage, isRegisteredLocalKey)
  sweepStorage(sessionStorage, isRegisteredSessionKey)
}

/**
 * Run `body` once the browser is idle, or on the next timer tick where
 * `requestIdleCallback` is absent. Returns the cancel.
 */
function whenIdle(body: () => void): () => void {
  if (typeof requestIdleCallback === 'function') {
    const handle = requestIdleCallback(body, { timeout: IDLE_SWEEP_TIMEOUT_MS })
    return () => cancelIdleCallback(handle)
  }
  const handle = setTimeout(body, 0)
  return () => clearTimeout(handle)
}

/**
 * Start the storage cleanup: one sweep when the browser next goes idle, then
 * one every hour. Returns a dispose function that cancels both.
 *
 * THE FIRST SWEEP IS DEFERRED, because `App` starts it in its own body and both
 * stores are synchronous: a sweep walks every key in the origin, and every key
 * is partitioned per account, so a browser several accounts have signed in to
 * pays for all of them on the critical path to first paint.
 *
 * Deferring is safe because the sweep reclaims SPACE and is not a correctness
 * gate. A read cannot see a value the sweep would have deleted: `readDynamic`
 * checks the same expiration and removes the entry itself, and a flat key from
 * an earlier build has no name any accessor composes.
 */
export function initStorageCleanup(): () => void {
  const cancelFirst = whenIdle(runCleanup)
  const id = setInterval(runCleanup, CLEANUP_INTERVAL_MS)
  return () => {
    cancelFirst()
    clearInterval(id)
  }
}
