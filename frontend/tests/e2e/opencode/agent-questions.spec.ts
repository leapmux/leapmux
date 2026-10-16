import { expect } from '@playwright/test'
import { chooseQuestionOption, exerciseQuestionAnswer, exerciseQuestionPages, exerciseTypedQuestionAnswer } from '../helpers/nativeQuestion'
import { exerciseQuestionIsolation } from '../helpers/nativeQuestionIsolation'
import { opencodeTest } from '../opencode-fixtures'

opencodeTest('answers a native question and resumes the turn', async ({ native }) => {
  const { result, request } = await exerciseQuestionAnswer(native, {
    questions: [{
      question: 'Pick a color.',
      header: 'Color',
      options: [
        { label: 'Blue', description: 'Use blue.' },
        { label: 'Green', description: 'Use green.' },
      ],
    }],
    callId: 'color-question',
    prompt: 'Ask me to pick a color.',
    answer: 'The answer was recorded.',
    reply: chooseQuestionOption('Green', native),
  })
  expect(request.protocol).toBe('openai-chat-completions')
  expect(result).toContain('Green')
  expect(result).not.toContain('Blue')
})

opencodeTest('preserves answers across native question pages and reloads', async ({ native }) => {
  await exerciseQuestionPages(native)
})

opencodeTest('returns a custom native answer after its draft survives reload', async ({ native }) => {
  await exerciseTypedQuestionAnswer(native)
})

opencodeTest('keeps a native question on its owner tab and workspace after reload', async ({ native }) => {
  await exerciseQuestionIsolation(native, { customAnswerDraft: true })
})
