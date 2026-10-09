import type { QuestionRequest } from '../helpers/providerToolCalls'
import { expect } from '@playwright/test'
import { chooseQuestionOption, exerciseQuestionAnswer, pickQuestionOption } from '../helpers/nativeQuestion'
import { controlButton, focusComposer, questionPagination, savedControlAnswer } from '../helpers/ui'
import { museTest } from '../muse-fixtures'

function question(multiSelect: boolean): QuestionRequest {
  return {
    question: 'Choose the native answer',
    header: 'Answer',
    options: [
      { label: 'Alpha', description: 'Use the first value.' },
      { label: 'Beta', description: 'Use the second value.' },
      { label: 'Gamma', description: 'Use the third value.' },
    ],
    multiSelect,
  }
}

museTest('returns the selected native answer and retains it after reload', async ({ native }) => {
  const { result, request } = await exerciseQuestionAnswer(native, {
    questions: [{
      question: 'Choose the native style',
      header: 'Style',
      options: [
        { label: 'Alpha', description: 'Use the first style.' },
        { label: 'Beta', description: 'Use the second style.' },
      ],
    }],
    callId: 'muse-style-question',
    prompt: 'Ask me which native style to use.',
    answer: 'The native style answer was recorded.',
    reply: chooseQuestionOption('Beta'),
  })
  expect(request.protocol).toBe('openai-responses')
  expect(result).toContain('Beta')
  expect(result).not.toContain('Alpha')
  await expect(savedControlAnswer(native.page)).toHaveText('Choose the native style: Beta')
  await native.page.reload()
  await expect(savedControlAnswer(native.page)).toHaveText('Choose the native style: Beta')
})

for (const multiSelect of [false, true]) {
  const mode = multiSelect ? 'multiple' : 'single'
  for (const kind of ['selected', 'typed'] as const) {
    museTest(`returns the ${kind} native ${mode} answer and keeps its saved response after reload`, async ({ native }) => {
      const { result, request } = await exerciseQuestionAnswer(native, {
        questions: [question(multiSelect)],
        callId: `muse-${mode}-${kind}`,
        reply: kind === 'typed'
          ? async () => {
            await focusComposer(native.page)
            await native.page.keyboard.insertText('A native typed answer')
            await controlButton(native.page, 'submit').click()
          }
          : chooseQuestionOption('Beta'),
      })
      expect(request.mockCredential?.accepted).toBe(true)
      expect(JSON.parse(result)).toEqual({
        status: 'answered',
        answers: [{
          id: 'question-1',
          ...(kind === 'typed' ? { free_text: 'A native typed answer' } : multiSelect ? { selected_labels: ['Beta'] } : { selected_label: 'Beta' }),
        }],
      })
      const answer = kind === 'typed' ? 'A native typed answer' : 'Beta'
      await expect(savedControlAnswer(native.page)).toContainText(answer)
      await native.page.reload()
      await expect(savedControlAnswer(native.page)).toContainText(answer)
    })
  }
  for (const text of ['', ' \t\n']) {
    const kind = text === '' ? 'empty' : 'whitespace'
    const form = text === '' ? 'an empty typed answer' : 'a typed answer that contains whitespace only'
    museTest(`keeps ${form} unavailable in native ${mode} mode and sends an explicit cancellation`, async ({ native }) => {
      const before = (await native.modelScript.status()).nextStep
      const { result, request } = await exerciseQuestionAnswer(native, {
        questions: [question(multiSelect)],
        callId: `muse-${mode}-${kind}`,
        reply: async (banner) => {
          const editor = await focusComposer(native.page)
          if (text)
            await native.page.keyboard.insertText(text)
          const entered = (await editor.textContent()) ?? ''
          expect(entered.trim()).toBe('')
          if (text)
            expect(entered.length).toBeGreaterThan(0)
          await expect(controlButton(native.page, 'submit')).toBeDisabled()
          await expect(banner).toContainText('Choose the native answer')
          expect((await native.modelScript.status()).nextStep).toBe(before + 1)
          await controlButton(native.page, 'stop').click()
        },
      })
      expect(request.mockCredential?.accepted).toBe(true)
      expect(JSON.parse(result)).toEqual({ status: 'cancelled', answers: [], reason: 'User stopped' })
    })
  }
}

museTest('returns each native question answer on its own page', async ({ native }) => {
  const first = question(false)
  const second = { ...question(true), question: 'Choose the native tools', header: 'Tools' }
  const { result, request } = await exerciseQuestionAnswer(native, {
    questions: [first, second],
    callId: 'muse-multiple-pages',
    reply: async (banner) => {
      await pickQuestionOption(banner, 'Beta')
      await expect(banner).toContainText(second.question)
      await expect(controlButton(native.page, 'submit')).toBeDisabled()
      await pickQuestionOption(banner, 'Alpha')
      await pickQuestionOption(banner, 'Gamma')
      await questionPagination(native.page).getByRole('button', { name: '1', exact: true }).click()
      await expect(banner).toContainText(first.question)
      await expect(banner.getByTestId('question-option-Beta').locator('input')).toBeChecked()
      await questionPagination(native.page).getByRole('button', { name: '2', exact: true }).click()
      await controlButton(native.page, 'submit').click()
    },
  })
  expect(request.mockCredential?.accepted).toBe(true)
  expect(JSON.parse(result)).toEqual({ status: 'answered', answers: [
    { id: 'question-1', selected_label: 'Beta' },
    { id: 'question-2', selected_labels: ['Alpha', 'Gamma'] },
  ] })
})

museTest('requires the explicit native selection count and keeps selected options removable at the maximum', async ({ native }) => {
  const limited = { ...question(true), minimumSelections: 2, maximumSelections: 2 }
  const { result, request } = await exerciseQuestionAnswer(native, {
    questions: [limited],
    callId: 'muse-limited-selections',
    reply: async (banner) => {
      await expect(banner).toContainText('Select 2 options if you use options.')
      await pickQuestionOption(banner, 'Alpha')
      await expect(controlButton(native.page, 'submit')).toBeDisabled()
      await pickQuestionOption(banner, 'Beta')
      await expect(controlButton(native.page, 'submit')).toBeEnabled()
      await expect(banner.getByTestId('question-option-Gamma').locator('input')).toBeDisabled()
      await pickQuestionOption(banner, 'Alpha')
      await expect(controlButton(native.page, 'submit')).toBeDisabled()
      await expect(banner.getByTestId('question-option-Gamma').locator('input')).toBeEnabled()
      await pickQuestionOption(banner, 'Gamma')
      await controlButton(native.page, 'submit').click()
    },
  })
  expect(request.mockCredential?.accepted).toBe(true)
  expect(JSON.parse(result)).toEqual({ status: 'answered', answers: [{ id: 'question-1', selected_labels: ['Beta', 'Gamma'] }] })
})
