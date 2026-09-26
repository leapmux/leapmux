import { AgentProvider } from '../../src/generated/proto/leapmux/v1/agent_pb'
import { bashToolCall } from './helpers/providerToolCalls'
import {
  assistantBubbles,
  sendMessage,
  waitForAgentIdle,
} from './helpers/ui'
import { expect, QODER_E2E_SKIP_REASON, qoderTest } from './qoder-fixtures'

/**
 * 250 — Qoder CLI tool execution.
 *
 * One scripted turn runs a Bash tool call. The tool call draws as a span with a
 * request row and a result row, and the answer closes the turn.
 */
qoderTest.skip(!!QODER_E2E_SKIP_REASON, QODER_E2E_SKIP_REASON || '')

qoderTest.describe('Qoder CLI tool execution', () => {
  qoderTest('runs a Bash tool and draws its span', async ({ qoderWorkspace, page, modelScript }) => {
    void qoderWorkspace
    const call = bashToolCall(AgentProvider.QODER, 'call-1', 'echo hi')
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
