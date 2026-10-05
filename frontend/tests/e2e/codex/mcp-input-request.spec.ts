import { expect } from '@playwright/test'
import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { codexTest } from '../codex-fixtures'
import { mcpToolCall } from '../helpers/providerToolCalls'
import { messageBubbles, sendMessage, waitForAgentIdle } from '../helpers/ui'

codexTest.describe('Codex MCP input form', () => {
  codexTest('sends zero and false form values to the native MCP server', async ({ authenticatedCodexWorkspace, page, modelScript }) => {
    void authenticatedCodexWorkspace
    await modelScript.queue(
      { toolCalls: [mcpToolCall(AgentProvider.CODEX, 'codex-mcp-form', { server: 'form_probe', tool: 'ask', input: {} })] },
      { text: 'The form completed.' },
    )
    await sendMessage(page, modelScript.prompt('Call form_probe ask exactly once.'))
    await modelScript.waitForSteps(1)

    const permission = page.locator('[data-testid="control-banner"]:visible')
    await expect(permission).toContainText('Allow the form_probe MCP server to run tool "ask"?')
    await page.getByTestId('control-allow-btn').filter({ visible: true }).click()

    const form = page.getByTestId('elicitation-form').filter({ visible: true })
    await expect(form).toBeVisible()
    await form.getByLabel('Count *').fill('0')
    await form.getByRole('button', { name: 'Enabled *', exact: true }).click()
    await page.getByRole('menuitemradio', { name: 'No', exact: true }).click()
    await form.getByRole('button', { name: 'Color *', exact: true }).click()
    await page.getByRole('menuitemradio', { name: 'Blue', exact: true }).click()
    await page.getByTestId('control-actions').getByRole('button', { name: 'Approve', exact: true }).click()

    const status = await modelScript.waitForSteps(2)
    await waitForAgentIdle(page)
    expect(JSON.stringify(status.requests.find(request => request.stepIndex === 1)?.body)).toContain('FORM_ROUND_TRIP_OK')
    await expect(messageBubbles(page).filter({ hasText: 'FORM_ROUND_TRIP_OK' }).first()).toBeVisible()
    await expect(form).toHaveCount(0)
  })
})
