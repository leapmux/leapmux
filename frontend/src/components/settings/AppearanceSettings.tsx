import type { Component, JSXElement } from 'solid-js'
import type { ThemePreference } from '~/app'
import type { DiffViewPreference, TurnEndSoundPreference } from '~/context/PreferencesContext'
import type { TerminalThemePreference } from '~/lib/terminal'
import { For, Show } from 'solid-js'
import { usePreferences } from '~/context/PreferencesContext'
import * as styles from './PreferencesDialog.css'

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

/**
 * One pill, as a member of a one-of-N group (role=radio).
 *
 * Selection used to be a CSS class alone, twelve times over, so a screen reader
 * read each group as identical stateless buttons. `aria-pressed` fixed the
 * "stateless" half and still described the wrong widget: these groups are all
 * one-of-N, and a toggle button announces "pressed" with no group name and no
 * set position, while promising it can be un-pressed (clicking the selected
 * pill does nothing). role=radio inside a named role=radiogroup announces
 * "Theme, Dark, radio button, checked, 2 of 3".
 *
 * Not exported: a pill outside a PillGroup has no radiogroup to belong to, and
 * a lone role=radio is worse than a plain button. Use PillToggle for a genuine
 * two-state control.
 */
function PillOption(props: {
  selected: boolean
  onClick: () => void
  ref?: (el: HTMLButtonElement) => void
  children: JSXElement
}) {
  return (
    <button
      class={props.selected ? styles.pillOptionActive : styles.pillOption}
      role="radio"
      aria-checked={props.selected}
      // Roving: Tab reaches the GROUP, arrows move within it (see PillGroup).
      tabIndex={props.selected ? 0 : -1}
      ref={el => props.ref?.(el)}
      onClick={() => props.onClick()}
    >
      {props.children}
    </button>
  )
}

/**
 * A standalone two-state pill (role=button + aria-pressed).
 *
 * The one shape where aria-pressed IS correct: it toggles a single setting on
 * and off rather than picking one of several, so "pressed" describes it and
 * un-pressing is a real affordance.
 */
function PillToggle(props: { pressed: boolean, onClick: () => void, children: JSXElement }) {
  return (
    <button
      class={props.pressed ? styles.pillOptionActive : styles.pillOption}
      aria-pressed={props.pressed}
      onClick={() => props.onClick()}
    >
      {props.children}
    </button>
  )
}

/**
 * A one-of-N pill group: the radiogroup wrapper, its accessible name, and the
 * arrow-key contract the role requires.
 *
 * `label` is what assistive tech announces on entry and is the piece a row of
 * bare buttons never had -- the visible <h3> above each group is not associated
 * with it in the accessibility tree.
 */
function PillGroup<T>(props: {
  label: string
  options: { value: T, label: JSXElement }[]
  selected: (value: T) => boolean
  onSelect: (value: T) => void
}) {
  const els: (HTMLButtonElement | undefined)[] = []
  const selectAt = (i: number) => {
    props.onSelect(props.options[i].value)
    els[i]?.focus()
  }
  const onKeyDown = (e: KeyboardEvent) => {
    const n = props.options.length
    const i = props.options.findIndex(o => props.selected(o.value))
    const cur = i < 0 ? 0 : i
    const next
      = e.key === 'ArrowRight' || e.key === 'ArrowDown'
        ? (cur + 1) % n
        : e.key === 'ArrowLeft' || e.key === 'ArrowUp'
          ? (cur - 1 + n) % n
          : e.key === 'Home'
            ? 0
            : e.key === 'End'
              ? n - 1
              : -1
    if (next < 0)
      return
    e.preventDefault()
    selectAt(next)
  }
  return (
    <div class={styles.pillGroup} role="radiogroup" aria-label={props.label} onKeyDown={onKeyDown}>
      <For each={props.options}>
        {(opt, i) => (
          <PillOption
            selected={props.selected(opt.value)}
            onClick={() => props.onSelect(opt.value)}
            ref={(el) => { els[i()] = el }}
          >
            {opt.label}
          </PillOption>
        )}
      </For>
    </div>
  )
}

// Exported for its unit test only: PillGroup carries the radiogroup semantics
// and the arrow-key contract, and both are behaviour a screen-reader user
// depends on rather than styling.
export { PillGroup as PillGroupForTest }

/** The "inherit the account setting" pill every browser-scoped group leads with. */
const ACCOUNT_DEFAULT = { value: null, label: 'Use account default' }

function renderThemeButtons(
  label: string,
  current: () => string,
  onChange: (v: string) => void,
  options: { value: string, label: string }[],
) {
  return (
    <PillGroup label={label} options={options} selected={v => current() === v} onSelect={onChange} />
  )
}

export const BrowserAppearanceSettings: Component = () => {
  const prefs = usePreferences()

  return (
    <>
      <div class={styles.section}>
        <h3>Theme</h3>
        <PillGroup
          label="Theme"
          options={[ACCOUNT_DEFAULT, ...themeOptions]}
          selected={v => prefs.browserTheme() === v}
          onSelect={v => prefs.setBrowserTheme(v as ThemePreference)}
        />
      </div>

      <div class={styles.section}>
        <h3>Terminal Theme</h3>
        <PillGroup
          label="Terminal theme"
          options={[ACCOUNT_DEFAULT, ...terminalThemeOptions]}
          selected={v => prefs.browserTerminalTheme() === v}
          onSelect={v => prefs.setBrowserTerminalTheme(v as TerminalThemePreference)}
        />
      </div>

      <div class={styles.section}>
        <h3>Diff View</h3>
        <PillGroup
          label="Diff view"
          options={[ACCOUNT_DEFAULT, ...diffViewOptions]}
          selected={v => prefs.browserDiffView() === v}
          onSelect={v => prefs.setBrowserDiffView(v as DiffViewPreference)}
        />
      </div>

      <div class={styles.section}>
        <h3>Turn End Sound</h3>
        <PillGroup
          label="Turn end sound"
          options={[ACCOUNT_DEFAULT, ...turnEndSoundOptions]}
          selected={v => prefs.browserTurnEndSound() === v}
          onSelect={v => prefs.setBrowserTurnEndSound(v as TurnEndSoundPreference)}
        />
        <Show when={prefs.turnEndSound() !== 'none'}>
          <div class={styles.sliderGroup}>
            <div class={styles.volumeOverrideRow}>
              <span class={styles.fieldLabel}>Volume</span>
              <PillToggle
                pressed={prefs.browserTurnEndSoundVolume() !== null}
                onClick={() => {
                  if (prefs.browserTurnEndSoundVolume() === null) {
                    prefs.setBrowserTurnEndSoundVolume(prefs.accountTurnEndSoundVolume())
                  }
                  else {
                    prefs.setBrowserTurnEndSoundVolume(null)
                  }
                }}
              >
                {prefs.browserTurnEndSoundVolume() !== null ? 'Custom volume' : 'Use account default'}
              </PillToggle>
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

      <div class={styles.section}>
        <h3>Debug Logging</h3>
        <PillGroup
          label="Debug logging"
          options={[ACCOUNT_DEFAULT, ...[{ value: true, label: 'On' }, { value: false, label: 'Off' }]]}
          selected={v => prefs.browserDebugLogging() === v}
          onSelect={v => prefs.setBrowserDebugLogging(v)}
        />
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

  const handleAccountDebugLoggingChange = async (enabled: boolean) => {
    prefs.setAccountDebugLogging(enabled)
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
        <h3>Theme</h3>
        {renderThemeButtons(
          'Theme',
          () => prefs.accountTheme(),
          v => handleAccountThemeChange(v as ThemePreference),
          themeOptions,
        )}
      </div>

      <div class={styles.section}>
        <h3>Terminal Theme</h3>
        {renderThemeButtons(
          'Terminal theme',
          () => prefs.accountTerminalTheme(),
          v => handleAccountTerminalThemeChange(v as TerminalThemePreference),
          terminalThemeOptions,
        )}
      </div>

      <div class={styles.section}>
        <h3>Diff View</h3>
        {renderThemeButtons(
          'Diff view',
          () => prefs.accountDiffView(),
          v => handleAccountDiffViewChange(v as DiffViewPreference),
          diffViewOptions,
        )}
      </div>

      <div class={styles.section}>
        <h3>Turn End Sound</h3>
        {renderThemeButtons(
          'Turn end sound',
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

      <div class={styles.section}>
        <h3>Debug Logging</h3>
        {renderThemeButtons(
          'Debug logging',
          () => prefs.accountDebugLogging() ? 'on' : 'off',
          v => handleAccountDebugLoggingChange(v === 'on'),
          [{ value: 'on', label: 'On' }, { value: 'off', label: 'Off' }],
        )}
      </div>
    </>
  )
}
