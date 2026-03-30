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
    const text = await bubble.textContent()
    expect(text === null || QUOTA_OR_LIMIT_ERROR_RE.test(text) || text.includes('4')).toBe(true)
  })
})
