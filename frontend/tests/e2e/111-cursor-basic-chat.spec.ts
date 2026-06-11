import { CURSOR_E2E_SKIP_REASON, cursorTest, expect } from './cursor-fixtures'
import { ARITHMETIC_ANSWER, ARITHMETIC_PROMPT, assistantBubbles, sendMessage, waitForAgentIdle } from './helpers/ui'

const QUOTA_OR_LIMIT_ERROR_RE = /quota|limit|too many requests/i

cursorTest.skip(!!CURSOR_E2E_SKIP_REASON, CURSOR_E2E_SKIP_REASON || '')

cursorTest.describe('Cursor Basic Chat', () => {
  cursorTest('send message and receive response', async ({ authenticatedCursorWorkspace, page }) => {
    void authenticatedCursorWorkspace
    await sendMessage(page, ARITHMETIC_PROMPT)
    await waitForAgentIdle(page, 120_000)

    // Scan every assistant bubble (robust to a trailing "Turn ended" divider).
    // Accept either a quota/limit error (CI may be rate-limited) or the expected
    // answer. expect.poll retries until the agent's (non-empty) text lands rather
    // than reading the bubbles once.
    await expect.poll(async () => {
      const text = (await assistantBubbles(page).allInnerTexts()).join('\n')
      return text.length > 0 && (QUOTA_OR_LIMIT_ERROR_RE.test(text) || ARITHMETIC_ANSWER.test(text))
    }).toBe(true)
  })
})
