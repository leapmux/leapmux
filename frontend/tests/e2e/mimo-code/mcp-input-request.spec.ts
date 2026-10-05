import { expect } from '@playwright/test'
import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { nativeToolResult } from '../helpers/nativeToolResult'
import { mcpToolCall } from '../helpers/providerToolCalls'
import { messageBubbles, sendMessage, waitForAgentIdle } from '../helpers/ui'
import { MIMO_E2E_SKIP_REASON, mimoTest } from '../mimo-fixtures'

mimoTest.skip(!!MIMO_E2E_SKIP_REASON, MIMO_E2E_SKIP_REASON || '')

mimoTest.describe('MiMo Code MCP confirmation', () => {
  // MiMo Code 0.1.15 declares no elicitation capability (its MCP client states
  // only `sampling` and an experimental turn-lifecycle capability) and registers
  // no elicitation handler. Its MCP SDK therefore answers the server's
  // `elicitation/create` with JSON-RPC error -32601 "Method not found". It never
  // sends a decline, so the probe server reports a refusal, not
  // MCP_CONFIRM_DECLINED.
  mimoTest('shows no form when the native client refuses MCP confirmation', async ({ authenticatedMiMoWorkspace, page, modelScript }) => {
    void authenticatedMiMoWorkspace
    await modelScript.queue(
      { toolCalls: [mcpToolCall(AgentProvider.MIMO_CODE, 'mimo-confirm', { server: 'form_probe', tool: 'ask', input: {} })] },
      { text: 'The confirmation was refused.' },
    )
    await sendMessage(page, modelScript.prompt('Call form_probe ask once.'))
    await modelScript.waitForSteps()
    await waitForAgentIdle(page, 120_000)
    const second = (await modelScript.status()).requests.find(request => request.stepIndex === 1)
    expect(second?.protocol).toBeTruthy()
    expect(nativeToolResult(second, 'mimo-confirm')).toContain('FORM_ROUND_TRIP_REFUSED: -32601 Method not found')
    await expect(page.getByTestId('elicitation-form').filter({ visible: true })).toHaveCount(0)
    await expect(page.getByTestId('control-banner').filter({ visible: true })).toHaveCount(0)
    await expect(messageBubbles(page).filter({ hasText: 'The confirmation was refused.' }).first()).toBeVisible()
  })
})
