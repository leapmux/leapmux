import { expect } from '@playwright/test'

import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { nativeToolResult } from '../helpers/nativeToolResult'
import { askUserQuestionToolCall } from '../helpers/providerToolCalls'
import { messageContents, savedControlAnswer, sendMessage, waitForAgentIdle } from '../helpers/ui'

import { MIMO_E2E_SKIP_REASON, mimoTest } from '../mimo-fixtures'

mimoTest.skip(!!MIMO_E2E_SKIP_REASON, MIMO_E2E_SKIP_REASON || '')

mimoTest.describe('MiMo Code questions', () => {
  const questions = [{
    question: 'Choose a style',
    header: 'Style',
    options: [
      { label: 'Alpha', description: 'Use the first style.' },
      { label: 'Beta', description: 'Use the second style.' },
    ],
  }]

  mimoTest('delivers the selected answer to the question tool', async ({ authenticatedMiMoWorkspace, page, modelScript }) => {
    void authenticatedMiMoWorkspace
    await modelScript.queue(
      { toolCalls: [askUserQuestionToolCall(AgentProvider.MIMO_CODE, 'style-question', questions)] },
      { text: 'Recorded the style.' },
    )
    await sendMessage(page, modelScript.prompt('Ask me which style to use.'))
    await modelScript.waitForSteps(1)

    const banner = page.getByTestId('control-banner').filter({ visible: true })
    await expect(banner).toContainText('Choose a style')
    await expect(banner.getByText('Use the second style.', { exact: true })).toBeVisible()
    await banner.getByTestId('question-option-Beta').click()
    await page.getByTestId('control-submit-btn').click()
    await expect(banner).toHaveCount(0)
    const status = await modelScript.waitForSteps()
    await waitForAgentIdle(page, 120_000)
    const answer = nativeToolResult(status.requests.find(request => request.stepIndex === 1), 'style-question')
    expect(answer).toContain('Beta')
    expect(answer).not.toContain('Alpha')

    // The question row reads the answer out of MiMo's own result, under the
    // question's header, and the saved answer states it too.
    await expect(messageContents(page).filter({ hasText: /Style\s*—\s*Beta/ }).first()).toBeVisible()
    await expect(savedControlAnswer(page)).toHaveText('Style: Beta')
    await expect(messageContents(page).filter({ hasText: 'Recorded the style.' }).first()).toBeVisible()
  })

  // MiMo accepts one answer list for each question.
  // A multi-select answer contains the selected options. A composer answer contains the reader's typed text.
  mimoTest('delivers several choices and typed words, one answer for each question', async ({ authenticatedMiMoWorkspace, page, modelScript }) => {
    void authenticatedMiMoWorkspace
    await modelScript.queue(
      {
        toolCalls: [askUserQuestionToolCall(AgentProvider.MIMO_CODE, 'pizza-questions', [
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
        ])],
      },
      { text: 'Recorded the pizza.' },
    )
    await sendMessage(page, modelScript.prompt('Ask me about the pizza.'))
    await modelScript.waitForSteps(1)

    const banner = page.getByTestId('control-banner').filter({ visible: true })
    await expect(banner).toContainText('Choose the toppings')
    await banner.getByTestId('question-option-Cheese').click()
    await banner.getByTestId('question-option-Basil').click()
    // A multi-select question stays on its page, so the reader moves on by hand.
    await page.getByTestId('control-pagination').locator('button').nth(1).click()
    await expect(banner).toContainText('Name the pizza')
    await page.getByTestId('composer-editor').locator('.ProseMirror').fill('Garden Special')
    const submit = page.getByTestId('control-submit-btn')
    await expect(submit).toBeEnabled()
    await submit.click()
    await expect(banner).toHaveCount(0)
    const status = await modelScript.waitForSteps()
    await waitForAgentIdle(page, 120_000)
    const answer = nativeToolResult(status.requests.find(request => request.stepIndex === 1), 'pizza-questions')
    expect(answer).toContain('Cheese')
    expect(answer).toContain('Basil')
    expect(answer).toContain('Garden Special')
    expect(answer).not.toContain('Olives')

    await expect(messageContents(page).filter({ hasText: /Toppings\s*—\s*Cheese, Basil/ }).first()).toBeVisible()
    await expect(messageContents(page).filter({ hasText: /Name\s*—\s*Garden Special/ }).first()).toBeVisible()
    await expect(savedControlAnswer(page)).toContainText('Toppings: Cheese, Basil')
    await expect(savedControlAnswer(page)).toContainText('Name: Garden Special')
    await expect(messageContents(page).filter({ hasText: 'Recorded the pizza.' }).first()).toBeVisible()
  })

  // Stop dismisses the question. A dismissal stops MiMo's loop, as a rejected
  // permission does.
  mimoTest('dismisses the question without an answer', async ({ authenticatedMiMoWorkspace, page, modelScript }) => {
    void authenticatedMiMoWorkspace
    await modelScript.queue({ toolCalls: [askUserQuestionToolCall(AgentProvider.MIMO_CODE, 'style-question', questions)] })
    await sendMessage(page, modelScript.prompt('Ask me which style to use.'))
    await modelScript.waitForSteps()

    const banner = page.getByTestId('control-banner').filter({ visible: true })
    await expect(banner).toContainText('Choose a style')
    await page.getByTestId('control-stop-btn').click()
    await expect(banner).toHaveCount(0)
    await waitForAgentIdle(page, 120_000)
    await expect(savedControlAnswer(page)).toHaveText('Dismissed')
  })
})
