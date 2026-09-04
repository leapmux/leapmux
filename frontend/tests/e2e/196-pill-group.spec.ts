import type { Locator, Page } from '@playwright/test'
import { expect, test } from './fixtures'
import { loginViaToken, openSettingsAt, resolvedColor } from './helpers/ui'

async function openThemeModes(page: Page, token: string): Promise<Locator> {
  await loginViaToken(page, token)
  await page.goto('/')
  const dialog = await openSettingsAt(page, 'appearance')
  return dialog
    .locator('[data-setting-id="appearance.theme"]')
    .getByRole('radiogroup', { name: 'Theme mode' })
}

async function openAppTypes(page: Page, token: string): Promise<Locator> {
  await loginViaToken(page, token)
  await page.goto('/')
  const dialog = await openSettingsAt(page, 'apps')
  const registrations = dialog.locator('[data-setting-id="apps.registrations"]')
  await registrations.getByRole('button', { name: 'Register an app' }).click()
  return registrations.getByRole('radiogroup', { name: 'App type' })
}

async function selectMode(group: Locator, label: string): Promise<Locator> {
  const radio = group.getByRole('radio', { name: label })
  if (await radio.getAttribute('aria-checked') !== 'true')
    await radio.click()
  await expect(radio).toHaveAttribute('aria-checked', 'true')
  return radio
}

function selectionIndicator(group: Locator): Locator {
  return group.locator(':scope > [data-pill-selection-fill]')
}

test.describe('pill group segmented control', () => {
  test('renders one content-sized control with dividers and an active fill', async ({ page, leapmuxServer }) => {
    const group = await openThemeModes(page, leapmuxServer.adminToken)
    const indicator = selectionIndicator(group)
    await expect(indicator).toHaveCount(1)

    const layout = await group.evaluate((element) => {
      const groupStyle = getComputedStyle(element)
      const buttons = [...element.querySelectorAll<HTMLButtonElement>(':scope > button')]
      const boxes = buttons.map((button) => {
        const rect = button.getBoundingClientRect()
        const style = getComputedStyle(button)
        return {
          left: rect.left,
          right: rect.right,
          width: rect.width,
          borderRadius: style.borderRadius,
          borderInlineStartWidth: style.borderInlineStartWidth,
          backgroundColor: style.backgroundColor,
          checked: button.getAttribute('aria-checked') === 'true',
        }
      })
      const groupRect = element.getBoundingClientRect()
      return {
        gap: groupStyle.columnGap,
        borderStyle: groupStyle.borderTopStyle,
        borderWidth: groupStyle.borderTopWidth,
        borderRadius: groupStyle.borderRadius,
        groupWidth: groupRect.width,
        groupLeft: groupRect.left,
        groupRight: groupRect.right,
        boxes,
      }
    })

    expect(layout.gap).toBe('0px')
    expect(layout.borderStyle).toBe('solid')
    expect(layout.borderWidth).toBe('1px')
    expect(Number.parseFloat(layout.borderRadius)).toBeGreaterThan(0)
    expect(layout.boxes).toHaveLength(3)
    expect(layout.boxes[0]!.left).toBeCloseTo(layout.groupLeft + 1, 1)
    expect(layout.boxes.at(-1)!.right).toBeCloseTo(layout.groupRight - 1, 1)
    expect(layout.groupWidth).toBeCloseTo(
      layout.boxes.reduce((width, box) => width + box.width, 0) + 2,
      1,
    )
    expect(new Set(layout.boxes.map(box => Math.round(box.width))).size).toBeGreaterThan(1)

    for (let index = 1; index < layout.boxes.length; index++) {
      expect(layout.boxes[index - 1]!.right).toBeCloseTo(layout.boxes[index]!.left, 1)
      expect(layout.boxes[index]!.borderInlineStartWidth).toBe('1px')
      expect(layout.boxes[index]!.borderRadius).toBe('0px')
    }
    for (const box of layout.boxes.filter(box => !box.checked))
      expect(box.backgroundColor).toBe('rgba(0, 0, 0, 0)')

    const selected = group.getByRole('radio', { checked: true })

    const tokens = await page.evaluate(() => {
      const root = getComputedStyle(document.documentElement)
      return {
        primary: root.getPropertyValue('--primary').trim(),
        primaryForeground: root.getPropertyValue('--primary-foreground').trim(),
        mutedForeground: root.getPropertyValue('--muted-foreground').trim(),
      }
    })
    expect(await indicator.evaluate(element => getComputedStyle(element).backgroundColor))
      .toBe(await resolvedColor(page, tokens.primary))
    expect(await selected.evaluate(element => getComputedStyle(element).backgroundColor))
      .toBe('rgba(0, 0, 0, 0)')
    expect(await group.locator(':scope > [data-pill-selection-labels]').evaluate(element => getComputedStyle(element).color))
      .toBe(await resolvedColor(page, tokens.primaryForeground))
    expect(await group.getByRole('radio', { checked: false }).first().evaluate(element => getComputedStyle(element).color))
      .toBe(await resolvedColor(page, tokens.mutedForeground))

    const governedGroup = page
      .locator('[data-setting-id="appearance.terminalTheme"]')
      .getByRole('radiogroup', { name: 'Terminal theme mode' })
    await expect(governedGroup.getByRole('radio').first()).toBeDisabled()
    expect(await governedGroup.evaluate(element => getComputedStyle(element).opacity)).toBe('0.55')
    expect(await governedGroup.getByRole('radio').first().evaluate(element => getComputedStyle(element).opacity))
      .toBe('1')
  })

  test('keeps the active fill and label colors paired while sliding', async ({ page, leapmuxServer }) => {
    const group = await openThemeModes(page, leapmuxServer.adminToken)
    const system = await selectMode(group, 'System')
    const dark = group.getByRole('radio', { name: 'Dark' })
    await dark.click()
    await expect(dark).toHaveAttribute('aria-checked', 'true')
    const tokens = await page.evaluate(() => {
      const root = getComputedStyle(document.documentElement)
      return {
        primary: root.getPropertyValue('--primary').trim(),
        primaryForeground: root.getPropertyValue('--primary-foreground').trim(),
      }
    })
    expect(await system.evaluate(element => getComputedStyle(element).backgroundColor))
      .toBe('rgba(0, 0, 0, 0)')
    expect(await dark.evaluate(element => getComputedStyle(element).transitionProperty))
      .toBe('none')
    expect(await selectionIndicator(group).evaluate(element => getComputedStyle(element).backgroundColor))
      .toBe(await resolvedColor(page, tokens.primary))
    expect(await group.locator(':scope > [data-pill-selection-labels]').evaluate(element => getComputedStyle(element).color))
      .toBe(await resolvedColor(page, tokens.primaryForeground))
  })

  test('uses the accent background only for an unselected hover', async ({ page, leapmuxServer }) => {
    const group = await openThemeModes(page, leapmuxServer.adminToken)
    await expect(selectionIndicator(group)).toHaveCount(1)
    const selected = group.getByRole('radio', { checked: true })
    const unselected = group.getByRole('radio', { checked: false }).first()
    const accent = await page.evaluate(() => getComputedStyle(document.documentElement).getPropertyValue('--accent').trim())

    await selected.hover()
    expect(await selected.evaluate(element => getComputedStyle(element).backgroundColor))
      .toBe('rgba(0, 0, 0, 0)')

    await unselected.hover()
    expect(await unselected.evaluate(element => getComputedStyle(element).backgroundColor))
      .toBe(await resolvedColor(page, accent))
  })

  test('slides the active fill between segments', async ({ page, leapmuxServer }) => {
    const group = await openThemeModes(page, leapmuxServer.adminToken)
    const indicator = selectionIndicator(group)
    await expect(indicator).toHaveCount(1)
    await selectMode(group, 'System')
    await indicator.evaluate(async (element) => {
      await new Promise<void>(resolve => requestAnimationFrame(() => requestAnimationFrame(() => resolve())))
      await Promise.allSettled(element.getAnimations().map(animation => animation.finished))
    })

    const transitionedProperty = await group.evaluate(element => new Promise<string>((resolve, reject) => {
      const fill = element.querySelector<HTMLElement>(':scope > [data-pill-selection-fill]')
      const target = [...element.querySelectorAll<HTMLButtonElement>(':scope > button')]
        .find(button => button.textContent === 'Dark')
      if (!fill || !target) {
        reject(new Error('The segmented control is incomplete.'))
        return
      }

      const timeout = window.setTimeout(() => reject(new Error('The selection did not slide.')), 2000)
      fill.addEventListener('transitionrun', (event) => {
        if (event.propertyName !== 'clip-path')
          return
        window.clearTimeout(timeout)
        resolve(event.propertyName)
      })
      target.click()
    }))

    expect(transitionedProperty).toBe('clip-path')
    const dark = group.getByRole('radio', { name: 'Dark' })
    await expect(dark).toHaveAttribute('aria-checked', 'true')
    expect(await dark.evaluate(element => getComputedStyle(element).boxShadow)).toBe('none')

    await indicator.evaluate(async (element) => {
      await Promise.allSettled(element.getAnimations().map(animation => animation.finished))
    })
    expect(await dark.evaluate(element => getComputedStyle(element).boxShadow)).not.toBe('none')
  })

  test('settles without a slide when motion is reduced', async ({ page, leapmuxServer }) => {
    await page.emulateMedia({ reducedMotion: 'reduce' })
    const group = await openThemeModes(page, leapmuxServer.adminToken)
    const indicator = selectionIndicator(group)
    await selectMode(group, 'System')

    const state = await group.evaluate(element => new Promise<{
      animations: number
      checked: string | null
      shadow: string
    }>((resolve, reject) => {
      const fill = element.querySelector<HTMLElement>(':scope > [data-pill-selection-fill]')
      const target = [...element.querySelectorAll<HTMLButtonElement>(':scope > button')]
        .find(button => button.textContent === 'Dark')
      if (!fill || !target) {
        reject(new Error('The segmented control is incomplete.'))
        return
      }
      target.click()
      requestAnimationFrame(() => resolve({
        animations: fill.getAnimations().length,
        checked: target.getAttribute('aria-checked'),
        shadow: getComputedStyle(target).boxShadow,
      }))
    }))

    expect(state.checked).toBe('true')
    expect(state.animations).toBe(0)
    expect(state.shadow).not.toBe('none')
    await expect(indicator).toHaveCount(1)
  })

  test('shows keyboard focus on the selected segment', async ({ page, leapmuxServer }) => {
    const group = await openThemeModes(page, leapmuxServer.adminToken)
    const selected = group.getByRole('radio', { checked: true })
    await page.keyboard.press('Tab')
    await selected.focus()

    const colors = await selected.evaluate((element) => {
      const root = getComputedStyle(document.documentElement)
      return {
        outline: getComputedStyle(element).outlineColor,
        outlineStyle: getComputedStyle(element).outlineStyle,
        selectedForeground: root.getPropertyValue('--primary-foreground').trim(),
      }
    })
    expect(colors.outlineStyle).toBe('solid')
    expect(colors.outline).toBe(await resolvedColor(page, colors.selectedForeground))
  })

  test('keeps the selected segment distinct in forced colors', async ({ page, leapmuxServer }) => {
    await page.emulateMedia({ forcedColors: 'active' })
    const group = await openThemeModes(page, leapmuxServer.adminToken)
    const selected = group.getByRole('radio', { checked: true })
    const unselected = group.getByRole('radio', { checked: false }).first()

    await expect(selected).toHaveAttribute('aria-checked', 'true')
    expect(await selectionIndicator(group).evaluate(element => getComputedStyle(element).backgroundColor))
      .not
      .toBe(await unselected.evaluate(element => getComputedStyle(element).backgroundColor))
  })

  test('keeps every segment inside a narrow settings row', async ({ page, leapmuxServer }) => {
    const group = await openAppTypes(page, leapmuxServer.adminToken)
    await page.setViewportSize({ width: 375, height: 800 })
    const layout = await group.evaluate((element) => {
      const container = element.closest<HTMLElement>('[data-setting-id]')
      if (!container)
        throw new Error('The segmented control has no settings row.')
      const groupRect = element.getBoundingClientRect()
      const containerRect = container.getBoundingClientRect()
      return {
        groupLeft: groupRect.left,
        groupRight: groupRect.right,
        containerLeft: containerRect.left,
        containerRight: containerRect.right,
        viewportWidth: window.innerWidth,
        maxButtonOverflow: Math.max(...[...element.querySelectorAll<HTMLButtonElement>('[role="radio"]')]
          .map(button => button.scrollWidth - button.clientWidth)),
      }
    })

    expect(layout.groupLeft).toBeGreaterThanOrEqual(layout.containerLeft)
    expect(layout.groupRight).toBeLessThanOrEqual(layout.containerRight)
    expect(layout.groupLeft).toBeGreaterThanOrEqual(0)
    expect(layout.groupRight).toBeLessThanOrEqual(layout.viewportWidth)
    expect(layout.maxButtonOverflow).toBeLessThanOrEqual(1)
  })
})
