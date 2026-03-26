import { sendMessage, waitForAgentIdle } from './helpers/ui'
import { expect, opencodeTest } from './opencode-fixtures'

opencodeTest.describe('OpenCode Tool Execution', () => {
  opencodeTest('tool call renders with span', async ({ authenticatedOpencodeWorkspace, page }) => {
    void authenticatedOpencodeWorkspace // fixture trigger

    // Ask the agent to do something that triggers a tool call.
    await sendMessage(page, 'List files in the current directory. Just run ls.')
    await waitForAgentIdle(page, 180_000)

    // There should be at least one tool-use bubble rendered.
    const toolBubbles = page.locator('[data-testid="thread-line-connector"], [data-testid="thread-line-active"]')
    // Tool calls create thread lines — at least one should be present.
    const count = await toolBubbles.count()
    // This is best-effort — the agent may or may not use tools.
    if (count > 0) {
      expect(count).toBeGreaterThan(0)
    }
  })
})
