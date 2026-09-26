import { AgentProvider } from '../../src/generated/proto/leapmux/v1/agent_pb'
import { CODEBUDDY_E2E_SKIP_REASON, codebuddyTest, expect } from './codebuddy-fixtures'
import { bashToolCall } from './helpers/providerToolCalls'
import {
  assistantBubbles,
  sendMessage,
  waitForAgentIdle,
} from './helpers/ui'

/**
 * 249 — CodeBuddy Code tool execution.
 *
 * One scripted turn runs a Bash tool call. The tool call draws as a span with a
 * request row and a result row, and the answer closes the turn.
 */
codebuddyTest.skip(!!CODEBUDDY_E2E_SKIP_REASON, CODEBUDDY_E2E_SKIP_REASON || '')

codebuddyTest.describe('CodeBuddy Code tool execution', () => {
  codebuddyTest('runs a Bash tool and draws its span', async ({ codebuddyWorkspace, page, modelScript }) => {
    void codebuddyWorkspace
    const call = bashToolCall(AgentProvider.CODEBUDDY, 'call-1', 'echo hi')
    await modelScript.queue({
      toolCalls: [call],
    })
    await modelScript.queue({ text: 'The command ran.' })
    await sendMessage(page, modelScript.prompt('Run echo hi.'))
    await modelScript.waitForSteps()
    await waitForAgentIdle(page, 180_000)

    await expect(assistantBubbles(page).filter({ hasText: 'The command ran.' }).first()).toBeVisible()
    await expect(page.getByTestId('thinking-indicator')).not.toBeVisible()
  })
})
