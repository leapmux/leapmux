import { expect } from '@playwright/test'
import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { codexTest } from '../codex-fixtures'
import { currentNativeAgent } from '../helpers/nativeScenario'
import { bashToolCall } from '../helpers/providerToolCalls'
import { armTurnEndSound, expectDoorbellCount, expectDoorbellQuiet, soundReceiptCursor } from '../helpers/turnEndSound'
import { ARITHMETIC_ANSWER_TEXT, ARITHMETIC_PROMPT, sendMessage, waitForAgentIdle } from '../helpers/ui'

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
    await waitForAgentIdle(page)

    await expectDoorbellCount(page, 1)
  })

  codexTest('should NOT play sound for simple Codex text exchange', async ({ page, authenticatedCodexWorkspace, leapmuxServer, modelScript }) => {
    void authenticatedCodexWorkspace // fixture trigger

    await armTurnEndSound(page, leapmuxServer.adminUserId, 'ding-dong')
    await expect(page.locator('[data-testid="composer-editor"] .ProseMirror')).toBeVisible()

    const agent = await currentNativeAgent({ page, leapmuxServer })
    const after = await soundReceiptCursor(page)
    // The native turn completes without tool activity.
    await modelScript.queue({ text: ARITHMETIC_ANSWER_TEXT })
    await sendMessage(page, modelScript.prompt(ARITHMETIC_PROMPT))
    await modelScript.waitForSteps()
    await waitForAgentIdle(page)

    await expectDoorbellQuiet(page, 0, { agentId: agent.id, after })
  })
})
