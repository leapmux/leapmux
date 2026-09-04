import { expect, test } from './fixtures'
import { openSettingsAt } from './helpers/ui'

/**
 * Runtime geometry for modal safe-area insets.
 *
 * Desktop Chromium reports `env(safe-area-inset-*)` as 0 by default. These
 * specs inject real device insets via the experimental CDP command
 * `Emulation.setSafeAreaInsetsOverride`, which overrides Blink's CSS
 * environment variables (the same `env()` path production uses) — not a
 * custom-property shim. Chromium-only; this suite's Playwright project is
 * chromium. No screenshots — bounding boxes only.
 *
 * Coverage:
 *   - Portrait (standard): status bar + home indicator (top/bottom).
 *   - Portrait phone-band (huge / Preferences): same vertical insets on the
 *     phone full-bleed huge path (`width: auto`, SAFE_MAX_HEIGHT).
 *   - Landscape notch (standard): notch on the right (close button side);
 *     width ≥ sm so the desktop-band dialog path applies.
 *   - Landscape notch (huge / Preferences): same notch path for the wider
 *     Preferences panel, which restates SAFE_MAX_WIDTH_HUGE on :modal.
 *   - Zero-inset desktop (standard): design ceiling still caps width at 900px
 *     when safe-area insets are 0 (SAFE_MAX_WIDTH_STANDARD must not become
 *     a bare 100dvw).
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

const ZERO_INSETS: SafeInsets = {
  top: 0,
  right: 0,
  bottom: 0,
  left: 0,
}

/**
 * Inject real `env(safe-area-inset-*)` values through CDP.
 *
 * Pass every edge, including 0: an omitted key makes that variable undefined
 * even if a previous override set it
 * (https://chromedevtools.github.io/devtools-protocol/tot/Emulation/#method-setSafeAreaInsetsOverride).
 */
async function applySimulatedSafeArea(
  page: import('@playwright/test').Page,
  insets: SafeInsets,
) {
  const session = await page.context().newCDPSession(page)
  await session.send('Emulation.setSafeAreaInsetsOverride', {
    insets: {
      top: insets.top,
      right: insets.right,
      bottom: insets.bottom,
      left: insets.left,
    },
  })
}

/**
 * Open New Workspace.
 *
 * A LOCAL copy, not `helpers/worktree`'s. It differs on three things this spec
 * is about, and folding it in would leak all of them into 070-073:
 *   - a THIRD branch for the desktop drawer, which the shared helper has no
 *     concept of;
 *   - raw `isVisible().catch()` rather than the helper's `expectAnyVisible`,
 *     because on this band a control can report visible while still translated
 *     off-screen;
 *   - a looser heading match plus the Close-button assertion, which is the
 *     geometry this file measures.
 *
 * On the phone band the control lives behind the workspaces drawer — always
 * open the drawer first (same as 180-dialog-mobile-scroll).
 *
 * "New workspace..." is an ITEM of the section header menu now, so every
 * branch opens the menu first. A closed `popover="auto"` is `display: none`,
 * which is why the availability probes read the TRIGGER.
 */
async function openNewWorkspaceDialog(page: import('@playwright/test').Page) {
  const sectionMenu = page.locator('[data-testid="sidebar-section-menu-workspaces_in_progress"]')
  const newWorkspaceItem = page.locator('[data-testid="sidebar-new-workspace"]:visible')
  const createBtn = page.locator('[data-testid="create-workspace-button"]')
  const toggle = page.getByRole('button', { name: 'Toggle workspaces' })
  const phoneBand = (page.viewportSize()?.width ?? 0) < 640

  if (phoneBand) {
    await toggle.click()
    await sectionMenu.click()
    await newWorkspaceItem.click()
  }
  else if (await sectionMenu.isVisible().catch(() => false)) {
    await sectionMenu.click()
    await newWorkspaceItem.click()
  }
  else if (await toggle.isVisible().catch(() => false)) {
    await toggle.click()
    await sectionMenu.click()
    await newWorkspaceItem.click()
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
      dialog: {
        top: d.top,
        right: d.right,
        bottom: d.bottom,
        left: d.left,
        width: d.width,
        height: d.height,
      },
      close: { top: c.top, right: c.right, bottom: c.bottom, left: c.left },
      computed: {
        top: cs.top,
        right: cs.right,
        bottom: cs.bottom,
        left: cs.left,
        maxWidth: cs.maxWidth,
      },
      viewport: { w: window.innerWidth, h: window.innerHeight },
    }
  })
}

function assertClearsSafeArea(
  g: NonNullable<Awaited<ReturnType<typeof readDialogGeometry>>>,
  insets: SafeInsets,
) {
  // Computed style must resolve CDP-injected env() (not stay at UA inset:0).
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
  // Width < sm so phone-band dialog rules apply (huge uses width:auto +
  // SAFE_MAX_HEIGHT, not the desktop SAFE_MAX_WIDTH_HUGE path).
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

  test('Preferences (huge) panel and close button clear status bar and home indicator', async ({
    page,
    authenticatedWorkspace,
  }) => {
    void authenticatedWorkspace
    await applySimulatedSafeArea(page, IPHONE_PORTRAIT)
    await openSettingsAt(page)

    const geometry = await readDialogGeometry(page)
    expect(geometry, 'open Preferences dialog + close button').not.toBeNull()
    assertClearsSafeArea(geometry!, IPHONE_PORTRAIT)

    // Phone-band huge forces SAFE_MAX_HEIGHT; the panel must fill the safe
    // rectangle vertically without covering the Island or home indicator.
    expect(geometry!.dialog.height).toBeGreaterThanOrEqual(
      geometry!.viewport.h
      - IPHONE_PORTRAIT.top
      - IPHONE_PORTRAIT.bottom
      - 2,
    )
    expect(geometry!.dialog.top).toBeGreaterThanOrEqual(IPHONE_PORTRAIT.top - 1)
    expect(geometry!.dialog.bottom).toBeLessThanOrEqual(
      geometry!.viewport.h - IPHONE_PORTRAIT.bottom + 1,
    )
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

  test('Preferences (huge) panel and close button clear a right-side notch', async ({
    page,
    authenticatedWorkspace,
  }) => {
    void authenticatedWorkspace
    await applySimulatedSafeArea(page, IPHONE_LANDSCAPE_NOTCH_RIGHT)
    await openSettingsAt(page)

    const geometry = await readDialogGeometry(page)
    expect(geometry, 'open Preferences dialog + close button').not.toBeNull()
    assertClearsSafeArea(geometry!, IPHONE_LANDSCAPE_NOTCH_RIGHT)

    // Huge restates SAFE_MAX_WIDTH_HUGE on :modal; with 59px side insets the
    // panel must still fit the safe rectangle (not bleed under the notch).
    expect(geometry!.dialog.width).toBeLessThanOrEqual(
      geometry!.viewport.w
      - IPHONE_LANDSCAPE_NOTCH_RIGHT.left
      - IPHONE_LANDSCAPE_NOTCH_RIGHT.right
      + 1,
    )
    expect(geometry!.close.right).toBeLessThanOrEqual(
      geometry!.viewport.w - IPHONE_LANDSCAPE_NOTCH_RIGHT.right + 1,
    )
  })
})

test.describe('dialog design max-width (zero safe-area insets)', () => {
  // Wide desktop: without composition, SAFE_MAX_WIDTH alone is ~100dvw and
  // would let a standard modal grow past the 900px design ceiling.
  test.use({ viewport: { width: 1400, height: 900 } })

  test('standard dialog width stays at or below 900px', async ({
    page,
    authenticatedWorkspace,
  }) => {
    void authenticatedWorkspace
    await applySimulatedSafeArea(page, ZERO_INSETS)
    await openNewWorkspaceDialog(page)

    const geometry = await readDialogGeometry(page)
    expect(geometry, 'open dialog + close button').not.toBeNull()
    assertClearsSafeArea(geometry!, ZERO_INSETS)

    expect(geometry!.dialog.width).toBeLessThanOrEqual(900 + 1)
    // getComputedStyle resolves min(900px, calc(100dvw - 0 - 0)) to the used
    // value. On a 1400px viewport that must be 900px — not ~1400px from a bare
    // SAFE_MAX_WIDTH that replaced the design ceiling.
    expect(geometry!.computed.maxWidth).toBe('900px')
  })
})
