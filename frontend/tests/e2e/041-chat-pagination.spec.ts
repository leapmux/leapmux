import { expect, test } from './fixtures'
import { sendScriptedTurn } from './helpers/scriptedTurn'
import { chatScrollContainer, readAttached } from './helpers/ui'

/**
 * Check chat layout and scroll position after a real streamed response.
 * `src/stores/chat.store.test.ts` covers pagination and duplicate removal.
 * This browser test checks row transforms, sequence attributes, and the final scroll position.
 */

test.describe('Chat Pagination & Scroll', () => {
  test('renders sequenced messages and clears the indicator after a response', async ({ page, authenticatedWorkspace, modelScript }) => {
    // The helper waits for the answer in an assistant bubble, then for the
    // thinking indicator of the visible chat to go away.
    await sendScriptedTurn(page, modelScript, { prompt: 'Say hello.', answer: 'Hello.' })

    // After the turn completes, no copy of the indicator and no Interrupt button remains.
    await expect(page.locator('[data-testid="interrupt-button"]')).not.toBeVisible()
    await expect(page.locator('[data-testid="thinking-indicator"]')).not.toBeVisible()

    // Each rendered message wrapper carries a positive data-seq from the
    // server — this is what powers chat.store's pagination ordering.
    // Scoped to the scroll container, which excludes the hidden premeasure
    // copies (they mount outside it) and the dots of the scroll rail (which
    // reuse data-seq).
    const scroller = chatScrollContainer(page)
    const seqElements = scroller.locator('[data-seq]')
    const count = await seqElements.count()
    expect(count).toBeGreaterThan(1)
    for (let i = 0; i < count; i++) {
      const seqValue = await seqElements.nth(i).getAttribute('data-seq')
      expect(Number(seqValue)).toBeGreaterThan(0)
    }

    // The first row has offset zero. A later row must have a nonzero transform.
    // Skip detached rows because replacing an entry creates its row again.
    // A detached row reports no transform. See readAttached.
    const rowTransform = (row: typeof seqElements) =>
      readAttached(row, 'the row transform', (matches) => {
        const el = matches.find(candidate => candidate.isConnected)
        return el ? getComputedStyle(el).transform : null
      })
    expect(await rowTransform(seqElements.last())).toMatch(/^matrix/)

    // The viewport must stay at the bottom after a streamed turn with virtualized rows.
    const distFromBottom = await scroller.evaluate(el => el.scrollHeight - el.scrollTop - el.clientHeight)
    expect(distFromBottom).toBeLessThan(40)
  })
})
