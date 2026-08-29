import type { Page } from '@playwright/test'
import { CODE_BLOCK_TINT_PERCENT } from '../../src/styles/codePalette'
import { motion } from '../../src/styles/tokens'
import { colorAlpha } from '../../src/test-support/color'
import { expect, test } from './fixtures'
import { getBrowserPrefValue, loginViaToken, openSettingsAt, pickTheme, resolvedColor } from './helpers/ui'

/**
 * The palette actually reaches the page.
 *
 * The unit suites cover the catalogue, the store and the binding; what they
 * cannot cover is the last hop -- that the attributes on <html> select a rule
 * `global.css.ts` really emitted, so the custom properties change. The palette
 * rules key on `data-ui-light` / `data-ui-dark`; `data-ui-theme` names the
 * resolved theme and is asserted here because it is the stable identity of what
 * the picker chose. A theme that is stored, resolved and never painted passes
 * every other test in the repo.
 */
async function backgroundVar(page: import('@playwright/test').Page): Promise<string> {
  return page.evaluate(() =>
    getComputedStyle(document.documentElement).getPropertyValue('--background').trim())
}

test.describe('Theme picker', () => {
  test('repaints the app and survives a reload', async ({ page, leapmuxServer }) => {
    await loginViaToken(page, leapmuxServer.adminToken)
    await page.goto('/')
    const dialog = await openSettingsAt(page, 'appearance')
    const themeRow = dialog.locator('[data-setting-id="appearance.theme"]')

    const before = await backgroundVar(page)
    expect(before).toBeTruthy()

    await pickTheme(themeRow, 'catppuccin')
    await expect(page.locator('html')).toHaveAttribute('data-ui-theme', 'catppuccin')
    await expect.poll(() => backgroundVar(page)).not.toBe(before)

    const catppuccinLight = await backgroundVar(page)

    // The mode is the other half of the same choice, and it selects the other
    // variant of the SAME palette.
    await themeRow.getByRole('radiogroup', { name: 'Theme mode' }).getByRole('radio', { name: 'Dark' }).click()
    await expect(page.locator('html')).toHaveAttribute('data-theme', 'dark')
    await expect(page.locator('html')).toHaveAttribute('data-ui-theme', 'catppuccin')
    await expect.poll(() => backgroundVar(page)).not.toBe(catppuccinLight)

    const catppuccinDark = await backgroundVar(page)

    await page.reload()
    await expect(page.locator('html')).toHaveAttribute('data-ui-theme', 'catppuccin')
    await expect(page.locator('html')).toHaveAttribute('data-theme', 'dark')
    await expect.poll(() => backgroundVar(page)).toBe(catppuccinDark)
  })

  test('states light positively, so the attribute is never merely absent', async ({ page, leapmuxServer }) => {
    // Light used to be "no data-theme attribute". Every palette now emits a
    // paired light and dark rule, and both halves need the positive statement.
    await loginViaToken(page, leapmuxServer.adminToken)
    await page.goto('/')
    await openSettingsAt(page, 'appearance')
    const themeRow = page.locator('[data-setting-id="appearance.theme"]')

    await themeRow.getByRole('radiogroup', { name: 'Theme mode' }).getByRole('radio', { name: 'Light' }).click()
    await expect(page.locator('html')).toHaveAttribute('data-theme', 'light')
  })

  test('offers the picker in the no-workspace empty state and writes the same preference', async ({ page, leapmuxServer }) => {
    await loginViaToken(page, leapmuxServer.adminToken)
    await page.goto('/')

    const emptyState = page.getByTestId('no-workspace-empty-state')
    await expect(emptyState).toBeVisible()

    await pickTheme(emptyState, 'gruvbox')
    await expect(page.locator('html')).toHaveAttribute('data-ui-theme', 'gruvbox')

    // The dialog reads the same key, so it reports what the empty state wrote.
    await openSettingsAt(page, 'appearance')
    const themeRow = page.locator('[data-setting-id="appearance.theme"]')
    await expect(themeRow.getByTestId('theme-chooser-name')).toHaveAttribute('data-value', 'gruvbox')

    // ...and it survives a reload, which is the journey the deleted setup-page
    // case used to cover. A better anchor than that one was: the empty state
    // carries no device override, so the pick routes to the ACCOUNT tier and
    // the reload proves the hub round-trip rather than a localStorage one.
    await page.reload()
    await expect(page.locator('html')).toHaveAttribute('data-ui-theme', 'gruvbox')
  })

  /**
   * Put a governed row back on the sentinel.
   *
   * Every test in this file drives the same admin account, so a preference one
   * of them writes is the state the next one starts from. These cases assert
   * what happens FROM the following state, so they establish it rather than
   * assume it -- which also makes them independent of the order Playwright
   * happens to run them in.
   */
  async function resetToMatchUi(row: import('@playwright/test').Locator) {
    await pickTheme(row, 'match-ui')
    await expect(row.getByTestId('theme-chooser-name')).toHaveAttribute('data-value', 'match-ui')
  }

  // Default borrows both of its non-UI palettes, and each picker says so. This
  // is the one assertion that reaches the wiring: the chooser's own suite proves
  // it honours the surface it is told, and nothing else proves each row tells it
  // the right one -- a copy-paste between the two controls is invisible without
  // this.
  test('names the palettes Default borrows, per row', async ({ page, leapmuxServer }) => {
    await loginViaToken(page, leapmuxServer.adminToken)
    await page.goto('/')
    const dialog = await openSettingsAt(page, 'appearance')

    // Each row's own menu names the palette Default borrows for that surface.
    // The option is keyed by theme id, so this reads the LABEL the picker chose.
    await expect(dialog.locator('[data-setting-id="appearance.theme"]')
      .getByTestId('theme-option-default')).toHaveText('Default')
    // Its sixteen ANSI colours are Dimidium's.
    await expect(dialog.locator('[data-setting-id="appearance.terminalTheme"]')
      .getByTestId('theme-option-default')).toHaveText('Default (Dimidium)')
    // And it highlights with GitHub's theme.
    await expect(dialog.locator('[data-setting-id="appearance.syntaxTheme"]')
      .getByTestId('theme-option-default')).toHaveText('Default (GitHub)')
  })

  // The chip is the only part of the picker that describes a palette instead of
  // naming it, and it is built from nine palette tokens drawn on a tenth. The
  // unit suites prove the token choice and the wiring; this proves the last hop
  // -- that the colours reach the DOM and that the chip agrees with what the app
  // actually painted.
  test('previews the chosen palette in the chip beside its name', async ({ page, leapmuxServer }) => {
    await loginViaToken(page, leapmuxServer.adminToken)
    await page.goto('/')
    const dialog = await openSettingsAt(page, 'appearance')
    const themeRow = dialog.locator('[data-setting-id="appearance.theme"]')

    await pickTheme(themeRow, 'gruvbox')
    await expect(page.locator('html')).toHaveAttribute('data-ui-theme', 'gruvbox')

    const swatch = themeRow.getByTestId('theme-chooser-name').getByTestId('theme-swatch')
    await expect(swatch.locator('rect')).toHaveCount(9)

    // The chip's fill is the palette's own --background, which is the value the
    // page is painted with. Compared through `resolvedColor` because the
    // palette states a hex and the DOM reports `rgb(...)`.
    const painted = await backgroundVar(page)
    await expect.poll(() => swatch.evaluate(el => getComputedStyle(el).backgroundColor))
      .toBe(await resolvedColor(page, painted))

    // Every pip stands off that background, which is the property that makes
    // the chip readable rather than a flat square.
    const fills = await swatch.locator('rect').evaluateAll(els => els.map(el => el.getAttribute('fill')))
    expect(fills).toHaveLength(9)
    expect(fills).not.toContain(painted)
    // Nine nearly-distinct colours, which catches a fill mapping that painted
    // every pip the same. Not exactly nine: --border and --input are one value
    // in Default Dark, so the catalogue does not promise it. ThemeSwatch's own
    // suite is what measures the separation, on all thirty variants.
    expect(new Set(fills).size).toBeGreaterThanOrEqual(8)
  })

  // The terminal is a SECOND appearance choice that defaults to following the
  // app. These cases are the requirement the split exists for.
  test('moves the terminal with the app while the terminal is left alone', async ({ page, leapmuxServer }) => {
    await loginViaToken(page, leapmuxServer.adminToken)
    await page.goto('/')
    const dialog = await openSettingsAt(page, 'appearance')
    const themeRow = dialog.locator('[data-setting-id="appearance.theme"]')
    const terminalRow = dialog.locator('[data-setting-id="appearance.terminalTheme"]')

    // The sentinel is the shipped default, and it is what makes the empty
    // state's single picker move the terminal too.
    await resetToMatchUi(terminalRow)
    // One control says "follow the app", and it governs the mode pills, which
    // keep reporting the mode the app is on.
    const terminalModes = terminalRow.getByRole('radiogroup', { name: 'Terminal theme mode' })
    await expect(terminalModes.getByRole('radio', { name: 'Match UI' })).toHaveCount(0)
    await expect(terminalModes.getByRole('radio', { name: 'System' })).toBeDisabled()

    await pickTheme(themeRow, 'nord')
    // Still following, not silently pinned to the value it happened to hold.
    await expect(terminalRow.getByTestId('theme-chooser-name')).toHaveAttribute('data-value', 'match-ui')
  })

  test('lets the terminal take its own palette and mode', async ({ page, leapmuxServer }) => {
    await loginViaToken(page, leapmuxServer.adminToken)
    await page.goto('/')
    const dialog = await openSettingsAt(page, 'appearance')
    const themeRow = dialog.locator('[data-setting-id="appearance.theme"]')
    const terminalRow = dialog.locator('[data-setting-id="appearance.terminalTheme"]')

    await pickTheme(themeRow, 'catppuccin')
    await themeRow.getByRole('radiogroup', { name: 'Theme mode' }).getByRole('radio', { name: 'Light' }).click()

    await pickTheme(terminalRow, 'nord')
    await terminalRow.getByRole('radiogroup', { name: 'Terminal theme mode' }).getByRole('radio', { name: 'Dark' }).click()

    // The app is unaffected by the terminal's choice.
    await expect(page.locator('html')).toHaveAttribute('data-ui-theme', 'catppuccin')
    await expect(page.locator('html')).toHaveAttribute('data-theme', 'light')
    await expect(themeRow.getByTestId('theme-chooser-name')).toHaveAttribute('data-value', 'catppuccin')

    // And the terminal keeps its own, as one document under one scope chip.
    const chip = terminalRow.getByTestId('scope-chip-appearance.terminalTheme')
    await chip.click()
    await page.getByRole('menuitemradio', { name: 'Override on this device' }).click()
    await expect.poll(() => getBrowserPrefValue(page, leapmuxServer.adminUserId, 'terminalTheme'))
      .toEqual({ name: 'nord', mode: 'dark' })
  })

  test('detaches the terminal with one control, seeding the mode from the app', async ({ page, leapmuxServer }) => {
    // The two halves are ONE decision, so leaving "Match UI" has to answer for
    // both. It seeds the mode from the app, which is what makes detaching
    // change nothing on screen until the user adjusts it.
    await loginViaToken(page, leapmuxServer.adminToken)
    await page.goto('/')
    await openSettingsAt(page, 'appearance')
    const themeRow = page.locator('[data-setting-id="appearance.theme"]')
    const terminalRow = page.locator('[data-setting-id="appearance.terminalTheme"]')

    await themeRow.getByRole('radiogroup', { name: 'Theme mode' }).getByRole('radio', { name: 'Dark' }).click()
    await resetToMatchUi(terminalRow)

    await pickTheme(terminalRow, 'gruvbox')
    const terminalModes = terminalRow.getByRole('radiogroup', { name: 'Terminal theme mode' })
    await expect(terminalModes.getByRole('radio', { name: 'Dark' })).toBeEnabled()
    await expect(terminalModes.getByRole('radio', { name: 'Dark' })).toHaveAttribute('aria-checked', 'true')

    const chip = terminalRow.getByTestId('scope-chip-appearance.terminalTheme')
    await chip.click()
    await page.getByRole('menuitemradio', { name: 'Override on this device' }).click()
    await expect.poll(() => getBrowserPrefValue(page, leapmuxServer.adminUserId, 'terminalTheme'))
      .toEqual({ name: 'gruvbox', mode: 'dark' })
  })

  // The THIRD appearance surface. Unlike the other two it cannot be applied in
  // CSS -- Shiki bakes the colour into every token -- so this checks the row
  // exists, writes the ordinary preference, and actually repaints code.
  test('offers a syntax theme row that writes its own preference', async ({ page, leapmuxServer }) => {
    await loginViaToken(page, leapmuxServer.adminToken)
    await page.goto('/')
    const dialog = await openSettingsAt(page, 'appearance')
    const syntaxRow = dialog.locator('[data-setting-id="appearance.syntaxTheme"]')

    const themeRow = dialog.locator('[data-setting-id="appearance.theme"]')
    await expect(syntaxRow.getByTestId('theme-chooser-name')).toBeVisible()

    // Pin the app's mode, so the seeded value below is a known one rather than
    // whatever an earlier case in this file left on the shared account.
    await themeRow.getByRole('radiogroup', { name: 'Theme mode' }).getByRole('radio', { name: 'Light' }).click()

    // The syntax row is governed by the same one control the terminal row uses.
    await resetToMatchUi(syntaxRow)
    await expect(
      syntaxRow.getByRole('radiogroup', { name: 'Syntax theme mode' }).getByRole('radio', { name: 'Light' }),
    ).toBeDisabled()

    await pickTheme(syntaxRow, 'nord')

    const chip = syntaxRow.getByTestId('scope-chip-appearance.syntaxTheme')
    await chip.click()
    await page.getByRole('menuitemradio', { name: 'Override on this device' }).click()
    await expect.poll(() => getBrowserPrefValue(page, leapmuxServer.adminUserId, 'syntaxTheme'))
      .toEqual({ name: 'nord', mode: 'light' })
  })

  test('leaves the app and terminal alone when only the syntax theme changes', async ({ page, leapmuxServer }) => {
    await loginViaToken(page, leapmuxServer.adminToken)
    await page.goto('/')
    const dialog = await openSettingsAt(page, 'appearance')
    const themeRow = dialog.locator('[data-setting-id="appearance.theme"]')
    const syntaxRow = dialog.locator('[data-setting-id="appearance.syntaxTheme"]')

    await pickTheme(themeRow, 'catppuccin')
    await expect(page.locator('html')).toHaveAttribute('data-ui-theme', 'catppuccin')

    await pickTheme(syntaxRow, 'solarized')
    // The three surfaces are independent: the app keeps its palette.
    await expect(page.locator('html')).toHaveAttribute('data-ui-theme', 'catppuccin')
    await expect(themeRow.getByTestId('theme-chooser-name')).toHaveAttribute('data-value', 'catppuccin')
  })

  test('keeps the palette when only the mode changes', async ({ page, leapmuxServer }) => {
    // The two halves are one key, so a partial write must not reset the other.
    await loginViaToken(page, leapmuxServer.adminToken)
    await page.goto('/')
    const dialog = await openSettingsAt(page, 'appearance')
    const themeRow = dialog.locator('[data-setting-id="appearance.theme"]')

    await pickTheme(themeRow, 'solarized')
    await themeRow.getByRole('radiogroup', { name: 'Theme mode' }).getByRole('radio', { name: 'Dark' }).click()

    await expect(themeRow.getByTestId('theme-chooser-name')).toHaveAttribute('data-value', 'solarized')
    await expect(page.locator('html')).toHaveAttribute('data-ui-theme', 'solarized')

    // Whichever tier it landed on, the stored document carries both halves.
    const chip = dialog.getByTestId('scope-chip-appearance.theme')
    await chip.click()
    await page.getByRole('menuitemradio', { name: 'Override on this device' }).click()
    await expect.poll(() => getBrowserPrefValue(page, leapmuxServer.adminUserId, 'theme'))
      .toEqual({ name: 'solarized', mode: 'dark' })
  })

  test('repaints the CODE palette from the chosen theme, and gives a block a field of its own', async ({ page, leapmuxServer }) => {
    // The code palette is published by its own rules, keyed on
    // `data-code-variant`, and the fallback that covers the frames before that
    // attribute is written used to be a `:root` rule declared AFTER them. `:root`
    // and `[data-code-variant="X"]` are both (0,1,0) and both match <html>, so
    // the fallback won every contest on declaration order and all thirty variants
    // painted Default's light code palette: a code block took the page's own
    // colour on the default theme, and became a white slab with dark text on
    // every dark one. Nothing threw, and no module returns the wrong value --
    // only a browser settles a cascade.
    //
    // Two explicit picks, so this asserts a TRANSITION and not the state the
    // previous test left, exactly as `resetToMatchUi` above argues.
    // The SYNTAX row is what owns the code palette. Earlier cases in this file
    // pin it away from the UI theme, so driving the app's row here would assert
    // against a palette the syntax preference is no longer following.
    await loginViaToken(page, leapmuxServer.adminToken)
    await page.goto('/')
    await openSettingsAt(page, 'appearance')
    const syntaxRow = page.locator('[data-setting-id="appearance.syntaxTheme"]')

    await pickTheme(syntaxRow, 'gruvbox')
    await expect(page.locator('html')).toHaveAttribute('data-code-variant', /^gruvbox-/)
    const gruvbox = await resolvedColor(page, 'var(--code-background)')

    await pickTheme(syntaxRow, 'nord')
    await expect(page.locator('html')).toHaveAttribute('data-code-variant', /^nord-/)
    await expect.poll(() => resolvedColor(page, 'var(--code-background)')).not.toBe(gruvbox)
  })

  /**
   * Open the UI theme menu inside a freshly opened Preferences dialog.
   *
   * Returns the panel and the popover. The popover is matched by aria-label as
   * well as testid: Appearance renders three `ThemeChooser`s -- UI, terminal
   * and syntax -- and they share the testid.
   */
  async function openThemeMenu(page: Page, token: string) {
    await loginViaToken(page, token)
    await page.goto('/')
    await openSettingsAt(page, 'appearance')
    const panel = page.locator('#preferences-panel')
    const trigger = panel.getByRole('button', { name: 'Theme', exact: true })
    const menu = panel.locator('[data-testid="theme-chooser-name-menu"][aria-label="Theme"]')
    return { panel, trigger, menu }
  }

  test('leaves an open dialog off the containing-block chain of its menus', async ({ page, leapmuxServer }) => {
    // A menu popover inside a dialog is `position: fixed` and is placed with
    // VIEWPORT coordinates by `calcPopoverPosition`. Any transform on an
    // ancestor -- `scale(1)` included, which paints nothing -- makes that
    // ancestor the containing block for it, so those coordinates come to mean
    // something else.
    //
    // The damage is invisible until dismiss. An open popover sits in the top
    // layer, which resolves against the viewport whatever its ancestors say; on
    // the way out it leaves the top layer and the same unchanged `left`
    // re-resolves against the dialog, jumping the menu right by the dialog's own
    // left offset. Chromium keeps it in the top layer for the whole close
    // transition and hides the jump, so this asserts the CAUSE rather than the
    // symptom -- the symptom is only reachable on WebKit.
    const { trigger, menu } = await openThemeMenu(page, leapmuxServer.adminToken)
    await trigger.click()
    await expect(menu).toBeVisible()

    const { creators, bodyLeft } = await menu.evaluate((el) => {
      const found: string[] = []
      for (let n = el.parentElement; n; n = n.parentElement) {
        // `body` is the ONE tolerated creator, and it is tolerated because its
        // box coincides with the viewport rather than because it is harmless in
        // principle. Its transform is the iOS viewport lock in
        // `global.css.ts` -- it cancels the residual `visualViewport.offsetTop`
        // WebKit leaves after a keyboard dismiss -- so it cannot simply be
        // dropped. `bodyLeft` below is what keeps the tolerance honest.
        if (n.tagName === 'BODY')
          continue
        const s = getComputedStyle(n)
        // `filter` and `perspective` establish one for fixed descendants too,
        // so a future backdrop treatment on the dialog cannot slip past.
        if (s.transform !== 'none' || s.filter !== 'none' || s.perspective !== 'none')
          found.push(`${n.tagName.toLowerCase()} transform=${s.transform} filter=${s.filter}`)
      }
      return { creators: found, bodyLeft: document.body.getBoundingClientRect().left }
    })
    expect(creators, `a fixed menu inside these would not be placed in viewport coordinates:\n  ${creators.join('\n  ')}`)
      .toEqual([])
    // The axis this bug moves on. A body whose box started anywhere but the
    // viewport's left edge would shift every dismissing menu by that much, and
    // the exclusion above would be hiding it.
    expect(bodyLeft, 'body is a containing block whose box is NOT at the viewport origin').toBe(0)
  })

  // A menu panel is a focus HOST, not a control, so it rings in NEITHER
  // modality -- and the item the user navigates to rings instead.
  //
  // `DropdownMenu` gives its popover `tabindex="-1"` and focuses it on open,
  // because `popover="auto"` moves focus nowhere and the arrow keys would
  // otherwise go to the document. Whether that non-control drew a ring was left
  // to each engine's `:focus-visible` heuristic, and they disagreed: Chromium
  // painted nothing after a click, while the WKWebView the macOS desktop app
  // runs on drew a ring around the whole panel, and not on every open.
  // `global.css.ts` answers it outright for the relay case.
  //
  // A static scan cannot pin this, because no stylesheet asks for the ring.
  test('rings no menu panel when the user opens it with the mouse', async ({ page, leapmuxServer }) => {
    const { trigger, menu } = await openThemeMenu(page, leapmuxServer.adminToken)
    await trigger.click()
    await expect(menu).toBeVisible()

    // The popover, not some descendant: it is the element that took focus.
    await expect.poll(() => menu.evaluate(el => el === document.activeElement)).toBe(true)
    // `outline-style`, not the width: a width alone would pass on a 0px outline
    // that a later rule could widen without this case noticing.
    await expect.poll(() => menu.evaluate(el => getComputedStyle(el).outlineStyle)).toBe('none')
  })

  test('rings the ITEM, not the panel, when the user opens it with the keyboard', async ({ page, leapmuxServer }) => {
    // THE HALF WITH TEETH ON THIS ENGINE. Chromium suppresses the ring after a
    // click by itself, so the mouse case above passes here even with the rule
    // deleted -- WebKit is what needed it, and this suite runs Chromium. A
    // keypress is where Chromium DOES paint the panel, so this case fails
    // without the rule on the very engine CI runs.
    const { trigger, menu } = await openThemeMenu(page, leapmuxServer.adminToken)
    await trigger.focus()
    await page.keyboard.press('Enter')
    await expect(menu).toBeVisible()
    await expect.poll(() => menu.evaluate(el => el === document.activeElement)).toBe(true)
    await expect.poll(() => menu.evaluate(el => getComputedStyle(el).outlineStyle)).toBe('none')

    // The keyboard user is not left without a cue. The first Arrow moves focus
    // onto an ITEM, which rings the way every other control does -- without
    // this, the rule above could be "delete the focus indicator" rather than
    // "move it to the element the user is actually navigating".
    await page.keyboard.press('ArrowDown')
    const item = await page.evaluate(() => {
      const el = document.activeElement as HTMLElement | null
      return el ? { role: el.getAttribute('role'), outline: getComputedStyle(el).outlineStyle } : null
    })
    expect(item?.role, 'ArrowDown did not land on a menu item').toMatch(/^menuitem/)
    expect(item?.outline, 'the focused menu item draws no ring').not.toBe('none')
  })

  // One Escape dismisses ONE layer, innermost first -- the WAI-ARIA menu
  // pattern. The menu is the layer above the dialog, so the first press takes
  // the menu and leaves the dialog; only the second press takes the dialog.
  //
  // Chromium is required. happy-dom has no `showModal`, no top layer and no
  // native popover light-dismiss, so a unit test passes on the broken app.
  test('dismisses the menu on the first Escape and the dialog on the second', async ({ page, leapmuxServer }) => {
    const { trigger, menu } = await openThemeMenu(page, leapmuxServer.adminToken)
    const dialog = page.getByRole('dialog', { name: 'Preferences' })
    await trigger.click()
    await expect(menu).toBeVisible()

    await page.keyboard.press('Escape')
    await expect(menu).toBeHidden()

    // `Dialog` defers its unmount by `motion.fast` ms so the fade-out can play,
    // so a bare visibility check here also passes on a dialog that IS closing.
    // Outlast that window before reading the dialog.
    await page.waitForTimeout(motion.fast * 3)
    await expect(dialog, 'the first Escape took the dialog as well as the menu').toBeVisible()

    // The dialog is still live, not merely still painted.
    await trigger.click()
    await expect(menu).toBeVisible()
    await page.keyboard.press('Escape')
    await expect(menu).toBeHidden()

    await page.keyboard.press('Escape')
    await expect(dialog, 'the second Escape left the dialog open').toBeHidden()
  })

  test('turns the code block opaque when the syntax theme opposes the app', async ({ page, leapmuxServer }) => {
    // The one case a translucent tint cannot answer. Shiki bakes each token's
    // colour at tokenize time, so a dark theme's tokens are light -- and a tint
    // over a light page stays a light field, which put those tokens at a median
    // 1.97:1. The field has to carry them across the flip instead.
    //
    // Both halves are established here rather than assumed, because every case
    // in this file drives the same admin account.
    await loginViaToken(page, leapmuxServer.adminToken)
    await page.goto('/')
    const dialog = await openSettingsAt(page, 'appearance')
    const themeRow = dialog.locator('[data-setting-id="appearance.theme"]')
    const syntaxRow = dialog.locator('[data-setting-id="appearance.syntaxTheme"]')

    await themeRow.getByRole('radiogroup', { name: 'Theme mode' }).getByRole('radio', { name: 'Light' }).click()
    await expect(page.locator('html')).toHaveAttribute('data-theme', 'light')

    // Agreeing first, so the flip below is a transition this case caused.
    await pickTheme(syntaxRow, 'nord')
    await syntaxRow.getByRole('radiogroup', { name: 'Syntax theme mode' }).getByRole('radio', { name: 'Light' }).click()
    await expect(page.locator('html')).toHaveAttribute('data-code-polarity', 'light')
    await expect
      .poll(() => resolvedColor(page, 'var(--code-block-background)').then(colorAlpha), { message: 'an agreeing syntax theme composites on its host' })
      .toBeCloseTo(CODE_BLOCK_TINT_PERCENT / 100, 4)

    await syntaxRow.getByRole('radiogroup', { name: 'Syntax theme mode' }).getByRole('radio', { name: 'Dark' }).click()
    await expect(page.locator('html')).toHaveAttribute('data-code-polarity', 'dark')
    await expect(page.locator('html')).toHaveAttribute('data-theme', 'light')
    await expect
      .poll(() => resolvedColor(page, 'var(--code-block-background)').then(colorAlpha), { message: 'an opposed syntax theme needs an opaque field' })
      .toBe(1)
  })
})

/**
 * `color-scheme` follows the APP, not the OS.
 *
 * Oat sets `color-scheme: light dark` at :root, which leaves every
 * `light-dark()` in the stylesheet resolving against the OS preference. The
 * app's polarity is its own choice, so on a dark app under a light OS three Oat
 * components took the light branch of an expression built from OUR tokens --
 * the skeleton's shimmer became `color-mix(in srgb, var(--muted) 15%, white)`,
 * a near-white band at a median 9.2x the row's luminance sweeping across it.
 *
 * This can only be tested in a browser: `light-dark()` is resolved by the UA
 * against a used value no unit test can observe. The OS is pinned OPPOSITE the
 * app in both cases below, because matching them hides the bug entirely.
 */
test.describe('color-scheme follows the app', () => {
  /** Resolve a `light-dark()` through the UA and report which branch it took. */
  async function branchTaken(page: import('@playwright/test').Page): Promise<'light' | 'dark'> {
    return page.evaluate(() => {
      const probe = document.createElement('div')
      // Opaque, maximally separated values, so the branch is unambiguous.
      probe.style.color = 'light-dark(rgb(255, 255, 255), rgb(0, 0, 0))'
      document.body.append(probe)
      const taken = getComputedStyle(probe).color === 'rgb(255, 255, 255)' ? 'light' : 'dark'
      probe.remove()
      return taken
    })
  }

  test.describe('under a LIGHT OS', () => {
    test.use({ colorScheme: 'light' })

    test('resolves light-dark() to the dark branch once the app is dark', async ({ page, leapmuxServer }) => {
      await loginViaToken(page, leapmuxServer.adminToken)
      await page.goto('/')
      await openSettingsAt(page, 'appearance')
      const themeRow = page.locator('[data-setting-id="appearance.theme"]')

      await themeRow.getByRole('radiogroup', { name: 'Theme mode' }).getByRole('radio', { name: 'Dark' }).click()
      await expect(page.locator('html')).toHaveAttribute('data-theme', 'dark')

      await expect.poll(() => page.evaluate(() =>
        getComputedStyle(document.documentElement).colorScheme)).toBe('dark')
      expect(await branchTaken(page)).toBe('dark')
    })
  })

  test.describe('under a DARK OS', () => {
    test.use({ colorScheme: 'dark' })

    test('resolves light-dark() to the light branch once the app is light', async ({ page, leapmuxServer }) => {
      await loginViaToken(page, leapmuxServer.adminToken)
      await page.goto('/')
      await openSettingsAt(page, 'appearance')
      const themeRow = page.locator('[data-setting-id="appearance.theme"]')

      await themeRow.getByRole('radiogroup', { name: 'Theme mode' }).getByRole('radio', { name: 'Light' }).click()
      await expect(page.locator('html')).toHaveAttribute('data-theme', 'light')

      await expect.poll(() => page.evaluate(() =>
        getComputedStyle(document.documentElement).colorScheme)).toBe('light')
      expect(await branchTaken(page)).toBe('light')
    })
  })
})
