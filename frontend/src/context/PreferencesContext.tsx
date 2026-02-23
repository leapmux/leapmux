import type { ParentComponent } from 'solid-js'
import type { ThemePreference } from '~/app'
import type { TerminalThemePreference } from '~/lib/terminal'
import { createContext, createSignal, onMount, useContext } from 'solid-js'
import { userClient } from '~/api/clients'
import { DiffView, TurnEndSound } from '~/generated/leapmux/v1/user_pb'

const DEFAULT_MONO_FONT_FAMILY = '"Hack NF", Hack, "SF Mono", Consolas, monospace'

export type DiffViewPreference = 'unified' | 'split'
export type TurnEndSoundPreference = 'none' | 'ding-dong'

/**
 * Sentinel value stored in localStorage to indicate
 * "use the account default" for this browser setting.
 */
const ACCOUNT_DEFAULT = 'account-default'

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

  // --- Account-level setters (Hub DB) ---
  /** Account-level theme default. */
  accountTheme: () => ThemePreference
  accountTerminalTheme: () => TerminalThemePreference
  accountUiFontCustomEnabled: () => boolean
  accountMonoFontCustomEnabled: () => boolean
  accountDiffView: () => DiffViewPreference
  accountTurnEndSound: () => TurnEndSoundPreference
  accountTurnEndSoundVolume: () => number

  setAccountTheme: (value: ThemePreference) => void
  setAccountTerminalTheme: (value: TerminalThemePreference) => void
  setAccountUiFontCustomEnabled: (enabled: boolean) => void
  setAccountMonoFontCustomEnabled: (enabled: boolean) => void
  setAccountDiffView: (value: DiffViewPreference) => void
  setAccountTurnEndSound: (value: TurnEndSoundPreference) => void
  setAccountTurnEndSoundVolume: (value: number) => void
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

function readLocalStorage(key: string): string | null {
  const v = localStorage.getItem(key)
  if (v === ACCOUNT_DEFAULT || v === null || v === '')
    return null
  return v
}

function writeLocalStorage(key: string, value: string | null) {
  if (value === null) {
    localStorage.setItem(key, ACCOUNT_DEFAULT)
  }
  else {
    localStorage.setItem(key, value)
  }
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

  // --- Browser-level (localStorage) ---
  const [browserTheme, setBrowserThemeSignal] = createSignal<ThemePreference | null>(
    readLocalStorage('leapmux-theme') as ThemePreference | null,
  )
  const [browserTerminalTheme, setBrowserTerminalThemeSignal] = createSignal<TerminalThemePreference | null>(
    readLocalStorage('leapmux-terminal-theme') as TerminalThemePreference | null,
  )

  const setBrowserTheme = (value: ThemePreference | null) => {
    setBrowserThemeSignal(value)
    writeLocalStorage('leapmux-theme', value)
    // Notify app.tsx for instant reactivity
    const setter = (window as any).__leapmux_setTheme
    if (setter) {
      setter(value ?? accountTheme())
    }
  }

  const setBrowserTerminalTheme = (value: TerminalThemePreference | null) => {
    setBrowserTerminalThemeSignal(value)
    writeLocalStorage('leapmux-terminal-theme', value)
  }

  const [browserDiffView, setBrowserDiffViewSignal] = createSignal<DiffViewPreference | null>(
    readLocalStorage('leapmux-diff-view') as DiffViewPreference | null,
  )

  const setBrowserDiffView = (value: DiffViewPreference | null) => {
    setBrowserDiffViewSignal(value)
    writeLocalStorage('leapmux-diff-view', value)
  }

  const [browserTurnEndSound, setBrowserTurnEndSoundSignal] = createSignal<TurnEndSoundPreference | null>(
    readLocalStorage('leapmux-turn-end-sound') as TurnEndSoundPreference | null,
  )

  const setBrowserTurnEndSound = (value: TurnEndSoundPreference | null) => {
    setBrowserTurnEndSoundSignal(value)
    writeLocalStorage('leapmux-turn-end-sound', value)
  }

  const [browserTurnEndSoundVolume, setBrowserTurnEndSoundVolumeSignal] = createSignal<number | null>(
    (() => {
      const v = readLocalStorage('leapmux-turn-end-sound-volume')
      return v !== null ? Number(v) : null
    })(),
  )

  const setBrowserTurnEndSoundVolume = (value: number | null) => {
    setBrowserTurnEndSoundVolumeSignal(value)
    writeLocalStorage('leapmux-turn-end-sound-volume', value !== null ? String(value) : null)
  }

  // --- Resolved values (browser override → account default → hardcoded) ---
  const theme = (): ThemePreference => browserTheme() ?? accountTheme()
  const terminalTheme = (): TerminalThemePreference => browserTerminalTheme() ?? accountTerminalTheme()
  const uiFontCustomEnabled = () => accountUiFontCustomEnabled()
  const monoFontCustomEnabled = () => accountMonoFontCustomEnabled()
  const diffView = (): DiffViewPreference => browserDiffView() ?? accountDiffView()
  const turnEndSound = (): TurnEndSoundPreference => browserTurnEndSound() ?? accountTurnEndSound()
  const turnEndSoundVolume = (): number => browserTurnEndSoundVolume() ?? accountTurnEndSoundVolume()

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
        setAccountTurnEndSoundVolume(p.turnEndSoundVolume || 100)
      }
    }
    catch {
      // Ignore errors on load
    }
  }

  const saveAccountPreferences = async () => {
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
    })
  }

  onMount(() => {
    reload()
  })

  return (
    <PreferencesContext.Provider value={{
      theme,
      terminalTheme,
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
      accountTheme,
      accountTerminalTheme,
      accountUiFontCustomEnabled,
      accountMonoFontCustomEnabled,
      accountDiffView,
      accountTurnEndSound,
      accountTurnEndSoundVolume,
      setAccountTheme,
      setAccountTerminalTheme,
      setAccountUiFontCustomEnabled,
      setAccountMonoFontCustomEnabled,
      setAccountDiffView,
      setAccountTurnEndSound,
      setAccountTurnEndSoundVolume,
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
