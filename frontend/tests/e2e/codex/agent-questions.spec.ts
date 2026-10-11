import { expect } from '@playwright/test'
import { codexTest } from '../codex-fixtures'
import { chooseQuestionOption, exerciseQuestionAnswer } from '../helpers/nativeQuestion'
import { chooseSettingsOption, waitForSettingsIdle } from '../helpers/ui'

codexTest.describe('Codex agent questions', () => {
  codexTest('returns the selected answer to the native question tool', async ({ native }) => {
    await chooseSettingsOption(native.page, 'collaboration_mode-plan')
    await waitForSettingsIdle(native.page)
    const { result, request } = await exerciseQuestionAnswer(native, {
      questions: [{
        header: 'Color',
        question: 'Which color should I use?',
        options: [
          { label: 'Blue (Recommended)', description: 'Use blue.' },
          { label: 'Red', description: 'Use red.' },
        ],
      }],
      callId: 'codex-color-question',
      prompt: 'Ask me which color to use.',
      answer: 'The answer was recorded.',
      reply: chooseQuestionOption('Red'),
    })
    expect(request.protocol).toBe('openai-responses')
    expect(result).toContain('Red')
    expect(result).not.toContain('Blue (Recommended)')
  })
})
