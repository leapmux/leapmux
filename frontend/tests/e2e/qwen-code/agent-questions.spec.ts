import { expect } from '@playwright/test'
import { chooseQuestionOption, exerciseQuestionAnswer, exerciseQuestionPages, exerciseTypedQuestionAnswer } from '../helpers/nativeQuestion'
import { exerciseQuestionIsolation } from '../helpers/nativeQuestionIsolation'
import { messageBubbles } from '../helpers/ui'
import { qwenTest } from '../qwen-fixtures'

qwenTest.describe('control requests for Qwen Code', () => {
  qwenTest('answers a question through its own reply field', async ({ native }) => {
    const { page } = native
    const { result } = await exerciseQuestionAnswer(native, {
      questions: [{
        question: 'Which color do you want?',
        header: 'Color',
        options: [{ label: 'Red', description: 'The red one' }, { label: 'Blue', description: 'The blue one' }],
      }],
      callId: 'qwen-question',
      prompt: 'Ask me for a color.',
      answer: 'You chose Blue.',
      reply: chooseQuestionOption('Blue', native),
    })

    // Qwen's native reply field carries the answer. Its tool result states that answer.
    // The saved answer appears under the question header.
    expect(result).toContain('**Color**: Blue')
    await expect(messageBubbles(page).filter({ hasText: 'Color: Blue' }).first()).toBeVisible()
  })
})

qwenTest('preserves answers across native question pages and reloads', async ({ native }) => {
  await exerciseQuestionPages(native)
})

qwenTest('returns a custom native answer after its draft survives reload', async ({ native }) => {
  await exerciseTypedQuestionAnswer(native)
})

qwenTest('keeps a native question on its owner tab and workspace after reload', async ({ native }) => {
  await exerciseQuestionIsolation(native, { customAnswerDraft: true })
})
