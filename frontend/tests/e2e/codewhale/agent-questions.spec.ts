import type { QuestionRequest } from '../helpers/providerToolCalls'
import { expect } from '@playwright/test'
import { codewhaleTest } from '../codewhale-fixtures'
import { chooseQuestionOption, exerciseQuestionAnswer, exerciseQuestionPages, exerciseTypedQuestionAnswer, pickQuestionOption, restoreQuestionSelection } from '../helpers/nativeQuestion'
import { exerciseQuestionIsolation } from '../helpers/nativeQuestionIsolation'
import { assistantBubbles, controlButton, questionPagination } from '../helpers/ui'

const DRINK_Q: QuestionRequest = {
  question: 'Do you prefer tea or coffee?',
  header: 'Drink',
  options: [
    { label: 'Tea', description: 'Prefer tea' },
    { label: 'Coffee', description: 'Prefer coffee' },
  ],
}

const SIZE_Q: QuestionRequest = {
  question: 'Which cup size?',
  header: 'Size',
  options: [
    { label: 'Small', description: 'A small cup' },
    { label: 'Large', description: 'A large cup' },
  ],
}

codewhaleTest.describe('questions for Codewhale', () => {
  codewhaleTest('sends the chosen option as the answer', async ({ native }) => {
    const { result } = await exerciseQuestionAnswer(native, {
      questions: [DRINK_Q],
      callId: 'ask-question',
      prompt: 'Ask me about my drink.',
      answer: 'You prefer tea.',
      reply: chooseQuestionOption('Tea', native),
    })
    expect(result).toContain('Tea')
    expect(result).not.toContain('Coffee')
    await expect(assistantBubbles(native.page).filter({ hasText: 'You prefer tea.' })).toBeVisible()
  })

  codewhaleTest('answers two questions, one page each', async ({ native }) => {
    const { page } = native
    const { result } = await exerciseQuestionAnswer(native, {
      questions: [DRINK_Q, SIZE_Q],
      callId: 'ask-question',
      prompt: 'Ask me about my drink.',
      answer: 'A large coffee.',
      reply: async (banner) => {
        await expect(questionPagination(page).locator('button')).toHaveCount(2)
        await expect(banner.getByText('Do you prefer tea or coffee?')).toBeVisible()
        await pickQuestionOption(banner, 'Coffee')
        await expect(banner.getByText('Which cup size?')).toBeVisible()
        await pickQuestionOption(banner, 'Large')
        await restoreQuestionSelection(native, banner, 'Large')
        const submit = controlButton(page, 'submit')
        await expect(submit).toBeEnabled()
        await submit.click()
      },
    })
    expect(result).toContain('Coffee')
    expect(result).toContain('Large')
    expect(result).not.toContain('Small')
    await expect(assistantBubbles(native.page).filter({ hasText: 'A large coffee.' })).toBeVisible()
  })

  codewhaleTest('tells the model that the reader declined', async ({ native }) => {
    const { result } = await exerciseQuestionAnswer(native, {
      questions: [DRINK_Q],
      callId: 'ask-question',
      prompt: 'Ask me about my drink.',
      answer: 'You declined to answer.',
      reply: async () => { await controlButton(native.page, 'stop').click() },
    })
    // The native runtime has no decline route. The Worker supplies the stop reason as a free-text answer.
    expect(result).toContain('question_1')
    expect(result).toContain('Other')
    expect(result).toContain('User stopped')
    expect(result).not.toContain('Tea')
  })
})

codewhaleTest('preserves answers across native question pages and reloads', async ({ native }) => {
  await exerciseQuestionPages(native)
})

codewhaleTest('returns a custom native answer after its draft survives reload', async ({ native }) => {
  await exerciseTypedQuestionAnswer(native)
})

codewhaleTest('keeps a native question on its owner tab and workspace after reload', async ({ native }) => {
  await exerciseQuestionIsolation(native, { customAnswerDraft: true })
})
