import { sendMessage, waitForAgentIdle } from './helpers/ui'
import { expect, OPENCODE_E2E_SKIP_REASON, opencodeTest } from './opencode-fixtures'

opencodeTest.skip(!!OPENCODE_E2E_SKIP_REASON, OPENCODE_E2E_SKIP_REASON || '')

opencodeTest.describe('OpenCode Tool Execution', () => {
  opencodeTest('tool call renders with span', async ({ authenticatedOpencodeWorkspace, page }) => {
    void authenticatedOpencodeWorkspace // fixture trigger

    // Force the agent to use a tool — listing files requires running `ls`,
    // which the agent must dispatch as a tool call. A response that doesn't
    // use a tool is a regression in this scenario.
    await sendMessage(page, 'Use your shell tool to run `ls` in the current directory and report the output. You must call the tool — do not describe what ls would do.')
    await waitForAgentIdle(page, 180_000)

    // A successful tool dispatch renders at least one thread-line. If the
    // agent answers without ever invoking a tool, that is the regression
    // this test was added to catch.
    const toolBubbles = page.locator('[data-testid="thread-line-connector"], [data-testid="thread-line-active"]')
    await expect(toolBubbles.first()).toBeVisible()
  })
})
