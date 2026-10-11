import type { Page } from '@playwright/test'
import { expect } from '@playwright/test'
import { test } from './fixtures'
import { settleFrames } from './helpers/frames'
import { QUICK_BROWN_FOX, sayExactly, sendScriptedTurn } from './helpers/scriptedTurn'
import { selectedText } from './helpers/selection'
import { ASSISTANT_BUBBLE_SELECTOR, chatScrollContainer, firstAssistantMessageRow } from './helpers/ui'

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
  async function selectionLength(page: Page): Promise<number> {
    return (await selectedText(page)).trim().length
  }

  /**
   * Wait until the release of a drag-select settled: the quote popover shows, and the frames after it ran.
   * The popover shows from a rAF after the release, so the click handler and the re-render that the release can
   * start ran before it. It cannot show for a selection that the release collapsed.
   */
  async function expectReleaseSettled(page: Page): Promise<void> {
    await expect(page.locator('[data-testid="quote-selection-button"]'), 'the quote popover shows for the selection').toBeVisible()
    await settleFrames(page)
  }

  async function dragSelectFirstLine(page: Page) {
    const assistantBubble = firstAssistantMessageRow(page).locator(ASSISTANT_BUBBLE_SELECTOR)
    await expect(assistantBubble).toBeVisible()
    const messageContent = assistantBubble.locator('[data-testid="message-content"]')
    await expect(messageContent).toBeVisible()
    const box = (await messageContent.boundingBox())!
    const y = box.y + 8
    await page.mouse.move(box.x + 4, y)
    await page.mouse.down()
    await page.mouse.move(box.x + Math.min(box.width - 4, 220), y, { steps: 12 })
    const whileDown = await selectionLength(page)
    await page.mouse.up()
    return whileDown
  }

  test('a drag-selection survives the mouse release', async ({ page, authenticatedWorkspace, modelScript }) => {
    await sendScriptedTurn(page, modelScript, sayExactly(QUICK_BROWN_FOX))

    // Retry the whole measure-and-drag as one unit. The box is read, then the
    // pointer walks it -- and between those the transcript can grow (the
    // turn-end divider lands after the thinking indicator clears, see
    // waitForAgentIdle), moving the bubble out from under the coordinates and
    // selecting nothing at all. That is how this failed: `whileDown` was 0, so
    // the drag never happened, not the release.
    await expect(async () => {
      expect(await dragSelectFirstLine(page), 'the drag selects text').toBeGreaterThan(0)
    }).toPass()

    // The release is the moment that used to lose it. The popover the
    // selection is FOR appears from a rAF after the click handler and any
    // re-render ran, and it cannot appear if the selection was collapsed
    // first. So its appearance ends the window, and the next frames flush
    // what it queued.
    await expectReleaseSettled(page)
    expect(await selectionLength(page), 'the selection survives the release').toBeGreaterThan(0)
  })

  test('selecting text while scrolled up does not move the viewport', async ({ page, authenticatedWorkspace, modelScript }) => {
    // Enough turns to make the transcript scrollable, so "scrolled up" is a real state.
    for (const n of [1, 2, 3, 4])
      await sendScriptedTurn(page, modelScript, sayExactly(`line ${n} -- ${QUICK_BROWN_FOX.toLowerCase()}`))

    const scroller = chatScrollContainer(page)
    await scroller.evaluate(el => el.scrollTo({ top: 0 }))
    // The first row is on screen, and the frames after the scroll ran the
    // re-anchoring that measuring the rows can do.
    await expect(firstAssistantMessageRow(page)).toBeInViewport()
    await settleFrames(page)
    const before = await scroller.evaluate(el => el.scrollTop)

    // A drag that selects nothing cannot move the viewport for the reason this test guards.
    expect(await dragSelectFirstLine(page), 'the drag selects text').toBeGreaterThan(0)
    await expectReleaseSettled(page)

    const after = await scroller.evaluate(el => el.scrollTop)
    expect(Math.abs(after - before), `viewport moved ${before} -> ${after} while selecting`).toBeLessThanOrEqual(2)
  })
})
