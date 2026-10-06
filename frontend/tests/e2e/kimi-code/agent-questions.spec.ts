import type { MockModelRequestRecord } from '../helpers/mockModelScript'
import { expect } from '@playwright/test'
import { chooseQuestionOption, exerciseQuestionAnswer } from '../helpers/nativeQuestion'
import { kimiTest } from '../kimi-fixtures'

/**
 * The encoded body of the request after the question.
 * Kimi Code answers with `{"answers":{"<question>":"<label>"}}` as the tool result TEXT. The encoded body escapes each
 * quote of that text whatever form the tool result content takes, so the check reads the escaped pair. The model's
 * own tool call repeats every option label in the body, so a bare label is no proof.
 */
function encodedBody(request: MockModelRequestRecord): string {
  return JSON.stringify(request.body)
}

kimiTest.describe('answers Kimi Code questions', () => {
  kimiTest('a selected answer reaches the model as the tool result', async ({ native }) => {
    const { result } = await exerciseQuestionAnswer(native, {
      questions: [{
        question: 'Which color do you prefer?',
        header: 'Color',
        options: [
          { label: 'Red', description: 'A warm color.' },
          { label: 'Blue', description: 'A cool color.' },
        ],
      }],
      callId: 'color-question',
      prompt: 'Ask me which color I prefer.',
      answer: 'Recorded the color.',
      reply: async (banner) => {
        await expect(banner.getByText('A cool color.', { exact: true })).toBeVisible()
        await chooseQuestionOption('Blue')(banner)
      },
      readResult: encodedBody,
    })
    expect(result).toContain('\\"Which color do you prefer?\\":\\"Blue\\"')
    expect(result).not.toContain('\\"Which color do you prefer?\\":\\"Red\\"')
  })
})
