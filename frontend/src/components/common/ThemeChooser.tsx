import type { PillOptions } from '~/components/common/pillOptions'
import type { ResolvedThemeMode, TerminalThemeValue, ThemeMode, ThemeSurface, ThemeValue, ThemeVariant, ThemeVariantChoice } from '~/styles/themes'
import ChevronDown from 'lucide-solid/icons/chevron-down'
import { createMemo, createSignal, For, on, Show } from 'solid-js'
import { DropdownMenu, DropdownMenuCheckableItem } from '~/components/common/DropdownMenu'
import { Icon } from '~/components/common/Icon'
import { PillGroup } from '~/components/common/PillGroup'
import { ThemeSwatch } from '~/components/common/ThemeSwatch'
import { isThemeMode } from '~/lib/themeStore'
import { errorText } from '~/styles/shared.css'
import { DEFAULT_THEME_ID, isThemeId, MATCH_UI, resolveVariant, themeById, themeLabel, THEMES, variantsFor } from '~/styles/themes'
import * as styles from './ThemeChooser.css'

// Use ThemeMode for each option key so the selection handler needs no unchecked string conversion.
// A renamed or removed mode must fail type checking.
// The MODES declaration in ~/lib/themeStore defines the supported modes.
// Use isThemeMode to validate a selected mode.
// The theme library must not depend on this component's option list.
const MODE_OPTIONS = [
  { key: 'system', label: 'System' },
  { key: 'light', label: 'Light' },
  { key: 'dark', label: 'Dark' },
] as const satisfies PillOptions<ThemeMode>

export interface ThemeChooserProps<T extends ThemeValue | TerminalThemeValue> {
  value: T
  /** Commit the whole value. Every half the user did not touch passes through unchanged. */
  onChange: (value: T) => void | Promise<boolean | void>
  /** Render the leading label. Off inside the Preferences dialog, whose row already has one. */
  showLabel?: boolean
  /**
   * Supply the UI theme that this control can follow through Match UI.
   * The terminal and syntax theme rows can follow it; the UI theme row cannot follow itself.
   * This value supplies the displayed mode and variants while matching.
   * When the user selects an independent palette, use the current mode and clear its prior variant choice.
   */
  matchUi?: ThemeValue
  /**
   * Supply the operating system's resolved light or dark mode.
   * Use it to resolve the system setting.
   * The caller supplies a reactive signal because an internal matchMedia read would bypass Solid tracking.
   * This required property prevents a dark system from accidentally editing a light variant through a fallback.
   */
  systemMode: ResolvedThemeMode
  /**
   * The appearance surface determines each palette's display name.
   * Each surface uses the same theme catalog.
   * Default uses Dimidium for terminals and GitHub for syntax highlighting.
   * Display those names so the user can identify the current palette.
   */
  surface?: ThemeSurface
  /**
   * Use center for the empty workspace view's centered column.
   * Use start in Preferences to align the control with the other rows.
   */
  align?: 'start' | 'center'
  /** Accessible name for the controls. Defaults to the UI theme's wording. */
  label?: string
}

/**
 * This control displays the palette menu and mode choices on one row.
 * It displays a variant menu when a palette offers variant choices.
 * It reads no preference and writes no preference directly.
 * useThemeChooser supplies the same preference behavior to Preferences and the empty workspace view.
 * Theme preferences belong to the account, so views before account resolution offer no theme control.
 *
 * Match UI is one palette choice.
 * Selecting it disables the other choices because the row follows the complete UI theme.
 * Do not offer Match UI again as a separate mode choice.
 *
 * The variant menu includes separate Light and Dark groups when either side offers more than one variant.
 * This keeps dark variants reachable when the current view uses the palette's single light variant.
 * Store each selection under its own polarity.
 * When another polarity has a variant with the same label, update both.
 */
export function ThemeChooser<T extends ThemeValue | TerminalThemeValue>(
  props: ThemeChooserProps<T>,
) {
  const [writeError, setWriteError] = createSignal<string | null>(null)

  const label = () => props.label ?? 'Theme'
  const modeLabel = () => `${label()} mode`

  const matching = () => props.matchUi !== undefined && props.value.name === MATCH_UI
  /** The value that actually decides what is painted: the app's while matching. */
  const effective = (): ThemeValue | TerminalThemeValue =>
    matching() ? props.matchUi! : props.value

  const selectedName = () => {
    if (matching())
      return MATCH_UI
    const name = props.value.name
    return isThemeId(name) ? name : DEFAULT_THEME_ID
  }

  const selectedMode = (): ThemeMode => {
    const mode = effective().mode
    return isThemeMode(mode) ? mode : MODE_OPTIONS[0].key
  }

  /**
   * Resolve the current display polarity for the selected swatch.
   */
  const polarity = (): ResolvedThemeMode => {
    const mode = selectedMode()
    if (mode === 'light' || mode === 'dark')
      return mode
    return props.systemMode
  }

  const theme = () => themeById(matching() ? props.matchUi!.name : props.value.name)

  /**
   * Group variants by their Light or Dark side and give each group an accessible name.
   * Include both sides even when the current view displays only one.
   * A palette can offer several dark variants and only one light variant.
   *
   * Memoize the groups by theme ID.
   * Solid For tracks strict object identity.
   * Recreating group objects after each selection would replace their buttons and lose keyboard focus.
   * Only the theme ID determines this grouping.
   */
  const variantGroups = createMemo(
    on(() => theme().id, () => ([
      { polarity: 'light' as const, label: 'Light', items: variantsFor(theme(), 'light') },
      { polarity: 'dark' as const, label: 'Dark', items: variantsFor(theme(), 'dark') },
    ].filter(g => g.items.length > 0))),
  )
  const current = () => resolveVariant(theme(), effective().variant?.[polarity()], polarity())
  /** The variant each polarity resolves to, for the checked state of every item. */
  const currentFor = (p: ResolvedThemeMode) =>
    resolveVariant(theme(), effective().variant?.[p], p)
  /**
   * Show the variant menu when either side offers more than one variant.
   * Counting all variants together would show a menu even for a palette with only one variant on each side.
   * The mode choices already select between those two sides.
   */
  const hasVariants = () =>
    variantsFor(theme(), 'light').length > 1 || variantsFor(theme(), 'dark').length > 1

  const variantLabel = () => `${label()} ${theme().variantLabel?.toLowerCase() ?? 'variant'}`

  /**
   * Write the preference change and display any refusal beside the control.
   * setAccount restores the prior value when the Hub refuses the write.
   * Display that refusal so a restored palette does not appear without an explanation.
   * The custom theme editors do not use SettingRow's ordinary commit wrapper, so this component handles their rejected promises.
   */
  // An explicit variant:undefined clears the stored variant choice.
  // An absent variant key preserves that choice.
  // The merge and its parameter type must preserve this distinction when selectName changes the palette.
  const commit = (patch: Omit<Partial<ThemeValue>, 'variant'> & { variant?: ThemeVariantChoice | undefined }) => {
    setWriteError(null)
    void Promise.resolve(props.onChange({ ...props.value, ...patch } as T))
      .catch((err: unknown) => {
        setWriteError(err instanceof Error ? err.message : String(err))
      })
  }

  /**
   * Write the selected palette and its required mode value.
   * When leaving Match UI, keep the current UI mode so the visible mode stays stable.
   * Clear the old variant choice because it identifies the previous palette's variant.
   */
  const selectName = (name: string) => {
    if (name === MATCH_UI) {
      commit({ name: MATCH_UI, mode: MATCH_UI as ThemeValue['mode'], variant: undefined })
      return
    }
    commit({ name, mode: effective().mode as ThemeValue['mode'], variant: undefined })
  }

  /**
   * Write the selected variant under its own polarity.
   * If the other polarity offers the same label, select that variant also.
   * This general rule connects Gruvbox contrast levels across both sides.
   * Palettes without a shared variant label keep their two selections independent.
   * Both variant groups remain available regardless of the current display mode.
   */
  const selectVariant = (chosen: ThemeVariant) => {
    // Write the variant under its own polarity even when the current view displays the other side.
    // A light view can therefore select the dark Macchiato variant.
    const other: ResolvedThemeMode = chosen.polarity === 'light' ? 'dark' : 'light'
    const twin = variantsFor(theme(), other).find(v => v.label === chosen.label)
    commit({
      variant: {
        ...props.value.variant,
        [chosen.polarity]: chosen.id,
        ...(twin ? { [other]: twin.id } : {}),
      },
    })
  }

  return (
    <div class={styles.row} data-testid="theme-chooser" data-align={props.align ?? 'start'}>
      {props.showLabel !== false && <span class={styles.label}>{label()}</span>}

      <DropdownMenu
        aria-label={label()}
        data-testid="theme-chooser-name-menu"
        trigger={triggerProps => (
          <button
            {...triggerProps}
            type="button"
            class={styles.trigger}
            aria-label={label()}
            data-testid="theme-chooser-name"
            data-value={selectedName()}
          >
            <ThemeSwatch variant={current()} />
            <span class={styles.triggerText}>
              {matching() ? 'Match UI' : themeLabel(theme(), props.surface ?? 'ui')}
            </span>
            <Icon icon={ChevronDown} size="xs" aria-hidden="true" />
          </button>
        )}
      >
        <Show when={props.matchUi !== undefined}>
          <DropdownMenuCheckableItem
            kind="radio"
            label="Match UI"
            checked={matching()}
            data-testid="theme-option-match-ui"
            leading={<ThemeSwatch variant={current()} />}
            onSelect={() => selectName(MATCH_UI)}
          />
        </Show>
        <For each={THEMES}>
          {option => (
            <DropdownMenuCheckableItem
              kind="radio"
              label={themeLabel(option, props.surface ?? 'ui')}
              checked={!matching() && selectedName() === option.id}
              data-testid={`theme-option-${option.id}`}
              leading={<ThemeSwatch variant={resolveVariant(option, undefined, polarity())} />}
              onSelect={() => selectName(option.id)}
            />
          )}
        </For>
      </DropdownMenu>

      <Show when={hasVariants()}>
        <DropdownMenu
          aria-label={variantLabel()}
          data-testid="theme-chooser-variant-menu"
          trigger={triggerProps => (
            <button
              {...triggerProps}
              type="button"
              class={styles.trigger}
              aria-label={variantLabel()}
              data-testid="theme-chooser-variant"
              data-value={current().id}
              disabled={matching()}
            >
              <ThemeSwatch variant={current()} />
              <span class={styles.triggerText}>{current().label}</span>
              <Icon icon={ChevronDown} size="xs" aria-hidden="true" />
            </button>
          )}
        >
          <For each={variantGroups()}>
            {group => (
              // Use a named group to distinguish identical labels on the Light and Dark sides.
              // For example, Gruvbox offers Soft in both groups.
              // The group name lets a screen reader identify the intended side.
              <div role="group" aria-label={group.label} data-testid={`variant-group-${group.polarity}`}>
                <Show when={variantGroups().length > 1}>
                  <div class={styles.variantGroup}>{group.label}</div>
                </Show>
                <For each={group.items}>
                  {option => (
                    <DropdownMenuCheckableItem
                      kind="radio"
                      label={option.label}
                      checked={currentFor(option.polarity).id === option.id}
                      data-testid={`variant-option-${option.id}`}
                      leading={<ThemeSwatch variant={option} />}
                      onSelect={() => selectVariant(option)}
                    />
                  )}
                </For>
              </div>
            )}
          </For>
        </DropdownMenu>
      </Show>

      <PillGroup
        label={modeLabel()}
        options={MODE_OPTIONS}
        disabled={matching()}
        selectedKey={selectedMode()}
        onSelect={mode => commit({ mode })}
      />

      {/* The row wraps, so the reason takes a line of its own under the
          controls rather than stretching them. */}
      <Show when={writeError()}>
        <div class={errorText} data-testid="theme-chooser-error">{writeError()}</div>
      </Show>
    </div>
  )
}
