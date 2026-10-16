import { expect } from '@playwright/test'
import { chooseQuestionOption, chooseQuestionPages, exerciseQuestionAnswer, exerciseQuestionPreviews } from '../helpers/nativeQuestion'
import { exerciseQuestionIsolation } from '../helpers/nativeQuestionIsolation'
import { restoreQuestionTextDraft } from '../helpers/nativeQuestionRestoration'
import { assistantBubbles, chatScrollContainer, controlButton, focusComposer, waitForControlBanner } from '../helpers/ui'
import { piTest } from '../pi-fixtures'

for (const answerKind of ['custom', 'selected']) {
  piTest(`delivers a ${answerKind} answer to the real Pi question extension`, async ({ native }) => {
    const { page } = native
    // The EXTENSION is real -- it is what turns the tool call into a control
    // request and carries the answer back. Only the decision to ask is scripted.
    const { result } = await exerciseQuestionAnswer(native, {
      questions: [{
        question: 'Choose a style',
        header: 'Style',
        options: [
          { label: 'Alpha', description: 'Use the first style.' },
          { label: 'Beta', description: 'Use the second style.' },
        ],
      }],
      callId: 'style-question',
      prompt: 'Ask me which style to use.',
      answer: 'Recorded the style.',
      reply: answerKind === 'custom'
        ? async () => {
          await focusComposer(page)
          await page.keyboard.insertText('A custom style')
          await restoreQuestionTextDraft(native, 'A custom style')
          await controlButton(page, 'submit').click()
        }
        : chooseQuestionOption('Beta', native),
    })
    expect(result).toContain(answerKind === 'custom' ? 'A custom style' : 'Beta')
    expect(result).not.toContain('Alpha')
    await expect(assistantBubbles(page).filter({ hasText: 'User has answered your questions:' })).toContainText(answerKind === 'custom' ? 'A custom style' : 'Beta')
    await expect(chatScrollContainer(page).getByText('User declined to answer questions', { exact: true })).toHaveCount(0)
  })
}

piTest('preserves answers across sequential native questions and reloads', async ({ native }) => {
  // The RPC extension sends one dialog per question. Multiple selection uses its numeric input protocol.
  const questions = [
    { header: 'Color', question: 'Choose a native color.', options: [{ label: 'Blue', description: 'Use blue.' }, { label: 'Green', description: 'Use green.' }] },
    { header: 'Size', question: 'Choose native sizes.', multiSelect: true, options: [{ label: 'Small', description: 'Use small.' }, { label: 'Medium', description: 'Use medium.' }, { label: 'Large', description: 'Use large.' }] },
  ]
  const { result } = await exerciseQuestionAnswer(native, {
    questions,
    reply: async () => {
      for (const [index, question] of questions.entries()) {
        await chooseQuestionPages(native, [question], [{ labels: index === 0 ? ['Green'] : ['Large', 'Small'] }])(await waitForControlBanner(native.page))
      }
    },
  })
  expect(result).toContain('Green')
  expect(result).toContain('Small')
  expect(result).toContain('Large')
  expect(result).not.toContain('Blue')
  expect(result).not.toContain('Medium')
})

piTest('preserves native option previews in a narrow layout after reload', async ({ native }) => {
  await exerciseQuestionPreviews(native)
})

piTest('keeps a native question on its owner tab and workspace after reload', async ({ native }) => {
  await exerciseQuestionIsolation(native, { customAnswerDraft: true })
})
