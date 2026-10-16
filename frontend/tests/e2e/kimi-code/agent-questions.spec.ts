import { expect } from '@playwright/test'
import { chooseQuestionOption, exerciseQuestionAnswer, exerciseQuestionPages, exerciseTypedQuestionAnswer } from '../helpers/nativeQuestion'
import { exerciseQuestionIsolation } from '../helpers/nativeQuestionIsolation'
import { kimiTest } from '../kimi-fixtures'

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
        await chooseQuestionOption('Blue', native)(banner)
      },
    })
    expect(JSON.parse(result)).toEqual({ answers: { 'Which color do you prefer?': 'Blue' } })
  })
})

kimiTest('preserves answers across native question pages and reloads', async ({ native }) => {
  await exerciseQuestionPages(native)
})

kimiTest('returns a custom native answer after its draft survives reload', async ({ native }) => {
  await exerciseTypedQuestionAnswer(native)
})

kimiTest('keeps a native question on its owner tab and workspace after reload', async ({ native }) => {
  await exerciseQuestionIsolation(native, { customAnswerDraft: true })
})
