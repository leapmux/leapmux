import { expect } from '@playwright/test'
import { lastUserText } from '../helpers/mockModelScript'
import { nativeToolResult } from '../helpers/nativeToolResult'
import { askUserQuestionToolCall } from '../helpers/providerToolCalls'
import { controlButton, expectNoControlBanner, savedControlAnswer, sendMessage, waitForAgentIdle, waitForControlBanner } from '../helpers/ui'
import { lettaTest } from '../letta-fixtures'
import { nativeContext } from './scenarios'

lettaTest.describe('Letta Code control requests', () => {
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

  // The shared question turn reads the answer from the request right after the question. Letta Code answers in a
  // later request, so this turn stays here.
  lettaTest('answers a question through the shared question banner', async ({ askingLettaWorkspace, page, modelScript, leapmuxServer }) => {
    const context = await nativeContext({ page, modelScript, leapmuxServer, workspaceId: askingLettaWorkspace.workspaceId })
    // Letta Code 0.34 posts the questions and returns a receipt at once, so the turn goes on and
    // ends before the reader answers. The answer returns later as a user message that holds a task
    // notification, and that message starts the third model request.
    const start = await modelScript.queue(
      {
        toolCalls: [askUserQuestionToolCall(context.provider, 'ask-1', [
          { question: 'Which color do you prefer?', header: 'Color', options: [{ label: 'Blue', description: 'The color blue' }, { label: 'Red', description: 'The color red' }] },
        ])],
      },
      { text: 'The question was posted.' },
      { text: 'The answer was recorded.' },
    )
    await sendMessage(page, modelScript.prompt('Ask me a question.'))
    await modelScript.waitForSteps(start + 2)

    const banner = await waitForControlBanner(page)
    await expect(banner).toContainText('Which color do you prefer?')
    // Each question option contains a radio input inside its label.
    // Select the option through its `question-option-*` test ID.
    await banner.getByTestId('question-option-Red').click()
    // Submit returns the selected answer.
    // The question control offers Submit and Stop. Permission controls offer Allow and Deny.
    await controlButton(page, 'submit').click()

    await modelScript.waitForSteps(start + 3)
    await waitForAgentIdle(page)
    // The call returned a receipt before the reader answered: the model saw the posted questions.
    const receipt = JSON.parse(nativeToolResult(await modelScript.requestAt(start + 1), 'ask-1'))
    expect(receipt.type).toBe('ask_user_question')
    expect(receipt.toolCallId).toBe('ask-1')
    // The answer reached the model as the notification. It states Red and never Blue as the answer,
    // although the questions it repeats offer both.
    const response = notifiedResponse(lastUserText((await modelScript.requestAt(start + 2)).body))
    expect(response.type).toBe('ask_user_question_response')
    expect(response.toolCallId).toBe('ask-1')
    expect(response.status).toBe('answered')
    expect(response.answers).toEqual({ 'Which color do you prefer?': 'Red' })
    await expect(savedControlAnswer(page)).toHaveText('Which color do you prefer?: Red')
    await expectNoControlBanner(page)
  })
})
