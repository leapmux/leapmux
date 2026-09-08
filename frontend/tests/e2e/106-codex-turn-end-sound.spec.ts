import { codexTest, expect } from './codex-fixtures'
import { armTurnEndSound, expectDoorbellCount, expectDoorbellQuiet } from './helpers/turnEndSound'
import { sendMessage, waitForAgentIdle } from './helpers/ui'

codexTest.describe('Codex Turn End Sound', () => {
  codexTest('should play ding-dong sound when Codex turn ends with tool use', async ({ page, authenticatedCodexWorkspace, leapmuxServer }) => {
    void authenticatedCodexWorkspace // fixture trigger

    await armTurnEndSound(page, leapmuxServer.adminUserId, 'ding-dong')
    await expect(page.locator('[data-testid="composer-editor"] .ProseMirror')).toBeVisible()

    // Send a message that triggers tool use (command execution) so num_tool_uses > 0
    await sendMessage(page, 'Run the command `pwd` and tell me the result.')
    await waitForAgentIdle(page, 120_000)

    await expectDoorbellCount(page, 1)
  })

  codexTest('should NOT play sound for simple Codex text exchange', async ({ page, authenticatedCodexWorkspace, leapmuxServer }) => {
    void authenticatedCodexWorkspace // fixture trigger

    await armTurnEndSound(page, leapmuxServer.adminUserId, 'ding-dong')
    await expect(page.locator('[data-testid="composer-editor"] .ProseMirror')).toBeVisible()

    // Send a simple question that completes without tool use (num_tool_uses = 0)
    await sendMessage(page, 'What is 1234 + 5678? Reply with just the number, nothing else.')
    await waitForAgentIdle(page, 120_000)

    // expectDoorbellQuiet, not a fixed sleep. The Worker holds a settle for
    // settleDelay, so a 500 ms window closed before an unwanted ding could
    // physically arrive and the assertion proved nothing. waitForAgentIdle
    // cannot cover the gap either: it swallows its own timeout by design.
    await expectDoorbellQuiet(page, 0)
  })
})
