import { expect, test } from './fixtures'

/**
 * Runtime geometry for modal safe-area insets.
 *
 * Desktop Chromium always reports `env(safe-area-inset-*)` as 0, so these
 * specs set the `--leapmux-safe-area-inset-*` bridges from Dialog.css.ts and
 * assert the open panel and its close button clear those insets. No
 * screenshots — bounding boxes only.
 *
 * Two orientations:
 *   - Portrait: status bar + home indicator (top/bottom).
 *   - Landscape: notch on the right (the close button's side) so a missing
 *     right inset would put the close control under the notch. Width ≥ sm so
 *     this also covers the desktop-band dialog path.
 */

interface SafeInsets {
  top: number
  right: number
  bottom: number
  left: number
}

/** iPhone 14 Pro portrait — Dynamic Island + home indicator. */
const IPHONE_PORTRAIT: SafeInsets = {
  top: 47,
  right: 0,
  bottom: 34,
  left: 0,
}

/**
 * iPhone 14 Pro landscape with the notch on the RIGHT.
 * CSS width 844 ≥ breakpoints.sm, so this also exercises the desktop-band
 * dialog path (not the phone full-bleed rules) with non-zero horizontal insets.
 */
const IPHONE_LANDSCAPE_NOTCH_RIGHT: SafeInsets = {
  top: 0,
  right: 59,
  bottom: 21,
  left: 59,
}

async function applySimulatedSafeArea(
  page: import('@playwright/test').Page,
  insets: SafeInsets,
) {
  await page.addStyleTag({
    content: `
      :root {
        --leapmux-safe-area-inset-top: ${insets.top}px;
        --leapmux-safe-area-inset-right: ${insets.right}px;
        --leapmux-safe-area-inset-bottom: ${insets.bottom}px;
        --leapmux-safe-area-inset-left: ${insets.left}px;
      }
    `,
  })
}

/**
 * Open New Workspace.
 *
 * On the phone band the control lives behind the workspaces drawer — always
 * open the drawer first (same as 180-dialog-mobile-scroll). `isVisible()` is
 * not enough: the button can report visible while still translated off-screen.
 * On the desktop band (≥ sm), prefer the sidebar control and fall back to the
 * empty-state create button when the sidebar is collapsed to a rail.
 */
async function openNewWorkspaceDialog(page: import('@playwright/test').Page) {
  const sidebarBtn = page.locator('[data-testid="sidebar-new-workspace"]')
  const createBtn = page.locator('[data-testid="create-workspace-button"]')
  const toggle = page.getByRole('button', { name: 'Toggle workspaces' })
  const phoneBand = (page.viewportSize()?.width ?? 0) < 640

  if (phoneBand) {
    await toggle.click()
    await sidebarBtn.click()
  }
  else if (await sidebarBtn.isVisible().catch(() => false)) {
    await sidebarBtn.click()
  }
  else if (await toggle.isVisible().catch(() => false)) {
    await toggle.click()
    await sidebarBtn.click()
  }
  else {
    await createBtn.click()
  }

  await expect(page.getByRole('heading', { name: /New workspace/i, level: 2 })).toBeVisible()
  await expect(page.locator('dialog[open]').getByRole('button', { name: 'Close' })).toBeVisible()
}

async function readDialogGeometry(page: import('@playwright/test').Page) {
  return page.evaluate(() => {
    const dlg = document.querySelector('dialog[open]')
    const close = dlg?.querySelector('button[aria-label="Close"]')
    if (!(dlg instanceof HTMLElement) || !(close instanceof HTMLElement))
      return null
    const d = dlg.getBoundingClientRect()
    const c = close.getBoundingClientRect()
    const cs = getComputedStyle(dlg)
    return {
      dialog: { top: d.top, right: d.right, bottom: d.bottom, left: d.left },
      close: { top: c.top, right: c.right, bottom: c.bottom, left: c.left },
      computed: {
        top: cs.top,
        right: cs.right,
        bottom: cs.bottom,
        left: cs.left,
      },
      viewport: { w: window.innerWidth, h: window.innerHeight },
    }
  })
}

function assertClearsSafeArea(
  g: NonNullable<Awaited<ReturnType<typeof readDialogGeometry>>>,
  insets: SafeInsets,
) {
  // Computed style must resolve the test bridges (not stay at UA inset:0).
  expect(g.computed.top).toBe(`${insets.top}px`)
  expect(g.computed.right).toBe(`${insets.right}px`)
  expect(g.computed.bottom).toBe(`${insets.bottom}px`)
  expect(g.computed.left).toBe(`${insets.left}px`)

  // Panel sits inside the safe rectangle.
  expect(g.dialog.top).toBeGreaterThanOrEqual(insets.top - 1)
  expect(g.dialog.bottom).toBeLessThanOrEqual(g.viewport.h - insets.bottom + 1)
  expect(g.dialog.left).toBeGreaterThanOrEqual(insets.left - 1)
  expect(g.dialog.right).toBeLessThanOrEqual(g.viewport.w - insets.right + 1)

  // Close control must clear every unsafe edge — top (status bar / Island)
  // and right (landscape notch), which is where the button lives.
  expect(g.close.top).toBeGreaterThanOrEqual(insets.top - 1)
  expect(g.close.right).toBeLessThanOrEqual(g.viewport.w - insets.right + 1)
  expect(g.close.left).toBeGreaterThanOrEqual(insets.left - 1)
  expect(g.close.bottom).toBeLessThanOrEqual(g.viewport.h - insets.bottom + 1)
}

test.describe('dialog safe-area geometry (portrait)', () => {
  test.use({ viewport: { width: 390, height: 844 } })

  test('panel and close button clear status bar and home indicator', async ({
    page,
    authenticatedWorkspace,
  }) => {
    void authenticatedWorkspace
    await applySimulatedSafeArea(page, IPHONE_PORTRAIT)
    await openNewWorkspaceDialog(page)

    const geometry = await readDialogGeometry(page)
    expect(geometry, 'open dialog + close button').not.toBeNull()
    assertClearsSafeArea(geometry!, IPHONE_PORTRAIT)
  })
})

test.describe('dialog safe-area geometry (landscape notch)', () => {
  // iPhone 14 Pro landscape CSS pixels; width ≥ sm so the desktop-band dialog
  // rules apply while horizontal safe-area insets are still non-zero.
  test.use({ viewport: { width: 844, height: 390 } })

  test('panel and close button clear a right-side notch', async ({
    page,
    authenticatedWorkspace,
  }) => {
    void authenticatedWorkspace
    await applySimulatedSafeArea(page, IPHONE_LANDSCAPE_NOTCH_RIGHT)
    await openNewWorkspaceDialog(page)

    const geometry = await readDialogGeometry(page)
    expect(geometry, 'open dialog + close button').not.toBeNull()
    assertClearsSafeArea(geometry!, IPHONE_LANDSCAPE_NOTCH_RIGHT)

    // Explicit notch-side check: the close button sits on the right; a missing
    // right inset would place it under the 59px notch band.
    expect(geometry!.close.right).toBeLessThanOrEqual(
      geometry!.viewport.w - IPHONE_LANDSCAPE_NOTCH_RIGHT.right + 1,
    )
  })
})
