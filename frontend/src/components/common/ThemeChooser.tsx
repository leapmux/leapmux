import type { ResolvedThemeMode, TerminalThemeValue, ThemeMode, ThemeSurface, ThemeValue, ThemeVariant } from '~/styles/themes'
import ChevronDown from 'lucide-solid/icons/chevron-down'
import { createMemo, createSignal, For, on, Show } from 'solid-js'
import { DropdownMenu, DropdownMenuCheckableItem } from '~/components/common/DropdownMenu'
import { Icon } from '~/components/common/Icon'
import { PillGroup } from '~/components/common/PillGroup'
import { isThemeMode } from '~/lib/themeStore'
import { errorText } from '~/styles/shared.css'
import { DEFAULT_THEME_ID, isThemeId, MATCH_UI, resolveVariant, themeById, themeLabel, THEMES, variantsFor } from '~/styles/themes'
import * as styles from './ThemeChooser.css'

// Typed with the DOMAIN type, not `string`. `PillGroup` is generic in its
// option value, so a `string`-typed list here was the only reason the pill
// handler had to cast on the way back into a `ThemeValue` -- a cast that would
// have gone on compiling if a mode were renamed or dropped.
//
// `MODES` in `~/lib/themeStore` stays the authority on WHICH modes exist, and
// `isThemeMode` below is how this file asks. Deriving MODES from this list
// instead would point the dependency the wrong way: the theme library would
// then need a component's option list to say what a mode is.
const MODE_OPTIONS: { value: ThemeMode, label: string }[] = [
  { value: 'system', label: 'System' },
  { value: 'light', label: 'Light' },
  { value: 'dark', label: 'Dark' },
]

export interface ThemeChooserProps<T extends ThemeValue | TerminalThemeValue> {
  value: T
  /** Commit the whole value. Every half the user did not touch passes through unchanged. */
  onChange: (value: T) => void | Promise<boolean | void>
  /** Render the leading label. Off inside the Preferences dialog, whose row already has one. */
  showLabel?: boolean
  /**
   * The UI theme this control may follow, which adds "Match UI" to the palette
   * list.
   *
   * Present for the terminal and syntax rows, absent for the UI theme itself,
   * which has nothing to match. Its value is what "Match UI" MEANS: it fills the
   * governed mode pills and variant menu so they report what the app resolved
   * to, and it seeds every half when the user picks a palette of their own.
   */
  matchUi?: ThemeValue
  /**
   * The OS's own light/dark answer, for resolving a `system` mode to the
   * polarity now showing.
   *
   * Passed in rather than read here so a caller in a reactive context can wire
   * it to a signal; reading `matchMedia` inside would bypass Solid's tracking
   * and freeze the variant menu on whichever polarity happened to be first.
   *
   * REQUIRED. It was optional with a `?? 'light'` fallback, and every one of
   * the callers passes `themeStore.systemMode()` -- so the fallback answered
   * for nobody and would have answered WRONGLY for the first caller to forget:
   * a user on a dark OS with mode `system` would edit the light variant while
   * looking at the dark one, and nothing on screen would say so. A missing
   * prop is a compile error now.
   */
  systemMode: ResolvedThemeMode
  /**
   * Which of the three appearance settings this picker chooses for, which
   * decides the NAME each palette goes by.
   *
   * The list is the same eleven either way. Only Default reads differently: its
   * terminal palette is Dimidium's and its highlighting is GitHub's, and a
   * picker that hid that would leave a user unable to find a palette they are
   * already looking at.
   */
  surface?: ThemeSurface
  /**
   * How the row sits in its container. `center` for the first-impression
   * surfaces, whose layouts are centred columns; `start` inside the Preferences
   * dialog, where every other row's control begins at the left edge.
   */
  align?: 'start' | 'center'
  /** Accessible name for the controls. Defaults to the UI theme's wording. */
  label?: string
}

/**
 * A theme's colours, as a chip: its background, with its text and its accent
 * marked on top.
 *
 * Decorative — the option's label carries the name. It exists because an
 * `<option>` never could: a palette is the one thing a picker cannot describe
 * in words, and it is the whole reason these two controls are menus rather than
 * native selects.
 */
function Swatch(props: { variant: ThemeVariant }) {
  return (
    <span
      class={styles.swatch}
      aria-hidden="true"
      style={{
        '--swatch-bg': props.variant.palette['--background'],
        '--swatch-fg': props.variant.palette['--foreground'],
        '--swatch-accent': props.variant.palette['--primary'],
      }}
    />
  )
}

/**
 * The one theme control: a palette menu, an optional variant menu, and a
 * system/light/dark tri-switch, on one line.
 *
 * Purely presentational -- it neither reads nor writes a preference. Every
 * surface binds it through `useThemeChooser`, so the Preferences dialog, the
 * desktop launcher, the no-workspace empty state and the first-run setup page
 * cannot drift into writing different things.
 *
 * "FOLLOW THE APP" IS A PALETTE, AND IT GOVERNS THE ROW. `match-ui` used to be
 * offered twice -- as the first palette AND as the first mode pill -- which read
 * as two settings for one idea and spelled two combinations nobody can describe.
 * It is now one entry in one list, and choosing it disables the rest of the row,
 * because following the app means following all of it.
 *
 * THE VARIANT MENU LISTS BOTH SIDES, under a named `role="group"` each, and it
 * appears when EITHER side offers a choice. Showing only the polarity on screen
 * kept the row to one line but made a lopsided theme unreachable: Catppuccin
 * has three dark flavours and one light, so a user on Light saw no menu at all.
 * A pick is written under the chosen variant's OWN polarity, and a variant
 * whose label matches one on the other side moves both.
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
    return isThemeMode(mode) ? mode : MODE_OPTIONS[0]!.value
  }

  /** The polarity on screen, which is the one the variant menu edits. */
  const polarity = (): ResolvedThemeMode => {
    const mode = selectedMode()
    if (mode === 'light' || mode === 'dark')
      return mode
    return props.systemMode
  }

  const theme = () => themeById(matching() ? props.matchUi!.name : props.value.name)

  /**
   * The variants split by side, so each half can be a named `role="group"`.
   *
   * BOTH SIDES, always -- not just the polarity on screen. Listing only that
   * one reads well until a theme is lopsided: Catppuccin has three dark
   * flavours and a single light one, so a user on Light saw no menu at all and
   * Macchiato was unreachable rather than one click further away.
   *
   * MEMOIZED, and that is load-bearing rather than an optimisation. `<For>`
   * diffs by strict identity, so a plain function handing back fresh group
   * objects disposes and rebuilds both halves on every commit -- and `commit`
   * replaces `props.value` on every pick, so every pick rebuilt them. The rebuilt
   * button carries DOM focus away with it, so a keyboard user is dropped back to
   * the document on every choice, and the whole list re-renders where one item's
   * checked state changed. Keyed on the theme id, because that is the only input
   * the groups actually depend on.
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
   * Whether the theme offers a real choice ON SOME SIDE.
   *
   * Not `variants.length > 1`: every theme has at least one light and one dark
   * variant, so that would put a two-item menu on all eleven and offer Nord a
   * pick between its only light palette and its only dark one -- which the
   * mode pills already make.
   */
  const hasVariants = () =>
    variantsFor(theme(), 'light').length > 1 || variantsFor(theme(), 'dark').length > 1

  const variantLabel = () => `${label()} ${theme().variantLabel?.toLowerCase() ?? 'variant'}`

  /**
   * Commit a patch and state a refusal inline.
   *
   * `setAccount` REJECTS when the hub refuses the write, after restoring the
   * pre-write value -- so this must catch it, or the palette snaps back with no
   * reason on screen and the rejection reaches the global sink, which reports a
   * generic "Something went wrong" and loses what the hub actually said. Every
   * enum row gets this from `SettingRow.commit`; the three theme rows are
   * `custom` editors, which `SettingRow` renders bare with no binding wrapper,
   * so they do their own -- as `KeybindingsControl` already does.
   */
  const commit = (patch: Partial<ThemeValue>) => {
    setWriteError(null)
    void Promise.resolve(props.onChange({ ...props.value, ...patch } as T))
      .catch((err: unknown) => {
        setWriteError(err instanceof Error ? err.message : String(err))
      })
  }

  /**
   * Commit a palette choice, and the halves that have to come with it.
   *
   * Leaving "Match UI" seeds the mode from the app, so detaching changes nothing
   * on screen and the user adjusts from what they can already see. The variant
   * is NOT carried over: it names a variant of the app's theme, which the newly
   * chosen theme does not have.
   */
  const selectName = (name: string) => {
    if (name === MATCH_UI) {
      commit({ name: MATCH_UI, mode: MATCH_UI as ThemeValue['mode'], variant: undefined })
      return
    }
    commit({ name, mode: effective().mode as ThemeValue['mode'], variant: undefined })
  }

  /**
   * Commit a variant, and the other polarity's when it answers to the same name.
   *
   * ONE GENERAL RULE, not a Gruvbox carve-out. Gruvbox offers Hard/Medium/Soft
   * on both sides, so picking "Soft" once means it in both -- which is what a
   * contrast level is for. Catppuccin, Ayu and Rosé Pine share no label across
   * polarities, so nothing links and the rule costs them nothing. Without it the
   * hidden polarity could only be reached by pinning the mode.
   */
  const selectVariant = (chosen: ThemeVariant) => {
    // Written under the CHOSEN variant's own polarity, not the one on screen: a
    // user on Light picking Macchiato means their dark half, and there is no
    // other reading of it.
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
            <Swatch variant={current()} />
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
            leading={<Swatch variant={current()} />}
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
              leading={<Swatch variant={resolveVariant(option, undefined, polarity())} />}
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
              <Swatch variant={current()} />
              <span class={styles.triggerText}>{current().label}</span>
              <Icon icon={ChevronDown} size="xs" aria-hidden="true" />
            </button>
          )}
        >
          <For each={variantGroups()}>
            {group => (
              // A real `role="group"` with a name, not a heading div. Gruvbox
              // offers "Soft" on BOTH sides, so the two items carry identical
              // labels; without the group a screen reader announces "Soft"
              // twice with nothing to tell them apart. The heading is the
              // group's own name, so it is read once on entry.
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
                      leading={<Swatch variant={option} />}
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
        selected={value => value === selectedMode()}
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
