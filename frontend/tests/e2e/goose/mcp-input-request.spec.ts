import { expect } from '@playwright/test'
import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { GOOSE_E2E_SKIP_REASON, gooseTest } from '../goose-fixtures'
import { fillMcpProbeForm } from '../helpers/mcpProbeForm'
import { mcpToolCall } from '../helpers/providerToolCalls'
import { messageBubbles, sendMessage, waitForAgentIdle } from '../helpers/ui'

gooseTest.skip(!!GOOSE_E2E_SKIP_REASON, GOOSE_E2E_SKIP_REASON || '')

gooseTest('roundtrips zero, false, and blue through native form elicitation', async ({ authenticatedGooseWorkspace, page, modelScript }) => {
  void authenticatedGooseWorkspace
  await modelScript.queue(
    { toolCalls: [mcpToolCall(AgentProvider.GOOSE, 'goose-form', { server: 'form_probe', tool: 'ask', input: {} })] },
    { text: 'The Goose form completed.' },
  )
  await sendMessage(page, modelScript.prompt('Call the form_probe ask tool exactly once.'))
  await modelScript.waitForSteps(1)
  const permission = page.getByTestId('control-banner').filter({ visible: true })
  await expect(permission).toContainText('form probe: ask')
  await page.getByTestId('control-actions').getByRole('button', { name: 'Allow', exact: true }).click()

  const form = await fillMcpProbeForm(page)
  await page.reload()
  await expect(form.getByLabel('Count *')).toHaveValue('0')
  await expect(form.getByRole('button', { name: 'Enabled *', exact: true })).toHaveText('No')
  await expect(form.getByRole('button', { name: 'Color *', exact: true })).toHaveText('Blue')
  await page.getByTestId('control-actions').getByRole('button', { name: 'Approve', exact: true }).click()
  const status = await modelScript.waitForSteps(2)
  await waitForAgentIdle(page)
  expect(JSON.stringify(status.requests.find(request => request.stepIndex === 1)?.body)).toContain('FORM_ROUND_TRIP_OK')
  await expect(messageBubbles(page).filter({ hasText: 'FORM_ROUND_TRIP_OK' }).first()).toBeVisible()
  await expect(form).toHaveCount(0)
})
