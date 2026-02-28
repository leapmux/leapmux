import type { Component } from 'solid-js'
import type { ThemePreference } from '~/app'
import type { DiffViewPreference, TurnEndSoundPreference } from '~/context/PreferencesContext'
import type { TerminalThemePreference } from '~/lib/terminal'
import { For, Show } from 'solid-js'
import { usePreferences } from '~/context/PreferencesContext'
import * as styles from './PreferencesPage.css'

const themeOptions = [
  { value: 'dark', label: 'Dark' },
  { value: 'light', label: 'Light' },
  { value: 'system', label: 'System' },
]

const terminalThemeOptions = [
  { value: 'match-ui', label: 'Match UI' },
  { value: 'dark', label: 'Dark' },
  { value: 'light', label: 'Light' },
]

const diffViewOptions = [
  { value: 'unified', label: 'Unified' },
  { value: 'split', label: 'Side-by-Side' },
]

const turnEndSoundOptions = [
  { value: 'none', label: 'None' },
  { value: 'ding-dong', label: 'Ding Dong' },
]

function renderThemeButtons(
  current: () => string,
  onChange: (v: string) => void,
  options: { value: string, label: string }[],
) {
  return (
    <div class={styles.pillGroup}>
      <For each={options}>
        {opt => (
          <button
            class={current() === opt.value ? styles.pillOptionActive : styles.pillOption}
            onClick={() => onChange(opt.value)}
          >
            {opt.label}
          </button>
        )}
      </For>
    </div>
  )
}

export const BrowserAppearanceSettings: Component = () => {
  const prefs = usePreferences()

  return (
    <>
      <div class={styles.section}>
        <h2>Theme</h2>
        <div class={styles.pillGroup}>
          <button
            class={prefs.browserTheme() === null ? styles.pillOptionActive : styles.pillOption}
            onClick={() => prefs.setBrowserTheme(null)}
          >
            Use account default
          </button>
          <For each={themeOptions}>
            {opt => (
              <button
                class={prefs.browserTheme() === opt.value ? styles.pillOptionActive : styles.pillOption}
                onClick={() => prefs.setBrowserTheme(opt.value as ThemePreference)}
              >
                {opt.label}
              </button>
            )}
          </For>
        </div>
      </div>

      <div class={styles.section}>
        <h2>Terminal Theme</h2>
        <div class={styles.pillGroup}>
          <button
            class={prefs.browserTerminalTheme() === null ? styles.pillOptionActive : styles.pillOption}
            onClick={() => prefs.setBrowserTerminalTheme(null)}
          >
            Use account default
          </button>
          <For each={terminalThemeOptions}>
            {opt => (
              <button
                class={prefs.browserTerminalTheme() === opt.value ? styles.pillOptionActive : styles.pillOption}
                onClick={() => prefs.setBrowserTerminalTheme(opt.value as TerminalThemePreference)}
              >
                {opt.label}
              </button>
            )}
          </For>
        </div>
      </div>

      <div class={styles.section}>
        <h2>Diff View</h2>
        <div class={styles.pillGroup}>
          <button
            class={prefs.browserDiffView() === null ? styles.pillOptionActive : styles.pillOption}
            onClick={() => prefs.setBrowserDiffView(null)}
          >
            Use account default
          </button>
          <For each={diffViewOptions}>
            {opt => (
              <button
                class={prefs.browserDiffView() === opt.value ? styles.pillOptionActive : styles.pillOption}
                onClick={() => prefs.setBrowserDiffView(opt.value as DiffViewPreference)}
              >
                {opt.label}
              </button>
            )}
          </For>
        </div>
      </div>

      <div class={styles.section}>
        <h2>Turn End Sound</h2>
        <div class={styles.pillGroup}>
          <button
            class={prefs.browserTurnEndSound() === null ? styles.pillOptionActive : styles.pillOption}
            onClick={() => prefs.setBrowserTurnEndSound(null)}
          >
            Use account default
          </button>
          <For each={turnEndSoundOptions}>
            {opt => (
              <button
                class={prefs.browserTurnEndSound() === opt.value ? styles.pillOptionActive : styles.pillOption}
                onClick={() => prefs.setBrowserTurnEndSound(opt.value as TurnEndSoundPreference)}
              >
                {opt.label}
              </button>
            )}
          </For>
        </div>
        <Show when={prefs.turnEndSound() !== 'none'}>
          <div class={styles.sliderGroup}>
            <div class={styles.volumeOverrideRow}>
              <span class={styles.fieldLabel}>Volume</span>
              <button
                aria-pressed={prefs.browserTurnEndSoundVolume() !== null}
                onClick={() => {
                  const pressed = !(prefs.browserTurnEndSoundVolume() !== null)
                  if (pressed) {
                    prefs.setBrowserTurnEndSoundVolume(prefs.accountTurnEndSoundVolume())
                  }
                  else {
                    prefs.setBrowserTurnEndSoundVolume(null)
                  }
                }}
                class={prefs.browserTurnEndSoundVolume() !== null
                  ? styles.pillOptionActive
                  : styles.pillOption}
              >
                {prefs.browserTurnEndSoundVolume() !== null ? 'Custom volume' : 'Use account default'}
              </button>
            </div>
            <Show when={prefs.browserTurnEndSoundVolume() !== null}>
              <div class={styles.sliderRow}>
                <input type="range" min={0} max={100} step={1} value={prefs.browserTurnEndSoundVolume()!} onInput={e => prefs.setBrowserTurnEndSoundVolume(Number(e.currentTarget.value))} />
                <span class={styles.sliderValue}>
                  {prefs.browserTurnEndSoundVolume()}
                  %
                </span>
              </div>
            </Show>
          </div>
        </Show>
      </div>
    </>
  )
}

export const AccountAppearanceSettings: Component = () => {
  const prefs = usePreferences()

  const handleAccountThemeChange = async (newTheme: ThemePreference) => {
    prefs.setAccountTheme(newTheme)
    try {
      await prefs.saveAccountPreferences()
    }
    catch {
      // Best effort
    }
  }

  const handleAccountTerminalThemeChange = async (newTheme: TerminalThemePreference) => {
    prefs.setAccountTerminalTheme(newTheme)
    try {
      await prefs.saveAccountPreferences()
    }
    catch {
      // Best effort
    }
  }

  const handleAccountDiffViewChange = async (newDiffView: DiffViewPreference) => {
    prefs.setAccountDiffView(newDiffView)
    try {
      await prefs.saveAccountPreferences()
    }
    catch {
      // Best effort
    }
  }

  const handleAccountTurnEndSoundChange = async (newSound: TurnEndSoundPreference) => {
    prefs.setAccountTurnEndSound(newSound)
    try {
      await prefs.saveAccountPreferences()
    }
    catch {
      // Best effort
    }
  }

  const handleAccountTurnEndSoundVolumeChangeEnd = async () => {
    try {
      await prefs.saveAccountPreferences()
    }
    catch {
      // Best effort
    }
  }

  return (
    <>
      <div class={styles.section}>
        <h2>Theme</h2>
        {renderThemeButtons(
          () => prefs.accountTheme(),
          v => handleAccountThemeChange(v as ThemePreference),
          themeOptions,
        )}
      </div>

      <div class={styles.section}>
        <h2>Terminal Theme</h2>
        {renderThemeButtons(
          () => prefs.accountTerminalTheme(),
          v => handleAccountTerminalThemeChange(v as TerminalThemePreference),
          terminalThemeOptions,
        )}
      </div>

      <div class={styles.section}>
        <h2>Diff View</h2>
        {renderThemeButtons(
          () => prefs.accountDiffView(),
          v => handleAccountDiffViewChange(v as DiffViewPreference),
          diffViewOptions,
        )}
      </div>

      <div class={styles.section}>
        <h2>Turn End Sound</h2>
        {renderThemeButtons(
          () => prefs.accountTurnEndSound(),
          v => handleAccountTurnEndSoundChange(v as TurnEndSoundPreference),
          turnEndSoundOptions,
        )}
        <Show when={prefs.accountTurnEndSound() !== 'none'}>
          <div class={styles.sliderGroup}>
            <span class={styles.fieldLabel}>Volume</span>
            <div class={styles.sliderRow}>
              <input type="range" min={0} max={100} step={1} value={prefs.accountTurnEndSoundVolume()} onInput={e => prefs.setAccountTurnEndSoundVolume(Number(e.currentTarget.value))} onChange={handleAccountTurnEndSoundVolumeChangeEnd} />
              <span class={styles.sliderValue}>
                {prefs.accountTurnEndSoundVolume()}
                %
              </span>
            </div>
          </div>
        </Show>
      </div>
    </>
  )
}
