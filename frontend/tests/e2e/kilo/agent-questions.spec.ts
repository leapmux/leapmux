import { expect } from '@playwright/test'
import { chooseQuestionOption, exerciseQuestionAnswer } from '../helpers/nativeQuestion'
import { kiloTest } from '../kilo-fixtures'

kiloTest('answers a native question and resumes the turn', async ({ native }) => {
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
    reply: chooseQuestionOption('Green'),
  })
  expect(request.protocol).toBe('openai-chat-completions')
  expect(result).toContain('Green')
  expect(result).not.toContain('Blue')
})
