import type { Locator } from '@playwright/test'
import type { ManagedNativeScenarioContext } from '../helpers/nativeScenario'
import type { QuestionRequest } from '../helpers/providerToolCalls'
import { expect } from '@playwright/test'
import { markdownToPlainText } from '../../../src/lib/markdownPlainText'
import { lastUserText } from '../helpers/mockModelScript'
import { chooseQuestionPages } from '../helpers/nativeQuestion'
import { exerciseQuestionIsolation } from '../helpers/nativeQuestionIsolation'
import { restoreQuestionSelection } from '../helpers/nativeQuestionRestoration'
import { nativeToolResult } from '../helpers/nativeToolResult'
import { nativeXmlField } from '../helpers/nativeXml'
import { askUserQuestionToolCall } from '../helpers/providerToolCalls'
import { controlButton, expectNoControlBanner, savedControlAnswer, sendMessage, waitForAgentIdle, waitForControlBanner } from '../helpers/ui'
import { lettaTest } from '../letta-fixtures'
import { nativeContext } from './scenarios'

lettaTest.describe('control requests for Letta Code', () => {
  /**
   * The JSON of the response inside the task notification that answers a question.
   * Letta Code escapes `&`, `<` and `>` in the response before it writes the notification.
   */
  function notifiedResponse(notification: string): { type: string, status: string, toolCallId: string, answers: Record<string, string> } {
    const response = nativeXmlField(notification, 'ask-user-question-response')
    if (!response)
      throw new Error(`The user message holds no question response: ${notification}`)
    return JSON.parse(response)
  }

  async function answerLettaQuestion(
    context: ManagedNativeScenarioContext,
    questions: QuestionRequest[],
    reply: (banner: Locator) => Promise<void>,
  ) {
    const { page, modelScript } = context
    const callId = 'letta-question'
    const start = await modelScript.queue(
      { toolCalls: [askUserQuestionToolCall(context.provider, callId, questions)] },
      { text: 'The question was posted.' },
      { text: 'The answer was recorded.' },
    )
    await sendMessage(page, modelScript.prompt('Ask the native questions.'))
    await modelScript.waitForSteps(start + 2)
    await reply(await waitForControlBanner(page))
    await modelScript.waitForSteps(start + 3)
    await waitForAgentIdle(page)
    const receipt = JSON.parse(nativeToolResult(await modelScript.requestAt(start + 1), callId))
    expect(receipt.type).toBe('ask_user_question')
    expect(receipt.toolCallId).toBe(callId)
    const notified = await modelScript.requestAt(start + 2)
    const response = notifiedResponse(lastUserText(notified.protocol, notified.body))
    expect(response.type).toBe('ask_user_question_response')
    expect(response.toolCallId).toBe(callId)
    await expectNoControlBanner(page)
    return response
  }

  // The shared question turn reads the answer from the request right after the question. Letta Code answers in a
  // later request, so this turn stays here.
  lettaTest('answers a question through the shared question banner', async ({ askingLettaWorkspace, page, modelScript, leapmuxServer }) => {
    const context = await nativeContext({ page, modelScript, leapmuxServer, workspaceId: askingLettaWorkspace.workspaceId })
    const response = await answerLettaQuestion(context, [
      { question: 'Which color do you prefer?', header: 'Color', options: [{ label: 'Blue', description: 'The color blue' }, { label: 'Red', description: 'The color red' }] },
    ], async (banner) => {
      await expect(banner).toContainText('Which color do you prefer?')
      // Each question option contains a radio input inside its label.
      // Select the option through its `question-option-*` test ID.
      await banner.getByTestId('question-option-Red').click()
      await restoreQuestionSelection(context, banner, 'Red')
      // Submit returns the selected answer.
      // The question control offers Submit and Stop. Permission controls offer Allow and Deny.
      await controlButton(page, 'submit').click()
    })
    expect(response.status).toBe('answered')
    expect(response.answers).toEqual({ 'Which color do you prefer?': 'Red' })
    await expect(savedControlAnswer(page)).toHaveText('Which color do you prefer?: Red')
    await expectNoControlBanner(page)
  })

  for (const typed of [false, true]) {
    lettaTest(typed ? 'returns a restored custom answer in its native notification' : 'returns every restored question page in its native notification', async ({ askingLettaWorkspace, page, modelScript, leapmuxServer }) => {
      const context = await nativeContext({ page, modelScript, leapmuxServer, workspaceId: askingLettaWorkspace.workspaceId })
      const questions: QuestionRequest[] = [
        { question: 'Choose a color.', header: 'Color', options: [{ label: 'Blue', description: 'Use blue.' }, { label: 'Green', description: 'Use green.' }] },
        ...(typed ? [] : [{ question: 'Choose sizes.', header: 'Size', multiSelect: true, options: [{ label: 'Small', description: 'Use small.' }, { label: 'Medium', description: 'Use medium.' }, { label: 'Large', description: 'Use large.' }] }]),
      ]
      const response = await answerLettaQuestion(context, questions, chooseQuestionPages(context, questions, typed ? [{ text: 'Custom color 0 界 & <value>' }] : [{ labels: ['Green'] }, { labels: ['Large', 'Small'] }]))
      expect(response.status).toBe('answered')
      if (typed) {
        expect(Object.keys(response.answers)).toEqual(['Choose a color.'])
        expect(markdownToPlainText(response.answers['Choose a color.']!)).toBe('Custom color 0 界 & <value>')
      }
      else {
        expect(response.answers['Choose a color.']).toBe('Green')
        expect(response.answers['Choose sizes.']).toContain('Large')
        expect(response.answers['Choose sizes.']).toContain('Small')
        expect(response.answers['Choose sizes.']).not.toContain('Medium')
      }
    })
  }
})

lettaTest('keeps a native question on its owner tab and workspace after reload', async ({ page, modelScript, leapmuxServer, askingLettaWorkspace }) => {
  const context = await nativeContext({ page, modelScript, leapmuxServer, workspaceId: askingLettaWorkspace.workspaceId })
  await exerciseQuestionIsolation(context, { customAnswerDraft: true, dismiss: () => controlButton(page, 'stop').click() })
})
