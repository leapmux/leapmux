import type { Locator, Page } from '@playwright/test'
import { expect, test } from './fixtures'
import { openAgentViaAPI } from './helpers/api'
import { COARSE_POINTER_METRICS, touchDown } from './helpers/touch'

/**
 * The mobile tab UI and the touch drag model.
 *
 * On phones the horizontal strip is replaced by a chip that opens a bottom
 * sheet listing every tab; reordering there (and in the desktop strip on a
 * touch-only tablet) starts from a dedicated drag grip — never from a
 * hold-then-drag on the row body, which now belongs to the scroller and the
 * 500ms context menu. These specs drive REAL touch input (see
 * ./helpers/touch.ts for why synthesized PointerEvents cannot).
 *
 * The `workspace` fixture seeds one initial agent tab, and where a new tab
 * lands in the order is the app's business — so these specs never assert
 * absolute counts or a starting order. They find their own titled tabs and
 * compare ORDER.
 */

/** Open two titled agent tabs through the API. */
async function openTwoAgentTabs(opts: { hubUrl: string, adminToken: string, workerId: string, workspaceId: string }) {
  await openAgentViaAPI(opts.hubUrl, opts.adminToken, opts.workerId, opts.workspaceId, undefined, { title: 'Alpha' })
  await openAgentViaAPI(opts.hubUrl, opts.adminToken, opts.workerId, opts.workspaceId, undefined, { title: 'Beta' })
}

function serverOf(leapmuxServer: { hubUrl: string, adminToken: string, workerId: string }) {
  return {
    hubUrl: leapmuxServer.hubUrl,
    adminToken: leapmuxServer.adminToken,
    workerId: leapmuxServer.workerId,
  }
}

/** Position of the row/tab whose text contains `needle`, throwing if absent. */
async function textIndex(rows: Locator, needle: string): Promise<number> {
  const texts = await rows.allTextContents()
  const index = texts.findIndex(text => text.includes(needle))
  expect(index, `a row containing "${needle}" (have: ${texts.join(' | ')})`).toBeGreaterThanOrEqual(0)
  return index
}

/**
 * Open the sheet and wait until its rows are actually on screen AND sitting
 * still. The panel is always mounted and slides via transform, so "visible"
 * is true even closed — in-viewport is the real oracle for a finger. The
 * stillness matters too: the long-press gesture cancels itself when the
 * pressed row moves under the finger, which is exactly what the 200ms
 * slide-in does to a press that lands too early.
 */
async function openSheet(page: Page): Promise<Locator> {
  const chip = page.getByTestId('tab-chip')
  await chip.tap()
  await expect(chip).toHaveAttribute('aria-expanded', 'true')
  const rows = page.getByTestId('tab-sheet-row')
  await expect(rows.nth(0)).toBeInViewport()
  await expect.poll(async () => {
    const first = await rows.nth(0).boundingBox()
    await page.waitForTimeout(60)
    const second = await rows.nth(0).boundingBox()
    return first !== null && second !== null && first.y === second.y
  }).toBe(true)
  return rows
}

/**
 * Touch-press `grip`, travel past the 10px activation distance, and drag
 * until the DRAGGED ROW's center sits on `target` — then lift.
 *
 * The finger does not stop at `target` itself: solid-dnd's collision
 * reference is the dragged element's transformed CENTER, which keeps the
 * grip-to-center offset it had at the press. A grip press therefore carries
 * the reference half a row past the finger, and a drop aimed at the finger's
 * position resolves a DIFFERENT droppable (observed: the tab-bar zone, a
 * same-tile no-op) whenever the drag runs right-to-left. Aiming the row's
 * center at the target makes the drop land on the target from any direction.
 *
 * `draggedRow` is also the oracle: the drag's start AND end are confirmed
 * against its `tabDragging` class, so a press that somehow never activated —
 * or a lift the drag pipeline never saw — fails HERE instead of as a
 * mysterious unchanged order later.
 */
async function touchDragGripOnto(
  grip: { x: number, y: number },
  target: { x: number, y: number },
  page: Page,
  draggedRow: Locator,
) {
  const rowBox = (await draggedRow.boundingBox())!
  // The collision reference sits this far right of the finger for the whole
  // gesture (grip press): aim the finger so the reference lands on target.
  const referenceOffsetX = rowBox.x + rowBox.width / 2 - grip.x
  const fingerTarget = { x: target.x - referenceOffsetX, y: target.y }

  const finger = await touchDown(page, grip.x, grip.y)
  try {
    // A move comfortably past the sensor's 10px activation distance, then a
    // short pause for the drag to start before the move to the target —
    // solid-dnd recomputes droppable collisions on every move, the same
    // shape the mouse-driven reorder specs use.
    await finger.moveTo(grip.x + 8, grip.y + 20)
    await expect(draggedRow).toHaveClass(/tabDragging/)
    const steps = 12
    for (let step = 1; step <= steps; step++) {
      await finger.moveTo(
        grip.x + 8 + ((fingerTarget.x - grip.x - 8) * step) / steps,
        grip.y + 20 + ((fingerTarget.y - grip.y - 20) * step) / steps,
      )
    }
    // Settle before lifting: CDP acknowledges the DISPATCH of a touchMove,
    // not its processing on the main thread, so a lift that races the final
    // dragOver resolves the drop prematurely. Two rAFs guarantee the queued
    // moves were consumed by a rendered frame; the pause after covers the
    // dragOver-to-store flush.
    await page.evaluate(() => new Promise<void>(resolve => requestAnimationFrame(() => requestAnimationFrame(() => resolve()))))
    await page.waitForTimeout(100)
  }
  finally {
    await finger.end()
  }
  // The lift ended the drag: a press whose pointerup the pipeline lost would
  // leave the row lifted and the reorder would never be attempted.
  await expect(draggedRow).not.toHaveClass(/tabDragging/)
}

test.describe('mobile tab sheet (phone)', () => {
  // Phone metrics: mobile layout (<768px) with a coarse primary pointer.
  test.use(COARSE_POINTER_METRICS)

  test('the chip replaces the strip and its sheet switches tabs', async ({ page, authenticatedWorkspace, leapmuxServer }) => {
    await openTwoAgentTabs({ ...serverOf(leapmuxServer), workspaceId: authenticatedWorkspace.workspaceId })

    // No strip on mobile — that is the point of the redesign.
    await expect(page.getByTestId('tab-list')).toHaveCount(0)
    const chip = page.getByTestId('tab-chip')
    await expect(chip).toBeVisible()
    await expect(chip).toHaveAttribute('aria-expanded', 'false')

    const rows = await openSheet(page)
    // The count badge reports exactly as many tabs as the sheet lists.
    await expect(page.getByTestId('tab-chip-count')).toHaveText(String(await rows.count()))
    // The list is full-bleed: a row spans the panel's whole width.
    const panelBox = (await page.getByTestId('tab-sheet').boundingBox())!
    const rowBox = (await rows.nth(0).boundingBox())!
    expect(rowBox.width).toBeGreaterThanOrEqual(panelBox.width - 1)
    // Exactly one row is the selected one (the attribute is on the row
    // itself, not a descendant).
    await expect(page.locator('[data-testid="tab-sheet-row"][aria-selected="true"]')).toHaveCount(1)

    await rows.filter({ hasText: 'Alpha' }).tap()
    await expect(chip).toHaveAttribute('aria-expanded', 'false')
    await expect(chip).toContainText('Alpha')
  })

  test('touch drag from a sheet grip reorders; a press on the row body does not', async ({ page, authenticatedWorkspace, leapmuxServer }) => {
    await openTwoAgentTabs({ ...serverOf(leapmuxServer), workspaceId: authenticatedWorkspace.workspaceId })
    const rows = await openSheet(page)
    const alphaRow = rows.filter({ hasText: 'Alpha' })
    await expect(alphaRow).toBeInViewport()

    // A press on the ROW BODY that travels is the scroller's, never a drag —
    // the activators there are mouse-only. Order must survive the swipe.
    const before = await rows.allTextContents()
    const bodyBox = (await alphaRow.boundingBox())!
    const swipe = await touchDown(page, bodyBox.x + bodyBox.width / 2, bodyBox.y + bodyBox.height / 2)
    await swipe.moveTo(bodyBox.x + bodyBox.width / 2, bodyBox.y + bodyBox.height / 2 + 60)
    await swipe.end()
    expect(await rows.allTextContents()).toEqual(before)

    // A press on the GRIP travels: that is the one touch surface that drags.
    // Dropping Alpha ONTO Beta inserts Alpha at Beta's slot in the reordered
    // list — which lands it AFTER Beta, from any starting order. Polled: the
    // reorder lands when the store confirms it, which can be after the lift.
    const grip = alphaRow.locator('[data-drag-handle]')
    const gripBox = (await grip.boundingBox())!
    const targetBox = (await rows.filter({ hasText: 'Beta' }).boundingBox())!
    await touchDragGripOnto(
      { x: gripBox.x + gripBox.width / 2, y: gripBox.y + gripBox.height / 2 },
      { x: targetBox.x + targetBox.width / 2, y: targetBox.y + targetBox.height / 2 },
      page,
      alphaRow,
    )
    await expect.poll(async () => await textIndex(rows, 'Beta') < await textIndex(rows, 'Alpha')).toBe(true)
  })

  test('a long press on a sheet row body opens its menu, not an unintended drag', async ({ page, authenticatedWorkspace, leapmuxServer }) => {
    await openTwoAgentTabs({ ...serverOf(leapmuxServer), workspaceId: authenticatedWorkspace.workspaceId })
    const rows = await openSheet(page)
    const alphaRow = rows.filter({ hasText: 'Alpha' })
    await expect(alphaRow).toBeInViewport()
    const before = await rows.allTextContents()

    // Hold past the 500ms long-press threshold on the row BODY. Before the
    // activator split, the sensor's own 250ms hold timer started a drag under
    // the finger with no movement, and the menu opened over a stuck overlay.
    const box = (await alphaRow.boundingBox())!
    const hold = await touchDown(page, box.x + box.width / 2, box.y + box.height / 2)
    await page.waitForTimeout(650)
    await hold.end()

    await expect(page.locator('[data-testid="tab-sheet-row-menu"]:popover-open')).toBeVisible()
    // The hold did not also reorder anything.
    expect(await rows.allTextContents()).toEqual(before)
  })

  test('closing a tab from the sheet removes its row and updates the chip count', async ({ page, authenticatedWorkspace, leapmuxServer }) => {
    await openTwoAgentTabs({ ...serverOf(leapmuxServer), workspaceId: authenticatedWorkspace.workspaceId })
    const rows = await openSheet(page)
    const before = await rows.count()

    await rows.filter({ hasText: 'Beta' }).locator('[data-testid="tab-close"]').tap()

    // The row is gone, the count badges agree, and the sheet stays up over
    // the remaining tabs — closing one tab is not a reason to hide the rest.
    await expect(rows.filter({ hasText: 'Beta' })).toHaveCount(0)
    await expect(rows).toHaveCount(before - 1)
    await expect(page.getByTestId('tab-chip-count')).toHaveText(String(before - 1))
    await expect(page.getByTestId('tab-chip')).toHaveAttribute('aria-expanded', 'true')
  })

  test('the overlay tap and the Escape key close the sheet', async ({ page, authenticatedWorkspace }) => {
    const chip = page.getByTestId('tab-chip')
    await openSheet(page)

    const panel = page.getByTestId('tab-sheet')
    await panel.press('Escape')
    await expect(chip).toHaveAttribute('aria-expanded', 'false')

    await chip.tap()
    await expect(page.getByTestId('tab-sheet-row').nth(0)).toBeInViewport()
    // The scrim starts below the tab bar (the bar stays undimmed and
    // tappable), so tap it near the BOTTOM — the sheet panel owns the top.
    const scrim = page.getByTestId('tab-sheet-overlay')
    const scrimBox = (await scrim.boundingBox())!
    const barBox = (await page.getByTestId('tab-bar').boundingBox())!
    expect(scrimBox.y).toBeGreaterThanOrEqual(barBox.y + barBox.height - 1)
    await scrim.tap({ position: { x: 10, y: scrimBox.height - 30 } })
    await expect(chip).toHaveAttribute('aria-expanded', 'false')
  })
})

test.describe('mobile drawers (phone)', () => {
  test.use(COARSE_POINTER_METRICS)

  test('the drawer starts below the tab bar, leaving the Files header actions reachable', async ({ page, authenticatedWorkspace }) => {
    await page.getByRole('button', { name: 'Toggle files' }).click()

    // The Files section header itself — the band the tab bar used to cover —
    // and the action buttons that live in it.
    const header = page.getByText('Files', { exact: true })
    const sort = page.getByTestId('files-sort-toggle')
    const refresh = page.getByTestId('files-refresh')
    await expect(header).toBeVisible()
    await expect(sort).toBeVisible()
    await expect(refresh).toBeVisible()

    // Reachable is not just visible: their top edge must sit at or below the
    // tab bar's bottom edge, and a tap must land.
    const barBox = (await page.getByTestId('tab-bar').boundingBox())!
    const headerBox = (await header.boundingBox())!
    const sortBox = (await sort.boundingBox())!
    expect(headerBox.y).toBeGreaterThanOrEqual(barBox.y + barBox.height - 1)
    expect(sortBox.y).toBeGreaterThanOrEqual(barBox.y + barBox.height - 1)

    // Full bleed: the drawer is an overlay panel spanning the viewport width
    // — no dimmed strip is left outside it to tap.
    const drawerWidth = await page.evaluate(() => {
      let el = document.querySelector('[data-testid="files-refresh"]') as HTMLElement | null
      while (el && getComputedStyle(el).position !== 'fixed' && getComputedStyle(el).position !== 'absolute')
        el = el.parentElement
      return el ? el.getBoundingClientRect().width : 0
    })
    expect(drawerWidth).toBeGreaterThanOrEqual(page.viewportSize()!.width - 1)

    await refresh.tap()
    // The sort toggle is a popover trigger: opening it proves the tap routed.
    await sort.tap()
    await expect(page.locator('[data-testid="files-sort-menu"]:popover-open')).toBeVisible()
    await page.keyboard.press('Escape')
    await expect(page.locator('[data-testid="files-sort-menu"]:popover-open')).toBeHidden()
  })
})

/**
 * The two halves of the soft-keyboard layout contract, each of which is one
 * string that a careless edit drops silently.
 *
 * The displacement they correct cannot be driven from here: Playwright raises
 * no soft keyboard, and the visual-viewport offset this compensates is WebKit
 * behaviour on a real iOS device. What IS testable is that the wiring holds --
 * the meta key Chromium reads, and that the body consumes the property the
 * hook publishes under the name it publishes it under.
 */
test.describe('soft-keyboard viewport contract (phone)', () => {
  test.use(COARSE_POINTER_METRICS)

  test('the viewport meta asks Chromium to resize the layout viewport for the keyboard', async ({ page, authenticatedWorkspace }) => {
    // Without this key Chromium defaults to `resizes-visual`: the layout
    // viewport keeps its full height, `100dvh` reports it, and the composer
    // sits behind the keyboard.
    await expect(page.locator('meta[name="viewport"]')).toHaveAttribute(
      'content',
      /interactive-widget=resizes-content/,
    )
  })

  // The bar has nothing reachable while the keyboard is up, and the body is
  // pinned to the visible region then, so a bar left in place re-seats itself
  // at the top of the screen on every focus and blur.
  test('the tab bar leaves the layout only while the keyboard takes screen space', async ({ page, authenticatedWorkspace }) => {
    const bar = page.getByTestId('tab-bar')
    await expect(bar).toBeVisible()

    const editor = page.locator('[data-testid="chat-editor"] .ProseMirror')
    await editor.click()
    await expect(editor).toBeFocused()

    // FOCUS ALONE MUST NOT HIDE IT. `MarkdownEditor` focuses the composer
    // whenever it is enabled, so a chat that merely opens holds focus with no
    // keyboard anywhere -- keying on focus alone hid the bar on open.
    await expect(bar).toBeVisible()

    // The keyboard, as Chromium models it under
    // `interactive-widget=resizes-content`: the viewport loses its height.
    const size = page.viewportSize()!
    await page.setViewportSize({ width: size.width, height: size.height - 300 })
    await expect(bar).toBeHidden()

    // ...and it returns, intact, when that height comes back.
    await page.setViewportSize(size)
    await expect(bar).toBeVisible()
    await expect(page.getByTestId('tab-chip')).toBeVisible()
  })

  // A completed send gives the screen back, but ONLY when a keyboard is taking
  // it. `handleSend` used to call `focusEditor()` on every path, which brought
  // the keyboard straight back after a tap on Send -- the tap does not move
  // focus on iOS, so the editor kept it.
  //
  // Both halves in one spec because they are one rule. A coarse pointer does
  // not imply an on-screen keyboard: this viewport reports one throughout, and
  // the first half is the phone-with-a-hardware-keyboard case, where dropping
  // the caret would reclaim nothing.
  test('a send releases focus only while the keyboard takes screen space', async ({ page, authenticatedWorkspace }) => {
    const editor = page.locator('[data-testid="chat-editor"] .ProseMirror')
    const size = page.viewportSize()!

    await editor.click()
    await page.keyboard.type('hardware')
    await page.getByTestId('send-button').tap()
    // The editor emptying is the app's acknowledgement that the send committed,
    // so each assertion below is about the SUCCESS path, not a refused send.
    await expect(editor).toHaveText('')
    await expect(editor).toBeFocused()

    // Now a keyboard really is in the way, as Chromium models it under
    // `interactive-widget=resizes-content`: the viewport loses its height.
    await page.keyboard.type('soft')
    await page.setViewportSize({ width: size.width, height: size.height - 300 })
    await page.getByTestId('send-button').tap()
    await expect(editor).toHaveText('')
    await expect(editor).not.toBeFocused()

    await page.setViewportSize(size)
  })

  test('the body takes its size from --vvh and its place from --vv-shift', async ({ page, authenticatedWorkspace }) => {
    const measured = await page.evaluate(() => {
      const read = () => {
        const style = getComputedStyle(document.body)
        return { height: style.height, transform: style.transform }
      }
      const idle = read()
      // What the hook publishes together while the keyboard is up.
      document.documentElement.style.setProperty('--vvh', '321px')
      document.documentElement.style.setProperty('--vv-shift', '17px')
      const keyboardUp = read()
      document.documentElement.style.removeProperty('--vvh')
      document.documentElement.style.removeProperty('--vv-shift')
      return { idle, keyboardUp, viewport: window.innerHeight }
    })

    // Identity, NOT `none`: the fallback keeps a transform on the body at all
    // times, which is what makes it the containing block SelectionQuotePopover
    // counter-translates against.
    expect(measured.idle.transform).toBe('matrix(1, 0, 0, 1, 0, 0)')
    // No `--vvh` → the `100dvh` fallback, which on this desktop-engine phone
    // emulation is the full viewport.
    expect(measured.idle.height).toBe(`${measured.viewport}px`)

    // Both published names reach the layout, the shift with its sign intact.
    expect(measured.keyboardUp.height).toBe('321px')
    expect(measured.keyboardUp.transform).toBe('matrix(1, 0, 0, 1, 0, 17)')
  })
})

test.describe('tablet touch (desktop layout)', () => {
  // iPad-like metrics: wide enough for the DESKTOP tiling layout (>=768px)
  // but mobile metrics throughout, so Blink reports a coarse primary pointer
  // and NO fine one — the one configuration where the strip's grips show.
  test.use({
    viewport: { width: 820, height: 1180 },
    deviceScaleFactor: 2,
    isMobile: true,
    hasTouch: true,
  })

  test('a mouse drag from the tab body reorders (the desktop regression guard)', async ({ page, authenticatedWorkspace, leapmuxServer }) => {
    await openTwoAgentTabs({ ...serverOf(leapmuxServer), workspaceId: authenticatedWorkspace.workspaceId })

    await expect(page.getByTestId('tab-list')).toBeVisible()
    const tabs = page.locator('[data-testid="tab"]')
    const alphaTab = tabs.filter({ hasText: 'Alpha' })
    const betaTab = tabs.filter({ hasText: 'Beta' })
    await expect(alphaTab).toBeVisible()
    await expect(betaTab).toBeVisible()
    expect(await textIndex(tabs, 'Alpha')).not.toBe(await textIndex(tabs, 'Beta'))

    // The mouse-only activators' path — the whole reason the row body keeps
    // guarded activators at all. Press the tab's MIDPOINT: a short title
    // makes the tab narrower than a fixed offset (an earlier `x + 60`
    // pressed the tab's NEIGHBOR), and the `tabDragging` class is the
    // activation oracle — a press that never claims the pointer fails
    // HERE, not as a mysteriously unchanged order later.
    const alphaBox = (await alphaTab.boundingBox())!
    const betaBox = (await betaTab.boundingBox())!
    await page.mouse.move(alphaBox.x + alphaBox.width / 2, alphaBox.y + alphaBox.height / 2)
    await page.mouse.down()
    await page.waitForTimeout(100)
    await page.mouse.move(betaBox.x + betaBox.width / 2, betaBox.y + betaBox.height / 2, { steps: 12 })
    await expect(alphaTab).toHaveClass(/tabDragging/)
    await page.waitForTimeout(100)
    await page.mouse.up()
    // Dropping Alpha ONTO Beta inserts it at Beta's slot — landing AFTER
    // Beta, whatever else the strip holds. Polled: the reorder lands when
    // the store confirms it, which can be after mouse.up returns.
    await expect.poll(async () => await textIndex(tabs, 'Beta') < await textIndex(tabs, 'Alpha')).toBe(true)
  })

  test('a touch drag from a grip reorders', async ({ page, authenticatedWorkspace, leapmuxServer }) => {
    await openTwoAgentTabs({ ...serverOf(leapmuxServer), workspaceId: authenticatedWorkspace.workspaceId })

    await expect(page.getByTestId('tab-list')).toBeVisible()
    const tabs = page.locator('[data-testid="tab"]')
    const alphaTab = tabs.filter({ hasText: 'Alpha' })
    const betaTab = tabs.filter({ hasText: 'Beta' })
    await expect(alphaTab).toBeVisible()
    await expect(betaTab).toBeVisible()
    expect(await textIndex(tabs, 'Alpha')).not.toBe(await textIndex(tabs, 'Beta'))

    const grip = alphaTab.locator('[data-drag-handle]')
    await expect(grip).toBeVisible()
    const gripBox = (await grip.boundingBox())!
    const betaBox = (await betaTab.boundingBox())!
    await touchDragGripOnto(
      { x: gripBox.x + gripBox.width / 2, y: gripBox.y + gripBox.height / 2 },
      { x: betaBox.x + betaBox.width / 2, y: betaBox.y + betaBox.height / 2 },
      page,
      alphaTab,
    )
    // Dropping Alpha ONTO Beta inserts it at Beta's slot — landing AFTER
    // Beta, from any starting order. Polled: the reorder lands when the
    // store confirms it, which can be after the lift.
    await expect.poll(async () => await textIndex(tabs, 'Beta') < await textIndex(tabs, 'Alpha')).toBe(true)
  })
})
