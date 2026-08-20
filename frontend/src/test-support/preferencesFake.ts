import type { DualPreference } from '~/context/PreferencesContext'
import { vi } from 'vitest'

/**
 * A dual-tier preference double: every writer is a spy, both readers
 * return the seed, and no browser override is set.
 */
function fakeDual<T>(protoKey: string, value: T): DualPreference<T> & {
  setBrowser: ReturnType<typeof vi.fn>
  setAccount: ReturnType<typeof vi.fn>
  reset: ReturnType<typeof vi.fn>
} {
  return {
    protoKey,
    resolved: () => value,
    browser: () => null,
    account: () => value,
    setBrowser: vi.fn(),
    setAccount: vi.fn(),
    customized: () => false,
    reset: vi.fn(async () => {}),
  } as never
}

/**
 * A preference-context double for the settings tests: every setter the reset
 * and binding paths can reach is a spy. Cast to PreferencesState at the
 * call site; only the members under test need to behave.
 *
 * The dual preferences are NESTED, matching the context: one entry per key
 * instead of four spies each. A test that needs a real value overrides the
 * whole entry.
 */
export function makeFakePrefs() {
  return {
    dual: {
      theme: fakeDual('theme', { name: 'default', mode: 'system' }),
      terminalTheme: fakeDual('terminal_theme', { name: 'match-ui', mode: 'match-ui' }),
      syntaxTheme: fakeDual('syntax_theme', { name: 'match-ui', mode: 'match-ui' }),
      diffView: fakeDual('diff_view', 'unified'),
      turnEndSound: fakeDual('turn_end_sound', 'ding-dong'),
      turnEndSoundVolume: fakeDual('turn_end_sound_volume', 100),
      debugLogging: fakeDual('debug_logging', false),
      uiFonts: fakeDual('ui_fonts', { enabled: false, fonts: [] as string[] }),
      monoFonts: fakeDual('mono_fonts', { enabled: false, fonts: [] as string[] }),
    },
    accountCustomized: () => ({} as Record<string, boolean>),
    resetUserSetting: vi.fn(async () => {}),
    // The real one opens a batch and stores one document at the end. The
    // double runs the body straight through, so a test observes the writes
    // it made rather than the batching.
    batchBrowserPrefWrites: vi.fn((body: () => void) => body()),
    setEnterKeyMode: vi.fn(),
    setTerminalRenderer: vi.fn(),
    setPreferredEditorId: vi.fn(),
    setExpandAgentThoughts: vi.fn(),
    setRevealAfterDownload: vi.fn(),
    setShowComposerStatusBar: vi.fn(),
    setDirectoryPickerShowHidden: vi.fn(),
    setTerminalOsNotifications: vi.fn(),
    setShowHiddenMessages: vi.fn(),
  }
}
