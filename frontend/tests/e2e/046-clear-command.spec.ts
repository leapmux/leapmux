import { expect, test } from './fixtures'
import { ARITHMETIC_PROMPT, expectAssistantAnswer, SECOND_ARITHMETIC_ANSWER, SECOND_ARITHMETIC_PROMPT, sendMessage, visibleOnly } from './helpers/ui'

test.describe('Clear Command', () => {
  /**
   * Both tests below asserted on `lastAssistantBubble(...)`, which is the LAST
   * agent-role bubble in the DOM -- and the last one is the turn-end divider
   * ("Took 1.8s"), not the answer. So they spent the full 120s expect budget
   * comparing 6912 against a duration and failed in every run at every worker
   * count. `expectAssistantAnswer` scans every agent bubble instead, which is
   * exactly the shape this needs.
   */
  test('slash reset clears context (alias for /clear)', async ({ page, authenticatedWorkspace }) => {
    void authenticatedWorkspace // fixture trigger

    // Send a message to establish a session
    await sendMessage(page, ARITHMETIC_PROMPT)
    await expectAssistantAnswer(page)

    // Send /reset (alias for /clear)
    await sendMessage(page, '/reset')

    // Verify notification bubble appears
    await expect(visibleOnly(page.getByText('Context cleared'))).toBeVisible()

    // Verify agent is still responsive (new session)
    await sendMessage(page, SECOND_ARITHMETIC_PROMPT)
    await expectAssistantAnswer(page, { answer: SECOND_ARITHMETIC_ANSWER })
  })

  test('slash clear clears context and shows notification', async ({ page, authenticatedWorkspace }) => {
    void authenticatedWorkspace // fixture trigger

    // Send a message to establish a session
    await sendMessage(page, ARITHMETIC_PROMPT)
    await expectAssistantAnswer(page)

    // Send /clear
    await sendMessage(page, '/clear')

    // Verify notification bubble appears
    await expect(visibleOnly(page.getByText('Context cleared'))).toBeVisible()

    // Verify agent is still responsive (new session)
    await sendMessage(page, SECOND_ARITHMETIC_PROMPT)
    await expectAssistantAnswer(page, { answer: SECOND_ARITHMETIC_ANSWER })

    // After /clear and a new response, context usage is repopulated by the
    // new session's system prompt tokens.  Verify the grid is visible again
    // (indicating the new session has active context).
    const grid = page.locator('svg[viewBox="0 0 11 11"]')
    await expect(grid).toBeVisible()
  })
})
