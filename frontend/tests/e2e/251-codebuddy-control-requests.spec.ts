import { AgentProvider } from '../../src/generated/proto/leapmux/v1/agent_pb'
import { CODEBUDDY_E2E_SKIP_REASON, codebuddyTest, expect } from './codebuddy-fixtures'
import { bashToolCall } from './helpers/providerToolCalls'
import {
  sendMessage,
  waitForAgentIdle,
} from './helpers/ui'

/**
 * 251 — CodeBuddy Code control requests.
 *
 * The agent opens in Default, which raises a banner for each tool call. The
 * spec scripts one Bash call, answers the banner with Allow, and proves the call
 * reaches the transcript. The wire answer the worker sends is CodeBuddy's own
 * `{"allowed":true}`, not Claude's `{"behavior":"allow"}`.
 */
codebuddyTest.skip(!!CODEBUDDY_E2E_SKIP_REASON, CODEBUDDY_E2E_SKIP_REASON || '')

codebuddyTest.describe('CodeBuddy Code control requests', () => {
  codebuddyTest('raises a banner for a tool call and runs it once allowed', async ({ askingCodebuddyWorkspace, page, modelScript }) => {
    void askingCodebuddyWorkspace
    const call = bashToolCall(AgentProvider.CODEBUDDY, 'call-1', 'echo hi')
    await modelScript.queue({ toolCalls: [call] })
    await modelScript.queue({ text: 'The command ran.' })
    await sendMessage(page, modelScript.prompt('Run echo hi.'))

    // Wait for the model to answer with the tool call BEFORE asserting the
    // banner: an agent process takes tens of seconds to start, and the banner
    // assertion's own 30s timeout would otherwise expire before the turn runs.
    await modelScript.waitForSteps(1)
    const banner = page.getByTestId('control-banner').filter({ visible: true })
    await expect(banner).toContainText('echo hi')
    await page.getByTestId('control-allow-btn').filter({ visible: true }).click()
    await modelScript.waitForSteps()
    await waitForAgentIdle(page, 180_000)
    await expect(banner).toHaveCount(0)
  })
})
