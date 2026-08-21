import type { Page } from '@playwright/test'
import { expect, test } from './fixtures'
import { COARSE_POINTER_METRICS, touchDown } from './helpers/touch'
import { ARITHMETIC_PROMPT, assistantBubbles, bandRows, CHAT_SCROLL_CONTAINER, chatScrollContainer, firstAssistantBubble, measureAgainstChatList, measureBubbleEdges, messageContents, sendMessage, userBubbles, waitForAgentIdle } from './helpers/ui'

/**
 * Send one prompt and wait for the turn to settle. Every test below opens the
 * same way, and `sendMessage` also waits for the editor to empty, which is the
 * app's own acknowledgement that the send committed.
 */
async function sendAndSettle(page: Page, prompt = ARITHMETIC_PROMPT) {
  await sendMessage(page, prompt)
  await expect(firstAssistantBubble(page)).toBeVisible()
  await waitForAgentIdle(page)
}

/**
 * Smoke test for end-to-end chat rendering: user input → real LLM response →
 * rendered bubbles. The bubble component itself is exhaustively unit-tested
 * in `src/components/chat/MessageBubble.test.tsx` (over 30 cases covering
 * thinking, todos, tools, attachments, edits, etc.). This e2e exercises the
 * remaining integration: the editor send path, the WebSocket/RPC delivery
 * to a real Claude agent, the streaming-to-rendered transition, and the
 * markdown HTML output that only Shiki + jsdom-incompatible CSS can verify.
 */

test.describe('Chat Message Rendering', () => {
  test('user message renders as human text and assistant reply renders as markdown', async ({ page, authenticatedWorkspace }) => {
    await sendAndSettle(page)

    // User bubble: shows the human text, NOT the raw JSON envelope.
    const userBubble = userBubbles(page).first()
    const userContent = userBubble.locator('[data-testid="message-content"]')
    await expect(userContent).toContainText('What is 1234 + 5678?')
    await expect(userContent).not.toContainText('{"content":')

    // Assistant bubble: rendered as HTML markdown (at least one <p>),
    // not raw text.
    const assistantBubble = assistantBubbles(page).filter({
      has: messageContents(page).locator('p'),
    }).first()
    const assistantContent = assistantBubble.locator('[data-testid="message-content"]')
    const paragraphs = await assistantContent.locator('p').count()
    expect(paragraphs).toBeGreaterThan(0)
  })

  /**
   * The band reaches both panel edges. Only a real browser can verify it: the
   * strip's width comes from CSS var arithmetic that cancels the scroll
   * container's gutter, and it works around paint containment, neither of which
   * jsdom computes.
   *
   * The seam check that follows is an OPPORTUNISTIC invariant, not the point of
   * this test: a live turn need not produce two ADJACENT band rows, so on a turn
   * that yields one band, or that puts a tool row between two bands, the loop
   * asserts nothing. The overlap arithmetic itself is covered exhaustively, and
   * deterministically, in `src/components/chat/useChatVirtualizer.geometry.test.ts`.
   */
  test('an assistant row paints a band that reaches both panel edges', async ({ page, authenticatedWorkspace }) => {
    await sendAndSettle(page)

    await expect(bandRows(page, 'text').first()).toBeVisible()

    // EVERY visible band, not just the first, and each one reported with the rail count
    // it was carrying. A row's rails push its content column right by a width only the
    // row knows, so a bleed sized to the bare gutter stops short on a railed row and
    // reaches the edge on a bare one -- the very case ROW_BLEED_LEFT_VAR exists for.
    // Checking one band would pass on a transcript whose only band has no rails.
    const bands = await bandRows(page).evaluateAll((els, selector) => els.map((el) => {
      const list = el.closest(selector)!
      return {
        rails: el.querySelector('[data-span-columns]')?.getAttribute('data-span-columns') ?? '0',
        width: el.getBoundingClientRect().width,
        listWidth: list.clientWidth,
      }
    }), CHAT_SCROLL_CONTAINER)

    expect(bands.length).toBeGreaterThan(0)
    for (const band of bands) {
      expect(band.listWidth).toBeGreaterThan(0)
      expect({ rails: band.rails, offBy: Math.round(Math.abs(band.width - band.listWidth)) })
        .toEqual({ rails: band.rails, offBy: 0 })
    }

    // No two ADJACENT bands may show a gap between them: the lower row overlaps the
    // upper one by the band border width so the pair reads as one line. Walk the rows in
    // DOM order and compare only true neighbours -- comparing every pair of BANDS instead
    // would fail whenever a tool row sits between two of them, on a layout the code got
    // right. The in-flow streaming tail is a neighbour like any other, which is what
    // covers its own merge (bandTailMerged) here.
    //
    // `data-seq` marks every virtual row; the tail has no seq but does carry `data-band`.
    // Scoped to the scroll container, which excludes the hidden premeasure copies (they
    // mount outside it) and the rail's dots (which reuse data-seq).
    const seams = await chatScrollContainer(page).locator('[data-seq], [data-band]').evaluateAll(els =>
      els
        .map((el) => {
          const r = el.getBoundingClientRect()
          return {
            band: el.getAttribute('data-band'),
            painting: globalThis.getComputedStyle(el).visibility !== 'hidden',
            top: r.top,
            bottom: r.bottom,
          }
        })
        .flatMap((row, i, all) => {
          const above = all[i - 1]
          if (!above || !above.band || !row.band || !above.painting || !row.painting)
            return []
          return [row.top - above.bottom]
        }),
    )
    for (const seam of seams) {
      expect(seam).toBeLessThanOrEqual(0)
      expect(seam).toBeGreaterThanOrEqual(-2)
    }
  })

  /**
   * The turn-end rule bleeds by a DESCENDANT's negative margin, not by the row's
   * own background, so it exercises the other half of the paint-containment
   * story: the row's padding box must be wide enough for the rule to reach it.
   * jsdom computes neither, which is why this lives here.
   */
  test('the turn-end divider runs its rule to both panel edges', async ({ page, authenticatedWorkspace }) => {
    await sendAndSettle(page)

    const divider = page.locator('[data-testid="result-divider"]:visible').first()
    await expect(divider).toBeVisible()

    const { width, listWidth } = await measureAgainstChatList(divider)
    expect(listWidth).toBeGreaterThan(0)
    expect(Math.abs(width - listWidth)).toBeLessThanOrEqual(1)
  })

  /**
   * A user bubble bleeds on ONE side only, which is the interesting case: the
   * right edge meets the panel, while the left keeps the bubble's rounded,
   * content-hugging shape well inside the gutter.
   *
   * The plan-execution card takes the same rule, and 050-plan-mode.spec.ts
   * measures it through the same helper.
   */
  test('a user bubble meets the right panel edge and keeps its left side inset', async ({ page, authenticatedWorkspace }) => {
    await sendMessage(page, 'hi')

    const bubble = userBubbles(page).first()
    await expect(bubble).toBeVisible()

    const box = await measureBubbleEdges(bubble)

    // Flush right: the bubble's right edge sits on the list's padding-box edge.
    expect(Math.abs(box.rightGap)).toBeLessThanOrEqual(1)
    // Still a bubble on the left: inset by at least the gutter, and short of the
    // full width -- 'hi' must not stretch the bubble across the panel.
    expect(box.leftGap).toBeGreaterThan(20)
    // A rounded corner flush against the edge would read as a mistake.
    expect(box.radius).toBe('0px')
    // Top edge on the row's. The row places the bubble on BOTH axes; a bubble-level
    // `alignSelf` is what would move this, and it would take the bubble's first line
    // off the line the toolbar beside it sits on.
    expect(Math.abs(box.topGapInRow)).toBeLessThanOrEqual(1)
  })

  /**
   * The chat list is the one surface where the row menu has to share the press
   * with text selection, and the one whose rows are tall enough that anchoring to
   * the row instead of the cursor would be obviously wrong.
   */
  test('right-click on a message opens its menu at the cursor, and leaves a selection to the browser', async ({ page, authenticatedWorkspace }) => {
    await sendAndSettle(page)

    const bubble = firstAssistantBubble(page)
    await expect(bubble).toBeVisible()

    const box = (await bubble.boundingBox())!
    const x = box.x + box.width / 2
    const y = box.y + Math.min(20, box.height / 2)

    await page.mouse.click(x, y, { button: 'right' })

    const menu = page.locator('[data-testid="message-context-menu"]:popover-open')
    await expect(menu).toBeVisible()
    // The same actions the hover toolbar carries, plus the send time.
    await expect(menu.locator('[data-testid="message-menu-copy-json"]')).toBeVisible()
    await expect(menu.locator('[data-testid="message-menu-info"]')).toBeVisible()

    // At the cursor. A message row is tall, so anchoring to the ROW would put the
    // menu far from the pointer.
    //
    // WHICH EDGE hangs off the cursor depends on the room below it. `pressAnchorRect`
    // hands calcPopoverPosition a zero-height rect at the press point, so a menu that
    // fits opens with its TOP there, and one that would overflow the viewport bottom
    // flips and puts its BOTTOM there instead. Both are anchored to the cursor, which
    // is what this measures; the app publishes `data-flipped` to say which happened,
    // so read the placement rather than assume the downward one. A settled turn puts
    // this bubble low enough that the menu does flip, and how low depends on the live
    // reply's length -- so asserting the downward edge alone failed on a placement the
    // app got right.
    //
    // One round trip for the box and the marker together: the menu re-anchors when its
    // measured size settles, so two reads could straddle that and mix a stale edge with
    // a fresh flip state.
    const placement = await menu.evaluate((el) => {
      const rect = el.getBoundingClientRect()
      return { left: rect.left, top: rect.top, bottom: rect.bottom, flipped: el.hasAttribute('data-flipped') }
    })
    const anchoredEdge = placement.flipped ? placement.bottom : placement.top
    expect(Math.abs(placement.left - x)).toBeLessThan(4)
    expect(Math.abs(anchoredEdge - y)).toBeLessThan(4)

    await page.keyboard.press('Escape')
    await expect(menu).toBeHidden()

    // With text selected under the cursor, the browser's own menu wins so Copy
    // still works. The app suppresses neither the selection nor the native menu.
    await bubble.evaluate((el) => {
      const range = document.createRange()
      range.selectNodeContents(el)
      const selection = window.getSelection()!
      selection.removeAllRanges()
      selection.addRange(range)
    })
    const selectionBox = await bubble.evaluate(() => {
      const rect = window.getSelection()!.getRangeAt(0).getClientRects()[0]
      return { x: rect.left + rect.width / 2, y: rect.top + rect.height / 2 }
    })
    await page.mouse.click(selectionBox.x, selectionBox.y, { button: 'right' })
    await expect(menu).toBeHidden()
  })
})

/**
 * The long press, on the surface a phone user actually presses.
 *
 * The chat list's menu is a SINGLETON host driven by `DropdownMenu`'s
 * controlled `open`, not by the `contextMenuFor` gesture the other rows use --
 * so it takes a different path through the component, and a fix applied to only
 * one of them left this menu still vanishing on release. That is what this
 * covers: the press-opened menu here has to behave like every other one.
 */
test.describe('message long press (phone)', () => {
  test.use(COARSE_POINTER_METRICS)

  test('a long press opens the menu on the hold and the release leaves it up', async ({ page, authenticatedWorkspace }) => {
    await sendAndSettle(page)

    const bubble = firstAssistantBubble(page)
    await expect(bubble).toBeVisible()
    const box = (await bubble.boundingBox())!
    const x = box.x + box.width / 2
    const y = box.y + Math.min(20, box.height / 2)

    const holding = await touchDown(page, x, y)
    const menu = page.locator('[data-testid="message-context-menu"]:popover-open')

    // ON the hold: the finger is still down, and the menu is already up.
    await expect(menu).toBeVisible()

    // ...and the release leaves it there. A `popover="auto"` shown under a
    // finger is what the HTML light-dismiss pass takes away, which is why this
    // menu opens as `manual` instead.
    await holding.end()
    await expect(menu).toBeVisible()

    // The dismissal that comes with `manual`, and which the platform is no
    // longer doing for it.
    await page.locator('body').dispatchEvent('pointerdown')
    await expect(menu).toBeHidden()
  })
})
