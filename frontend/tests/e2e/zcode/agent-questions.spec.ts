import { expect } from '@playwright/test'
import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { nativeToolResult } from '../helpers/nativeToolResult'
import { askUserQuestionToolCall } from '../helpers/providerToolCalls'
import { createTestDirectory } from '../helpers/runDirectory'
import { openWorkspace, sendMessage, waitForAgentIdle } from '../helpers/ui'
import { openProviderAgent } from '../helpers/workspace'
import { ZCODE_AGENT, zcodeTest } from '../zcode-fixtures'

zcodeTest('renders ZCode native question descriptions and selects an answer', async ({ page, authenticatedEmptyWorkspace, leapmuxServer, modelScript }) => {
  await page.setViewportSize({ width: 600, height: 900 })
  const provider = AgentProvider.ZCODE
  const { agentId } = await openProviderAgent(leapmuxServer, authenticatedEmptyWorkspace.workspaceId, ZCODE_AGENT, { workingDir: createTestDirectory('renderer-zcode-question-') })
  const diagram = '┌────────┐\n│ sample │\n└────────┘'
  // No `value` on an option: ZCode's own schema refuses it with
  // `InputValidationError: An unexpected parameter \`value\` was provided`,
  // which the agent reports as a failed tool call and then talks past.
  const questions = [{ question: 'Pick a color.', header: 'Color', options: [{ label: 'Blue', description: 'Choose the color blue.', preview: diagram }, { label: 'Green', description: 'Choose the color green.', preview: '```ts\nconst color = "green"\n```' }] }]
  void agentId
  // The agent's OWN question extension raises this control request, from a
  // scripted tool call. Seeding the request row instead skipped the extension
  // entirely, so nothing proved that ZCode's own payload reaches this surface.
  await modelScript.queue(
    { toolCalls: [askUserQuestionToolCall(provider, 'color-question', questions)] },
    { text: 'The choice was recorded.' },
  )
  await page.reload()
  await openWorkspace(page, authenticatedEmptyWorkspace.workspaceId)
  await sendMessage(page, modelScript.prompt('Ask me to pick a color.'))
  await modelScript.waitForSteps(1)
  const banner = page.getByTestId('control-banner').filter({ visible: true })
  await expect(banner.getByText('Choose the color blue.', { exact: true })).toBeVisible()
  await expect(banner.getByText('Choose the color green.', { exact: true })).toBeVisible()
  const preview = banner.getByRole('region', { name: 'Blue preview' })
  await expect(preview).toContainText('│ sample │')
  await expect(preview).toHaveCSS('white-space', 'pre')
  await expect(banner.getByRole('region', { name: 'Green preview' }).locator('pre code')).toContainText('const color = "green"')
  await expect(banner.getByRole('region', { name: 'Green preview' })).toBeInViewport()
  expect(await banner.evaluate(element => element.scrollWidth <= element.clientWidth)).toBe(true)
  const option = banner.getByTestId('question-option-Green')
  await option.click()
  await expect(option.getByRole('radio')).toBeChecked()
  await expect(page.getByTestId('control-submit-btn')).toBeEnabled()
  await page.getByTestId('control-submit-btn').click()
  const status = await modelScript.waitForSteps(2)
  await waitForAgentIdle(page)
  const answerRequest = status.requests.find(request => request.stepIndex === 1)
  const answer = nativeToolResult(answerRequest, 'color-question')
  expect(answer).toContain('Green')
  expect(answer).not.toContain('Blue')
})
