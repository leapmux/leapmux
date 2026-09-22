import { AgentProvider } from '../../src/generated/proto/leapmux/v1/agent_pb'
import { codexTest, expect } from './codex-fixtures'
import { bashToolCall } from './helpers/providerToolCalls'
import { armTurnEndSound, expectDoorbellCount, expectDoorbellQuiet } from './helpers/turnEndSound'
import { ARITHMETIC_ANSWER_TEXT, ARITHMETIC_PROMPT, sendMessage, waitForAgentIdle } from './helpers/ui'

codexTest.describe('Codex Turn End Sound', () => {
  codexTest('should play ding-dong sound when Codex turn ends with tool use', async ({ page, authenticatedCodexWorkspace, leapmuxServer, modelScript }) => {
    void authenticatedCodexWorkspace // fixture trigger

    await armTurnEndSound(page, leapmuxServer.adminUserId, 'ding-dong')
    await expect(page.locator('[data-testid="composer-editor"] .ProseMirror')).toBeVisible()

    // Send a message that triggers tool use (command execution) so num_tool_uses > 0
    await modelScript.queue(
      { toolCalls: [bashToolCall(AgentProvider.CODEX, 'pwd-call', 'pwd')] },
      { text: 'The working directory is above.' },
    )
    await sendMessage(page, modelScript.prompt('Run the command `pwd` and tell me the result.'))
    await modelScript.waitForSteps()
    await waitForAgentIdle(page, 120_000)

    await expectDoorbellCount(page, 1)
  })

  codexTest('should NOT play sound for simple Codex text exchange', async ({ page, authenticatedCodexWorkspace, leapmuxServer, modelScript }) => {
    void authenticatedCodexWorkspace // fixture trigger

    await armTurnEndSound(page, leapmuxServer.adminUserId, 'ding-dong')
    await expect(page.locator('[data-testid="composer-editor"] .ProseMirror')).toBeVisible()

    // Send a simple question that completes without tool use (num_tool_uses = 0)
    await modelScript.queue({ text: ARITHMETIC_ANSWER_TEXT })
    await sendMessage(page, modelScript.prompt(ARITHMETIC_PROMPT))
    await modelScript.waitForSteps()
    await waitForAgentIdle(page, 120_000)

    // expectDoorbellQuiet, not a fixed sleep. The Worker holds a settle for
    // settleDelay, so a 500 ms window closed before an unwanted ding could
    // physically arrive and the assertion proved nothing. waitForAgentIdle
    // cannot cover the gap either: it swallows its own timeout by design.
    await expectDoorbellQuiet(page, 0)
  })
})
