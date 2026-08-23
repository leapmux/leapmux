import { expect, test } from './fixtures'

/**
 * Runtime geometry for modal safe-area insets.
 *
 * Desktop Chromium always reports `env(safe-area-inset-*)` as 0, so this
 * spec sets the `--leapmux-safe-area-inset-*` bridges from Dialog.css.ts to
 * iPhone 14 Pro–like values and asserts the open panel and its close button
 * clear those insets. No screenshots — bounding boxes only.
 */

const IPHONE_SAFE = {
  top: 47,
  right: 0,
  bottom: 34,
  left: 0,
} as const

async function applySimulatedSafeArea(page: import('@playwright/test').Page) {
  await page.addStyleTag({
    content: `
      :root {
        --leapmux-safe-area-inset-top: ${IPHONE_SAFE.top}px;
        --leapmux-safe-area-inset-right: ${IPHONE_SAFE.right}px;
        --leapmux-safe-area-inset-bottom: ${IPHONE_SAFE.bottom}px;
        --leapmux-safe-area-inset-left: ${IPHONE_SAFE.left}px;
      }
    `,
  })
}

test.describe('dialog safe-area geometry', () => {
  test.use({ viewport: { width: 390, height: 844 } })

  test('panel and close button clear simulated iPhone safe-area insets', async ({
    page,
    authenticatedWorkspace,
  }) => {
    void authenticatedWorkspace

    await applySimulatedSafeArea(page)

    await page.getByRole('button', { name: 'Toggle workspaces' }).click()
    await page.locator('[data-testid="sidebar-new-workspace"]').click()
    await expect(page.getByRole('heading', { name: 'New workspace', level: 2 })).toBeVisible()

    const dialog = page.locator('dialog[open]')
    const closeBtn = dialog.getByRole('button', { name: 'Close' })
    await expect(closeBtn).toBeVisible()

    const geometry = await page.evaluate(() => {
      const dlg = document.querySelector('dialog[open]')
      const close = dlg?.querySelector('button[aria-label="Close"]')
      if (!(dlg instanceof HTMLElement) || !(close instanceof HTMLElement))
        return null
      const d = dlg.getBoundingClientRect()
      const c = close.getBoundingClientRect()
      const cs = getComputedStyle(dlg)
      return {
        dialog: { top: d.top, right: d.right, bottom: d.bottom, left: d.left },
        close: { top: c.top, bottom: c.bottom },
        computed: {
          top: cs.top,
          right: cs.right,
          bottom: cs.bottom,
          left: cs.left,
        },
        viewport: { w: window.innerWidth, h: window.innerHeight },
      }
    })
    expect(geometry, 'open dialog + close button').not.toBeNull()
    const g = geometry!

    // Computed style must resolve the test bridges (not stay at UA inset:0).
    expect(g.computed.top).toBe(`${IPHONE_SAFE.top}px`)
    expect(g.computed.bottom).toBe(`${IPHONE_SAFE.bottom}px`)
    expect(g.computed.left).toBe(`${IPHONE_SAFE.left}px`)
    expect(g.computed.right).toBe(`${IPHONE_SAFE.right}px`)

    // Panel sits inside the safe rectangle.
    expect(g.dialog.top).toBeGreaterThanOrEqual(IPHONE_SAFE.top - 1)
    expect(g.dialog.bottom).toBeLessThanOrEqual(g.viewport.h - IPHONE_SAFE.bottom + 1)
    expect(g.dialog.left).toBeGreaterThanOrEqual(IPHONE_SAFE.left - 1)
    expect(g.dialog.right).toBeLessThanOrEqual(g.viewport.w - IPHONE_SAFE.right + 1)

    // Close control is the original failure mode — must clear the status bar.
    expect(g.close.top).toBeGreaterThanOrEqual(IPHONE_SAFE.top - 1)
  })
})
