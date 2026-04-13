import type { ParentComponent } from 'solid-js'
import type { ThemePreference } from '~/app'
import type { BrowserPreferences, EnterKeyMode } from '~/lib/browserStorage'
import type { UserKeybindingOverride } from '~/lib/shortcuts/types'
import type { TerminalThemePreference } from '~/lib/terminal'
import { createContext, createEffect, createSignal, onMount, useContext } from 'solid-js'
import { userClient } from '~/api/clients'
import { DiffView, TurnEndSound } from '~/generated/leapmux/v1/user_pb'
import { KEY_BROWSER_PREFS, loadBrowserPrefs, safeSetJson } from '~/lib/browserStorage'
import { setDebugEnabled } from '~/lib/logger'

const DEFAULT_MONO_FONT_FAMILY = '"Hack NF", Hack, "SF Mono", Consolas, monospace'

export type DiffViewPreference = 'unified' | 'split'
export type TurnEndSoundPreference = 'none' | 'ding-dong'

interface PreferencesState {
  /** Resolved theme preference (localStorage override → account default → hardcoded default). */
  theme: () => ThemePreference
  /** Resolved terminal theme preference. */
  terminalTheme: () => TerminalThemePreference
  /** Whether UI custom fonts are enabled (resolved). */
  uiFontCustomEnabled: () => boolean
  /** Whether mono custom fonts are enabled (resolved). */
  monoFontCustomEnabled: () => boolean
  /** Account-level UI font list. */
  uiFonts: () => string[]
  /** Account-level mono font list. */
  monoFonts: () => string[]
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
  /** Whether hidden messages are shown in the chat view (developer feature). */
  showHiddenMessages: () => boolean
  setShowHiddenMessages: (value: boolean) => void
  /** Resolved enter key mode. */
  enterKeyMode: () => EnterKeyMode
  setEnterKeyMode: (value: EnterKeyMode) => void
  /** Custom keybinding overrides (account-level, stored in Hub DB). */
  customKeybindings: () => UserKeybindingOverride[]
  setCustomKeybindings: (value: UserKeybindingOverride[]) => void

  // --- Browser-level overrides (localStorage) ---
  /** Raw browser-level theme override. null means "use account default". */
  browserTheme: () => ThemePreference | null
  setBrowserTheme: (value: ThemePreference | null) => void
  browserTerminalTheme: () => TerminalThemePreference | null
  setBrowserTerminalTheme: (value: TerminalThemePreference | null) => void
  browserDiffView: () => DiffViewPreference | null
  setBrowserDiffView: (value: DiffViewPreference | null) => void
  browserTurnEndSound: () => TurnEndSoundPreference | null
  setBrowserTurnEndSound: (value: TurnEndSoundPreference | null) => void
  browserTurnEndSoundVolume: () => number | null
  setBrowserTurnEndSoundVolume: (value: number | null) => void
  browserDebugLogging: () => boolean | null
  setBrowserDebugLogging: (value: boolean | null) => void

  // --- Account-level setters (Hub DB) ---
  /** Account-level theme default. */
  accountTheme: () => ThemePreference
  accountTerminalTheme: () => TerminalThemePreference
  accountUiFontCustomEnabled: () => boolean
  accountMonoFontCustomEnabled: () => boolean
  accountDiffView: () => DiffViewPreference
  accountTurnEndSound: () => TurnEndSoundPreference
  accountTurnEndSoundVolume: () => number
  accountDebugLogging: () => boolean

  setAccountTheme: (value: ThemePreference) => void
  setAccountTerminalTheme: (value: TerminalThemePreference) => void
  setAccountUiFontCustomEnabled: (enabled: boolean) => void
  setAccountMonoFontCustomEnabled: (enabled: boolean) => void
  setAccountDiffView: (value: DiffViewPreference) => void
  setAccountTurnEndSound: (value: TurnEndSoundPreference) => void
  setAccountTurnEndSoundVolume: (value: number) => void
  setAccountDebugLogging: (value: boolean) => void
  setUiFonts: (fonts: string[]) => void
  setMonoFonts: (fonts: string[]) => void
  /** Save current account preferences to Hub. */
  saveAccountPreferences: () => Promise<void>
  /** Reload account preferences from Hub. */
  reload: () => Promise<void>
}

const PreferencesContext = createContext<PreferencesState>()

/** Build a CSS font-family string by quoting each font name and joining with commas. */
function buildFontFamily(fonts: string[]): string {
  return fonts.map(f => `"${f}"`).join(', ')
}

function diffViewFromProto(dv: DiffView): DiffViewPreference {
  return dv === DiffView.SPLIT ? 'split' : 'unified'
}

function diffViewToProto(dv: DiffViewPreference): DiffView {
  return dv === 'split' ? DiffView.SPLIT : DiffView.UNIFIED
}

function turnEndSoundFromProto(tes: TurnEndSound): TurnEndSoundPreference {
  return tes === TurnEndSound.NONE ? 'none' : 'ding-dong'
}

function turnEndSoundToProto(tes: TurnEndSoundPreference): TurnEndSound {
  return tes === 'ding-dong' ? TurnEndSound.DING_DONG : TurnEndSound.NONE
}

/** Update a single field in the consolidated browser preferences. */
function updateBrowserPref(key: keyof BrowserPreferences, value: BrowserPreferences[keyof BrowserPreferences] | undefined): void {
  const prefs = loadBrowserPrefs()
  if (value === undefined) {
    delete prefs[key]
  }
  else {
    (prefs as Record<string, unknown>)[key] = value
  }
  safeSetJson(KEY_BROWSER_PREFS, prefs)
}

export const PreferencesProvider: ParentComponent = (props) => {
  // --- Account-level (Hub DB) ---
  const [accountTheme, setAccountTheme] = createSignal<ThemePreference>('system')
  const [accountTerminalTheme, setAccountTerminalTheme] = createSignal<TerminalThemePreference>('match-ui')
  const [accountUiFontCustomEnabled, setAccountUiFontCustomEnabled] = createSignal(false)
  const [accountMonoFontCustomEnabled, setAccountMonoFontCustomEnabled] = createSignal(false)
  const [uiFonts, setUiFonts] = createSignal<string[]>([])
  const [monoFonts, setMonoFonts] = createSignal<string[]>([])
  const [accountDiffView, setAccountDiffView] = createSignal<DiffViewPreference>('unified')
  const [accountTurnEndSound, setAccountTurnEndSound] = createSignal<TurnEndSoundPreference>('ding-dong')
  const [accountTurnEndSoundVolume, setAccountTurnEndSoundVolume] = createSignal<number>(100)
  const [accountDebugLogging, setAccountDebugLogging] = createSignal(false)

  // --- Browser-level (localStorage) --- load once from consolidated key
  const initialPrefs = loadBrowserPrefs()

  const [browserTheme, setBrowserThemeSignal] = createSignal<ThemePreference | null>(
    (initialPrefs.theme as ThemePreference) ?? null,
  )
  const [browserTerminalTheme, setBrowserTerminalThemeSignal] = createSignal<TerminalThemePreference | null>(
    (initialPrefs.terminalTheme as TerminalThemePreference) ?? null,
  )

  const setBrowserTheme = (value: ThemePreference | null) => {
    setBrowserThemeSignal(value)
    updateBrowserPref('theme', value ?? undefined)
    // Notify app.tsx for instant reactivity
    const setter = (window as any).__leapmux_setTheme
    if (setter) {
      setter(value ?? accountTheme())
    }
  }

  const setBrowserTerminalTheme = (value: TerminalThemePreference | null) => {
    setBrowserTerminalThemeSignal(value)
    updateBrowserPref('terminalTheme', value ?? undefined)
  }

  const [browserDiffView, setBrowserDiffViewSignal] = createSignal<DiffViewPreference | null>(
    (initialPrefs.diffView as DiffViewPreference) ?? null,
  )

  const setBrowserDiffView = (value: DiffViewPreference | null) => {
    setBrowserDiffViewSignal(value)
    updateBrowserPref('diffView', value ?? undefined)
  }

  const [browserTurnEndSound, setBrowserTurnEndSoundSignal] = createSignal<TurnEndSoundPreference | null>(
    (initialPrefs.turnEndSound as TurnEndSoundPreference) ?? null,
  )

  const setBrowserTurnEndSound = (value: TurnEndSoundPreference | null) => {
    setBrowserTurnEndSoundSignal(value)
    updateBrowserPref('turnEndSound', value ?? undefined)
  }

  const [browserTurnEndSoundVolume, setBrowserTurnEndSoundVolumeSignal] = createSignal<number | null>(
    initialPrefs.turnEndSoundVolume ?? null,
  )

  const setBrowserTurnEndSoundVolume = (value: number | null) => {
    setBrowserTurnEndSoundVolumeSignal(value)
    updateBrowserPref('turnEndSoundVolume', value ?? undefined)
  }

  const [browserDebugLogging, setBrowserDebugLoggingSignal] = createSignal<boolean | null>(
    initialPrefs.debugLogging ?? null,
  )

  const setBrowserDebugLogging = (value: boolean | null) => {
    setBrowserDebugLoggingSignal(value)
    updateBrowserPref('debugLogging', value ?? undefined)
  }

  // --- Browser-only preferences ---
  const [showHiddenMessages, setShowHiddenMessagesSignal] = createSignal(
    initialPrefs.showHiddenMessages === true,
  )
  const setShowHiddenMessages = (value: boolean) => {
    setShowHiddenMessagesSignal(value)
    updateBrowserPref('showHiddenMessages', value || undefined)
  }

  const [enterKeyMode, setEnterKeyModeSignal] = createSignal<EnterKeyMode>(
    initialPrefs.enterKeyMode ?? 'cmd-enter-sends',
  )
  const setEnterKeyMode = (value: EnterKeyMode) => {
    setEnterKeyModeSignal(value)
    updateBrowserPref('enterKeyMode', value)
  }

  const [customKeybindings, setCustomKeybindingsSignal] = createSignal<UserKeybindingOverride[]>([])

  // --- Resolved values (browser override → account default → hardcoded) ---
  const theme = (): ThemePreference => browserTheme() ?? accountTheme()
  const terminalTheme = (): TerminalThemePreference => browserTerminalTheme() ?? accountTerminalTheme()
  const uiFontCustomEnabled = () => accountUiFontCustomEnabled()
  const monoFontCustomEnabled = () => accountMonoFontCustomEnabled()
  const diffView = (): DiffViewPreference => browserDiffView() ?? accountDiffView()
  const turnEndSound = (): TurnEndSoundPreference => browserTurnEndSound() ?? accountTurnEndSound()
  const turnEndSoundVolume = (): number => browserTurnEndSoundVolume() ?? accountTurnEndSoundVolume()
  const debugLogging = (): boolean => browserDebugLogging() ?? accountDebugLogging()

  const monoFontFamily = () => {
    if (!monoFontCustomEnabled() || monoFonts().length === 0) {
      return DEFAULT_MONO_FONT_FAMILY
    }
    return `${buildFontFamily(monoFonts())}, ${DEFAULT_MONO_FONT_FAMILY}`
  }

  const uiFontFamily = () => {
    if (!uiFontCustomEnabled() || uiFonts().length === 0) {
      return undefined
    }
    return buildFontFamily(uiFonts())
  }

  const reload = async () => {
    try {
      const resp = await userClient.getPreferences({})
      if (resp.preferences) {
        const p = resp.preferences
        setAccountTheme((p.theme || 'system') as ThemePreference)
        setAccountTerminalTheme((p.terminalTheme || 'match-ui') as TerminalThemePreference)
        setAccountUiFontCustomEnabled(p.uiFontCustomEnabled)
        setAccountMonoFontCustomEnabled(p.monoFontCustomEnabled)
        setUiFonts(p.uiFonts)
        setMonoFonts(p.monoFonts)
        setAccountDiffView(diffViewFromProto(p.diffView))
        setAccountTurnEndSound(turnEndSoundFromProto(p.turnEndSound))
        setAccountTurnEndSoundVolume(p.turnEndSoundVolume ?? 100)
        setAccountDebugLogging(p.debugLogging)
        if (p.customKeybindingsJson) {
          try {
            const parsed = JSON.parse(p.customKeybindingsJson)
            setCustomKeybindingsSignal(Array.isArray(parsed) ? parsed as UserKeybindingOverride[] : [])
          }
          catch {
            setCustomKeybindingsSignal([])
          }
        }
        else {
          setCustomKeybindingsSignal([])
        }
      }
    }
    catch {
      // Ignore errors on load
    }
  }

  // Serialize saves so concurrent callers don't clobber each other.
  // If a save is requested while one is in flight, we queue a re-save
  // that will read the latest signal values when it runs.
  let saveInFlight = false
  let saveQueued = false

  const saveAccountPreferences = async () => {
    if (saveInFlight) {
      saveQueued = true
      return
    }
    saveInFlight = true
    try {
      await userClient.updatePreferences({
        theme: accountTheme(),
        terminalTheme: accountTerminalTheme(),
        uiFontCustomEnabled: accountUiFontCustomEnabled(),
        monoFontCustomEnabled: accountMonoFontCustomEnabled(),
        uiFonts: uiFonts(),
        monoFonts: monoFonts(),
        diffView: diffViewToProto(accountDiffView()),
        turnEndSound: turnEndSoundToProto(accountTurnEndSound()),
        turnEndSoundVolume: accountTurnEndSoundVolume(),
        debugLogging: accountDebugLogging(),
        customKeybindingsJson: customKeybindings().length > 0 ? JSON.stringify(customKeybindings()) : '',
      })
    }
    finally {
      saveInFlight = false
      if (saveQueued) {
        saveQueued = false
        saveAccountPreferences().catch(() => {})
      }
    }
  }

  const setCustomKeybindings = (value: UserKeybindingOverride[]) => {
    setCustomKeybindingsSignal(value)
    saveAccountPreferences().catch(() => {})
  }

  createEffect(() => {
    setDebugEnabled(debugLogging())
  })

  onMount(() => {
    reload()
  })

  return (
    <PreferencesContext.Provider value={{
      theme,
      terminalTheme,
      debugLogging,
      showHiddenMessages,
      setShowHiddenMessages,
      enterKeyMode,
      setEnterKeyMode,
      customKeybindings,
      setCustomKeybindings,
      uiFontCustomEnabled,
      monoFontCustomEnabled,
      uiFonts,
      monoFonts,
      monoFontFamily,
      uiFontFamily,
      diffView,
      turnEndSound,
      turnEndSoundVolume,
      browserTheme,
      setBrowserTheme,
      browserTerminalTheme,
      setBrowserTerminalTheme,
      browserDiffView,
      setBrowserDiffView,
      browserTurnEndSound,
      setBrowserTurnEndSound,
      browserTurnEndSoundVolume,
      setBrowserTurnEndSoundVolume,
      browserDebugLogging,
      setBrowserDebugLogging,
      accountTheme,
      accountTerminalTheme,
      accountUiFontCustomEnabled,
      accountMonoFontCustomEnabled,
      accountDiffView,
      accountTurnEndSound,
      accountTurnEndSoundVolume,
      accountDebugLogging,
      setAccountTheme,
      setAccountTerminalTheme,
      setAccountUiFontCustomEnabled,
      setAccountMonoFontCustomEnabled,
      setAccountDiffView,
      setAccountTurnEndSound,
      setAccountTurnEndSoundVolume,
      setAccountDebugLogging,
      setUiFonts,
      setMonoFonts,
      saveAccountPreferences,
      reload,
    }}
    >
      {props.children}
    </PreferencesContext.Provider>
  )
}

export function usePreferences(): PreferencesState {
  const ctx = useContext(PreferencesContext)
  if (!ctx) {
    throw new Error('usePreferences must be used within PreferencesProvider')
  }
  return ctx
}
