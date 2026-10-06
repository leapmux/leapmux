import type { MockModelRequestRecord } from '../helpers/mockModelScript'
import { droidTest, expect } from '../droid-fixtures'
import { chooseQuestionOption, exerciseQuestionAnswer } from '../helpers/nativeQuestion'
import { nativeToolResult } from '../helpers/nativeToolResult'
import { controlButton, questionPagination, savedControlAnswer } from '../helpers/ui'
import { nativeContext } from './scenarios'
import { nativeDroidCallId } from './toolResult'

/**
 * Read the result of Droid's AskUser call.
 * Droid gives the call its own ID, and the tool result reader of the context reads an Execute call alone.
 */
function askUserResult(request: MockModelRequestRecord, callId: string): string {
  return nativeToolResult(request, nativeDroidCallId(request, 'AskUser', callId))
}

droidTest.describe('Factory Droid control requests', () => {
  droidTest('answers a question through the shared question banner', async ({ askingDroidWorkspace, page, modelScript, leapmuxServer }) => {
    const context = await nativeContext({ page, modelScript, leapmuxServer, workspaceId: askingDroidWorkspace.workspaceId })
    const { result } = await exerciseQuestionAnswer(context, {
      questions: [{ question: 'Which color do you prefer?', header: 'Color', options: [{ label: 'Blue', description: 'The color blue' }, { label: 'Red', description: 'The color red' }] }],
      callId: 'ask-1',
      prompt: 'Ask me a question.',
      answer: 'The answer was recorded.',
      reply: chooseQuestionOption('Red'),
      readResult: askUserResult,
    })
    // Droid reports each answer under the index of its question. Droid numbers its
    // questions from 1, and the reply keeps that number.
    expect(result).toContain('1. [question] Which color do you prefer?')
    expect(result).toContain('Red')
    expect(result).not.toContain('Blue')
    // The saved row reads the answer list of Droid's own reply.
    await expect(savedControlAnswer(page)).toHaveText('Which color do you prefer?: Red')
  })

  // Droid takes each answer as one string. A multiple-choice answer joins every pick
  // in the order of the options, as Droid's own TUI does. The reply lists the answers
  // in the order of the questions, each under the index of its question.
  droidTest('sends every pick of a multiple-choice question, one answer for each question', async ({ askingDroidWorkspace, page, modelScript, leapmuxServer }) => {
    const context = await nativeContext({ page, modelScript, leapmuxServer, workspaceId: askingDroidWorkspace.workspaceId })
    const { result } = await exerciseQuestionAnswer(context, {
      questions: [
        {
          question: 'Which colors do you like?',
          header: 'Colors',
          multiSelect: true,
          options: [
            { label: 'Blue', description: 'The color blue' },
            { label: 'Green', description: 'The color green' },
            { label: 'Red', description: 'The color red' },
          ],
        },
        {
          question: 'Which size do you want?',
          header: 'Size',
          options: [{ label: 'Small', description: 'The small size' }, { label: 'Large', description: 'The large size' }],
        },
      ],
      callId: 'ask-multi',
      prompt: 'Ask me about colors and sizes.',
      answer: 'The answers were recorded.',
      reply: async (banner) => {
        // Pick against the order of the options. The answer follows the options.
        await banner.getByTestId('question-option-Red').click()
        await banner.getByTestId('question-option-Blue').click()
        // A multiple-choice question stays on its page, so the reader moves on by hand.
        await questionPagination(page).locator('button').nth(1).click()
        await expect(banner).toContainText('Which size do you want?')
        await banner.getByTestId('question-option-Large').click()
        await controlButton(page, 'submit').click()
      },
      readResult: askUserResult,
    })
    // Droid writes "<index>. [question] <question>", then "[answer] <answer>", for each
    // answer in the order of the reply. The parts must appear in exactly that order.
    const parts = [
      '1. [question] Which colors do you like?',
      '[answer] Blue, Red',
      '2. [question] Which size do you want?',
      '[answer] Large',
    ]
    for (const part of parts)
      expect(result).toContain(part)
    const positions = parts.map(part => result.indexOf(part))
    expect(positions).toEqual([...positions].sort((a, b) => a - b))
    expect(result).not.toContain('Green')
    await expect(savedControlAnswer(page)).toContainText('Which colors do you like?: Blue, Red')
    await expect(savedControlAnswer(page)).toContainText('Which size do you want?: Large')
  })
})
