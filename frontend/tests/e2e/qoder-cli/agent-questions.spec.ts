import { expect } from '@playwright/test'
import { chooseQuestionOption, exerciseQuestionAnswer } from '../helpers/nativeQuestion'
import { qoderTest } from '../qoder-fixtures'

qoderTest.describe('Qoder CLI control answers', () => {
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
      reply: chooseQuestionOption('Red'),
    })
    expect(result).toContain('Red')
    expect(result).not.toContain('Blue')
  })
})
