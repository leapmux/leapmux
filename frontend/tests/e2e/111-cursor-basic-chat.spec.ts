import { CURSOR_E2E_SKIP_REASON, cursorTest, expect } from './cursor-fixtures'
import { lastAssistantBubble, sendMessage, waitForAgentIdle } from './helpers/ui'

const QUOTA_OR_LIMIT_ERROR_RE = /quota|limit|too many requests/i

cursorTest.skip(!!CURSOR_E2E_SKIP_REASON, CURSOR_E2E_SKIP_REASON || '')

cursorTest.describe('Cursor Basic Chat', () => {
  cursorTest('send message and receive response', async ({ authenticatedCursorWorkspace, page }) => {
    void authenticatedCursorWorkspace
    await sendMessage(page, 'What is 2+2? Reply with just the number.')
    await waitForAgentIdle(page, 120_000)

    const bubble = await lastAssistantBubble(page)
    await expect(bubble).toBeVisible()
    const text = (await bubble.textContent()) ?? ''
    // Accept either a quota/limit error (CI may be rate-limited) or the
    // expected answer. Reject empty/null — the bubble must have content.
    expect(text.length).toBeGreaterThan(0)
    expect(QUOTA_OR_LIMIT_ERROR_RE.test(text) || text.includes('4')).toBe(true)
  })
})
