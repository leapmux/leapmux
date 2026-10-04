import type { Page } from '@playwright/test'
import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { DROID_E2E_SKIP_REASON, DROID_TITLE_RULE, droidTest, expect } from '../droid-fixtures'
import { nativeToolResult } from '../helpers/nativeToolResult'
import { askUserQuestionToolCall } from '../helpers/providerToolCalls'
import { sendMessage, waitForAgentIdle } from '../helpers/ui'
import { nativeDroidCallId } from './toolResult'

droidTest.describe('Factory Droid control requests', () => {
  droidTest.skip(!!DROID_E2E_SKIP_REASON, DROID_E2E_SKIP_REASON || '')

  function banner(page: Page) {
    return page.getByTestId('control-banner').filter({ visible: true })
  }

  const PROVIDER = AgentProvider.DROID

  droidTest('answers a question through the shared question banner', async ({ askingDroidWorkspace, page, modelScript }) => {
    void askingDroidWorkspace
    await modelScript.rule(DROID_TITLE_RULE)
    await modelScript.queue(
      {
        toolCalls: [askUserQuestionToolCall(PROVIDER, 'ask-1', [
          { question: 'Which color do you prefer?', header: 'Color', options: [{ label: 'Blue', description: 'The color blue' }, { label: 'Red', description: 'The color red' }] },
        ])],
      },
      { text: 'The answer was recorded.' },
    )
    await sendMessage(page, modelScript.prompt('Ask me a question.'))
    await modelScript.waitForSteps(1)

    await expect(banner(page)).toContainText('Which color do you prefer?')
    await page.getByTestId('question-option-Red').filter({ visible: true }).first().click()
    await page.getByTestId('control-submit-btn').filter({ visible: true }).click()

    const status = await modelScript.waitForSteps()
    await waitForAgentIdle(page, 180_000)
    const followUp = status.requests.find(request => request.stepIndex === 1)
    const nativeCallId = nativeDroidCallId(followUp, 'AskUser', 'ask-1')
    const answer = nativeToolResult(followUp, nativeCallId)
    expect(answer).toContain('Red')
    expect(answer).not.toContain('Blue')
    await expect(banner(page)).toHaveCount(0)
  })
})
