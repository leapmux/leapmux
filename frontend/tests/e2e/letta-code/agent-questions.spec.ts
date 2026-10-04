import type { Page } from '@playwright/test'
import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { nativeToolResult } from '../helpers/nativeToolResult'
import { askUserQuestionToolCall } from '../helpers/providerToolCalls'
import { sendMessage, waitForAgentIdle } from '../helpers/ui'
import { expect, LETTA_E2E_SKIP_REASON, LETTA_TITLE_RULE, lettaTest } from '../letta-fixtures'

lettaTest.describe('Letta Code control requests', () => {
  lettaTest.skip(!!LETTA_E2E_SKIP_REASON, LETTA_E2E_SKIP_REASON || '')

  const PROVIDER = AgentProvider.LETTA

  function banner(page: Page) {
    return page.getByTestId('control-banner').filter({ visible: true })
  }

  lettaTest('answers a question through the shared question banner', async ({ askingLettaWorkspace, page, modelScript }) => {
    void askingLettaWorkspace
    await modelScript.rule(LETTA_TITLE_RULE)
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
    // Each question option contains a radio input inside its label.
    // Select the option through its `question-option-*` test ID.
    await page.locator('[data-testid="question-option-Red"]:visible').click()
    // Submit returns the selected answer.
    // The question control offers Submit and Stop. Permission controls offer Allow and Deny.
    await page.getByTestId('control-submit-btn').filter({ visible: true }).click()

    const status = await modelScript.waitForSteps()
    await waitForAgentIdle(page, 180_000)
    const answer = nativeToolResult(status.requests.find(request => request.stepIndex === 1), 'ask-1')
    expect(answer).toContain('Red')
    expect(answer).not.toContain('Blue')
    await expect(banner(page)).toHaveCount(0)
  })
})
