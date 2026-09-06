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
 * Every value carries an expiration; reads may refresh it on access. Long-lived
 * preferences use a 1-year TTL plus that refresh, so opening the app at any
 * point in a year keeps them alive; total inactivity for a year is the only way
 * they expire.
 *
 * `runCleanup` sweeps on a timer, deleting any `leapmux:`-family key that is
 * unregistered, that carries a scope its registration does not allow, or whose
 * expiration has passed. It keeps OTHER accounts' keys, which is the whole point
 * of scoping them.
 *
 * A module that MIRRORS an account-scoped key in memory subscribes to
 * `onStorageAccountChange`, so the mirror moves with the namespace instead of
 * serving the previous account's copy.
 *
 * TWO BACKENDS, AND THE SPLIT IS NOT ARBITRARY.
 *
 * The `localStorage` family moved to IndexedDB (see `~/lib/browserStorageDb`).
 * localStorage is synchronous main-thread I/O under a ~5 MB origin cap, and
 * several families here are unbounded -- a multi-KB composite public key per
 * worker, tens of KB of measured row heights per chat, base64 attachments,
 * arbitrary draft prose. Writing those on the main thread costs frames and the
 * cap is a real ceiling.
 *
 * IndexedDB is asynchronous, and many readers here cannot await: a
 * `createSignal` initializer, a `createMemo`, the synchronous
 * `onStorageAccountChange` callback, an xterm constructor. So every key
 * declares an `access` tier. The `sync` tier is MIRRORED IN MEMORY and keeps the
 * synchronous accessors; the `async` tier is not mirrored and returns promises.
 * `hydrateStorageAccount` loads the sync tier, and `setStorageAccount` refuses an
 * account it was not run for.
 *
 * ONE THING IS WEAKER THAN IT WAS, AND IT IS NOT REPAIRABLE HERE. Writes go
 * through a write-behind queue, so a write issued in the last moments before a
 * reload can be lost where the synchronous `setItem` it replaces could not be.
 * `App` flushes on `pagehide`, which narrows that window to what an unload can
 * interrupt rather than closing it. A caller that must know whether its value
 * reached disk reads `StorageWrite.durable`; `persistedSeq` is the one that does.
 *
 * `sessionStorage` STAYS on the Web Storage API. IndexedDB is per-origin and
 * shared by every tab, while sessionStorage is per-tab and dies with the tab --
 * and that lifetime is load-bearing for every key registered there: the CRDT
 * client identity (`checkpointStore` keys its records by `[userId, clientId]`),
 * the tab / tile / focus / sidebar pointers, the MRU stamps. Reproducing it on
 * IndexedDB needs a tab id plus a collector for dead tabs, and the tab id itself
 * would have to live in sessionStorage.
 */
import type { KvRow, StorageWrite } from './browserStorageDb'
import {
  enqueueKvDelete,
  enqueueKvPut,
  flushKvWrites,
  onKvBroadcast,
  peekKvPending,
  publishKvRemovals,
  readKvPrefix,
  readKvRow,
  readKvRows,
  REFUSED_WRITE,
  resetKvForTests,
  sweepKv,
} from './browserStorageDb'
import { createLogger } from './logger'

const log = createLogger('browserStorage')

export type { StorageWrite } from './browserStorageDb'

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
export const KEY_PREFERRED_EXTERNAL_APP = 'preferred-external-app'
export const KEY_ACTIVE_WORKSPACE = 'active-workspace'
export const KEY_WORKSPACE_SORT = 'workspace-sort'
export const KEY_USER_EVENTS_RELAY_SEQ = 'user-events-relay-seq'
export const KEY_CHANNEL_RELAY_SEQ = 'channel-relay-seq'

/** Dynamic key prefixes — single source of truth for all consumers. */
export const PREFIX_EDITOR_DRAFT = 'editor-draft:'
export const PREFIX_EDITOR_MIN_HEIGHT = 'editor-min-height:'
export const PREFIX_AGENT_SESSION = 'agent-session:'
export const PREFIX_CONTROL_STATE = 'control-state:'
export const PREFIX_WORKER_INFO = 'worker-info:'
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
 * How a localStorage-family key is READ.
 *
 * The backing store is IndexedDB, which is asynchronous, so this is the one
 * decision the move forced on every key.
 *
 * `sync` keys are MIRRORED IN MEMORY and keep `localStorageGet` /
 * `localStorageSet`. The mirror is the price: every tab holds every sync value
 * for the session and pays for them all on the sign-in path. So the tier is for
 * keys whose reader genuinely cannot await -- a `createSignal` initializer, a
 * `createMemo`, a synchronous `onStorageAccountChange` callback, a constructor.
 *
 * `async` keys are not mirrored and use `localStorageLoad` / `localStorageStore`.
 * It is the answer for everything else, and the answer a NEW key should take
 * unless it can name the reader that cannot await.
 *
 * The two tiers coincide with small versus unbounded, and that is not a
 * coincidence: a value big enough to matter already had something asynchronous
 * around it.
 */
export type KeyAccess = 'sync' | 'async'

/**
 * One localStorage-family key's registration.
 *
 * `access` is deliberately not optional, for the same reason `scope` is not:
 * `satisfies` below turns a new key that omits it into a COMPILE error, so the
 * question is answered when the key is added rather than discovered later by
 * whoever finds a `createSignal` initializer reading `undefined`.
 *
 * sessionStorage keys carry no `access`. They are still on the Web Storage API,
 * which is synchronous by nature, so the tier is a property of this store alone.
 */
export interface LocalKeySpec extends KeySpec {
  readonly access: KeyAccess
  /**
   * Merge the value as a HIGH-WATER MARK rather than last-write-wins.
   *
   * Only the two relay sequence marks. See `KvWriteOptions.monotonic` in
   * `~/lib/browserStorageDb` for what it prevents.
   */
  readonly monotonic?: true
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
  // `sync`: read inside the synchronous `onStorageAccountChange` callback (see
  // PreferencesContext.reseedBrowserTier), and by `~/lib/terminal` while it
  // constructs an xterm instance.
  [KEY_BROWSER_PREFS]: { match: 'exact', scope: 'account', ttlMs: YEAR_MS, access: 'sync' },
  // `sync`: read from `useMruProviders.mruProviders()`, a plain accessor the
  // render path calls, and from the synchronous `pickDefaultProvider`.
  [KEY_MRU_AGENT_PROVIDERS]: { match: 'exact', scope: 'account', ttlMs: YEAR_MS, access: 'sync' },
  // `sync`: `KeyPinStore.resolve` hands back a `commit` closure that re-reads
  // and rewrites the map with NO await between, which is what closes the
  // intra-tab pin-clobber race.
  [KEY_KEY_PINS]: { match: 'exact', scope: 'account', ttlMs: YEAR_MS, access: 'sync' },
  // `sync`: seeded eagerly from the synchronous account-change callback.
  [KEY_DIRECTORY_SELECTOR_SHOW_HIDDEN]: { match: 'exact', scope: 'account', ttlMs: YEAR_MS, access: 'sync' },
  [KEY_PREFERRED_EXTERNAL_APP]: { match: 'exact', scope: 'account', ttlMs: YEAR_MS, access: 'sync' },
  // The workspace this account was last on. A year rather than the days its
  // templated table-mates get: this is a preference, not a cache -- it is the
  // only record of where the app should reopen, since the URL no longer carries
  // the workspace id.
  //
  // `sync` is the one judgement call in this table. Its reader is a tracked
  // effect that COULD await, but only by hoisting five reactive reads above the
  // first await and adding a re-entrancy guard, for one short string.
  [KEY_ACTIVE_WORKSPACE]: { match: 'exact', scope: 'account', ttlMs: YEAR_MS, access: 'sync' },
  // How the sidebar orders every workspace section. A preference the user set
  // once, like the browser preferences above -- so a year, not the seven days a
  // per-directory cache takes. `sync`: `createAccountScopedSignal` seeds it from
  // a synchronous `get()`.
  [KEY_WORKSPACE_SORT]: { match: 'exact', scope: 'account', ttlMs: YEAR_MS, access: 'sync' },
  // High-water mark for the desktop userevents relay ids (see useUserEvents).
  // Device-scoped: see the note above the table. `sync` because the allocator is
  // a synchronous `() => number`, and `monotonic` because a smaller mark must
  // never overwrite a larger one.
  [KEY_USER_EVENTS_RELAY_SEQ]: { match: 'exact', scope: 'device', ttlMs: YEAR_MS, access: 'sync', monotonic: true },
  // High-water mark for the desktop channel relay ids (see relayClaim). Same
  // reason, same sidecar fence.
  [KEY_CHANNEL_RELAY_SEQ]: { match: 'exact', scope: 'device', ttlMs: YEAR_MS, access: 'sync', monotonic: true },

  // Every `async` entry below is an unbounded family whose reader already sits
  // inside an effect or an async function. They are the reason the tier split
  // exists: mirroring them would read a user's whole draft and row-height
  // history into every tab on the sign-in path, which is exactly the cost the
  // move off localStorage was meant to stop paying.
  [PREFIX_EDITOR_DRAFT]: { match: 'prefix', scope: 'account', ttlMs: 7 * DAY_MS, access: 'async' },
  [PREFIX_EDITOR_MIN_HEIGHT]: { match: 'prefix', scope: 'account', ttlMs: 7 * DAY_MS, access: 'async' },
  [PREFIX_AGENT_SESSION]: { match: 'prefix', scope: 'account', ttlMs: 7 * DAY_MS, access: 'async' },
  [PREFIX_CONTROL_STATE]: { match: 'prefix', scope: 'account', ttlMs: 1 * DAY_MS, access: 'async' },
  [PREFIX_WORKER_INFO]: { match: 'prefix', scope: 'account', ttlMs: 7 * DAY_MS, access: 'async' },
  // `sync`: both are read inside a `createSignal` initializer (see
  // `createPersistedSignal`), so the file list would paint with the wrong
  // hidden-file state and then flip.
  [PREFIX_FILES_SHOW_HIDDEN]: { match: 'prefix', scope: 'account', ttlMs: 7 * DAY_MS, access: 'sync' },
  [PREFIX_FILES_SORT_ORDER]: { match: 'prefix', scope: 'account', ttlMs: 7 * DAY_MS, access: 'sync' },
  // The git mode a repository was last started with, keyed by
  // `<workerId>:<gitToplevel>` (see `gitModeStickyKey`). The TTL is the only
  // thing limiting growth -- there is one entry per repository the user ever
  // starts a workspace in -- and a read refreshes it, so a repository in weekly
  // use never expires while an abandoned one does. `sync`: read from a
  // `createMemo` in WorkspaceSectionMenu, which cannot await.
  [PREFIX_WORKSPACE_GIT_MODE]: { match: 'prefix', scope: 'account', ttlMs: 7 * DAY_MS, access: 'sync' },
  // Measured chat-row heights (see chatRowHeightPersistence). A warm-start
  // cache: stale entries are harmless (each row's key digest must match its
  // live heightKey to hydrate), so the TTL only limits storage growth.
  [PREFIX_CHAT_ROW_HEIGHTS]: { match: 'prefix', scope: 'account', ttlMs: 7 * DAY_MS, access: 'async' },
} as const satisfies Record<string, LocalKeySpec>

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
function indexSpecs<S extends KeySpec>(specs: Record<string, S>, store: string, table: string) {
  const exact = new Map<string, S>()
  const prefixes: Array<{ name: string, spec: S }> = []
  for (const [name, spec] of Object.entries(specs)) {
    if (spec.match === 'exact')
      exact.set(name, spec)
    else
      prefixes.push({ name, spec })
  }
  return { exact, prefixes, store, table }
}

const LOCAL_INDEX = indexSpecs<LocalKeySpec>(LOCAL_KEY_SPECS, 'localStorage', 'LOCAL_KEY_SPECS')
const SESSION_INDEX = indexSpecs<KeySpec>(SESSION_KEY_SPECS, 'sessionStorage', 'SESSION_KEY_SPECS')

type SpecIndex<S extends KeySpec = KeySpec> = ReturnType<typeof indexSpecs<S>>

/**
 * The registration for a logical name, or null when nothing registers it.
 *
 * Exact entries are consulted FIRST and never prefix-match, so a singleton
 * cannot inherit a TTL because some prefix happens to be its leading substring.
 */
function specFor<S extends KeySpec>(name: string, index: SpecIndex<S>): S | null {
  const exact = index.exact.get(name)
  if (exact !== undefined)
    return exact
  for (const { name: prefix, spec } of index.prefixes) {
    if (name.startsWith(prefix))
      return spec
  }
  return null
}

// ---------------------------------------------------------------------------
// The two localStorage key vocabularies
// ---------------------------------------------------------------------------

type LocalSpecs = typeof LOCAL_KEY_SPECS

type ExactNamesOf<A extends KeyAccess> = {
  [K in keyof LocalSpecs]: LocalSpecs[K] extends { match: 'exact', access: A } ? K : never
}[keyof LocalSpecs] & string

type PrefixNamesOf<A extends KeyAccess> = {
  [K in keyof LocalSpecs]: LocalSpecs[K] extends { match: 'prefix', access: A } ? K : never
}[keyof LocalSpecs] & string

/**
 * A name the SYNCHRONOUS accessors take: an exact `sync` name, or a `sync`
 * prefix followed by anything.
 *
 * A composed key such as `` `${PREFIX_FILES_SHOW_HIDDEN}${workerId}` `` infers
 * as a template literal type and matches with no cast. Using the wrong accessor
 * is therefore a COMPILE error -- and `resolveLocalKey` throws for it too,
 * because a type cannot reach a key built from a runtime `string`.
 */
export type SyncLocalKey = ExactNamesOf<'sync'> | `${PrefixNamesOf<'sync'>}${string}`

/** A name the ASYNCHRONOUS accessors take. See {@link SyncLocalKey}. */
export type AsyncLocalKey = ExactNamesOf<'async'> | `${PrefixNamesOf<'async'>}${string}`

/** Returns the TTL in ms for a registered localStorage name, or null if unknown. */
export function getTtlForKey(name: string): number | null {
  return specFor(name, LOCAL_INDEX)?.ttlMs ?? null
}

// ---------------------------------------------------------------------------
// The account namespace
// ---------------------------------------------------------------------------

/** The account every `account`-scoped key resolves under, or null before sign-in. */
let storageAccount: string | null = null

/** One mirrored row, exactly as the database holds it. */
interface MirrorEntry {
  v: unknown
  e: number
}

/**
 * The synchronous tier, in memory, keyed by the STORED key.
 *
 * By the stored key rather than the logical name, because every other party
 * that talks about a row speaks stored keys: the write queue, the cross-tab
 * message (which may name an account that is not this one), the sweep, and
 * `parseStoredKey`. Keying by name would need a translation at each of those,
 * and each translation is a place to answer the account question differently.
 *
 * Holds the DEVICE rows plus the CURRENT account's synchronous rows. Another
 * account's rows stay on disk untouched, which is exactly what the sweep already
 * means by "keeps another account's fresh keys".
 */
const mirror = new Map<string, MirrorEntry>()

/** Which account's rows the mirror holds, or null before the first hydration. */
let mirroredAccount: string | null = null

/** Bumped by every hydration; one whose token went stale discards its rows. */
let hydrationToken = 0

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
  // THE MIRROR IS THE SYNCHRONOUS TIER, so pointing the namespace at an account
  // whose rows were never loaded would serve every consumer its built-in default
  // and then overwrite the stored values with those defaults on the first write.
  //
  // The invariant this establishes -- `hasStorageAccount()` implies the mirror
  // holds that account's rows -- is what every synchronous reader in the app
  // rests on, so it is checked HERE, at the one writer, rather than discovered
  // later at some unrelated read.
  if (mirroredAccount !== userId) {
    throw new Error(
      `Storage account "${userId}" is not hydrated. `
      + `Await hydrateStorageAccount(userId) before setStorageAccount(userId).`,
    )
  }
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
 * Move the namespace to `userId` synchronously, without reading a database.
 * FOR TESTS.
 *
 * Production must `await hydrateStorageAccount` first, because it has rows on
 * disk to load and a synchronous reader downstream that would otherwise take
 * its default. A test has none unless it wrote them, and it wrote them into the
 * mirror -- which is keyed by the STORED key, so the outgoing account's rows
 * simply become unreachable rather than needing to be dropped. That models the
 * disk exactly: another account's rows are still there, and only its own
 * namespace can name them.
 *
 * A test that seeds a DATABASE and wants it read awaits `hydrateStorageAccount`
 * itself.
 */
export function setStorageAccountForTests(userId: string): void {
  hydrationToken++
  mirroredAccount = userId
  setStorageAccount(userId)
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
function resolveKey<S extends KeySpec>(name: string, index: SpecIndex<S>): { key: string, ttl: number, spec: S } {
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
  return { key, ttl: spec.ttlMs, spec }
}

/**
 * `resolveKey` for the localStorage family, plus the tier check.
 *
 * The tier is enforced at RUNTIME as well as in the types, because the types
 * cannot reach a key composed from a runtime `string`, a JavaScript caller, or
 * an E2E helper. The message names the accessor to use, so a mismatch reads as
 * an instruction rather than a puzzle.
 */
function resolveLocalKey(name: string, access: KeyAccess): { key: string, ttl: number, spec: LocalKeySpec } {
  const resolved = resolveKey(name, LOCAL_INDEX)
  if (resolved.spec.access !== access) {
    const use = resolved.spec.access === 'sync'
      ? 'localStorageGet / localStorageSet / localStorageRemove'
      : 'localStorageLoad / localStorageStore / localStorageDrop'
    throw new Error(
      `Storage key "${name}" is registered as access: '${resolved.spec.access}'. Use ${use}.`,
    )
  }
  return resolved
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

// ---------------------------------------------------------------------------
// The mirror: the synchronous tier's view of the database
// ---------------------------------------------------------------------------

/** Every exact `sync` local name, computed once. */
const SYNC_EXACT_NAMES = Object.entries(LOCAL_KEY_SPECS)
  .filter(([, spec]) => spec.match === 'exact' && spec.access === 'sync')
  .map(([name]) => name)

/** Every `sync` local prefix, computed once. */
const SYNC_PREFIX_NAMES = Object.entries(LOCAL_KEY_SPECS)
  .filter(([, spec]) => spec.match === 'prefix' && spec.access === 'sync')
  .map(([name]) => name)

/** The device-scoped names, which every account shares. */
const DEVICE_NAMES = Object.entries(LOCAL_KEY_SPECS)
  .filter(([, spec]) => spec.scope === 'device')
  .map(([name]) => name)

/**
 * Load `userId`'s synchronous rows, plus the device rows, into the mirror.
 *
 * AWAIT THIS BEFORE `setStorageAccount(userId)`. It deliberately does NOT move
 * the namespace itself: that move must stay synchronous and adjacent to the
 * identity write, which is the ordering `AuthContext` and `PreferencesContext`
 * are built on.
 *
 * IT NEVER REJECTS. No IndexedDB, an open that failed and a read that threw all
 * leave an empty mirror for this account, which reads as "no stored value" and
 * takes every consumer's built-in default -- the same outcome as a fresh
 * profile, and never a reason a user cannot sign in.
 *
 * The device rows are loaded here too rather than at module evaluation. Their
 * one reader, `relayClaim.claim()`, runs in a channel-wrapper constructor with
 * no lifecycle hook to wait on -- but a channel only opens for an authenticated
 * session, so this gate is already ahead of it, and doing the work here keeps
 * the module free of asynchronous side effects at import time.
 */
export async function hydrateStorageAccount(userId: string): Promise<void> {
  const token = ++hydrationToken
  let rows: KvRow[] = []
  try {
    rows = await readSyncRows(userId)
  }
  catch (err) {
    log.warn('browser storage hydration failed; this session runs on defaults', err)
  }
  // A newer identity started hydrating while this one was in flight. Installing
  // now would put the outgoing account's rows under the incoming account's
  // namespace -- the exact leak the scoping exists to close.
  if (token !== hydrationToken)
    return
  installMirror(userId, rows)
}

/**
 * The rows the mirror needs: one keyed lookup for the exact names, one bound
 * range per synchronous prefix family.
 *
 * Deliberately NOT one scan of `accountStorageKeyPrefix(userId)`. That would
 * also materialize every `chat-row-heights:` and `local-messages:` row -- the
 * unbounded families -- on the sign-in path, which is precisely the cost the
 * tier split exists to avoid. A handful of index ranges is cheap; reading a
 * megabyte of drafts is not.
 */
async function readSyncRows(userId: string): Promise<KvRow[]> {
  const exactKeys = [
    ...SYNC_EXACT_NAMES.map(name => composeFor(userId, name)),
    ...DEVICE_NAMES.map(name => deviceStorageKey(name)),
  ]
  const accountPrefix = accountStorageKeyPrefix(userId)
  const [exact, ...families] = await Promise.all([
    readKvRows(exactKeys),
    ...SYNC_PREFIX_NAMES.map(prefix => readKvPrefix(accountPrefix + prefix)),
  ])
  return [...exact, ...families.flat()]
}

/** The stored key `name` takes under `userId`, honouring its registered scope. */
function composeFor(userId: string, name: string): string {
  const spec = specFor(name, LOCAL_INDEX)
  return spec?.scope === 'device' ? deviceStorageKey(name) : accountStorageKey(userId, name)
}

/**
 * Replace the mirror with `rows`, dropping whatever the previous account left.
 *
 * A row that already expired is not installed and its deletion is queued: a
 * hydration is a read, and a read has always been where an expired value is
 * noticed and removed.
 */
function installMirror(userId: string, rows: readonly KvRow[]): void {
  const now = Date.now()
  mirror.clear()
  for (const row of rows) {
    if (row.e <= now) {
      enqueueKvDelete(row.k, { publish: true })
      continue
    }
    mirror.set(row.k, { v: row.v, e: row.e })
  }
  mirroredAccount = userId
}

/**
 * Read a mirrored row, applying the same expiration and refresh rules the
 * `{v, e}` envelope carried.
 *
 * No `try` of its own, unlike the localStorage version: the refresh moves a
 * number in a Map and appends to the write queue, neither of which can throw. A
 * refused FLUSH settles that write's durability false without touching the
 * value, which is the property the old inner `try` existed to guarantee.
 */
function readMirror(key: string, ttl: number): unknown | undefined {
  const entry = mirror.get(key)
  if (entry === undefined)
    return undefined
  const now = Date.now()
  if (entry.e <= now) {
    mirror.delete(key)
    enqueueKvDelete(key, { publish: true })
    return undefined
  }
  if (shouldRefreshExpiration(entry.e, ttl)) {
    entry.e = now + ttl
    enqueueKvPut({ k: key, v: entry.v, e: entry.e }, { publish: false })
  }
  return entry.v
}

/**
 * Snapshot `value` for storage, or report that it cannot be stored.
 *
 * A snapshot rather than a reference, because the write is behind: without it a
 * caller that mutates the object before the flush would persist the mutation,
 * where the JSON serialization this replaces captured the value on the spot.
 *
 * TWO STRATEGIES, AND THE SECOND IS NOT A FALLBACK FOR RARE INPUT. Structured
 * clone is exact and cheap, and it is what widens a stored value beyond JSON --
 * `NaN`, a `Uint8Array`, a `Map` all survive it. But it REFUSES A PROXY, and
 * several callers here hand over a value read out of a Solid store, whose
 * nested objects are proxies: `agentSession.store` persists its `rateLimits`
 * and `contextUsage` straight from the reactive state. Those are ordinary data,
 * not a mistake, so a JSON round trip serializes them -- which is exactly what
 * every write here did before, so nothing regresses by taking it.
 *
 * What is left after both refuse is a genuine programming error (a function, a
 * DOM node, a cycle), and it is reported rather than dropped in silence.
 */
function snapshotValue(name: string, value: unknown): { ok: true, value: unknown } | { ok: false } {
  try {
    return { ok: true, value: structuredClone(value) }
  }
  catch {
    // Fall through to the serializer that reads properties rather than
    // inspecting the object's internals.
  }
  try {
    return { ok: true, value: JSON.parse(JSON.stringify(value)) as unknown }
  }
  catch (err) {
    log.error(`browser storage value for "${name}" cannot be stored; it must be plain data`, err)
    return { ok: false }
  }
}

// ---------------------------------------------------------------------------
// The synchronous tier
// ---------------------------------------------------------------------------

/**
 * Read a mirrored value. Returns undefined when it is missing or expired.
 *
 * Synchronous, and answers entirely from memory: no IndexedDB request is issued
 * on this path at all.
 */
export function localStorageGet<T>(name: SyncLocalKey): T | undefined {
  const { key, ttl } = resolveLocalKey(name, 'sync')
  return readMirror(key, ttl) as T | undefined
}

/**
 * Write a mirrored value.
 *
 * The mirror moves SYNCHRONOUSLY and the row is queued, so a read-after-write in
 * the same turn already sees the new value. That total ordering against the
 * mirror is what keeps `KeyPinStore.resolve`'s no-await read-modify-write
 * correct.
 */
export function localStorageSet(name: SyncLocalKey, value: unknown): StorageWrite {
  const { key, ttl, spec } = resolveLocalKey(name, 'sync')
  const snapshot = snapshotValue(name, value)
  if (!snapshot.ok)
    return REFUSED_WRITE
  const entry: MirrorEntry = { v: snapshot.value, e: Date.now() + ttl }
  mirror.set(key, entry)
  return enqueueKvPut({ k: key, v: entry.v, e: entry.e }, { publish: true, monotonic: spec.monotonic })
}

/**
 * Remove a mirrored value.
 *
 * It validates the name, unlike a call that took a whole stored key: composing
 * the stored key requires the registration that says which namespace the name
 * lives in, so an unregistered name has no key to remove and a silent no-op
 * would leave the caller believing it deleted something.
 */
export function localStorageRemove(name: SyncLocalKey): StorageWrite {
  const { key } = resolveLocalKey(name, 'sync')
  mirror.delete(key)
  return enqueueKvDelete(key, { publish: true })
}

// ---------------------------------------------------------------------------
// The asynchronous tier
// ---------------------------------------------------------------------------

/**
 * Read an unmirrored value straight from the database.
 *
 * Consults the write queue first, so a read issued before the flush sees what
 * the caller just wrote. The synchronous tier gets that from the mirror; this
 * tier has none, and two of its callers do a read-modify-write over a list.
 */
export async function localStorageLoad<T>(name: AsyncLocalKey): Promise<T | undefined> {
  const { key, ttl } = resolveLocalKey(name, 'async')
  const now = Date.now()
  const queued = peekKvPending(key)
  if (queued !== undefined) {
    if ('removed' in queued || queued.row.e <= now)
      return undefined
    // CLONED, because the queue still holds this exact object and will write it.
    // Handing the reference out would let a caller that mutates what it read --
    // which a read-modify-write over a list does by construction -- mutate the
    // pending row underneath the flush. A read from disk is already a fresh
    // structured clone, so this is what makes the two paths behave alike.
    return structuredClone(queued.row.v) as T
  }
  // The queue may have JUST drained this key into a transaction that has not
  // committed, in which case `peekKvPending` no longer knows about it and the
  // row is not yet on disk. Waiting for that flush is what makes a read see
  // every write this tab issued, whichever side of the drain it lands on.
  // It resolves immediately when nothing is in flight, which is the common case.
  await flushKvWrites()
  const row = await readKvRow(key)
  if (row === undefined)
    return undefined
  if (row.e <= now) {
    enqueueKvDelete(key, { publish: false })
    return undefined
  }
  if (shouldRefreshExpiration(row.e, ttl))
    enqueueKvPut({ k: key, v: row.v, e: now + ttl }, { publish: false })
  return row.v as T
}

/** Write an unmirrored value. See {@link localStorageSet} for the shared contract. */
export function localStorageStore(name: AsyncLocalKey, value: unknown): StorageWrite {
  const { key, ttl } = resolveLocalKey(name, 'async')
  const snapshot = snapshotValue(name, value)
  if (!snapshot.ok)
    return REFUSED_WRITE
  return enqueueKvPut({ k: key, v: snapshot.value, e: Date.now() + ttl }, { publish: false })
}

/** Remove an unmirrored value. See {@link localStorageRemove}. */
export function localStorageDrop(name: AsyncLocalKey): StorageWrite {
  const { key } = resolveLocalKey(name, 'async')
  return enqueueKvDelete(key, { publish: false })
}

/** Wait for every queued write to reach disk. For a teardown, and for tests. */
export function flushStorageWrites(): Promise<void> {
  return flushKvWrites()
}

/**
 * Clear the legacy localStorage entries. For tests only.
 *
 * The `leapmux:` family no longer lives in localStorage at all; this exists so
 * the retirement sweep has something to test against, and so a fixture that
 * resets both Web Storage stores still routes through this module rather than
 * reaching for a raw `clear()`. `resetBrowserStorageForTests` is what resets the
 * IndexedDB-backed half.
 */
export function localStorageClearForTests(): void {
  try {
    localStorage.clear()
  }
  catch { /* ignore errors */ }
}

/**
 * The mirrored entry at `storedKey`, or undefined. FOR TESTS ONLY.
 *
 * The expiration is not readable as text any more -- a value is a structured
 * row, not a JSON envelope -- so a test that means to assert a TTL reads it
 * here rather than parsing something out of a store.
 */
export function mirrorEntryForTests(storedKey: string): { v: unknown, e: number } | undefined {
  return mirror.get(storedKey)
}

/**
 * Drop the mirror, the write queue and the cached connection. FOR TESTS ONLY.
 *
 * Synchronous, and it touches no database: see `resetKvForTests` for why
 * awaiting an in-flight flush here would hang the wrong test.
 */
export function resetBrowserStorageForTests(): void {
  resetKvForTests()
  mirror.clear()
  mirroredAccount = null
  hydrationToken++
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
export async function runCleanup(): Promise<void> {
  sweepStorage(sessionStorage, isRegisteredSessionKey)
  sweepLegacyLocalStorage()
  const deleted = await sweepKv(Date.now(), isRegisteredLocalKey)
  if (deleted.length === 0)
    return
  // Drop what the sweep deleted from the mirror, so a synchronous read cannot
  // serve a value that is no longer on disk, and tell the other tabs.
  const evicted = deleted.filter(key => mirror.delete(key))
  publishKvRemovals(evicted)
}

/**
 * Delete every `leapmux:` and `leapmux-` key left in localStorage.
 *
 * The family lives in IndexedDB now, so nothing in localStorage is registered
 * any more and no registration check is needed. Unconditional, and no values are
 * copied across: the local UI state, drafts and worker key pins a browser holds
 * are re-earned on the next load, and carrying them would mean maintaining a
 * translation between two layouts for the sake of one release.
 */
function sweepLegacyLocalStorage(): void {
  try {
    const doomed: string[] = []
    for (let i = 0; i < localStorage.length; i++) {
      const key = localStorage.key(i)
      if (key && (key.startsWith(NAMESPACE) || key.startsWith(RETIRED_NAMESPACE)))
        doomed.push(key)
    }
    for (const key of doomed)
      localStorage.removeItem(key)
  }
  catch { /* no localStorage here (Node, SSR): nothing to retire */ }
}

// ---------------------------------------------------------------------------
// Cross-tab changes
// ---------------------------------------------------------------------------

/** Subscribers to another tab's committed changes. */
const changeListeners = new Set<(storedKeys: ReadonlySet<string> | null) => void>()

/**
 * Run `listener` when ANOTHER tab changes a mirrored key.
 *
 * It receives the set of STORED keys, so a subscriber matches with
 * `storedKeyFor` exactly as the `storage` listener this replaced did -- and
 * therefore ignores another account's change for the same reason. `null` means
 * the whole store changed and every entry must answer for it, which is the
 * `event.key === null` case.
 *
 * This exists because IndexedDB raises no event of its own; the transport is a
 * BroadcastChannel. See `~/lib/browserStorageDb` for why the value travels in
 * the message rather than being re-read.
 */
export function onStorageChanged(listener: (storedKeys: ReadonlySet<string> | null) => void): () => void {
  changeListeners.add(listener)
  return () => changeListeners.delete(listener)
}

onKvBroadcast((changes) => {
  if (changes === null) {
    for (const listener of changeListeners)
      listener(null)
    return
  }
  const touched = new Set<string>()
  for (const change of changes) {
    // Only rows THIS tab mirrors. An unmirrored key, an unregistered key and
    // another ACCOUNT's key all fall out here -- matched on the key as stored,
    // which is the same rule a subscriber matches on.
    if (!mirror.has(change.k) && !isMirroredKey(change.k))
      continue
    if ('removed' in change)
      mirror.delete(change.k)
    else
      mirror.set(change.k, { v: change.v, e: change.e })
    touched.add(change.k)
  }
  if (touched.size > 0) {
    for (const listener of changeListeners)
      listener(touched)
  }
})

/**
 * Deliver a change notification as if another tab had sent one. FOR TESTS.
 *
 * The values are assumed to be in the mirror already, which is what a test that
 * wrote them through the ordinary accessors has. It exists so a SUBSCRIBER's
 * test can drive the callback synchronously and stay about the subscriber; the
 * transport itself -- the channel, the echo suppression, the key filtering --
 * is covered in this module's own tests.
 */
export function deliverStorageChangeForTests(storedKeys: ReadonlySet<string> | null): void {
  for (const listener of changeListeners)
    listener(storedKeys)
}

/** Whether `stored` is a key this tab's mirror is responsible for holding. */
function isMirroredKey(stored: string): boolean {
  const parsed = parseStoredKey(stored)
  if (parsed === null)
    return false
  const spec = specFor(parsed.name, LOCAL_INDEX)
  if (spec === null || spec.scope !== parsed.scope || spec.access !== 'sync')
    return false
  if (spec.scope === 'device')
    return true
  return mirroredAccount !== null && stored.startsWith(accountStorageKeyPrefix(mirroredAccount))
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
 * THE FIRST SWEEP IS DEFERRED, because `App` starts it in its own body: it
 * walks every key in the origin, and every key is partitioned per account, so a
 * browser several accounts have signed in to pays for all of them on the
 * critical path to first paint. The sessionStorage half and the legacy
 * localStorage pass are both SYNCHRONOUS main-thread walks, which is what makes
 * that cost land on the frame rather than behind it.
 *
 * Deferring is safe because the sweep reclaims SPACE and is not a correctness
 * gate. A read cannot see a value the sweep would have deleted: `readMirror` and
 * `readDynamic` check the same expiration and remove the entry themselves, and a
 * flat key from an earlier build has no name any accessor composes.
 */
export function initStorageCleanup(): () => void {
  // A latch, not a queue: a sweep that is still running has already read every
  // key the next one would, so starting a second is pure duplicate work -- and
  // two concurrent passes would each report the other's deletions as their own.
  let sweeping = false
  const sweep = (): void => {
    if (sweeping)
      return
    sweeping = true
    void runCleanup().finally(() => {
      sweeping = false
    })
  }
  const cancelFirst = whenIdle(sweep)
  const id = setInterval(sweep, CLEANUP_INTERVAL_MS)
  return () => {
    cancelFirst()
    clearInterval(id)
  }
}
