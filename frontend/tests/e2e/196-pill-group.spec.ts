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

function selectionIndicator(group: Locator): Locator {
  return group.locator(':scope > [aria-hidden="true"]')
}

async function selectMode(group: Locator, label: string): Promise<Locator> {
  const radio = group.getByRole('radio', { name: label })
  if (await radio.getAttribute('aria-checked') !== 'true')
    await radio.click()
  await expect(radio).toHaveAttribute('aria-checked', 'true')
  return radio
}

async function indicatorDelta(indicator: Locator, radio: Locator) {
  const indicatorBox = await indicator.boundingBox()
  const radioBox = await radio.boundingBox()
  if (!indicatorBox || !radioBox)
    return null
  return {
    left: indicatorBox.x - radioBox.x,
    top: indicatorBox.y - radioBox.y,
    width: indicatorBox.width - radioBox.width,
    height: indicatorBox.height - radioBox.height,
  }
}

async function expectIndicatorAt(indicator: Locator, radio: Locator): Promise<void> {
  await expect.poll(async () => {
    const delta = await indicatorDelta(indicator, radio)
    return delta === null
      ? Number.POSITIVE_INFINITY
      : Math.max(...Object.values(delta).map(value => Math.abs(value)))
  }).toBeLessThanOrEqual(0.05)
}

test.describe('Pill group segmented control', () => {
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
      expect(layout.boxes[index]!.backgroundColor).toBe('rgba(0, 0, 0, 0)')
    }

    const selected = group.getByRole('radio', { checked: true })
    await expectIndicatorAt(indicator, selected)

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
    expect(await selected.evaluate(element => getComputedStyle(element).color))
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

  test('moves the active fill between segments when motion is allowed', async ({ page, leapmuxServer }) => {
    await page.emulateMedia({ reducedMotion: 'no-preference' })
    const group = await openThemeModes(page, leapmuxServer.adminToken)
    const indicator = selectionIndicator(group)
    const system = await selectMode(group, 'System')
    await expectIndicatorAt(indicator, system)

    const transitionedProperties = await group.evaluate(element => new Promise<string[]>((resolve, reject) => {
      const fill = element.querySelector<HTMLElement>(':scope > [aria-hidden="true"]')
      const target = [...element.querySelectorAll<HTMLButtonElement>(':scope > button')]
        .find(button => button.textContent === 'Dark')
      if (!fill || !target) {
        reject(new Error('The segmented control is incomplete.'))
        return
      }

      const properties = new Set<string>()
      const timeout = window.setTimeout(() => {
        reject(new Error(`Missing transitions: ${[...properties].join(', ')}`))
      }, 2000)
      fill.addEventListener('transitionrun', (event) => {
        properties.add(event.propertyName)
        if (properties.has('transform') && properties.has('width')) {
          window.clearTimeout(timeout)
          resolve([...properties])
        }
      })
      target.click()
    }))

    expect(transitionedProperties).toContain('transform')
    expect(transitionedProperties).toContain('width')
    const dark = group.getByRole('radio', { name: 'Dark' })
    await expect(dark).toHaveAttribute('aria-checked', 'true')
    await expectIndicatorAt(indicator, dark)
  })

  test('moves the active fill without visible motion when motion is reduced', async ({ page, leapmuxServer }) => {
    await page.emulateMedia({ reducedMotion: 'reduce' })
    const group = await openThemeModes(page, leapmuxServer.adminToken)
    const indicator = selectionIndicator(group)
    const system = await selectMode(group, 'System')
    await expectIndicatorAt(indicator, system)

    const dark = group.getByRole('radio', { name: 'Dark' })
    await dark.click()
    await expect(dark).toHaveAttribute('aria-checked', 'true')
    await expectIndicatorAt(indicator, dark)

    expect(await page.evaluate(() => matchMedia('(prefers-reduced-motion: reduce)').matches)).toBe(true)
    // Oat restricts every reduced transition to 0.01 ms with an important
    // global rule. This duration produces no visible movement.
    const transitionMs = await indicator.evaluate((element) => {
      const durationList = getComputedStyle(element).transitionDuration
      const durations = durationList.split(',').map(value =>
        Number.parseFloat(value) * (value.trim().endsWith('ms') ? 1 : 1000))
      return Math.max(...durations)
    })
    expect(transitionMs).toBeLessThanOrEqual(0.01)
    expect(await indicator.evaluate(element => element.getAnimations().length)).toBe(0)
  })
})
