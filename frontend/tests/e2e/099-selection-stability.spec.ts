import { expect, test } from './fixtures'
import { ASSISTANT_BUBBLE_SELECTOR, firstAssistantMessageRow, sendMessage, waitForAgentIdle } from './helpers/ui'

/**
 * A text selection in the chat transcript must survive the mouse release, and
 * making one must not move the viewport.
 *
 * Both used to fail for one reason: a tile's `onFocus` fires on every click,
 * re-activating the tile's already-active tab, which stamped MRU onto its
 * metadata. A `Tab` is a join result rebuilt on any metadata change, and the
 * panes keyed their `<For>` rows on that object -- so the click that ENDED a
 * drag-select tore down the transcript it had just selected and rebuilt it,
 * restoring the saved scroll position on the way. See TileRenderer's
 * `tileAgentTabIds` and `tabMetadata.touchMru`.
 */
test.describe('chat text selection stability', () => {
  async function dragSelectFirstLine(page: import('@playwright/test').Page) {
    const assistantBubble = firstAssistantMessageRow(page).locator(ASSISTANT_BUBBLE_SELECTOR)
    await expect(assistantBubble).toBeVisible()
    const messageContent = assistantBubble.locator('[data-testid="message-content"]')
    await expect(messageContent).toBeVisible()
    const box = (await messageContent.boundingBox())!
    const y = box.y + 8
    await page.mouse.move(box.x + 4, y)
    await page.mouse.down()
    await page.mouse.move(box.x + Math.min(box.width - 4, 220), y, { steps: 12 })
    const whileDown = await page.evaluate(() => (window.getSelection()?.toString() ?? '').trim().length)
    await page.mouse.up()
    return whileDown
  }

  const selectionLength = (page: import('@playwright/test').Page) =>
    page.evaluate(() => (window.getSelection()?.toString() ?? '').trim().length)

  test('a drag-selection survives the mouse release', async ({ page, authenticatedWorkspace }) => {
    await expect(page.locator('[data-testid="chat-editor"] .ProseMirror')).toBeVisible()
    await sendMessage(page, 'Say exactly: The quick brown fox jumps over the lazy dog')
    await waitForAgentIdle(page)

    // Retry the whole measure-and-drag as one unit. The box is read, then the
    // pointer walks it -- and between those the transcript can grow (the
    // turn-end divider lands after the thinking indicator clears, see
    // waitForAgentIdle), moving the bubble out from under the coordinates and
    // selecting nothing at all. That is how this failed: `whileDown` was 0, so
    // the drag never happened, not the release.
    await expect(async () => {
      expect(await dragSelectFirstLine(page), 'the drag selects text').toBeGreaterThan(0)
    }).toPass()

    // The release is the moment that used to lose it; give the click handler,
    // the popover's rAF, and any re-render a chance to land.
    await page.waitForTimeout(600)
    expect(await selectionLength(page), 'the selection survives the release').toBeGreaterThan(0)

    // And the popover the selection is FOR actually appears, which it cannot do
    // if the selection was collapsed before its rAF ran.
    await expect(page.locator('[data-testid="quote-selection-button"]')).toBeVisible()
  })

  test('selecting text while scrolled up does not move the viewport', async ({ page, authenticatedWorkspace }) => {
    await expect(page.locator('[data-testid="chat-editor"] .ProseMirror')).toBeVisible()
    // Enough turns to make the transcript scrollable, so "scrolled up" is a real state.
    for (const n of [1, 2, 3, 4]) {
      await sendMessage(page, `Say exactly: line ${n} -- the quick brown fox jumps over the lazy dog`)
      await waitForAgentIdle(page)
    }

    const scroller = page.locator('[data-chat-scroll-container="true"]')
    await scroller.evaluate(el => el.scrollTo({ top: 0 }))
    await page.waitForTimeout(300)
    const before = await scroller.evaluate(el => el.scrollTop)

    await dragSelectFirstLine(page)
    await page.waitForTimeout(600)

    const after = await scroller.evaluate(el => el.scrollTop)
    expect(Math.abs(after - before), `viewport moved ${before} -> ${after} while selecting`).toBeLessThanOrEqual(2)
  })
})
