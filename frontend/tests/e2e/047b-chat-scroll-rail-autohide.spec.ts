import { devices } from '@playwright/test'
import { expect, test } from './fixtures'
import { sendMessage, userBubbles, waitForAgentIdle } from './helpers/ui'

/**
 * The scroll rail's floating auto-hide, which is E2E-only by construction. Everything that
 * makes the `railIdle` class DO anything -- `opacity: 0`, `pointer-events: none`, the phone
 * gutters, and the media queries that scope all three -- is invisible to vitest: the unit
 * config inserts no stylesheet for a `.css.ts` import and jsdom evaluates no media query.
 * ChatScrollRail.test.tsx therefore pins only that the class flips; this pins that the class
 * and the gutters are scoped to the viewports they were meant for.
 *
 * The rail's geometry, dots, drag and seek are covered by 047; nothing here re-asserts them.
 */

/**
 * Byte-for-byte 047's filler, deliberately. These specs drive a REAL agent, so the prompt
 * is the test's runtime: a 2.5x longer filler pushed the turn past waitForAgentIdle's
 * 120s bound under parallel load. Keep this string in step with 047's; make the VIEWPORT
 * shorter, never the message longer, when a test needs more overflow.
 *
 * One message per test, not 047's two: 047 needs two jump dots and nothing here does, and
 * an agent turn is the most expensive thing in the suite.
 */
const LONG_MESSAGE = `Please just reply with "ok". Ignore this filler: ${'the quick brown fox jumps over the lazy dog. '.repeat(12)}`

const RAIL = '[data-testid="chat-scroll-rail"]'
const SCROLLER = '[data-chat-scroll-container="true"]'

/**
 * Pixel 7's device metrics WITHOUT its `defaultBrowserType`, and with a shorter viewport.
 * Playwright refuses a `defaultBrowserType` inside a describe group -- it would force a new
 * worker -- and this suite's only project is already chromium, so the field is both unusable
 * and redundant. The rest is what actually gives Blink a COARSE primary pointer. The height
 * is cut from the device's 915px to 047's 380px: the device is narrower than 720px, so its
 * lines wrap more and one seeded message still overflows comfortably.
 */
const PIXEL_7_METRICS = {
  viewport: { width: devices['Pixel 7'].viewport.width, height: 380 },
  deviceScaleFactor: devices['Pixel 7'].deviceScaleFactor,
  isMobile: devices['Pixel 7'].isMobile,
  hasTouch: devices['Pixel 7'].hasTouch,
} as const

/**
 * Send one tall message so the conversation overflows and the rail takes over scrolling.
 * Asserts the rail is present before returning: without overflow the rail correctly hides
 * itself, and every test here would then fail on a confusing missing-element error rather
 * than on "the viewport was too tall for one message".
 */
async function seedOverflowingConversation(page: import('@playwright/test').Page) {
  const editor = page.locator('[data-testid="chat-editor"] .ProseMirror')
  await expect(editor).toBeVisible()
  // Let the agent finish starting so the send takes the fast path (see 010).
  await expect(page.getByText(/^Starting /)).not.toBeVisible()
  await sendMessage(page, LONG_MESSAGE)
  await waitForAgentIdle(page)
  await expect(userBubbles(page)).toHaveCount(1)
  await expect(page.locator(RAIL)).toBeVisible()
}

test.describe('chat scroll rail auto-hide', () => {
  test('fades the rail when scrolling stops and brings it back on the next scroll', async ({ page, authenticatedWorkspace }) => {
    // A DESKTOP viewport (fine pointer, well above the phone breakpoint): auto-hide used to be
    // touch/narrow-only, but now applies on every screen. This test pins that it reaches desktop.
    await page.setViewportSize({ width: 1024, height: 600 })
    await seedOverflowingConversation(page)

    const rail = page.locator(RAIL)

    // The idle window closes on its own; toHaveCSS retries to the global timeout, so this
    // needs no hand-rolled wait and no per-call timeout override.
    await expect(rail).toHaveCSS('opacity', '0')

    // A real user scroll relights it...
    await page.locator(SCROLLER).hover()
    await page.mouse.wheel(0, -240)
    await expect(rail).toHaveCSS('opacity', '1')

    // ...and it fades again once that gesture goes quiet.
    await expect(rail).toHaveCSS('opacity', '0')
  })

  test('keeps the rail hidden while the agent auto-scrolls a streaming reply', async ({ page, authenticatedWorkspace }) => {
    // The load-bearing case. The stick-to-bottom writes scrollTop on every streaming
    // commit; if those counted as scroll activity the rail would stay lit for the whole
    // response, which is most of the time a reader looks at the screen.
    await page.setViewportSize({ width: 1024, height: 600 })
    await seedOverflowingConversation(page)

    const rail = page.locator(RAIL)
    await expect(rail).toHaveCSS('opacity', '0')

    // Start a turn WITHOUT touching the scroller. sendMessage types into the editor, a
    // sibling of the scroll container, so its keystrokes never reach the list's handlers.
    await sendMessage(page, LONG_MESSAGE)

    // Sample every frame from inside the page: a round trip per sample could straddle the
    // window and miss a flash. `toHaveCSS('opacity', '0')` cannot express this at all --
    // it retries until it passes, which proves the value was 0 once, not that it was never
    // 1. Bounded by a frame budget so a stalled agent fails the assert instead of hanging.
    const observed = await page.evaluate(async ({ railSel, scrollerSel }) => {
      const rail = document.querySelector(railSel)!
      const scroller = document.querySelector(scrollerSel) as HTMLElement
      let autoScrolls = 0
      let last = scroller.scrollTop
      let maxOpacity = 0
      // Sample for the FULL frame budget, with no early-exit on an event count: capping at 8
      // auto-scrolls could end sampling before the 1.2s idle window ever exercised a flash, which
      // would make maxOpacity === 0 vacuously true for a fast agent. The frame cap is the bound.
      for (let frame = 0; frame < 900; frame++) {
        await new Promise(resolve => requestAnimationFrame(() => resolve(null)))
        if (scroller.scrollTop !== last) {
          last = scroller.scrollTop
          autoScrolls++
        }
        maxOpacity = Math.max(maxOpacity, Number.parseFloat(getComputedStyle(rail).opacity))
      }
      return { autoScrolls, maxOpacity }
    }, { railSel: RAIL, scrollerSel: SCROLLER })

    // The auto-scroll really ran, so a maxOpacity of 0 is evidence and not a vacuous pass.
    expect(observed.autoScrolls).toBeGreaterThan(0)
    expect(observed.maxOpacity).toBe(0)

    await waitForAgentIdle(page)
  })

  test('keeps the message list gutters uniform above the phone breakpoint', async ({ page, authenticatedWorkspace }) => {
    // Only the PHONE breakpoint shrinks the gutters (reclaiming text width on a narrow
    // viewport). Above it both sides match, so a desktop column stays balanced.
    const scroller = page.locator(SCROLLER)
    await expect(scroller).toBeVisible()

    await page.setViewportSize({ width: 900, height: 600 })
    await expect(scroller).toHaveCSS('padding-left', '16px')
    await expect(scroller).toHaveCSS('padding-right', '16px')

    // Below the phone breakpoint the text column reclaims 24px in total.
    await page.setViewportSize({ width: 600, height: 600 })
    await expect(scroller).toHaveCSS('padding-left', '12px')
    await expect(scroller).toHaveCSS('padding-right', '4px')
  })

  test('stays hit-testable on a fine pointer but rejects a click on the faded rail', async ({ page, authenticatedWorkspace }) => {
    // The faded rail stays hit-testable on a fine pointer (pointer-events: auto) so a
    // pointermove onto it can relight it -- but a CLICK on the faded rail is rejected at the
    // pointer handler: you can't click what you can't see. The next click, rail now lit, jumps.
    await page.setViewportSize({ width: 1024, height: 600 })
    await seedOverflowingConversation(page)

    const rail = page.locator(RAIL)
    await expect(rail).toHaveCSS('opacity', '0')
    await expect(rail).toHaveCSS('pointer-events', 'auto')

    const railBox = await rail.boundingBox()
    const clickX = railBox!.x + railBox!.width / 2
    const clickY = railBox!.y + railBox!.height - 6

    const scroller = page.locator(SCROLLER)
    await scroller.evaluate((el: HTMLElement) => {
      el.scrollTop = 0
    })

    // A real mouse.click() MOVES to the target first, firing a pointermove over the strip that
    // relights the faded rail before the press lands. To exercise the faded rejection, dispatch a
    // bare pointerdown directly on the rail element: no implicit pointermove, so the rail is still
    // faded when the handler reads it. The grab is rejected and the rail lights (onActivity), but
    // scrollTop stays at 0.
    await page.evaluate(({ railSel, x, y }) => {
      const el = document.querySelector(railSel)!
      el.dispatchEvent(new PointerEvent('pointerdown', { bubbles: true, clientX: x, clientY: y, pointerId: 1 }))
    }, { railSel: RAIL, x: clickX, y: clickY })
    await expect(rail).toHaveCSS('opacity', '1')
    await expect.poll(async () => await scroller.evaluate((el: HTMLElement) => el.scrollTop)).toBe(0)

    // SECOND click on the now-LIT rail: the track-click jump goes through. A click near the
    // bottom of the strip seeks toward the end, so scrollTop must move off zero.
    await page.mouse.click(clickX, clickY)
    await expect.poll(async () => await scroller.evaluate((el: HTMLElement) => el.scrollTop)).toBeGreaterThan(0)
  })

  // Device-metrics emulation is the only way to get `(pointer: coarse)`: Blink derives the
  // primary pointer type from the mobile viewport, not from `hasTouch`, so a project that
  // only set hasTouch would look like coverage and be none. Scoped to the one test that
  // needs it -- `test.use` at describe level takes per-test context options and needs no
  // change to playwright.config.ts.
  test.describe('on a coarse pointer', () => {
    test.use(PIXEL_7_METRICS)

    test('makes the idle rail inert, so its strip cannot swallow a tap', async ({ page, authenticatedWorkspace }) => {
      // With the phone gutters the 22px coarse strip overlaps ~20px of message content. An
      // INVISIBLE strip that still took pointer events would turn a tap on the text into a
      // track-click jump, so going inert while idle is what makes that overlap acceptable.
      await seedOverflowingConversation(page)

      const rail = page.locator(RAIL)
      await expect(rail).toHaveCSS('opacity', '0')
      await expect(rail).toHaveCSS('pointer-events', 'none')
    })
  })
})
