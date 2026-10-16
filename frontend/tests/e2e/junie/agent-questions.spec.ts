import { expect } from '@playwright/test'
import { exerciseQuestionAnswer } from '../helpers/nativeQuestion'
import { exerciseQuestionIsolation } from '../helpers/nativeQuestionIsolation'
import { controlActions, controlButton } from '../helpers/ui'
import { junieTest } from '../junie-fixtures'

junieTest.describe('questions for Junie', () => {
  junieTest('ask_user raises a native elicitation choice', async ({ native }) => {
    const { page } = native

    // The turn waits for approval. The next native model request must carry
    // the selected choice, while the scripted answer stays neutral.
    const { result } = await exerciseQuestionAnswer(native, {
      questions: [{
        question: 'Which one?',
        header: 'Pick',
        options: [
          { label: 'First', description: 'The first choice.' },
          { label: 'Second', description: 'The second choice.' },
        ],
      }],
      callId: 'junie-question',
      prompt: 'Ask me which one.',
      answer: 'You picked.',
      // The native question creates an elicitation form. Its choice menu holds the available answers.
      reply: async () => {
        await page.getByRole('button', { name: 'Pick' }).click()
        await expect(page.getByText('First').filter({ visible: true }).first()).toBeVisible()
        await expect(page.getByText('Second').filter({ visible: true }).first()).toBeVisible()
        await page.getByText('Second').filter({ visible: true }).first().click()
        await page.getByRole('button', { name: 'Approve' }).click()
      },
    })
    expect(result).toContain('Second')
    expect(result).not.toContain('First')
  })
})

junieTest('returns multiple native form answers including multiple selection', async ({ native }) => {
  const { result } = await exerciseQuestionAnswer(native, {
    questions: [
      { header: 'Color', question: 'Choose a native color.', options: [{ label: 'Blue', description: 'Use blue.' }, { label: 'Green', description: 'Use green.' }] },
      { header: 'Size', question: 'Choose native sizes.', multiSelect: true, options: [{ label: 'Small', description: 'Use small.' }, { label: 'Medium', description: 'Use medium.' }, { label: 'Large', description: 'Use large.' }] },
    ],
    reply: async (banner) => {
      await banner.getByRole('button', { name: /^Color/ }).click()
      await native.page.getByRole('menuitemradio', { name: 'Green', exact: true }).filter({ visible: true }).click()
      await banner.getByRole('checkbox', { name: 'Large', exact: true }).check()
      await banner.getByRole('checkbox', { name: 'Small', exact: true }).check()
      await controlButton(native.page, 'allow').click()
    },
  })
  expect(result).toContain('Green')
  expect(result).toContain('Small')
  expect(result).toContain('Large')
  expect(result).not.toContain('Blue')
  expect(result).not.toContain('Medium')
})

junieTest('returns a custom answer from its native elicitation form', async ({ native }) => {
  const { result } = await exerciseQuestionAnswer(native, {
    questions: [{ header: 'Color', question: 'Choose a native color.', options: [{ label: 'Blue', description: 'Use blue.' }, { label: 'Green', description: 'Use green.' }] }],
    reply: async (banner) => {
      await banner.getByLabel('Other', { exact: true }).fill('Custom color 0 界')
      await controlButton(native.page, 'allow').click()
    },
  })
  expect(result).toContain('Custom color 0 界')
  expect(result).not.toContain('Blue')
  expect(result).not.toContain('Green')
})

junieTest('cancels its native elicitation form without choosing an offered answer', async ({ native }) => {
  const { result } = await exerciseQuestionAnswer(native, {
    questions: [{ header: 'Color', question: 'Choose a native color.', options: [{ label: 'Blue', description: 'Use blue.' }, { label: 'Green', description: 'Use green.' }] }],
    reply: async () => {
      await controlActions(native.page).getByTestId('control-more-actions').click()
      await native.page.getByTestId('control-cancel-btn').filter({ visible: true }).click()
    },
  })
  expect(result).toContain('(no response)')
  expect(result).not.toContain('Blue')
  expect(result).not.toContain('Green')
})

junieTest('keeps a native question on its owner tab and workspace after reload', async ({ native }) => {
  await exerciseQuestionIsolation(native, { customAnswerDraft: false })
})
