import type { Page } from '@playwright/test'
import { expect, test } from './fixtures'
import { COARSE_POINTER_METRICS, touchDown, touchSwipe } from './helpers/touch'
import { sendMessage, userBubbles, waitForAgentIdle } from './helpers/ui'

/**
 * Scrubbing the scroll rail: press anywhere on it, the view jumps there, and the SAME press
 * drags on to scan the history. This is E2E-only by construction. The logic is unit tested
 * (chatScrollRailDrag.test.ts, ChatScrollRail.test.tsx), but what makes the gesture work on a
 * phone is the browser: a trusted touch pointer that `setPointerCapture` accepts, the rail's
 * `touch-action: none` refusing the pan that would otherwise scroll the list under the finger,
 * and the coarse-pointer hit areas that let a fingertip land on a 6px dot at all. jsdom has
 * none of those, and a synthesized PointerEvent cannot stand in for one -- see ./helpers/touch.
 *
 * 047 covers the rail's geometry, dots and dot-click seek; 047b covers its auto-hide. Nothing
 * here re-asserts them.
 */

/**
 * Byte-for-byte 047's and 047b's filler. These specs drive a REAL agent, so the prompt is the
 * test's runtime. Make the VIEWPORT shorter, never the message longer, when a test needs more
 * overflow.
 */
const LONG_MESSAGE = `Please just reply with "ok". Ignore this filler: ${'the quick brown fox jumps over the lazy dog. '.repeat(12)}`

const RAIL = '[data-testid="chat-scroll-rail"]'
const THUMB = '[data-testid="chat-scroll-rail-thumb"]'
const PREVIEW = '[data-testid="chat-scroll-rail-preview"]'
const SCROLLER = '[data-chat-scroll-container="true"]'

/** Send one tall message so the conversation overflows and the rail takes over scrolling. */
async function seedOverflowingConversation(page: Page) {
  const editor = page.locator('[data-testid="chat-editor"] .ProseMirror')
  await expect(editor).toBeVisible()
  // Let the agent finish starting so the send takes the fast path (see 010).
  await expect(page.getByText(/^Starting /)).not.toBeVisible()
  await sendMessage(page, LONG_MESSAGE)
  await waitForAgentIdle(page)
  await expect(userBubbles(page)).toHaveCount(1)
  await expect(page.locator(RAIL)).toBeVisible()
}

const scrollTop = (page: Page) => page.locator(SCROLLER).evaluate((el: HTMLElement) => el.scrollTop)

/** The thumb's centre in viewport coordinates, for "does the thumb follow the finger" checks. */
async function thumbCentreY(page: Page): Promise<number> {
  const box = await page.locator(THUMB).boundingBox()
  expect(box).not.toBeNull()
  return box!.y + box!.height / 2
}

/**
 * Put the transcript at the top and reveal the rail with a brief touch swipe -- the reader's
 * own way in, and the only one on a coarse pointer: the idle rail is `pointer-events: none`,
 * so a finger that lands on a faded strip falls through to the message underneath.
 *
 * Returns the rail's box, whose x is where every press below lands.
 */
async function revealRailByTouch(page: Page) {
  const scroller = page.locator(SCROLLER)
  await scroller.evaluate((el: HTMLElement) => {
    el.scrollTop = 0
  })
  const scrollerBox = await scroller.boundingBox()
  expect(scrollerBox).not.toBeNull()
  // Measure the rail BEFORE the swipe, not after. The rail's activity window is short, and every
  // awaited round trip between the swipe and the caller's press eats into it -- on a loaded CI
  // worker enough of them let the rail go idle, where a coarse pointer finds it
  // `pointer-events: none` and the press falls through to the message underneath. The rail's box
  // does not depend on the swipe (it is faded, not unmounted -- see 047b), so taking it first
  // leaves only the opacity assertion in that gap.
  const rail = page.locator(RAIL)
  const railBox = await rail.boundingBox()
  expect(railBox).not.toBeNull()
  // Swipe well clear of the rail strip on the right edge.
  const swipeX = scrollerBox!.x + scrollerBox!.width / 3
  await touchSwipe(page, {
    from: { x: swipeX, y: scrollerBox!.y + scrollerBox!.height * 0.7 },
    to: { x: swipeX, y: scrollerBox!.y + scrollerBox!.height * 0.5 },
  })
  await expect(rail).toHaveCSS('opacity', '1')
  return railBox!
}

test.describe('chat scroll rail scrubbing', () => {
  test.describe('by touch', () => {
    test.use(COARSE_POINTER_METRICS)

    test('jumps to the touched point, then scrubs while the finger stays down', async ({ page, authenticatedWorkspace }) => {
      await seedOverflowingConversation(page)
      const railBox = await revealRailByTouch(page)
      const x = railBox.x + railBox.width / 2
      // Both points sit inside the thumb-CENTRE travel range, which is inset by half the 24px
      // thumb at each end: a press in that inset still seeks to the very end of the history, but
      // the thumb cannot be drawn centred on it without overrunning the rail, so a
      // thumb-follows-the-finger assertion has to stay off the last 12px.
      const nearBottom = railBox.y + railBox.height - 30
      const nearTop = railBox.y + 20

      // Press near the bottom of the rail: the view jumps there at once, without waiting for
      // the finger to lift. This is the press that used to do nothing at all under a finger.
      const finger = await touchDown(page, x, nearBottom)
      await expect.poll(() => scrollTop(page)).toBeGreaterThan(0)
      // The thumb is under the finger, not wherever it rested before the press.
      expect(Math.abs(await thumbCentreY(page) - nearBottom)).toBeLessThan(4)
      const afterPress = await scrollTop(page)

      // Scrub back up WITHOUT lifting: the thumb follows the finger and the view follows the
      // thumb. Both are what a scrubbing gesture means; neither happened before.
      await finger.moveTo(x, nearTop)
      await expect.poll(() => thumbCentreY(page)).toBeLessThan(nearBottom - 40)
      await expect.poll(() => scrollTop(page)).toBeLessThan(afterPress)

      await finger.end()
      // The release lands where the scrub ended, not back at the pressed point.
      await expect.poll(() => scrollTop(page)).toBeLessThan(afterPress)

      // A plain TAP still works as a track click: down and up with no travel in between.
      const tap = await touchDown(page, x, nearBottom)
      await tap.end()
      await expect.poll(() => scrollTop(page)).toBeGreaterThanOrEqual(afterPress)
    })

    test('previews the message under the finger when a scrub starts on a dot', async ({ page, authenticatedWorkspace }) => {
      // On a coarse pointer each dot carries a 24px hit circle, so on a marked conversation most
      // of the rail is dot rather than track -- and a touch has no hover to open the preview
      // with. The press itself must both open it and start the scrub.
      await seedOverflowingConversation(page)
      await revealRailByTouch(page)

      const dot = page.locator('[data-testid="chat-scroll-rail-dot"]').first()
      const dotBox = await dot.boundingBox()
      expect(dotBox).not.toBeNull()
      const dotX = dotBox!.x + dotBox!.width / 2
      const dotY = dotBox!.y + dotBox!.height / 2

      const finger = await touchDown(page, dotX, dotY)
      // The preview opens from the press alone, and shows the message the dot marks.
      await expect(page.locator(PREVIEW)).toContainText('Please just reply with "ok"')
      // The same press scrubs on: dragging to the bottom moves the view off the dot's message.
      const railBox = await page.locator(RAIL).boundingBox()
      await finger.moveTo(dotX, railBox!.y + railBox!.height - 8)
      await expect.poll(() => scrollTop(page)).toBeGreaterThan(0)
      await finger.end()
    })
  })

  test('scrubs the same way under a mouse on a desktop viewport', async ({ page, authenticatedWorkspace }) => {
    // The press-jump-then-scrub rule is uniform across pointer types -- exactly what a native
    // scrollbar track already does under a mouse. Pin that here so nobody re-introduces a
    // coarse-pointer branch, and to cover the fine-pointer path CDP touch cannot reach.
    await page.setViewportSize({ width: 1024, height: 400 })
    await seedOverflowingConversation(page)

    const scroller = page.locator(SCROLLER)
    await scroller.evaluate((el: HTMLElement) => {
      el.scrollTop = 0
    })
    const rail = page.locator(RAIL)
    const railBox = await rail.boundingBox()
    expect(railBox).not.toBeNull()
    const x = railBox!.x + railBox!.width / 2
    const nearBottom = railBox!.y + railBox!.height - 8
    const nearTop = railBox!.y + 8

    // Moving onto the strip relights the faded rail (a fine pointer keeps it hit-testable),
    // so the press that follows is not rejected by the can't-click-what-you-can't-see guard.
    await page.mouse.move(x, nearBottom)
    await expect(rail).toHaveCSS('opacity', '1')

    await page.mouse.down()
    await expect.poll(() => scrollTop(page)).toBeGreaterThan(0)
    await page.mouse.move(x, nearTop, { steps: 8 })
    await expect.poll(() => scrollTop(page)).toBe(0)
    await page.mouse.up()
    await expect.poll(() => scrollTop(page)).toBe(0)
  })
})
