import type { QuestionRequest } from '../helpers/providerToolCalls'
import { expect } from '@playwright/test'
import { chooseQuestionOption, exerciseQuestionAnswer } from '../helpers/nativeQuestion'
import { askUserQuestionToolCall } from '../helpers/providerToolCalls'
import { composerEditor, controlButton, expectNoControlBanner, messageContents, questionPagination, savedControlAnswer, sendMessage, waitForAgentIdle, waitForControlBanner } from '../helpers/ui'
import { mimoTest } from '../mimo-fixtures'

mimoTest.describe('MiMo Code questions', () => {
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
        await chooseQuestionOption('Beta')(banner)
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
    const { result } = await exerciseQuestionAnswer(native, {
      questions: [
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
      ],
      callId: 'pizza-questions',
      prompt: 'Ask me about the pizza.',
      answer: 'Recorded the pizza.',
      reply: async (banner) => {
        await banner.getByTestId('question-option-Cheese').click()
        await banner.getByTestId('question-option-Basil').click()
        // A multi-select question stays on its page, so the reader moves on by hand.
        await questionPagination(page).locator('button').nth(1).click()
        await expect(banner).toContainText('Name the pizza')
        await composerEditor(page).fill('Garden Special')
        await controlButton(page, 'submit').click()
      },
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
    const { page, modelScript } = native
    const start = await modelScript.queue({ toolCalls: [askUserQuestionToolCall(native.provider, 'style-question', questions)] })
    await sendMessage(page, modelScript.prompt('Ask me which style to use.'))
    await modelScript.waitForSteps(start + 1)

    const banner = await waitForControlBanner(page)
    await expect(banner).toContainText('Choose a style')
    await controlButton(page, 'stop').click()
    await expectNoControlBanner(page)
    await waitForAgentIdle(page)
    await expect(savedControlAnswer(page)).toHaveText('Dismissed')
  })
})
