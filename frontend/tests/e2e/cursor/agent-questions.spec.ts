import type { QuestionRequest } from '../helpers/providerToolCalls'
import { expect } from '@playwright/test'
import { cursorTest } from '../cursor-fixtures'
import { chooseQuestionPages, restoreQuestionSelection } from '../helpers/nativeQuestion'
import { exerciseQuestionIsolation } from '../helpers/nativeQuestionIsolation'
import { askUserQuestionToolCall } from '../helpers/providerToolCalls'
import { assistantBubbles, composerEditor, controlButton, sendMessage, waitForAgentIdle, waitForControlBanner } from '../helpers/ui'

// Cursor's service answers the question itself in the same Run exchange, so the turn is one scripted step and the
// answer is the service's own words for the chosen option.
cursorTest('sends the selected question answer to the native Run stream', async ({ native }) => {
  const { page, modelScript } = native
  const start = await modelScript.queue({ toolCalls: [askUserQuestionToolCall(native.provider, 'cursor-color', [{
    header: 'Color',
    question: 'Which color should I use?',
    options: [{ label: 'Blue', description: 'Use blue.' }, { label: 'Green', description: 'Use green.' }],
  }])] })
  await sendMessage(page, modelScript.prompt('Ask which color to use.'))
  await modelScript.waitForSteps(start + 1)
  const banner = await waitForControlBanner(page)
  await expect(banner).toContainText('Which color should I use?')
  await banner.getByTestId('question-option-Blue').click()
  await restoreQuestionSelection(native, banner, 'Blue')
  await controlButton(page, 'submit').click()

  await waitForAgentIdle(page)
  await expect(assistantBubbles(page).filter({ hasText: 'Cursor question selected: option-1-1' }).first()).toBeVisible()
  await page.reload()
  await expect(assistantBubbles(page).filter({ hasText: 'Cursor question selected: option-1-1' }).first()).toBeVisible()
})

for (const singleQuestion of [false, true]) {
  cursorTest(singleQuestion ? 'keeps custom answers unavailable on the native ACP path' : 'returns multiple native answers after their pages survive reload', async ({ native }) => {
    const questions: QuestionRequest[] = [
      { header: 'Color', question: 'Choose a native color.', options: [{ label: 'Blue', description: 'Use blue.' }, { label: 'Green', description: 'Use green.' }] },
      ...singleQuestion ? [] : [{ header: 'Size', question: 'Choose native sizes.', multiSelect: true, options: [{ label: 'Small', description: 'Use small.' }, { label: 'Medium', description: 'Use medium.' }, { label: 'Large', description: 'Use large.' }] }],
    ]
    const start = await native.modelScript.queue({ toolCalls: [askUserQuestionToolCall(native.provider, 'cursor-pages', questions)] })
    await sendMessage(native.page, native.modelScript.prompt('Ask the native questions.'))
    await native.modelScript.waitForSteps(start + 1)
    const banner = await waitForControlBanner(native.page)
    if (singleQuestion) {
      await expect(banner).toContainText('This agent accepts selected options only.')
      await expect(composerEditor(native.page)).toHaveCount(0)
      await expect(controlButton(native.page, 'submit')).toBeDisabled()
    }
    await chooseQuestionPages(native, questions, singleQuestion ? [{ labels: ['Green'] }] : [{ labels: ['Green'] }, { labels: ['Large', 'Small'] }])(banner)
    await waitForAgentIdle(native.page)
    const expected = singleQuestion ? 'Cursor question selected: option-1-2' : 'Cursor question selected: option-1-2, option-2-3, option-2-1'
    const answer = assistantBubbles(native.page).filter({ hasText: expected }).first()
    await expect(answer).toBeVisible()
    await native.page.reload()
    await expect(answer).toBeVisible()
    await expect(answer).not.toContainText('option-1-1')
    await expect(answer).not.toContainText('option-2-2')
  })
}

cursorTest('keeps a native question on its owner tab and workspace after reload', async ({ native }) => {
  await exerciseQuestionIsolation(native, { customAnswerDraft: false })
})
