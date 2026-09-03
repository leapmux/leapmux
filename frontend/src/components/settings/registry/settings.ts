import type { CategoryId, SentinelShape, SettingBinding, SettingControl, SettingScope } from '../types'
import type { DualPreference, PreferencesState } from '~/context/PreferencesContext'
import {
  START_MINIMIZED_MINIMIZED,
  START_MINIMIZED_WINDOW,
  TRAY_ON_CLOSE_QUIT,
  TRAY_ON_CLOSE_TRAY,
  TRAY_ON_MINIMIZE_TASKBAR,
  TRAY_ON_MINIMIZE_TRAY,
} from '~/generated/contracts/desktop'
import { createLogger } from '~/lib/logger'
import { isMac } from '~/lib/shortcuts/platform'
import { isDesktopApp, isSoloMode } from '~/lib/systemInfo'
import { themeLabel, THEMES } from '~/styles/themes'
import { browserToggle, CUSTOM_EDITOR_OWNS_ITS_VALUE, dualFontHalf, dualScalar } from './bindings'
import { requestTerminalOsNotifications } from './terminalNotifications'

const log = createLogger('settingsRegistry')

/**
 * The operating system's own word for each surface that the Desktop rows
 * identify.
 *
 * macOS has a MENU BAR and a DOCK; Linux and Windows have a TRAY and a
 * TASKBAR. "Tray icon" on macOS identifies something the platform does not
 * have, so one wording cannot serve both -- that is the "one term per concept"
 * rule failing at the platform boundary, not a nicety. Each row keeps both
 * words in its `keywords`, unconditionally, so search still finds it.
 *
 * Resolved at MODULE scope, not per render: `getPlatform` caches a user-agent
 * read on first call and the answer cannot change while the page is open, so a
 * render-time accessor would buy nothing and would force `label` to become a
 * function on every declaration in this file. `CustomTitlebar.tsx` resolves
 * `isMacDesktop` the same way for the same reason.
 */
const TRAY_ICON_LABEL = isMac() ? 'Menu bar icon' : 'Tray icon'
const TRAY_SURFACE = isMac() ? 'menu bar' : 'tray'
const TRAY_HIDE_OPTION = isMac() ? 'Hide to the menu bar' : 'Hide to the tray'
const TRAY_KEEP_OPTION = isMac() ? 'Keep in the Dock' : 'Keep in the taskbar'

/**
 * Whether NEITHER tier of a dual boolean turns its feature on.
 *
 * The rule a dependent row hides by, written once. A rule on the RESOLVED value
 * alone takes the ACCOUNT default away from a user who turned the feature off
 * on this device, and that default is what their other devices obey -- the
 * mistake the turn-end volume row records as already made once. Three Desktop
 * rows depend on this, so the next one must not have to copy it correctly.
 */
function neitherTierEnables(pref: DualPreference<boolean>): boolean {
  return !pref.resolved() && !pref.account()
}

/**
 * What EVERY registry entry declares, whichever tier stores its value: the
 * row's identity, the text a user reads, its visibility rules, the sentinel
 * shape of its browser tier, and the binding that edits it.
 *
 * The two entry shapes beneath add what the hub cannot supply for them,
 * under one rule: the hub declares the SHAPE of every account key (see
 * `usersettings/keys.go`), so an account-backed entry must not restate it,
 * and a browser-only key exists nowhere else, so that entry states its own.
 */
interface SettingDeclBase {
  /** The row id: the panel anchor, the reset list, and the e2e locators. */
  id: string
  label: string
  help?: string
  keywords?: string[]
  scope: SettingScope
  /**
   * The sentinel shape of this entry's browser tier. An `account`-scope
   * entry has no browser tier; its sentinel is recorded for uniformity,
   * because the registry test walks every entry.
   */
  sentinel: SentinelShape
  /** A visibility rule that reads only the ENVIRONMENT (solo, desktop). */
  hidden?: () => boolean
  /**
   * A visibility rule that reads another PREFERENCE, beside the static
   * `hidden` that reads only the environment.
   *
   * The panels this registry replaced wrapped such rows in `<Show>`: the
   * font stacks appeared only while their tier was enabled, and the
   * turn-end volume only while a sound was selected. Without the rule a
   * user turns custom fonts off, still sees the stack editor, adds a
   * family, and nothing changes.
   */
  hiddenWhen?: (prefs: PreferencesState) => boolean
  /** Bind this entry's value/set/override state to the preferences context. */
  bind: (prefs: PreferencesState) => SettingBinding
}

/**
 * An entry whose value lives ONLY in this browser. Nothing else declares
 * it, so it states its own category, control and reset.
 *
 * `protoKey?: undefined` is what tells the two shapes apart, at a call site
 * and inside `createBrowserRows`. Writing it is never required; the type
 * REFUSES a key here, which is what makes the discrimination sound.
 */
export interface BrowserOnlySettingDecl extends SettingDeclBase {
  protoKey?: undefined
  category: CategoryId
  control: SettingControl
  restart?: boolean
  /**
   * Whether the hub refuses this row's writes on a session that did not
   * prove a factor recently. See `SettingDescriptor.needsElevation`; the
   * value passes straight through to the descriptor.
   *
   * An ACCOUNT-backed entry states none of the descriptor's shape, so it
   * cannot declare this either — the hub's own descriptor would have to
   * carry it. No account KEY is elevation-guarded today, so nothing is
   * missing; the custom account editors below are, and they are
   * browser-only entries.
   */
  needsElevation?: boolean
  /**
   * Return this entry's browser tier to its sentinel default.
   *
   * BROWSER-ONLY entries declare it. A `dual` entry does NOT: its binding
   * already builds exactly this action as `clearOverride`, and the two
   * copies already drifted into a pair that reset one font tier twice
   * and could reset the other never. `buildBrowserReset` takes the dual
   * action from the binding.
   */
  resetBrowser?: (prefs: PreferencesState) => void
}

/**
 * An entry backed by an ACCOUNT key that the hub declares.
 *
 * The hub ships a full descriptor for every account key on
 * `ListUserSettings`, so this entry states none of the value's shape — not
 * the category, not the control kind, not the enum values, not the numeric
 * limits. `createBrowserRows` reads all of that off the wire and joins it
 * to this declaration on `protoKey` + `protoField`. Restating that shape
 * here made a second source of truth that one golden file alone pinned to
 * Go, and a client which offers a value the hub's validator refuses stores
 * nothing and explains nothing.
 *
 * What stays here is what the wire does NOT carry: the text a user reads,
 * and the browser tier. `usersettings/keys.go` declares no field label and
 * no enum label, deliberately (see its `schema_golden_test.go`), so a row
 * driven only by the wire would render both font rows under one label and
 * an enum whose options have empty names.
 */
export interface AccountSettingDecl extends SettingDeclBase {
  /**
   * The account setting this entry edits: the wire KEY, and the field
   * inside it for an object-shaped key.
   *
   * It is the JOIN between this registry and the hub's descriptor. The
   * parity test in index.test.ts reads it, and `CLAIMED_PROTO_KEYS` derives
   * the claim set from it.
   *
   * The key and the field are DECLARED apart, never one dotted string that
   * a reader has to split: a settings key may itself contain a dot
   * (`captcha.altcha` on the hub scope), so splitting on the first one is
   * a rule that holds only until it does not. An absent `protoField`
   * addresses the key's scalar, which the wire identifies as `''`.
   */
  protoKey: string
  protoField?: string
  /**
   * The name a user reads for each value the hub's enum declares, by VALUE.
   *
   * A lookup, never an authority: the wire decides which values exist and
   * in what order, and a value with no entry here renders under its own
   * slug instead of an empty option. So a value the hub adds is offered as
   * soon as it is declared, and a value the hub drops takes its stale label
   * out of the dialog with it.
   */
  optionLabels?: Record<string, string>
  /**
   * The accessible name of a string-list row's add box and add button.
   *
   * `controlForField` has no per-list name to give — it answers for every
   * key of both scopes at once — so it builds a plain "Add". The two font
   * stacks render in one panel, and a shared name gives a screen-reader
   * user two identically named fields and two identically named buttons.
   */
  addLabel?: string
}

/** One entry of the browser registry: either shape. */
export type BrowserSettingDecl = BrowserOnlySettingDecl | AccountSettingDecl

export const browserSettings: BrowserSettingDecl[] = [
  // --- Appearance ---
  {
    id: 'appearance.theme',
    protoKey: 'theme',
    label: 'Theme',
    help: 'Color palette, and whether it follows the system or is pinned to light or dark.',
    // The palette NAMES are searchable too, so a user who knows what they want
    // can type it. The list comes from the catalogue rather than being restated,
    // so a palette added there is findable here with no second edit.
    keywords: ['dark', 'light', 'palette', 'color scheme', 'appearance', ...THEMES.map(t => t.label)],
    scope: 'dual',
    // No `optionLabels`: the wire field is a custom editor, not an enum, and
    // the palette list is the client's own (see ~/styles/themes).
    sentinel: 'nullable',
    bind: prefs => dualScalar(prefs.dual.theme),
  },
  {
    id: 'appearance.terminalTheme',
    protoKey: 'terminal_theme',
    label: 'Terminal theme',
    help: 'Colors for terminal tabs. Follows the app until you give it a palette of its own.',
    // `themeLabel(t, 'terminal')`, not the plain label: the surface labels
    // exist so Dimidium stays findable under a theme called Default, and the
    // row's own menu shows exactly that word. Indexing the plain label meant a
    // user who typed what the menu showed got no hit on this row.
    keywords: ['terminal', 'ansi', 'palette', 'match ui', ...THEMES.map(t => themeLabel(t, 'terminal'))],
    scope: 'dual',
    // No `optionLabels`: a custom editor, like `theme` above.
    sentinel: 'nullable',
    bind: prefs => dualScalar(prefs.dual.terminalTheme),
  },
  {
    id: 'appearance.syntaxTheme',
    protoKey: 'syntax_theme',
    label: 'Syntax theme',
    help: 'Colors for highlighted code. Follows the app until you give it a palette of its own. Changing it re-highlights, so code repaints as you scroll.',
    // `themeLabel(t, 'syntax')`, for the reason the terminal row above gives.
    keywords: ['syntax', 'highlight', 'code', 'palette', 'match ui', ...THEMES.map(t => themeLabel(t, 'syntax'))],
    scope: 'dual',
    sentinel: 'nullable',
    bind: prefs => dualScalar(prefs.dual.syntaxTheme),
  },
  {
    id: 'appearance.diffView',
    protoKey: 'diff_view',
    label: 'Diff view',
    help: 'How file diffs render in chat and the file viewer.',
    keywords: ['unified', 'split', 'side-by-side'],
    scope: 'dual',
    // The hub declares the VALUES; the name a user reads is the dialog's
    // alone. "split" reads as "Side by side" here because that is what the
    // layout does.
    optionLabels: { unified: 'Unified', split: 'Side by side' },
    sentinel: 'nullable',
    bind: prefs => dualScalar(prefs.dual.diffView),
  },
  {
    id: 'appearance.uiFonts',
    protoKey: 'ui_fonts',
    protoField: 'enabled',
    label: 'Custom UI fonts',
    help: 'Replace the interface font stack.',
    scope: 'dual',
    sentinel: 'nullable',
    bind: prefs => dualFontHalf('enabled', prefs.dual.uiFonts),
  },
  {
    id: 'appearance.uiFontStack',
    protoKey: 'ui_fonts',
    protoField: 'fonts',
    label: 'UI font stack',
    help: 'Custom UI font families, in priority order.',
    scope: 'dual',
    // The add box and the add button take this string as their accessible
    // name, and the monospace stack renders the same pair in the same
    // panel. A shared "Add font" gave a screen-reader user two identically
    // named fields and two identically named buttons.
    addLabel: 'Add UI font',
    sentinel: 'nullable',
    hiddenWhen: prefs => !prefs.uiFonts().enabled,
    bind: prefs => dualFontHalf('fonts', prefs.dual.uiFonts),
  },
  {
    id: 'appearance.monoFonts',
    protoKey: 'mono_fonts',
    protoField: 'enabled',
    label: 'Custom monospace fonts',
    help: 'Replace the terminal and code font stack.',
    scope: 'dual',
    sentinel: 'nullable',
    bind: prefs => dualFontHalf('enabled', prefs.dual.monoFonts),
  },
  {
    id: 'appearance.monoFontStack',
    protoKey: 'mono_fonts',
    protoField: 'fonts',
    label: 'Monospace font stack',
    help: 'Custom monospace families, in priority order.',
    scope: 'dual',
    addLabel: 'Add monospace font',
    sentinel: 'nullable',
    hiddenWhen: prefs => !prefs.monoFonts().enabled,
    bind: prefs => dualFontHalf('fonts', prefs.dual.monoFonts),
  },

  // --- Notifications ---
  {
    id: 'notifications.turnEndSound',
    protoKey: 'turn_end_sound',
    label: 'Turn-end sound',
    help: 'Sound played when an agent turn finishes.',
    keywords: ['volume', 'ding', 'dong', 'alert'],
    scope: 'dual',
    optionLabels: { 'none': 'None', 'ding-dong': 'Ding dong' },
    sentinel: 'nullable',
    bind: prefs => dualScalar(prefs.dual.turnEndSound),
  },
  {
    id: 'notifications.turnEndSoundVolume',
    protoKey: 'turn_end_sound_volume',
    label: 'Turn-end volume',
    help: 'Playback volume for the turn-end sound.',
    scope: 'dual',
    sentinel: 'nullable',
    // The row edits BOTH tiers, so it hides only when NEITHER can play a
    // sound. A rule on the RESOLVED value alone took the account default's
    // volume away from a user who muted the sound on THIS device, and that
    // volume is what their other devices play at.
    hiddenWhen: prefs => prefs.dual.turnEndSound.resolved() === 'none'
      && prefs.dual.turnEndSound.account() === 'none',
    bind: prefs => dualScalar(prefs.dual.turnEndSoundVolume),
  },
  {
    id: 'notifications.terminalOsNotifications',
    category: 'notifications',
    label: 'Terminal OS notifications',
    help: 'Show desktop notifications for terminal alerts (OSC 9 / 777 / 99).',
    scope: 'browser',
    control: { kind: 'toggle' },
    sentinel: 'default-off',
    bind: (prefs) => {
      const read = prefs.terminalOsNotifications
      const write = (v: boolean) => {
        if (!v) {
          // Turning OFF stores false and must NOT prompt. The branch is
          // here, not behind the request helper: re-asking for a
          // permission the user is in the act of switching off is prompt
          // fatigue, and a denied origin answers from its own sticky
          // decision, which would flip the toggle back on.
          prefs.setTerminalOsNotifications(false)
          return
        }
        // The ON path asks the OS, and `requestTerminalOsNotifications`
        // holds that call so it stays testable without a render.
        //
        // The `.catch` is required. `browserToggle.set` returns void, so
        // `SettingRow` never sees this promise and its own catch cannot
        // report the failure: a rejected permission request left the
        // toggle ON with nothing behind it, and the rejection went
        // unhandled.
        void requestTerminalOsNotifications()
          .then((granted) => {
            prefs.setTerminalOsNotifications(granted)
          })
          .catch((err: unknown) => {
            log.warn('notification permission request failed', err)
            prefs.setTerminalOsNotifications(false)
          })
      }
      return browserToggle(read, write)
    },
    resetBrowser: prefs => prefs.setTerminalOsNotifications(false),
  },

  // --- Chat ---
  {
    id: 'chat.expandAgentThoughts',
    category: 'chat',
    label: 'Expand agent thoughts',
    help: 'Start thinking and reasoning bubbles expanded.',
    scope: 'browser',
    control: { kind: 'toggle' },
    sentinel: 'default-on',
    bind: prefs => browserToggle(prefs.expandAgentThoughts, prefs.setExpandAgentThoughts),
    resetBrowser: prefs => prefs.setExpandAgentThoughts(true),
  },
  {
    id: 'chat.showHiddenMessages',
    category: 'chat',
    label: 'Show hidden messages',
    help: 'Developer feature: render messages the UI normally folds away.',
    scope: 'browser',
    control: { kind: 'toggle' },
    sentinel: 'default-off',
    bind: prefs => browserToggle(prefs.showHiddenMessages, prefs.setShowHiddenMessages),
    resetBrowser: prefs => prefs.setShowHiddenMessages(false),
  },
  {
    id: 'chat.enterKeyMode',
    category: 'chat',
    label: 'Enter key behavior',
    help: 'Whether Enter sends the message or inserts a newline.',
    scope: 'browser',
    control: { kind: 'enum', options: [
      { value: 'enter-sends', label: 'Enter sends' },
      { value: 'cmd-enter-sends', label: 'Cmd/Ctrl+Enter sends' },
    ] },
    sentinel: 'nullable',
    bind: prefs => ({
      value: prefs.enterKeyMode,
      set: v => prefs.setEnterKeyMode(v as 'enter-sends' | 'cmd-enter-sends'),
    }),
    resetBrowser: prefs => prefs.setEnterKeyMode(null),
  },
  {
    id: 'chat.showComposerStatusBar',
    category: 'chat',
    label: 'Composer status bar',
    help: 'Branch / model / effort chips beneath the message input.',
    scope: 'browser',
    control: { kind: 'toggle' },
    sentinel: 'default-on',
    bind: prefs => browserToggle(prefs.showComposerStatusBar, prefs.setShowComposerStatusBar),
    resetBrowser: prefs => prefs.setShowComposerStatusBar(true),
  },

  // --- Terminal ---
  {
    id: 'terminal.renderer',
    category: 'terminal',
    label: 'Terminal renderer',
    help: 'GPU-accelerated (WebGL) or DOM (canvas) text rendering.',
    scope: 'browser',
    control: { kind: 'enum', options: [
      { value: 'auto', label: 'Auto' },
      { value: 'webgl', label: 'WebGL' },
      { value: 'canvas', label: 'Canvas' },
    ] },
    sentinel: 'nullable',
    bind: prefs => ({
      value: prefs.terminalRenderer,
      set: v => prefs.setTerminalRenderer(v as 'auto' | 'webgl' | 'canvas'),
    }),
    resetBrowser: prefs => prefs.setTerminalRenderer(null),
  },

  // --- Desktop ---
  //
  // Every row here declares `hidden` outside the desktop app, so the whole
  // section disappears in a browser (`occupiedNavGroups` drops a group with no
  // visible rows). The dependent rows use `hiddenWhen` rather than a disabled
  // control: without it a user turns the tray off, still sees "when you close
  // the window", picks Quit, and nothing changes.
  {
    id: 'desktop.trayEnabled',
    protoKey: 'tray_enabled',
    label: TRAY_ICON_LABEL,
    help: `Show a LeapMux icon in the ${TRAY_SURFACE}. The icon keeps LeapMux available when no window is open.`,
    // BOTH platform words, whichever the label shows. A user who knows the
    // feature from Windows types "tray" on a Mac and must still find the row.
    keywords: ['tray', 'menu bar', 'system tray', 'status icon', 'notification area', 'background'],
    scope: 'dual',
    sentinel: 'nullable',
    hidden: () => !isDesktopApp(),
    bind: prefs => dualScalar(prefs.dual.trayEnabled),
  },
  {
    id: 'desktop.trayOnClose',
    protoKey: 'tray_on_close',
    label: 'When you close the window',
    help: 'Select what LeapMux does when you close the last window.',
    keywords: ['close', 'quit', 'exit', 'tray', 'menu bar', 'background'],
    scope: 'dual',
    optionLabels: { [TRAY_ON_CLOSE_TRAY]: TRAY_HIDE_OPTION, [TRAY_ON_CLOSE_QUIT]: 'Quit LeapMux' },
    sentinel: 'nullable',
    hidden: () => !isDesktopApp(),
    hiddenWhen: prefs => neitherTierEnables(prefs.dual.trayEnabled),
    bind: prefs => dualScalar(prefs.dual.trayOnClose),
  },
  {
    id: 'desktop.trayOnMinimize',
    protoKey: 'tray_on_minimize',
    label: 'When you minimize the window',
    help: 'Select what LeapMux does when you minimize the window.',
    keywords: ['minimize', 'taskbar', 'dock', 'tray', 'menu bar', 'hide'],
    scope: 'dual',
    optionLabels: { [TRAY_ON_MINIMIZE_TRAY]: TRAY_HIDE_OPTION, [TRAY_ON_MINIMIZE_TASKBAR]: TRAY_KEEP_OPTION },
    sentinel: 'nullable',
    hidden: () => !isDesktopApp(),
    hiddenWhen: prefs => neitherTierEnables(prefs.dual.trayEnabled),
    bind: prefs => dualScalar(prefs.dual.trayOnMinimize),
  },
  {
    id: 'desktop.startOnLogin',
    protoKey: 'start_on_login',
    label: 'Start at login',
    help: 'Start LeapMux when you sign in to the computer.',
    keywords: ['login', 'startup', 'autostart', 'start up', 'launch', 'boot', 'login item'],
    scope: 'dual',
    sentinel: 'nullable',
    hidden: () => !isDesktopApp(),
    bind: prefs => dualScalar(prefs.dual.startOnLogin),
  },
  {
    id: 'desktop.startMinimized',
    protoKey: 'start_minimized',
    label: 'Window at login',
    help: `Select if LeapMux shows a window when it starts at login. If the ${TRAY_SURFACE} icon is on, LeapMux starts in the ${TRAY_SURFACE}. If it is off, LeapMux starts minimized.`,
    keywords: ['login', 'startup', 'minimized', 'hidden', 'background', 'window'],
    scope: 'dual',
    optionLabels: { [START_MINIMIZED_WINDOW]: 'Show the window', [START_MINIMIZED_MINIMIZED]: 'Hide the window' },
    sentinel: 'nullable',
    hidden: () => !isDesktopApp(),
    // Over `start_on_login`, not the tray: this row governs the login launch
    // alone, so it means nothing while neither tier starts LeapMux at login.
    hiddenWhen: prefs => neitherTierEnables(prefs.dual.startOnLogin),
    bind: prefs => dualScalar(prefs.dual.startMinimized),
  },

  // --- Files & editors ---
  {
    id: 'files.preferredEditor',
    category: 'files',
    label: 'Preferred editor',
    help: 'Editor the "Open in …" button launches (desktop only).',
    scope: 'browser',
    control: { kind: 'text', placeholder: 'e.g. vscode' },
    sentinel: 'nullable',
    hidden: () => !isDesktopApp(),
    bind: prefs => ({
      value: prefs.preferredEditorId,
      set: v => prefs.setPreferredEditorId(typeof v === 'string' && v !== '' ? v : undefined),
    }),
    resetBrowser: prefs => prefs.setPreferredEditorId(undefined),
  },
  {
    id: 'files.revealAfterDownload',
    category: 'files',
    label: 'Reveal after download',
    help: 'Open the OS file manager on the saved file after a download (desktop only).',
    scope: 'browser',
    control: { kind: 'toggle' },
    sentinel: 'default-on',
    hidden: () => !isDesktopApp(),
    bind: prefs => browserToggle(prefs.revealAfterDownload, prefs.setRevealAfterDownload),
    resetBrowser: prefs => prefs.setRevealAfterDownload(true),
  },
  {
    id: 'files.directoryPickerHiddenFiles',
    category: 'files',
    label: 'Hidden files in directory picker',
    help: 'Show dotfiles in the working-directory picker.',
    scope: 'browser',
    control: { kind: 'toggle' },
    sentinel: 'default-on',
    bind: prefs => browserToggle(prefs.directoryPickerShowHidden, prefs.setDirectoryPickerShowHidden),
    resetBrowser: prefs => prefs.setDirectoryPickerShowHidden(true),
  },

  // --- Shortcuts ---
  {
    id: 'shortcuts.keybindings',
    protoKey: 'keybindings',
    label: 'Keyboard shortcuts',
    help: 'Per-command keybinding overrides, saved to your account.',
    scope: 'account',
    sentinel: 'nullable',
    bind: prefs => ({
      value: prefs.customKeybindings,
      set: v => prefs.setCustomKeybindings(v as Parameters<typeof prefs.setCustomKeybindings>[0]),
      customized: () => prefs.accountCustomized().keybindings === true,
      reset: () => prefs.resetUserSetting('keybindings'),
    }),
  },

  // --- Advanced ---
  {
    id: 'advanced.debugLogging',
    protoKey: 'debug_logging',
    label: 'Debug logging',
    help: 'Verbose client-side diagnostic logging.',
    scope: 'dual',
    sentinel: 'nullable',
    bind: prefs => dualScalar(prefs.dual.debugLogging),
  },
  {
    id: 'advanced.keyPins',
    category: 'advanced',
    label: 'Trusted worker keys',
    help: 'Worker public keys pinned on first use (TOFU).',
    scope: 'browser',
    control: { kind: 'custom', id: 'keyPins' },
    sentinel: 'nullable',
    // The key-pin store is trust state, not a preference override: the
    // "reset all browser overrides" row deliberately leaves it alone, so this
    // entry declares no resetBrowser.
    bind: () => CUSTOM_EDITOR_OWNS_ITS_VALUE,
  },
  {
    id: 'advanced.resetBrowserOverrides',
    category: 'advanced',
    label: 'Reset all browser overrides',
    help: 'Return every this-device preference to its default (account defaults and trusted keys are kept).',
    scope: 'browser',
    control: { kind: 'action', label: 'Reset overrides', danger: true },
    sentinel: 'nullable',
    bind: prefs => ({
      value: () => null,
      set: () => {
        // ONE document write for the whole run. Each reset is otherwise a
        // full read, parse, serialize and write of the consolidated
        // preferences, and one `storage` event per field for the other
        // tabs.
        prefs.batchBrowserPrefWrites(() => {
          for (const action of buildBrowserReset(prefs))
            action.reset()
        })
      },
    }),
  },

  // --- Account ---
  //
  // ONE ROW PER CONCERN, each with its own custom editor. They were a single
  // "Account" row whose editor drew its own <h3> headings inside it, so the
  // panel carried three label styles at once and printed "Command-line
  // credentials" twice — once as the row's label and once as a heading inside
  // it. A row per concern gives the panel the same vocabulary as every other
  // group: the row supplies the label, the help and the separator.
  //
  // `needsElevation` is declared per row and not per group, because the group
  // is genuinely mixed: a name is not a credential and moves no recovery
  // identity, and revoking a command-line credential can only REDUCE access
  // (demanding a fresh factor from somebody who believes one is stolen is the
  // wrong failure mode). The other four move a durable identity, and the hub
  // refuses each without a recently proven factor.
  //
  // FOUR of the five are hidden in solo mode, because the hub refuses the RPCs
  // behind them: solo authenticates every request as the local user, so it
  // offers no sign-up, no passkey, no account recovery and no provider link.
  // Password is the exception, and the section exists in solo for it alone.
  {
    id: 'account.profile',
    category: 'account',
    label: 'Profile',
    help: 'The username other people address you by, and the name the app shows.',
    keywords: ['username', 'display name', 'profile', 'name'],
    scope: 'account',
    control: { kind: 'custom', id: 'accountProfile' },
    sentinel: 'nullable',
    hidden: () => isSoloMode(),
    bind: () => CUSTOM_EDITOR_OWNS_ITS_VALUE,
  },
  {
    id: 'account.email',
    category: 'account',
    label: 'Email',
    help: 'The address that receives the account-recovery link, and how to change it.',
    keywords: ['email', 'address', 'verify', 'recovery'],
    scope: 'account',
    control: { kind: 'custom', id: 'accountEmail' },
    sentinel: 'nullable',
    needsElevation: true,
    hidden: () => isSoloMode(),
    bind: () => CUSTOM_EDITOR_OWNS_ITS_VALUE,
  },
  {
    id: 'account.password',
    category: 'account',
    label: 'Password',
    help: 'Set or change the password you sign in with.',
    keywords: ['password', 'credential', 'sign in'],
    scope: 'account',
    control: { kind: 'custom', id: 'accountPassword' },
    sentinel: 'nullable',
    needsElevation: true,
    /*
     * The ONE account row a solo hub keeps, and it keeps it whether or not a
     * password exists yet: the editor sets the first one and replaces a stored
     * one through the same RPC. Its four neighbours stay hidden, because solo
     * refuses the RPCs behind them; ChangePassword is reachable there, because
     * the password is what lets the hub answer on a network address at all.
     *
     * Administration → Network access asks for the first password as well,
     * beside the addresses it guards, and that is not a duplicate: publishing
     * an address without one would put the whole app behind nothing, so the
     * panel must ask there rather than send the operator away mid-edit. Both
     * surfaces write through `userClient.changePassword`, and each re-reads
     * what the other one moved.
     */
    bind: () => CUSTOM_EDITOR_OWNS_ITS_VALUE,
  },
  {
    id: 'account.passkeys',
    category: 'account',
    label: 'Passkeys',
    help: 'The passkeys registered to this account, and how to add or remove one.',
    keywords: ['passkey', 'webauthn', 'security key', 'fido'],
    scope: 'account',
    control: { kind: 'custom', id: 'accountPasskeys' },
    sentinel: 'nullable',
    needsElevation: true,
    hidden: () => isSoloMode(),
    bind: () => CUSTOM_EDITOR_OWNS_ITS_VALUE,
  },
  {
    id: 'account.linkedProviders',
    category: 'account',
    label: 'Linked accounts',
    help: 'The identity providers you sign in through, and how to detach one.',
    keywords: ['oauth', 'provider', 'github', 'link', 'unlink', 'sso'],
    scope: 'account',
    control: { kind: 'custom', id: 'accountLinkedProviders' },
    sentinel: 'nullable',
    needsElevation: true,
    hidden: () => isSoloMode(),
    bind: () => CUSTOM_EDITOR_OWNS_ITS_VALUE,
  },
  /*
   * The APPS section, one errand in two halves: what this account AUTHORIZED
   * first, then what it REGISTERED. The authorized half leads because it is
   * the one an ordinary account returns to -- "what can reach my account" --
   * while registering is the developer's afterthought.
   */
  {
    id: 'account.connectedApps',
    category: 'apps',
    label: 'Connected apps',
    help: 'Apps you authorized, what each one may do, and how to disconnect it.',
    keywords: ['app', 'oauth', 'cli', 'token', 'device', 'revoke', 'disconnect', 'permission', 'scope'],
    scope: 'account',
    control: { kind: 'custom', id: 'accountConnectedApps' },
    sentinel: 'nullable',
    // NOT hidden in solo. A solo hub authorizes apps like any other -- the
    // solo rung yields to a presented bearer, so a scoped credential binds
    // there too -- and hiding this would leave a solo user unable to see or
    // disconnect what they authorized.
    bind: () => CUSTOM_EDITOR_OWNS_ITS_VALUE,
  },
  {
    id: 'apps.registrations',
    category: 'apps',
    label: 'App registrations',
    help: 'Apps registered on this hub. Yours are visible to you alone; an administrator\'s are visible to everybody.',
    keywords: ['app', 'oauth', 'register', 'client', 'redirect', 'secret', 'developer'],
    scope: 'account',
    control: { kind: 'custom', id: 'accountAppRegistrations' },
    sentinel: 'nullable',
    // ELEVATED, unlike Connected apps above it, and the asymmetry is the
    // point. Disconnecting an app only reduces access. Editing a registration
    // rewrites where a consent redirects, so it diverts an in-flight
    // authorization code to an address the editor chose -- the most dangerous
    // write in the feature. The hub enforces the same rule on every write verb
    // (see appWriteGate); this makes the dialog ask for the factor first,
    // instead of letting somebody fill a form and lose it to a refusal.
    needsElevation: true,
    bind: () => CUSTOM_EDITOR_OWNS_ITS_VALUE,
  },
]

export interface BrowserResetAction {
  /** The setting id the action resets. */
  id: string
  /** The sentinel shape the reset honors. */
  sentinel: SentinelShape
  /** Execute the reset: return the browser tier to its sentinel default. */
  reset: () => void
}

/**
 * The reset operations behind "Reset all browser overrides", each
 * respecting its sentinel shape:
 *
 * - `nullable` tiers clear the override (delete the key), restoring the
 *   account default;
 * - `default-on` tiers are reset to true, which their setters store as a
 *   DELETED key (absent means true — storing `true` would be an override in
 *   the opposite direction);
 * - `default-off` tiers are reset to false, likewise deleting the key.
 *
 * A DUAL entry's action comes from its own binding's `clearOverride`, not
 * from a second copy on the entry. That is the same operation, and the
 * copies drifted. It also makes the action ONE PER KEY rather than one
 * per row: a font tier renders its toggle and its stack as two rows over
 * one override document, so a per-row list cleared `ui_fonts` twice and
 * misstated what it does.
 *
 * Pure in the sense that it only closes over the given context: tests pass a
 * fake prefs object and observe which setters ran.
 */
export function buildBrowserReset(prefs: PreferencesState): BrowserResetAction[] {
  const actions: BrowserResetAction[] = []
  const clearedKeys = new Set<string>()
  for (const decl of browserSettings) {
    // An ACCOUNT-BACKED entry, keyed and deduplicated by its proto key. An
    // account-only entry (keybindings) has no browser tier, so its binding
    // builds no `clearOverride` and it drops out here.
    if (decl.protoKey !== undefined) {
      if (clearedKeys.has(decl.protoKey))
        continue
      const clearOverride = decl.bind(prefs).clearOverride
      if (clearOverride === undefined)
        continue
      clearedKeys.add(decl.protoKey)
      actions.push({ id: decl.id, sentinel: decl.sentinel, reset: clearOverride })
      continue
    }
    const resetBrowser = decl.resetBrowser
    if (resetBrowser === undefined)
      continue
    actions.push({ id: decl.id, sentinel: decl.sentinel, reset: () => resetBrowser(prefs) })
  }
  return actions
}
