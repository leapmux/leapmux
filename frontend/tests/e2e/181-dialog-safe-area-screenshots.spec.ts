import { expect, test } from './fixtures'

/**
 * Visual proof that the modal dialog panel respects safe-area insets.
 *
 * Desktop Chromium always reports `env(safe-area-inset-*)` as 0, so this
 * spec sets the `--leapmux-safe-area-inset-*` bridges from Dialog.css.ts to
 * iPhone 14 Pro-like values (47px top / 34px bottom).
 *
 * Red "unsafe" bands are drawn with the Popover API so they join the top
 * layer as their own entry (above the dialog) and stay viewport-fixed —
 * a `position: fixed` child of the dialog would be trapped by the dialog's
 * transform containing block and paint over the header instead of the
 * status-bar strip.
 *
 * Screenshots land under test-results/ for the agent to surface to the user.
 */

const IPHONE_SAFE = {
  top: 47,
  right: 0,
  bottom: 34,
  left: 0,
} as const

async function paintUnsafeOverlay(page: import('@playwright/test').Page) {
  await page.evaluate((safe) => {
    document.getElementById('leapmux-unsafe-overlay')?.remove()

    const root = document.createElement('div')
    root.id = 'leapmux-unsafe-overlay'
    // Popover top-layer entry, stacked above the already-open dialog.
    root.setAttribute('popover', 'manual')
    Object.assign(root.style, {
      position: 'fixed',
      inset: '0',
      width: '100dvw',
      height: '100dvh',
      margin: '0',
      padding: '0',
      border: 'none',
      background: 'transparent',
      overflow: 'hidden',
      pointerEvents: 'none',
    })

    const band = (edge: 'top' | 'bottom', height: number, text: string, alpha: number) => {
      const el = document.createElement('div')
      el.textContent = text
      Object.assign(el.style, {
        position: 'absolute',
        left: '0',
        right: '0',
        [edge]: '0',
        height: `${height}px`,
        background: `rgba(220, 38, 38, ${alpha})`,
        display: 'flex',
        alignItems: 'center',
        justifyContent: 'center',
        color: '#fff',
        font: '600 11px/1.2 ui-sans-serif, system-ui, sans-serif',
        textShadow: '0 1px 2px rgba(0,0,0,.6)',
      })
      return el
    }

    root.append(
      band('top', safe.top, `unsafe / status bar (${safe.top}px)`, 0.55),
      band('bottom', safe.bottom, `unsafe / home indicator (${safe.bottom}px)`, 0.45),
    )
    document.documentElement.append(root)
    root.showPopover()
  }, IPHONE_SAFE)
}

async function openNewWorkspaceDialog(page: import('@playwright/test').Page) {
  await page.getByRole('button', { name: 'Toggle workspaces' }).click()
  await page.locator('[data-testid="sidebar-new-workspace"]').click()
  await expect(page.getByRole('heading', { name: 'New workspace', level: 2 })).toBeVisible()
  await expect(page.locator('dialog[open]').getByRole('button', { name: 'Close' })).toBeVisible()
}

test.describe('dialog safe-area screenshots', () => {
  test.use({ viewport: { width: 390, height: 844 } })

  test('close button clears the unsafe top band when safe-area insets apply', async ({
    page,
    authenticatedWorkspace,
  }) => {
    void authenticatedWorkspace

    // --- Broken (pre-fix): force the panel under the status bar ----------
    await page.addStyleTag({
      content: `
        dialog:modal {
          top: 0 !important;
          right: 0 !important;
          bottom: 0 !important;
          left: 0 !important;
          max-height: 100dvh !important;
          height: auto !important;
        }
      `,
    })
    await openNewWorkspaceDialog(page)
    await paintUnsafeOverlay(page)

    const dialog = page.locator('dialog[open]')
    const closeBtn = dialog.getByRole('button', { name: 'Close' })
    const broken = await closeBtn.evaluate((el, safeTop) => {
      const r = el.getBoundingClientRect()
      return { top: r.top, bottom: r.bottom, overlapsUnsafe: r.top < safeTop }
    }, IPHONE_SAFE.top)
    expect(broken.overlapsUnsafe, 'pre-fix close button must sit under the status bar').toBe(true)

    await page.screenshot({
      path: 'test-results/dialog-safe-area-before.png',
      fullPage: false,
    })

    // --- Fixed: drop the force, set iPhone-like safe-area bridges ----------
    await page.evaluate(() => {
      for (const s of Array.from(document.querySelectorAll('style'))) {
        if (s.textContent?.includes('dialog:modal') && s.textContent.includes('!important'))
          s.remove()
      }
      document.getElementById('leapmux-unsafe-overlay')?.remove()
    })
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
    await page.waitForTimeout(50)
    await paintUnsafeOverlay(page)

    const dialogBox = await dialog.boundingBox()
    expect(dialogBox, 'dialog geometry').not.toBeNull()
    expect(dialogBox!.y, 'dialog top clears status bar').toBeGreaterThanOrEqual(IPHONE_SAFE.top - 1)

    const fixed = await closeBtn.evaluate((el, safeTop) => {
      const r = el.getBoundingClientRect()
      return { top: r.top, bottom: r.bottom, clearsUnsafe: r.top >= safeTop }
    }, IPHONE_SAFE.top)
    expect(fixed.clearsUnsafe, 'fixed close button must clear the status bar').toBe(true)

    await page.screenshot({
      path: 'test-results/dialog-safe-area-after.png',
      fullPage: false,
    })
  })
})
