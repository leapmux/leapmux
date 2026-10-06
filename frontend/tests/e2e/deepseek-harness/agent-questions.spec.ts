import { expect } from '@playwright/test'
import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { deepseekHarnessTest } from '../deepseek-harness-fixtures'
import { askUserQuestionToolCall } from '../helpers/providerToolCalls'
import { assistantBubbles, controlBanner, sendMessage, waitForAgentIdle } from '../helpers/ui'
import { deepseekHarnessToolResultText } from './nativeToolResultText'

deepseekHarnessTest('returns the exact chosen native answer and keeps the saved answer after reload', async ({ deepseekHarnessWorkspace, page, modelScript }) => {
  void deepseekHarnessWorkspace
  const callId = 'native-route-question'
  await modelScript.queue(
    { toolCalls: [askUserQuestionToolCall(AgentProvider.DEEPSEEK_HARNESS, callId, [{ question: 'Choose a route', header: 'Route', options: [{ label: 'First', description: 'Use the first route.' }, { label: 'Second', description: 'Use the second route.' }] }])] },
    { text: 'The native question completed.' },
  )
  await sendMessage(page, modelScript.prompt('Ask the scripted native route question.'))
  await modelScript.waitForSteps(1)
  const banner = controlBanner(page)
  await expect(banner).toContainText('Choose a route')
  await banner.getByTestId('question-option-Second').click()
  const submit = page.locator('[data-testid="control-submit-btn"]:visible')
  await expect(submit).toBeEnabled()
  await submit.click()
  const status = await modelScript.waitForSteps(2)
  await waitForAgentIdle(page)
  const answer = deepseekHarnessToolResultText(status.requests.find(request => request.stepIndex === 1), callId)
  expect(JSON.parse(answer)).toEqual({ answers: [{ id: 'question-1', selected: ['Second'] }] })
  await expect(banner).toHaveCount(0)
  await expect(assistantBubbles(page).filter({ hasText: 'The native question completed.' })).toBeVisible()
  const saved = page.locator('[data-testid="control-response-text"]:visible')
  await expect(saved).toHaveText('Choose a route: Second')
  await page.reload()
  await expect(saved).toHaveText('Choose a route: Second')
})
