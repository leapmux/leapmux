import { expect } from '@playwright/test'
import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { mcpToolCall } from '../helpers/providerToolCalls'
import { messageBubbles, sendMessage, waitForAgentIdle } from '../helpers/ui'
import { MIMO_E2E_SKIP_REASON, mimoTest } from '../mimo-fixtures'

mimoTest.skip(!!MIMO_E2E_SKIP_REASON, MIMO_E2E_SKIP_REASON || '')

mimoTest.describe('MiMo Code MCP confirmation', () => {
  mimoTest('shows no form when the native client declines MCP confirmation', async ({ authenticatedMiMoWorkspace, page, modelScript }) => {
    void authenticatedMiMoWorkspace
    await modelScript.queue(
      { toolCalls: [mcpToolCall(AgentProvider.MIMO_CODE, 'mimo-confirm', { server: 'form_probe', tool: 'ask', input: {} })] },
      { text: 'The confirmation was declined.' },
    )
    await sendMessage(page, modelScript.prompt('Call form_probe ask once.'))
    await modelScript.waitForSteps()
    await waitForAgentIdle(page, 120_000)
    const second = (await modelScript.status()).requests.find(request => request.stepIndex === 1)
    expect(second?.protocol).toBeTruthy()
    expect(JSON.stringify(second?.body).includes('MCP_CONFIRM_DECLINED')).toBe(true)
    await expect(page.getByTestId('elicitation-form').filter({ visible: true })).toHaveCount(0)
    await expect(page.getByTestId('control-banner').filter({ visible: true })).toHaveCount(0)
    await expect(messageBubbles(page).filter({ hasText: 'The confirmation was declined.' }).first()).toBeVisible()
  })
})
