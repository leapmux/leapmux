import type { MockModelRequestRecord } from '../helpers/mockModelScript'
import type { NativeScenarioContext } from '../helpers/nativeScenario'
import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { mcpToolCall } from '../helpers/providerToolCalls'
import { sendMessage, visibleOnly, waitForAgentIdle } from '../helpers/ui'
import { expect } from '../qoder-fixtures'

/** Exercise the actual native control and retain every original assertion. */
export async function exerciseNativeMcpForm(context: NativeScenarioContext): Promise<MockModelRequestRecord> {
  const { page, modelScript } = context

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
  await waitForAgentIdle(page)
  await expect(visibleOnly(page.getByText('FORM_ROUND_TRIP_OK', { exact: false })).first()).toBeVisible()
  await expect(form).toHaveCount(0)

  const next = (await modelScript.status()).requests.find(request => request.stepIndex === 1)
  if (!next)
    throw new Error('The related native scenario reached no model request.')
  return next
}
