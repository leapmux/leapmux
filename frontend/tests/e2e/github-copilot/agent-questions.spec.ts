import { expect } from '@playwright/test'
import { copilotTest } from '../copilot-fixtures'
import { chooseQuestionOption, exerciseQuestionAnswer, exerciseTypedQuestionAnswer } from '../helpers/nativeQuestion'
import { exerciseQuestionIsolation } from '../helpers/nativeQuestionIsolation'

copilotTest('answers one native ask_user choice and resumes the model turn', async ({ native }) => {
  const { result, request } = await exerciseQuestionAnswer(native, {
    questions: [{
      question: 'Which color should I use?',
      header: 'Color',
      options: [
        { label: 'Blue', description: 'Use blue.' },
        { label: 'Green', description: 'Use green.' },
      ],
    }],
    callId: 'copilot-question',
    prompt: 'Ask me which color to use, then report that choice.',
    answer: 'I used the chosen color.',
    reply: chooseQuestionOption('Green', native),
  })
  expect(request.protocol).toBe('openai-chat-completions')
  expect(result).toContain('Green')
  expect(result).not.toContain('Blue')
})

copilotTest('returns a custom native answer after its draft survives reload', async ({ native }) => {
  await exerciseTypedQuestionAnswer(native)
})

copilotTest('keeps a native question on its owner tab and workspace after reload', async ({ native }) => {
  await exerciseQuestionIsolation(native, { customAnswerDraft: true })
})
