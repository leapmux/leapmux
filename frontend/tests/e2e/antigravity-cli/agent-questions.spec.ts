import { expect } from '@playwright/test'
import { antigravityTest } from '../antigravity-fixtures'
import { chooseQuestionOption, exerciseQuestionAnswer, exerciseQuestionPages, exerciseTypedQuestionAnswer } from '../helpers/nativeQuestion'
import { exerciseQuestionIsolation } from '../helpers/nativeQuestionIsolation'

antigravityTest('answers a native question and resumes the turn', async ({ native }) => {
  const { result, request } = await exerciseQuestionAnswer(native, {
    questions: [
      {
        question: 'Pick a color.',
        header: 'Color',
        options: [
          { label: 'Blue', description: 'Use blue.' },
          { label: 'Green', description: 'Use green.' },
        ],
      },
    ],
    callId: 'color-question',
    prompt: 'Ask me to pick a color.',
    answer: 'The answer was recorded.',
    reply: chooseQuestionOption('Green', native),
  })
  expect(request.protocol).toBe('google-generative-language')
  expect(result).toContain('Green')
  expect(result).not.toContain('Blue')
})

antigravityTest('preserves answers across native question pages and reloads', async ({ native }) => {
  await exerciseQuestionPages(native)
})

antigravityTest('returns a custom native answer after its draft survives reload', async ({ native }) => {
  await exerciseTypedQuestionAnswer(native)
})

antigravityTest('keeps a native question on its owner tab and workspace after reload', async ({ native }) => {
  await exerciseQuestionIsolation(native, { customAnswerDraft: true })
})
