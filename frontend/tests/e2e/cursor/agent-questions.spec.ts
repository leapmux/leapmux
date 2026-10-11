import { expect } from '@playwright/test'
import { cursorTest } from '../cursor-fixtures'
import { askUserQuestionToolCall } from '../helpers/providerToolCalls'
import { assistantBubbles, controlButton, sendMessage, waitForAgentIdle, waitForControlBanner } from '../helpers/ui'

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
  await controlButton(page, 'submit').click()

  await waitForAgentIdle(page)
  await expect(assistantBubbles(page).filter({ hasText: 'Cursor question selected: option-1-1' }).first()).toBeVisible()
  await page.reload()
  await expect(assistantBubbles(page).filter({ hasText: 'Cursor question selected: option-1-1' }).first()).toBeVisible()
})
