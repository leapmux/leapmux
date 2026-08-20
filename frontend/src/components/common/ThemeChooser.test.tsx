import type { TerminalThemeValue, ThemeValue } from '~/styles/themes'
import { fireEvent, render, screen, within } from '@solidjs/testing-library'
import { describe, expect, it, vi } from 'vitest'
import { ALL_VARIANTS, resolveVariant, themeById, themeLabel, THEMES } from '~/styles/themes'
import { ThemeChooser } from './ThemeChooser'
import { SWATCH_TOKENS } from './ThemeSwatch'

const MATCH_BOTH: TerminalThemeValue = { name: 'match-ui', mode: 'match-ui' }

function renderChooser(value: ThemeValue, onChange = vi.fn()) {
  const result = render(() => <ThemeChooser value={value} onChange={onChange} systemMode="light" />)
  return { ...result, onChange }
}

/** The terminal row: the same control, plus the UI theme it can follow. */
function renderTerminal(
  value: TerminalThemeValue,
  ui: ThemeValue = { name: 'default', mode: 'system' },
  onChange = vi.fn(),
) {
  const result = render(() => (
    <ThemeChooser
      value={value}
      onChange={onChange}
      matchUi={ui}
      systemMode="light"
      surface="terminal"
      label="Terminal theme"
    />
  ))
  return { ...result, onChange }
}

const nameTrigger = () => screen.getByTestId('theme-chooser-name')
const variantTrigger = () => screen.queryByTestId('theme-chooser-variant')
const modeRadios = () => screen.getAllByRole('radio')
const modeGroup = () => screen.getByRole('radiogroup', { name: 'Theme mode' })

/**
 * The options one menu offers, in order.
 *
 * Scoped to that menu's own popover and queried with `hidden`, because both
 * menus keep their items mounted and a closed popover is outside the
 * accessibility tree. The role is still asserted -- these have to be
 * `menuitemradio`, which is what makes the group a one-of-N choice.
 */
function optionsOf(menu: 'name' | 'variant'): string[] {
  const popover = screen.getByTestId(`theme-chooser-${menu}-menu`)
  return within(popover)
    .getAllByRole('menuitemradio', { hidden: true })
    .map(el => el.textContent ?? '')
}

function pick(menu: 'name' | 'variant', label: string) {
  const popover = screen.getByTestId(`theme-chooser-${menu}-menu`)
  fireEvent.click(within(popover).getByRole('menuitemradio', { name: label, hidden: true }))
}

/** The nine pip colours of the one chip inside `scope`, in token order. */
function swatchFills(scope: HTMLElement): (string | null)[] {
  const chips = scope.querySelectorAll('svg')
  const pips = [...scope.querySelectorAll('rect')]
  if (pips.length !== SWATCH_TOKENS.length)
    throw new Error(`expected one chip of ${SWATCH_TOKENS.length} pips, found ${pips.length} in ${chips.length} svg`)
  return pips.map(r => r.getAttribute('fill'))
}

describe('the appearance picker (ThemeChooser)', () => {
  it('offers every catalogued palette, in catalogue order', () => {
    renderChooser({ name: 'default', mode: 'system' })
    expect(optionsOf('name')).toEqual(THEMES.map(t => t.label))
  })

  it('names the palette on its trigger, so the row reads without opening it', () => {
    renderChooser({ name: 'catppuccin', mode: 'dark' })
    expect(nameTrigger()).toHaveAttribute('data-value', 'catppuccin')
    expect(nameTrigger()).toHaveTextContent('Catppuccin')
  })

  it('shows the default palette for a name this build does not carry', () => {
    // A blank trigger would leave the user unable to tell which palette is
    // live. It falls back to the id themeStore paints with, so the control and
    // the screen agree.
    renderChooser({ name: 'from-the-future', mode: 'dark' })
    expect(nameTrigger()).toHaveAttribute('data-value', 'default')
  })

  it('exposes the mode as a radiogroup with the stored mode checked', () => {
    renderChooser({ name: 'default', mode: 'dark' })
    expect(modeRadios().map(r => r.textContent)).toEqual(['System', 'Light', 'Dark'])
    expect(modeGroup()).toBeInTheDocument()
    expect(screen.getByRole('radio', { name: 'Dark' })).toHaveAttribute('aria-checked', 'true')
  })

  it('keeps the mode when the palette changes', () => {
    // The halves are one value, so a change to either carries the others
    // through -- otherwise picking a palette silently resets dark mode.
    const { onChange } = renderChooser({ name: 'default', mode: 'dark' })
    pick('name', 'Nord')
    expect(onChange).toHaveBeenCalledWith({ name: 'nord', mode: 'dark', variant: undefined })
  })

  it('keeps the palette when the mode changes', () => {
    const { onChange } = renderChooser({ name: 'gruvbox', mode: 'system' })
    fireEvent.click(screen.getByRole('radio', { name: 'Light' }))
    expect(onChange).toHaveBeenCalledWith({ name: 'gruvbox', mode: 'light' })
  })

  // --- the variant menu -----------------------------------------------------

  it('renders no variant menu for a theme with one look per polarity', () => {
    // Every theme has at least one light and one dark variant, so a menu keyed
    // on the total would appear on all eleven and offer Nord a pick between its
    // only light palette and its only dark one -- which the mode pills make.
    renderChooser({ name: 'nord', mode: 'dark' })
    expect(variantTrigger()).toBeNull()
  })

  it('offers every flavour, whichever polarity is on screen', () => {
    // THE BUG THIS REPLACED: the menu used to list only the polarity showing.
    // Catppuccin has three dark flavours and a single light one, so a user on
    // Light saw no menu at all and Macchiato was unreachable -- not one click
    // away, unreachable.
    const { unmount } = renderChooser({ name: 'catppuccin', mode: 'light' })
    expect(variantTrigger()).not.toBeNull()
    expect(optionsOf('variant')).toEqual(['Latte', 'Frappé', 'Macchiato', 'Mocha'])
    unmount()

    renderChooser({ name: 'catppuccin', mode: 'dark' })
    expect(optionsOf('variant')).toEqual(['Latte', 'Frappé', 'Macchiato', 'Mocha'])
  })

  // `systemMode` is what resolves mode `system` to a polarity, and EVERY other
  // case in this file passes 'light' -- so the `?? 'light'` fallback the prop
  // used to carry would satisfy all of them. It is required now, and this is
  // the case that proves it is read: under a dark OS the trigger must report
  // the DARK flavour, and the variant the user picks is written under the
  // polarity they are actually looking at.
  it('resolves mode `system` with the OS answer it is given', () => {
    const dark = vi.fn()
    const { unmount } = render(() => (
      <ThemeChooser value={{ name: 'catppuccin', mode: 'system' }} onChange={dark} systemMode="dark" />
    ))
    expect(variantTrigger()).toHaveAttribute('data-value', 'catppuccin-mocha')
    unmount()

    const light = vi.fn()
    render(() => (
      <ThemeChooser value={{ name: 'catppuccin', mode: 'system' }} onChange={light} systemMode="light" />
    ))
    expect(variantTrigger()).toHaveAttribute('data-value', 'catppuccin-latte')
  })

  it('heads the two sides, so a flavour reads as light or dark', () => {
    renderChooser({ name: 'catppuccin', mode: 'dark' })
    const popover = screen.getByTestId('theme-chooser-variant-menu')
    expect(within(popover).getByText('Light')).toBeInTheDocument()
    expect(within(popover).getByText('Dark')).toBeInTheDocument()
  })

  it('names the variant the polarity on screen resolves to, on the trigger', () => {
    // The menu lists both sides; the TRIGGER still reports what is painted now.
    const { unmount } = renderChooser({ name: 'catppuccin', mode: 'dark' })
    expect(variantTrigger()).toHaveAttribute('data-value', 'catppuccin-mocha')
    unmount()

    renderChooser({ name: 'catppuccin', mode: 'light' })
    expect(variantTrigger()).toHaveAttribute('data-value', 'catppuccin-latte')
  })

  it('commits a flavour under its OWN polarity, not the one showing', () => {
    // A user on Light picking Macchiato means their dark half; there is no
    // other reading of it, and writing it under `light` would name a variant
    // the light side cannot answer for.
    const { onChange } = renderChooser({ name: 'catppuccin', mode: 'light' })
    pick('variant', 'Macchiato')
    expect(onChange).toHaveBeenCalledWith({
      name: 'catppuccin',
      mode: 'light',
      variant: { dark: 'catppuccin-macchiato' },
    })
  })

  it('checks the variant each side resolves to, on both sides at once', () => {
    renderChooser({
      name: 'catppuccin',
      mode: 'light',
      variant: { dark: 'catppuccin-frappe' },
    })
    const popover = screen.getByTestId('theme-chooser-variant-menu')
    const checked = within(popover)
      .getAllByRole('menuitemradio', { hidden: true })
      .filter(el => el.getAttribute('aria-checked') === 'true')
      .map(el => el.textContent)
    expect(checked).toEqual(['Latte', 'Frappé'])
  })

  it('names each side as a group, so repeated labels stay distinguishable', () => {
    // Gruvbox offers "Soft" on BOTH sides, so the two items carry identical
    // labels. Without a named group a screen reader announces "Soft" twice
    // with nothing to tell them apart.
    renderChooser({ name: 'gruvbox', mode: 'dark' })
    const popover = screen.getByTestId('theme-chooser-variant-menu')
    expect(within(popover).getByRole('group', { name: 'Light', hidden: true })).toBeInTheDocument()
    expect(within(popover).getByRole('group', { name: 'Dark', hidden: true })).toBeInTheDocument()
    expect(within(popover).getAllByRole('menuitemradio', { hidden: true })).toHaveLength(6)
  })

  it('moves the other polarity too when it answers to the same name', () => {
    // ONE GENERAL RULE, not a Gruvbox carve-out: a contrast level exists on
    // both sides, so picking "Soft" once means it in both.
    const { onChange } = renderChooser({ name: 'gruvbox', mode: 'dark' })
    const dark = screen.getByTestId('variant-group-dark')
    fireEvent.click(within(dark).getByRole('menuitemradio', { name: 'Soft', hidden: true }))
    expect(onChange.mock.calls[0]![0]!.variant).toEqual({
      dark: 'gruvbox-dark-soft',
      light: 'gruvbox-light-soft',
    })
  })

  it('links nothing when the polarities share no variant name', () => {
    const { onChange } = renderChooser({ name: 'catppuccin', mode: 'dark' })
    pick('variant', 'Frappé')
    expect(onChange).toHaveBeenCalledWith({
      name: 'catppuccin',
      mode: 'dark',
      variant: { dark: 'catppuccin-frappe' },
    })
  })

  it('drops the variant when the palette changes out from under it', () => {
    // A variant id names a variant of ONE theme. Carrying it to another would
    // name something that theme does not have, which resolveVariant discards
    // anyway -- storing it would just be a stale id in the document.
    const { onChange } = renderChooser({ name: 'catppuccin', mode: 'dark', variant: { dark: 'catppuccin-frappe' } })
    pick('name', 'Nord')
    expect(onChange).toHaveBeenCalledWith({ name: 'nord', mode: 'dark', variant: undefined })
  })

  // --- Match UI -------------------------------------------------------------

  it('offers Match UI once, in the palette list, and only where asked', () => {
    // The complaint this design answers: `match-ui` used to be the first
    // palette AND the first mode pill -- two controls for one idea.
    renderTerminal(MATCH_BOTH)
    expect(optionsOf('name')).toEqual(['Match UI', ...THEMES.map(t => themeLabel(t, 'terminal'))])
    expect(modeRadios().map(r => r.textContent)).toEqual(['System', 'Light', 'Dark'])
  })

  it('does not offer Match UI to the UI theme, which has nothing to match', () => {
    renderChooser({ name: 'default', mode: 'system' })
    expect(optionsOf('name')).not.toContain('Match UI')
  })

  it('governs the whole row with Match UI, because following means all of it', () => {
    const { onChange } = renderTerminal(MATCH_BOTH, { name: 'catppuccin', mode: 'dark' })
    for (const radio of modeRadios())
      expect(radio).toBeDisabled()
    expect(modeRadios().every(r => r.getAttribute('tabindex') === '-1')).toBe(true)
    expect(variantTrigger()).toBeDisabled()

    fireEvent.click(screen.getByRole('radio', { name: 'Light' }))
    expect(onChange).not.toHaveBeenCalled()
  })

  it('keeps the governed controls reporting what the app resolved to', () => {
    // "Match UI" alone does not say which flavour the terminal is wearing, and
    // that is what the user opened the row to read.
    renderTerminal(MATCH_BOTH, { name: 'catppuccin', mode: 'dark', variant: { dark: 'catppuccin-frappe' } })
    expect(screen.getByRole('radio', { name: 'Dark' })).toHaveAttribute('aria-checked', 'true')
    expect(variantTrigger()).toHaveAttribute('data-value', 'catppuccin-frappe')
  })

  it('seeds the mode from the app when a palette detaches the row', () => {
    // Detaching must change nothing on screen: the user picks a palette first
    // and adjusts light/dark second. Handing over `system` instead of the app's
    // pinned mode would detach the mode as well, in one click.
    const { onChange } = renderTerminal(MATCH_BOTH, { name: 'gruvbox', mode: 'dark' })
    pick('name', 'Nord')
    expect(onChange).toHaveBeenCalledWith({ name: 'nord', mode: 'dark', variant: undefined })
  })

  it('takes every half back when the row returns to Match UI', () => {
    const { onChange } = renderTerminal({ name: 'nord', mode: 'dark' })
    pick('name', 'Match UI')
    expect(onChange).toHaveBeenCalledWith({ name: 'match-ui', mode: 'match-ui', variant: undefined })
  })

  it('lets a detached row set either half without disturbing the other', () => {
    const { onChange } = renderTerminal({ name: 'nord', mode: 'system' })
    expect(modeRadios().every(r => !(r as HTMLButtonElement).disabled)).toBe(true)

    fireEvent.click(screen.getByRole('radio', { name: 'Dark' }))
    expect(onChange).toHaveBeenCalledWith({ name: 'nord', mode: 'dark' })
  })

  // --- naming and layout ----------------------------------------------------

  it('names a borrowed palette after the project it came from', () => {
    // Default's terminal is Dimidium's and its highlighting is GitHub's. A
    // picker that listed both as plain "Default" would leave a user hunting for
    // a palette they are already looking at.
    const { unmount } = renderChooser({ name: 'default', mode: 'system' })
    expect(optionsOf('name')).toContain('Default')
    unmount()

    const { unmount: unmountTerminal } = renderTerminal(MATCH_BOTH)
    expect(optionsOf('name')).toContain('Default (Dimidium)')
    unmountTerminal()

    render(() => (
      <ThemeChooser
        value={MATCH_BOTH}
        onChange={() => {}}
        matchUi={{ name: 'default', mode: 'system' }}
        systemMode="light"
        surface="syntax"
        label="Syntax theme"
      />
    ))
    expect(optionsOf('name')).toContain('Default (GitHub)')
  })

  it('gives the two menus and the pills distinct accessible names', () => {
    // Three controls sit on one row. Two sharing a name leaves a screen-reader
    // user unable to tell which one they are on.
    renderChooser({ name: 'catppuccin', mode: 'dark' })
    expect(screen.getByRole('radiogroup', { name: 'Theme mode' })).toBeInTheDocument()
    expect(nameTrigger()).toHaveAttribute('aria-label', 'Theme')
    expect(variantTrigger()).toHaveAttribute('aria-label', 'Theme flavor')
  })

  it('names the controls of the terminal row after that row', () => {
    renderTerminal(MATCH_BOTH, { name: 'gruvbox', mode: 'dark' })
    expect(screen.getByRole('radiogroup', { name: 'Terminal theme mode' })).toBeInTheDocument()
    expect(nameTrigger()).toHaveAttribute('aria-label', 'Terminal theme')
    expect(variantTrigger()).toHaveAttribute('aria-label', 'Terminal theme contrast')
  })

  it('renders its own label by default and drops it on request', () => {
    const { unmount } = renderChooser({ name: 'default', mode: 'system' })
    expect(screen.getByText('Theme')).toBeInTheDocument()
    unmount()

    // Inside the Preferences dialog the row already renders a "Theme" label,
    // and a second one would be read twice.
    render(() => (
      <ThemeChooser value={{ name: 'default', mode: 'system' }} onChange={() => {}} systemMode="light" showLabel={false} />
    ))
    expect(screen.queryByText('Theme')).toBeNull()
    // The trigger keeps its accessible name either way.
    expect(nameTrigger()).toHaveAttribute('aria-label', 'Theme')
  })

  it('starts at the left edge unless a centred layout asks otherwise', () => {
    // Inside the Preferences dialog every other row's control begins at the
    // left edge, and this one used to be centred there.
    const { unmount } = renderChooser({ name: 'default', mode: 'system' })
    expect(screen.getByTestId('theme-chooser')).toHaveAttribute('data-align', 'start')
    unmount()

    render(() => (
      <ThemeChooser value={{ name: 'default', mode: 'system' }} onChange={() => {}} systemMode="light" align="center" />
    ))
    expect(screen.getByTestId('theme-chooser')).toHaveAttribute('data-align', 'center')
  })
})

// A refused write must state its reason ON THE ROW.
//
// `setAccount` REJECTS when the hub refuses, after restoring the pre-write
// value. Every enum row gets the inline report from `SettingRow.commit`, but
// the three theme rows are `custom` editors, which `SettingRow` renders bare
// with no binding wrapper — so this control does its own, as `KeybindingsControl`
// already does. Without it the palette snapped back with nothing on the row and
// the rejection reached the global sink, which reports a generic "Something went
// wrong" and loses what the hub actually said.
describe('the appearance picker reporting a refused write', () => {
  it('states the hub\'s reason on the row when the write is refused', async () => {
    const onChange = vi.fn().mockRejectedValue(new Error('theme name is not allowed'))
    render(() => (
      <ThemeChooser value={{ name: 'default', mode: 'system' }} onChange={onChange} systemMode="light" />
    ))

    fireEvent.click(within(modeGroup()).getByRole('radio', { name: 'Dark' }))
    await vi.waitFor(() => {
      expect(screen.getByTestId('theme-chooser-error')).toHaveTextContent('theme name is not allowed')
    })
  })

  it('clears the reason once a later write succeeds', async () => {
    const onChange = vi.fn()
      .mockRejectedValueOnce(new Error('theme name is not allowed'))
      .mockResolvedValueOnce(undefined)
    render(() => (
      <ThemeChooser value={{ name: 'default', mode: 'system' }} onChange={onChange} systemMode="light" />
    ))

    fireEvent.click(within(modeGroup()).getByRole('radio', { name: 'Dark' }))
    await vi.waitFor(() => expect(screen.queryByTestId('theme-chooser-error')).not.toBeNull())

    fireEvent.click(within(modeGroup()).getByRole('radio', { name: 'Light' }))
    await vi.waitFor(() => expect(screen.queryByTestId('theme-chooser-error')).toBeNull())
  })

  it('shows nothing when the write succeeds', async () => {
    const onChange = vi.fn().mockResolvedValue(undefined)
    render(() => (
      <ThemeChooser value={{ name: 'default', mode: 'system' }} onChange={onChange} systemMode="light" />
    ))

    fireEvent.click(within(modeGroup()).getByRole('radio', { name: 'Dark' }))
    await vi.waitFor(() => expect(onChange).toHaveBeenCalled())
    expect(screen.queryByTestId('theme-chooser-error')).toBeNull()
  })
})

/**
 * The chip beside every label, which is the only thing here that describes a
 * palette rather than naming it.
 *
 * `ThemeSwatch.test.tsx` owns what the chip draws and which tokens it draws.
 * These cases own the wiring: that each mount site is handed the right VARIANT.
 */
describe('the appearance picker\'s palette chips', () => {
  it('previews each palette option with that option\'s own theme', () => {
    renderChooser({ name: 'default', mode: 'light' })

    for (const theme of THEMES) {
      const option = screen.getByTestId(`theme-option-${theme.id}`)
      const expected = resolveVariant(theme, undefined, 'light')
      expect(swatchFills(option), `${theme.id} previews the wrong palette`)
        .toEqual(SWATCH_TOKENS.map(t => expected.palette[t]))
    }
  })

  it('previews an unpicked palette at the polarity on screen', () => {
    renderChooser({ name: 'default', mode: 'dark' })

    const option = screen.getByTestId('theme-option-nord')
    expect(swatchFills(option)).toEqual(
      SWATCH_TOKENS.map(t => resolveVariant(themeById('nord'), undefined, 'dark').palette[t]),
    )
  })

  it('follows the current selection on the palette trigger', () => {
    renderChooser({ name: 'gruvbox', mode: 'dark' })

    const expected = resolveVariant(themeById('gruvbox'), undefined, 'dark')
    expect(swatchFills(nameTrigger())).toEqual(SWATCH_TOKENS.map(t => expected.palette[t]))
  })

  it('previews each variant option with that variant, not the one on screen', () => {
    // Catppuccin on Light, so every dark flavour's chip has to disagree with
    // the polarity showing -- which is the whole reason the menu lists both.
    renderChooser({ name: 'catppuccin', mode: 'light' })

    const mocha = screen.getByTestId('variant-option-catppuccin-mocha')
    const expected = ALL_VARIANTS.find(v => v.id === 'catppuccin-mocha')!
    expect(swatchFills(mocha)).toEqual(SWATCH_TOKENS.map(t => expected.palette[t]))
  })

  it('previews Match UI with the palette the app resolved to', () => {
    renderTerminal(MATCH_BOTH, { name: 'nord', mode: 'dark' })

    const expected = resolveVariant(themeById('nord'), undefined, 'dark')
    expect(swatchFills(screen.getByTestId('theme-option-match-ui')))
      .toEqual(SWATCH_TOKENS.map(t => expected.palette[t]))
  })
})
