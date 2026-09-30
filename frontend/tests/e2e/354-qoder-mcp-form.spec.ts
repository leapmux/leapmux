import { AgentProvider } from '../../src/generated/proto/leapmux/v1/agent_pb'
import { mcpToolCall } from './helpers/providerToolCalls'
import { sendMessage, visibleOnly, waitForAgentIdle } from './helpers/ui'
import { expect, QODER_E2E_SKIP_REASON, qoderTest } from './qoder-fixtures'

qoderTest.skip(!!QODER_E2E_SKIP_REASON, QODER_E2E_SKIP_REASON || '')

qoderTest.describe('Qoder CLI MCP input form', () => {
  qoderTest('returns zero and false values to the native MCP tool', async ({ askingQoderWorkspace, page, modelScript }) => {
    void askingQoderWorkspace
    await modelScript.queue(
      { toolCalls: [mcpToolCall(AgentProvider.QODER, 'qoder-mcp-form', { server: 'form_probe', tool: 'ask', input: {} })] },
      { text: 'The form completed.' },
    )
    await sendMessage(page, modelScript.prompt('Call form_probe ask exactly once.'))
    await modelScript.waitForSteps(1)

    const permission = page.locator('[data-testid="control-banner"]:visible')
    await expect(permission).toContainText('mcp__form_probe__ask')
    await page.getByTestId('control-allow-btn').filter({ visible: true }).click()

    const form = page.locator('[data-testid="elicitation-form"]:visible')
    await expect(form).toBeVisible()
    await form.getByLabel('Count *').fill('0')
    await form.getByRole('button', { name: 'Enabled *', exact: true }).click()
    await page.getByRole('menuitemradio', { name: 'No', exact: true }).click()
    await form.getByRole('button', { name: 'Color *', exact: true }).click()
    await page.getByRole('menuitemradio', { name: 'Blue', exact: true }).click()
    await page.getByTestId('control-actions').getByRole('button', { name: 'Approve', exact: true }).click()

    await modelScript.waitForSteps()
    await waitForAgentIdle(page, 180_000)
    await expect(visibleOnly(page.getByText('FORM_ROUND_TRIP_OK', { exact: false })).first()).toBeVisible()
    await expect(form).toHaveCount(0)
  })
})
