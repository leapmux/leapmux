import type { Page } from '@playwright/test'
import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { lastUserText } from '../helpers/mockModelScript'
import { nativeToolResult } from '../helpers/nativeToolResult'
import { askUserQuestionToolCall } from '../helpers/providerToolCalls'
import { savedControlAnswer, sendMessage, waitForAgentIdle } from '../helpers/ui'
import { expect, LETTA_E2E_SKIP_REASON, LETTA_TITLE_RULE, lettaTest } from '../letta-fixtures'

lettaTest.describe('Letta Code control requests', () => {
  lettaTest.skip(!!LETTA_E2E_SKIP_REASON, LETTA_E2E_SKIP_REASON || '')

  const PROVIDER = AgentProvider.LETTA

  function banner(page: Page) {
    return page.getByTestId('control-banner').filter({ visible: true })
  }

  /**
   * The JSON of the response inside the task notification that answers a question.
   * Letta Code escapes `&`, `<` and `>` in the response before it writes the notification.
   */
  function notifiedResponse(notification: string): { type: string, status: string, toolCallId: string, answers: Record<string, string> } {
    const match = /<ask-user-question-response>([\s\S]*)<\/ask-user-question-response>/.exec(notification)
    if (!match?.[1])
      throw new Error(`The user message holds no question response: ${notification}`)
    return JSON.parse(match[1].replaceAll('&lt;', '<').replaceAll('&gt;', '>').replaceAll('&amp;', '&'))
  }

  lettaTest('answers a question through the shared question banner', async ({ askingLettaWorkspace, page, modelScript }) => {
    void askingLettaWorkspace
    await modelScript.rule(LETTA_TITLE_RULE)
    // Letta Code 0.34 posts the questions and returns a receipt at once, so the turn goes on and
    // ends before the reader answers. The answer returns later as a user message that holds a task
    // notification, and that message starts the third model request.
    await modelScript.queue(
      {
        toolCalls: [askUserQuestionToolCall(PROVIDER, 'ask-1', [
          { question: 'Which color do you prefer?', header: 'Color', options: [{ label: 'Blue', description: 'The color blue' }, { label: 'Red', description: 'The color red' }] },
        ])],
      },
      { text: 'The question was posted.' },
      { text: 'The answer was recorded.' },
    )
    await sendMessage(page, modelScript.prompt('Ask me a question.'))
    await modelScript.waitForSteps(2)

    await expect(banner(page)).toContainText('Which color do you prefer?')
    // Each question option contains a radio input inside its label.
    // Select the option through its `question-option-*` test ID.
    await page.locator('[data-testid="question-option-Red"]:visible').click()
    // Submit returns the selected answer.
    // The question control offers Submit and Stop. Permission controls offer Allow and Deny.
    await page.getByTestId('control-submit-btn').filter({ visible: true }).click()

    const status = await modelScript.waitForSteps()
    await waitForAgentIdle(page, 180_000)
    // The call returned a receipt before the reader answered: the model saw the posted questions.
    const receipt = JSON.parse(nativeToolResult(status.requests.find(request => request.stepIndex === 1), 'ask-1'))
    expect(receipt.type).toBe('ask_user_question')
    expect(receipt.toolCallId).toBe('ask-1')
    // The answer reached the model as the notification. It states Red and never Blue as the answer,
    // although the questions it repeats offer both.
    const response = notifiedResponse(lastUserText(status.requests.find(request => request.stepIndex === 2)?.body))
    expect(response.type).toBe('ask_user_question_response')
    expect(response.toolCallId).toBe('ask-1')
    expect(response.status).toBe('answered')
    expect(response.answers).toEqual({ 'Which color do you prefer?': 'Red' })
    await expect(savedControlAnswer(page)).toHaveText('Which color do you prefer?: Red')
    await expect(banner(page)).toHaveCount(0)
  })
})
