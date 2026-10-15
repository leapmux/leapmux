import { expect } from '@playwright/test'
import { chooseQuestionOption, exerciseQuestionAnswer, exerciseQuestionPages, exerciseQuestionPreviews, exerciseTypedQuestionAnswer } from '../helpers/nativeQuestion'
import { exerciseQuestionIsolation } from '../helpers/nativeQuestionIsolation'
import { qoderTest } from '../qoder-fixtures'

qoderTest.describe('control answers for Qoder CLI', () => {
  qoderTest('returns a selected answer from its native question tool', async ({ native }) => {
    const { result } = await exerciseQuestionAnswer(native, {
      questions: [{
        question: 'Which color should I use?',
        header: 'Color',
        options: [
          { label: 'Blue', description: 'Use blue.' },
          { label: 'Red', description: 'Use red.' },
        ],
      }],
      callId: 'qoder-question',
      prompt: 'Ask me to choose a color.',
      answer: 'QODER_QUESTION_ANSWERED.',
      reply: chooseQuestionOption('Red', native),
    })
    expect(result).toContain('Red')
    expect(result).not.toContain('Blue')
  })
})

qoderTest('preserves answers across native question pages and reloads', async ({ native }) => {
  await exerciseQuestionPages(native)
})

qoderTest('preserves native option previews in a narrow layout after reload', async ({ native }) => {
  await exerciseQuestionPreviews(native)
})

qoderTest('returns a custom native answer after its draft survives reload', async ({ native }) => {
  await exerciseTypedQuestionAnswer(native)
})

qoderTest('keeps a native question on its owner tab and workspace after reload', async ({ native }) => {
  await exerciseQuestionIsolation(native, { customAnswerDraft: true })
})
