import type { QuestionRequest } from '../helpers/providerToolCalls'
import { expect } from '@playwright/test'
import { chooseQuestionOption, chooseQuestionPages, exerciseQuestionAnswer, exerciseQuestionCancellation } from '../helpers/nativeQuestion'
import { exerciseQuestionIsolation } from '../helpers/nativeQuestionIsolation'
import { controlButton, messageContents, savedControlAnswer } from '../helpers/ui'
import { mimoTest } from '../mimo-fixtures'

mimoTest.describe('questions for MiMo Code', () => {
  const questions: QuestionRequest[] = [{
    question: 'Choose a style',
    header: 'Style',
    options: [
      { label: 'Alpha', description: 'Use the first style.' },
      { label: 'Beta', description: 'Use the second style.' },
    ],
  }]

  mimoTest('delivers the selected answer to the question tool', async ({ native }) => {
    const { result } = await exerciseQuestionAnswer(native, {
      questions,
      callId: 'style-question',
      prompt: 'Ask me which style to use.',
      answer: 'Recorded the style.',
      reply: async (banner) => {
        await expect(banner.getByText('Use the second style.', { exact: true })).toBeVisible()
        await chooseQuestionOption('Beta', native)(banner)
      },
    })
    expect(result).toContain('Beta')
    expect(result).not.toContain('Alpha')

    // The question row reads the answer out of MiMo's own result, under the
    // question's header, and the saved answer states it too.
    await expect(messageContents(native.page).filter({ hasText: /Style\s*—\s*Beta/ }).first()).toBeVisible()
    await expect(savedControlAnswer(native.page)).toHaveText('Style: Beta')
  })

  // MiMo accepts one answer list for each question.
  // A multi-select answer contains the selected options. A composer answer contains the reader's typed text.
  mimoTest('delivers several choices and typed words, one answer for each question', async ({ native }) => {
    const { page } = native
    const questions: QuestionRequest[] = [
      {
        question: 'Choose the toppings',
        header: 'Toppings',
        multiSelect: true,
        options: [
          { label: 'Cheese', description: 'Add cheese.' },
          { label: 'Olives', description: 'Add olives.' },
          { label: 'Basil', description: 'Add basil.' },
        ],
      },
      {
        question: 'Name the pizza',
        header: 'Name',
        options: [
          { label: 'Margherita', description: 'The classic name.' },
          { label: 'Marinara', description: 'The name with no cheese.' },
        ],
      },
    ]
    const { result } = await exerciseQuestionAnswer(native, {
      questions,
      callId: 'pizza-questions',
      prompt: 'Ask me about the pizza.',
      answer: 'Recorded the pizza.',
      reply: chooseQuestionPages(native, questions, [{ labels: ['Cheese', 'Basil'] }, { text: 'Garden Special' }]),
    })
    expect(result).toContain('Cheese')
    expect(result).toContain('Basil')
    expect(result).toContain('Garden Special')
    expect(result).not.toContain('Olives')

    await expect(messageContents(page).filter({ hasText: /Toppings\s*—\s*Cheese, Basil/ }).first()).toBeVisible()
    await expect(messageContents(page).filter({ hasText: /Name\s*—\s*Garden Special/ }).first()).toBeVisible()
    await expect(savedControlAnswer(page)).toContainText('Toppings: Cheese, Basil')
    await expect(savedControlAnswer(page)).toContainText('Name: Garden Special')
  })

  // Stop dismisses the question. A dismissal stops MiMo's loop, as a rejected
  // permission does, so the script holds no answer step.
  mimoTest('dismisses the question without an answer', async ({ native }) => {
    const { page } = native
    await exerciseQuestionCancellation(native, {
      questions,
      callId: 'style-question',
      prompt: 'Ask me which style to use.',
      reply: async () => { await controlButton(page, 'stop').click() },
    })
    await expect(savedControlAnswer(page)).toHaveText('Dismissed')
  })
})

mimoTest('keeps a native question on its owner tab and workspace after reload', async ({ native }) => {
  await exerciseQuestionIsolation(native, { customAnswerDraft: true })
})
