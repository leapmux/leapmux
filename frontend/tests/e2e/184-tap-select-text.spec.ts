import type { Locator, Page } from '@playwright/test'
import type { TouchPoint } from './helpers/touch'
import { expect, test } from './fixtures'
import { COARSE_POINTER_METRICS, touchHold, touchSwipe, touchTap } from './helpers/touch'
import { sendMessage, userBubbles, waitForAgentIdle } from './helpers/ui'

/**
 * Selecting part of a message with a finger: double tap for the word, triple tap
 * for the paragraph.
 *
 * The half a browser cannot be asked about any other way. `Intl.Segmenter`'s word
 * rules, the paragraph boundaries and the recognizer's own state machine are
 * covered in ~/src/lib/textSelection.test.ts and ~/src/lib/tapSelect.test.ts,
 * against real text and real DOM. What only a real engine answers is whether the
 * SELECTION is real once it is set -- a coarse pointer puts `user-select: none`
 * on every chat row, and Blink serializes a range over that as the empty string.
 * The first spec below asserts the suppression is in force and THEN asserts the
 * selected text, so a lift that stopped working fails here rather than passing as
 * a range nobody can copy.
 *
 * REAL touch input throughout (see ./helpers/touch.ts): the gesture takes a
 * primary TOUCH pointer only, and a synthesized `PointerEvent` would not exercise
 * the browser's own arbitration between a tap, a pan and a text selection.
 *
 * Chromium only, and not because the behaviour is: Playwright reaches real touch
 * through a Chromium-only protocol -- its WebKit backend dispatches a tap with no
 * move phase, and its Firefox backend does nothing at all.
 */

const MESSAGE = 'the quick brown fox jumps over the lazy dog'

/**
 * How long a long press holds, comfortably past `motion.longPress`.
 *
 * Stated here rather than imported: this suite drives the app through a browser,
 * and a threshold read from the app's own source would move with it and stop
 * being an independent statement of what a user has to do.
 */
const HOLD_MS = 900

/** The message row, which is the element that carries the coarse-pointer suppression. */
function messageRow(page: Page): Locator {
  return page.locator('[data-ctx-menu="selectable"]:visible').first()
}

/**
 * The centre of `word` inside `host`, in viewport pixels.
 *
 * Measured from a range around the word rather than guessed from the row's own
 * box: the point has to land on that word for the assertion to mean anything, and
 * where it falls depends on the font, the wrap and the device scale.
 */
async function wordCentre(host: Locator, word: string): Promise<TouchPoint> {
  return host.evaluate((el, needle) => {
    const walker = document.createTreeWalker(el, NodeFilter.SHOW_TEXT)
    for (let node = walker.nextNode(); node; node = walker.nextNode()) {
      const text = node as Text
      const at = text.data.indexOf(needle)
      if (at < 0)
        continue
      const range = document.createRange()
      range.setStart(text, at)
      range.setEnd(text, at + needle.length)
      const rect = range.getBoundingClientRect()
      return { x: rect.left + rect.width / 2, y: rect.top + rect.height / 2 }
    }
    throw new Error(`no text node under the row contains ${JSON.stringify(needle)}`)
  }, word)
}

/**
 * The point to tap for `word`, with the row brought into view and the landing
 * checked before anything touches the screen.
 *
 * Both halves earned their place. The agent's reply arrives above the fold and
 * pushes the message up, so the row has to be scrolled back before its geometry
 * means anything. And a point that misses is SILENT otherwise: a tap outside the
 * transcript lands on the shell -- the drawer toggle sits directly above it --
 * so a mis-aimed run reported "nothing was selected" while it was busy opening
 * drawers and closing the tab. Naming what is under the point separates "the
 * gesture did not fire" from "the finger was never on the text".
 */
async function aimAt(page: Page, row: Locator, word: string): Promise<TouchPoint> {
  await row.scrollIntoViewIfNeeded()
  const point = await wordCentre(row, word)
  const landing = await page.evaluate(([x, y]) => {
    const el = document.elementFromPoint(x, y)
    if (!el)
      return 'nothing'
    return el.closest('[data-testid]')?.getAttribute('data-testid') ?? el.tagName
  }, [point.x, point.y])
  const where = `what a tap for "${word}" lands on at (${Math.round(point.x)}, ${Math.round(point.y)})`
  expect(landing, where).toBe('message-content')
  return point
}

/**
 * The selected text, read from the RANGE rather than from the selection.
 *
 * `Selection.toString()` is layout-aware and reads empty over `user-select:
 * none`, which the app puts back the moment a finger lands away from the
 * highlight -- so it reports "nothing is selected" while the range is still
 * live. Every guard in the app asks the range (see `selectionInside` in
 * ~/src/lib/textSelection.ts), and so must this, or a spec passes on a
 * selection that never went away.
 */
function selectedText(page: Page): Promise<string> {
  return page.evaluate(() => {
    const selection = window.getSelection()
    if (!selection || selection.rangeCount === 0 || selection.isCollapsed)
      return ''
    return selection.getRangeAt(0).toString()
  })
}

test.describe('tap to select text (phone)', () => {
  // Phone metrics: a coarse primary pointer, which is what puts `user-select:
  // none` on the message rows in the first place.
  test.use(COARSE_POINTER_METRICS)

  /**
   * Send the message and let the transcript settle before anything measures it.
   *
   * Both waits are load-bearing, and neither is a general "wait for quiet". A
   * row that has not committed yet renders an extra pending strip under itself,
   * and the reply that follows re-anchors the list -- so a word's centre
   * measured before then names a point the text has already left, and the
   * second tap of a double tap lands somewhere else. That is what made the
   * first spec in this file fail while the same double tap passed in the rest.
   */
  test.beforeEach(async ({ page, authenticatedWorkspace }) => {
    void authenticatedWorkspace // fixture trigger
    await sendMessage(page, MESSAGE)
    await expect(userBubbles(page)).toHaveCount(1)
    await waitForAgentIdle(page)
    await expect(page.getByTestId('message-pending')).toHaveCount(0)
  })

  test('a double tap selects the word and offers to copy or quote it', async ({ page }) => {
    const row = messageRow(page)
    // The premise. Without this the next assertion would pass on a row that was
    // selectable all along, and prove nothing about the lift.
    await expect.poll(() => row.evaluate(el => getComputedStyle(el).userSelect)).toBe('none')

    await touchTap(page, await aimAt(page, row, 'brown'), { taps: 2 })

    await expect.poll(() => selectedText(page)).toBe('brown')
    await expect(page.getByTestId('quote-selection-popover')).toBeVisible()
  })

  test('a triple tap widens the selection to the whole paragraph', async ({ page }) => {
    const row = messageRow(page)
    await touchTap(page, await aimAt(page, row, 'brown'), { taps: 3 })

    await expect.poll(() => selectedText(page)).toBe(MESSAGE)
  })

  test('a single tap selects nothing', async ({ page }) => {
    const row = messageRow(page)
    await touchTap(page, await aimAt(page, row, 'brown'))

    // A fixed wait is the only way to assert that something does NOT happen: the
    // gesture acts on the release, so there is no later state to poll towards.
    await page.waitForTimeout(500)
    expect(await selectedText(page)).toBe('')
    await expect(page.getByTestId('quote-selection-popover')).toBeHidden()
  })

  // The transcript is a scroller, and a finger that pans it must keep panning it.
  // A drag counted as a tap would select a word every time a reader scrolled.
  test('a finger that drags the transcript selects nothing', async ({ page }) => {
    const row = messageRow(page)
    const point = await aimAt(page, row, 'brown')

    await touchTap(page, point)
    await touchSwipe(page, { from: point, to: { x: point.x, y: point.y - 120 } })

    await page.waitForTimeout(500)
    expect(await selectedText(page)).toBe('')
  })

  // The finger goes back to the same word after the selection is up. Without the
  // guard against the mouse events a tap synthesizes, the `mousedown` that
  // follows the second tap collapses the selection a frame after it is made.
  test('the selection survives the mouse events the tap synthesizes', async ({ page }) => {
    const row = messageRow(page)
    await touchTap(page, await aimAt(page, row, 'jumps'), { taps: 2 })

    await expect.poll(() => selectedText(page)).toBe('jumps')
    await page.waitForTimeout(500)
    expect(await selectedText(page)).toBe('jumps')
  })

  /**
   * A live selection owns the finger.
   *
   * Changing the range means dragging the platform's own handles, and those sit
   * at the edges of the highlight -- so the press that reaches for one lands on
   * the row, where a hold would otherwise put the message menu over the text the
   * reader is still choosing.
   *
   * The control comes second and is what makes the assertion mean anything: once
   * the selection is gone, the SAME hold opens the menu. It is not `Escape` that
   * clears the menu here, because the menu a hold opens is a `popover="manual"`
   * and the HTML light-dismiss pass does not act on those.
   */
  test('a long press over a live selection opens no message menu', async ({ page }) => {
    const row = messageRow(page)
    const menu = page.locator('[data-testid="message-context-menu"]:popover-open')
    const point = await aimAt(page, row, 'brown')

    await touchTap(page, point, { taps: 2 })
    await expect.poll(() => selectedText(page)).toBe('brown')

    await touchHold(page, point, HOLD_MS)
    await expect(menu).toBeHidden()
    // ...and the hold left the selection alone, so it is still there to adjust.
    expect(await selectedText(page)).toBe('brown')

    const away = await aimAt(page, row, 'dog')
    await touchTap(page, away)
    await expect.poll(() => selectedText(page)).toBe('')

    await touchHold(page, away, HOLD_MS)
    await expect(menu).toBeVisible()
  })

  /**
   * The drawer swipe stands aside for the same reason, and it is the one that
   * would hurt most: widening a selection is a HORIZONTAL drag along the line,
   * which is the very shape the swipe recognizer reads. Without the rule the
   * reach for a handle opens a drawer over the text instead.
   */
  test('a swipe over a live selection opens no drawer', async ({ page }) => {
    const row = messageRow(page)
    const drawer = page.getByTestId('mobile-drawer-left')
    const width = page.viewportSize()!.width
    const point = await aimAt(page, row, 'brown')
    const band = { from: { x: width * 0.2, y: point.y }, to: { x: width * 0.7, y: point.y } }
    // The precondition, so a drawer that was already out fails as that rather
    // than as "the guard did not hold".
    await expect(drawer).not.toBeInViewport()

    await touchTap(page, point, { taps: 2 })
    await expect.poll(() => selectedText(page)).toBe('brown')

    await touchSwipe(page, band)
    await expect(drawer).not.toBeInViewport()

    // The control: the same travel across the same band opens the drawer once
    // nothing is selected.
    await touchTap(page, await aimAt(page, row, 'dog'))
    await expect.poll(() => selectedText(page)).toBe('')

    await touchSwipe(page, band)
    await expect(drawer).toBeInViewport()
  })

  // The trade the suppression exists to make: the long press stays the message
  // menu's, so the row must have its `user-select: none` back before the next
  // press can be held long enough to raise the platform's own callout.
  //
  // The selection is cleared with a tap rather than with the popover's own Copy
  // button, because that button is not reliably reachable on a phone: the
  // popover sits below the mobile tab bar in the stacking order, so a selection
  // low in the transcript puts Copy under the bar.
  test('the row stops being selectable once the selection is gone', async ({ page }) => {
    const row = messageRow(page)
    await touchTap(page, await aimAt(page, row, 'brown'), { taps: 2 })
    await expect.poll(() => selectedText(page)).toBe('brown')
    await expect.poll(() => row.evaluate(el => getComputedStyle(el).userSelect)).toBe('text')

    await touchTap(page, await aimAt(page, row, 'dog'))

    await expect.poll(() => selectedText(page)).toBe('')
    await expect.poll(() => row.evaluate(el => getComputedStyle(el).userSelect)).toBe('none')
  })
})
