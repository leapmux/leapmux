import type { Locator, Page } from '@playwright/test'
import { expect, test } from './fixtures'
import { COARSE_POINTER_METRICS, touchSwipe } from './helpers/touch'

/**
 * The mobile drawers, driven by a finger instead of the tab bar's toggles.
 *
 * One rule from either side: a swipe moves the screen the way the finger goes.
 * Travelling right pulls the workspaces drawer in from the left edge, and
 * pushes an open files drawer back out to the right. Travelling left mirrors
 * that. See `nextOverlayForSwipe` in
 * ~/src/components/shell/MobileLayout.tsx for the whole table.
 *
 * Both panels stay mounted and slide by transform, so "visible" is true even
 * closed -- these specs assert `toBeInViewport`, as the tab-sheet specs do.
 *
 * REAL touch input throughout (see ./helpers/touch.ts for why a synthesized
 * PointerEvent proves nothing here): the recognizer takes a primary TOUCH
 * pointer only, and the guards it declines on are the browser's own arbitration
 * between a swipe, a pan and a text selection.
 */

interface Drawers {
  left: Locator
  right: Locator
}

function drawers(page: Page): Drawers {
  return {
    left: page.getByTestId('mobile-drawer-left'),
    right: page.getByTestId('mobile-drawer-right'),
  }
}

/**
 * The mobile shell is up and neither drawer is on screen.
 *
 * Both halves matter, and the first is the subtle one. The drawer test ids
 * exist ONLY in the mobile layout, so resolving them is what waits for that
 * layout to mount -- and the shell picks its layout from a media query, so the
 * desktop one can paint first and be swapped out. `tab-bar` and `composer-editor`
 * are in BOTH layouts, so a spec that measures against them and then swipes
 * reads as ready while nothing has armed the gesture yet, and the swipe is lost.
 */
async function expectMobileShellIdle(page: Page) {
  const { left, right } = drawers(page)
  // ATTACHED first, then out of viewport. The two assertions below hold on the
  // DESKTOP layout too -- it has no drawers at all, so both are trivially out
  // of view -- which let this helper report an idle mobile shell while the
  // desktop one was still painted and nothing had armed the gesture. The swipe
  // was then lost, and the drawer never opened. The drawers exist only in the
  // mobile shell, so waiting for one to attach is what actually ends that state.
  await page.getByTestId('mobile-swipe-band').waitFor({ state: 'attached' })
  await left.waitFor({ state: 'attached' })
  await right.waitFor({ state: 'attached' })
  await expect(left).not.toBeInViewport()
  await expect(right).not.toBeInViewport()
  // The SHEET counts as idle too. `nextOverlayForSwipe` returns the sheet
  // unchanged for either direction -- a finger under it is not reaching for a
  // drawer -- so a swipe taken while it is up does nothing at all. The two
  // assertions above cannot see that: the sheet covers the region without
  // bringing either drawer into view, so an open sheet read as an idle shell
  // and the swipe vanished.
  // The scrim is mounted unconditionally and flips opacity + pointer-events, so
  // neither its presence nor `toBeVisible` can tell the two states apart.
  // `pointer-events` is what actually differs.
  await expect(page.getByTestId('tab-sheet-overlay')).toHaveCSS('pointer-events', 'none')
  await expectMainThreadIdle(page)
}

/**
 * Wait until the main thread can answer a touch move.
 *
 * This is a precondition of the GESTURE, not a settle for the assertions. The
 * recognizer suppresses the browser's scroll from a non-passive `touchmove`
 * listener, and Blink waits only a short deadline for the main thread to answer
 * that move; past the deadline it assumes nothing was prevented, starts the
 * scroll and CANCELS the pointer. The finger then travels and no drawer
 * arrives, with every other assertion in the test still passing. The recognizer
 * states all of this at the top of ~/src/lib/horizontalSwipe.ts, and it names
 * this file as where the race shows.
 *
 * Every test here boots the app into a fresh context -- coarse-pointer metrics
 * cannot share one -- so each one swipes into the window where the shell is
 * still doing its first layout and measure work. Waiting for the elements to
 * attach does not leave that window; `requestIdleCallback` is the browser's own
 * statement that it has.
 *
 * TWO grants, because the first can be handed out between two long tasks. The
 * timeout is the browser's: a grant that never comes resolves at the deadline
 * rather than hanging, and the swipe then fails on its own assertion.
 */
async function expectMainThreadIdle(page: Page) {
  await page.evaluate(async () => {
    const grant = () => new Promise<void>(resolve => requestIdleCallback(() => resolve(), { timeout: 2000 }))
    await grant()
    await grant()
  })
}

/**
 * The vertical middle of the strip between the tab bar and the composer.
 *
 * Derived, never a fixed offset: the composer grows with its draft and the
 * transcript takes what is left, so a hard-coded y lands on a different element
 * as soon as either changes. This strip is the transcript on any conversation,
 * and it is covered by an open drawer -- so one y serves both the swipes that
 * open a drawer and the swipes that close one.
 */
async function transcriptY(page: Page): Promise<number> {
  const bar = (await page.getByTestId('tab-bar').boundingBox())!
  const editor = (await page.getByTestId('composer-editor').boundingBox())!
  return (bar.y + bar.height + editor.y) / 2
}

/**
 * What is actually under the finger, as a short description.
 *
 * A swipe that misses reports nothing, and "the drawer never opened" is the
 * same failure whether the recognizer declined or the finger simply landed on
 * the composer. Asserting the landing separates the two.
 */
async function elementUnder(page: Page, x: number, y: number): Promise<string> {
  return page.evaluate(([px, py]) => {
    const el = document.elementFromPoint(px, py)
    if (!el)
      return 'nothing'
    const owner = el.closest('[data-chat-scroll-container], [data-testid]')
    return `${el.tagName}|${owner?.getAttribute('data-testid') ?? (owner ? 'chat-scroll-container' : 'no-owner')}`
  }, [x, y] as const)
}

/**
 * How far every swipe below travels, as a fraction of the viewport width.
 *
 * It must stay comfortably past the recognizer's own `SWIPE_MIN_PX` (see
 * ~/src/lib/horizontalSwipe.ts), which is stated in CSS pixels rather than as a
 * fraction. Raise this if that constant ever grows past it -- the specs would
 * otherwise stop swiping far enough and fail as "the drawer never opened".
 */
const SWIPE_TRAVEL_RATIO = 0.4

/**
 * Swipe `direction` across the content band, starting a quarter of the way in
 * from the edge the finger travels away from.
 *
 * That start keeps the finger clear of both edges. The right edge carries the
 * chat scroll rail, which declares `touch-action: none` and therefore owns
 * every press on it; the left edge is where iOS Safari watches for its own
 * back-navigation gesture.
 */
async function swipeBand(page: Page, direction: 'left' | 'right') {
  const width = page.viewportSize()!.width
  const y = await transcriptY(page)
  const travel = width * SWIPE_TRAVEL_RATIO * (direction === 'right' ? 1 : -1)
  const fromX = direction === 'right' ? width * 0.25 : width * 0.75
  // The finger must start on the transcript or on the drawer over it. A landing
  // on the composer or the tab bar is a mis-aimed swipe, and it must fail as
  // that rather than as "the drawer never opened".
  const landing = await elementUnder(page, fromX, y)
  const where = `the swipe start at (${Math.round(fromX)}, ${Math.round(y)})`
  expect(landing, where).not.toMatch(/composer-editor|tab-bar|nothing/)
  await touchSwipe(page, { from: { x: fromX, y }, to: { x: fromX + travel, y } })
}

const swipeRight = (page: Page) => swipeBand(page, 'right')
const swipeLeft = (page: Page) => swipeBand(page, 'left')

test.describe('mobile drawer swipes (phone)', () => {
  // Phone metrics: mobile layout (<768px) with a coarse primary pointer.
  test.use(COARSE_POINTER_METRICS)

  // Stated once, so no spec can act on a shell that has not armed the gesture.
  // See `expectMobileShellIdle` for what it actually waits on.
  test.beforeEach(async ({ page, authenticatedWorkspace }) => {
    void authenticatedWorkspace // fixture trigger
    await expectMobileShellIdle(page)
  })

  test('a swipe right opens the workspaces drawer, and a swipe left closes it', async ({ page, authenticatedWorkspace }) => {
    const { left, right } = drawers(page)

    await swipeRight(page)
    await expect(left).toBeInViewport()
    // Opening one drawer must never bring the other with it.
    await expect(right).not.toBeInViewport()

    // The finger now travels back over the OPEN drawer, which is what makes
    // this the drawer's only pointer-driven dismissal: the panel is full bleed
    // and leaves no scrim to tap.
    await swipeLeft(page)
    await expectMobileShellIdle(page)
  })

  test('a swipe left opens the files drawer, and a swipe right closes it', async ({ page, authenticatedWorkspace }) => {
    const { left, right } = drawers(page)

    await swipeLeft(page)
    await expect(right).toBeInViewport()
    await expect(left).not.toBeInViewport()

    await swipeRight(page)
    await expectMobileShellIdle(page)
  })

  // The open drawer takes the swipe first. Without that, one flick would close
  // the workspaces drawer and open the files drawer in the same gesture.
  test('a swipe towards the side an open drawer came from leaves it open', async ({ page, authenticatedWorkspace }) => {
    const { left, right } = drawers(page)
    await swipeRight(page)
    await expect(left).toBeInViewport()

    await swipeRight(page)
    await expect(left).toBeInViewport()
    await expect(right).not.toBeInViewport()
  })

  // The transcript is the region's main scroller and every read of it is a
  // vertical drag. A recognizer that took those would make the chat unusable.
  test('a drag up the transcript opens no drawer', async ({ page, authenticatedWorkspace }) => {
    const width = page.viewportSize()!.width
    const y = await transcriptY(page)
    await touchSwipe(page, {
      from: { x: width / 2, y: y + 40 },
      to: { x: width / 2, y: y - 40 },
    })
    await expectMobileShellIdle(page)
  })

  // A finger sweeping the composer is placing a caret or extending a selection.
  // The recognizer declines every press inside an editing host for that reason.
  test('a swipe across the composer opens no drawer', async ({ page, authenticatedWorkspace }) => {
    const editorBox = (await page.locator('[data-testid="composer-editor"] .ProseMirror').boundingBox())!
    const y = editorBox.y + editorBox.height / 2
    await touchSwipe(page, {
      from: { x: editorBox.x + editorBox.width * 0.25, y },
      to: { x: editorBox.x + editorBox.width * 0.85, y },
    })
    await expectMobileShellIdle(page)
  })

  /**
   * The trade-off the whole design exists to protect, and the one that no other
   * spec here would catch: the gesture refuses the browser's scroll only for a
   * finger it has CLAIMED, so a code block, a table or a diff still pans.
   *
   * A recognizer that claimed every horizontal press -- or one whose direction
   * test went away -- passes every other spec in this file and fails this one.
   *
   * The pannable block is INJECTED. Seeding a real code block needs a live
   * agent writing one, and what is under test is the recognizer's arbitration
   * against a sideways scroller. Any sideways scroller in the region proves it,
   * and this one costs no agent turn.
   */
  test('a sideways scroller keeps the finger while it can still move that way', async ({ page, authenticatedWorkspace }) => {
    const { left, right } = drawers(page)
    await page.evaluate(() => {
      // The content region: the drawers' own parent, which is where the
      // gesture is armed.
      const region = document.querySelector('[data-testid="mobile-drawer-left"]')!.parentElement!
      const block = document.createElement('div')
      block.dataset.testid = 'e2e-wide-block'
      Object.assign(block.style, {
        position: 'absolute',
        top: '0',
        left: '0',
        right: '0',
        height: '140px',
        overflowX: 'auto',
        overflowY: 'hidden',
        // Over the tiles, under the drawers (which sit at z-index 100).
        zIndex: '2',
        background: 'var(--card)',
      })
      const inner = document.createElement('div')
      Object.assign(inner.style, { width: '4000px', height: '120px' })
      block.appendChild(inner)
      region.appendChild(block)
    })

    const block = page.getByTestId('e2e-wide-block')
    const blockBox = (await block.boundingBox())!
    const y = blockBox.y + blockBox.height / 2
    const width = page.viewportSize()!.width
    const swipeLeftOverBlock = () => touchSwipe(page, {
      from: { x: width * 0.75, y },
      to: { x: width * (0.75 - SWIPE_TRAVEL_RATIO), y },
    })

    // Parked mid-range, so the block has room in both directions.
    await block.evaluate((el: HTMLElement) => {
      el.scrollLeft = 400
    })
    await swipeLeftOverBlock()
    expect(await block.evaluate((el: HTMLElement) => el.scrollLeft)).toBeGreaterThan(400)
    await expectMobileShellIdle(page)

    // At the block's right end it can no longer consume a leftward swipe, so
    // the gesture takes it -- the reason the check is per direction and not
    // "is the finger inside a sideways scroller".
    await block.evaluate((el: HTMLElement) => {
      el.scrollLeft = el.scrollWidth
    })
    await swipeLeftOverBlock()
    await expect(right).toBeInViewport()
    await expect(left).not.toBeInViewport()
  })

  // The bar sits above the swipe region and carries the drawer toggles, the tab
  // chip and the "+" menu. A finger there is working the bar.
  test('a swipe across the tab bar opens no drawer', async ({ page, authenticatedWorkspace }) => {
    const barBox = (await page.getByTestId('tab-bar').boundingBox())!
    const y = barBox.y + barBox.height / 2
    await touchSwipe(page, {
      from: { x: barBox.width * 0.25, y },
      to: { x: barBox.width * 0.75, y },
    })
    await expectMobileShellIdle(page)
  })
})
