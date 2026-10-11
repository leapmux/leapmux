import { expect } from '@playwright/test'
import { copilotTest } from '../copilot-fixtures'
import { chooseQuestionOption, exerciseQuestionAnswer } from '../helpers/nativeQuestion'

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
    reply: chooseQuestionOption('Green'),
  })
  expect(request.protocol).toBe('openai-chat-completions')
  expect(result).toContain('Green')
  expect(result).not.toContain('Blue')
})
