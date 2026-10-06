import type { Page } from '@playwright/test'
import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { droidTest, expect } from '../droid-fixtures'
import { nativeToolResult } from '../helpers/nativeToolResult'
import { askUserQuestionToolCall } from '../helpers/providerToolCalls'
import { savedControlAnswer, sendMessage, waitForAgentIdle } from '../helpers/ui'
import { nativeDroidCallId } from './toolResult'

droidTest.describe('Factory Droid control requests', () => {
  function banner(page: Page) {
    return page.getByTestId('control-banner').filter({ visible: true })
  }

  const PROVIDER = AgentProvider.DROID

  droidTest('answers a question through the shared question banner', async ({ askingDroidWorkspace, page, modelScript }) => {
    void askingDroidWorkspace
    await modelScript.queue(
      {
        toolCalls: [askUserQuestionToolCall(PROVIDER, 'ask-1', [
          { question: 'Which color do you prefer?', header: 'Color', options: [{ label: 'Blue', description: 'The color blue' }, { label: 'Red', description: 'The color red' }] },
        ])],
      },
      { text: 'The answer was recorded.' },
    )
    await sendMessage(page, modelScript.prompt('Ask me a question.'))
    await modelScript.waitForSteps(1)

    await expect(banner(page)).toContainText('Which color do you prefer?')
    await page.getByTestId('question-option-Red').filter({ visible: true }).first().click()
    await page.getByTestId('control-submit-btn').filter({ visible: true }).click()

    const status = await modelScript.waitForSteps()
    await waitForAgentIdle(page)
    const followUp = status.requests.find(request => request.stepIndex === 1)
    const nativeCallId = nativeDroidCallId(followUp, 'AskUser', 'ask-1')
    const answer = nativeToolResult(followUp, nativeCallId)
    // Droid reports each answer under the index of its question. Droid numbers its
    // questions from 1, and the reply keeps that number.
    expect(answer).toContain('1. [question] Which color do you prefer?')
    expect(answer).toContain('Red')
    expect(answer).not.toContain('Blue')
    await expect(banner(page)).toHaveCount(0)
    // The saved row reads the answer list of Droid's own reply.
    await expect(savedControlAnswer(page)).toHaveText('Which color do you prefer?: Red')
  })

  // Droid takes each answer as one string. A multiple-choice answer joins every pick
  // in the order of the options, as Droid's own TUI does. The reply lists the answers
  // in the order of the questions, each under the index of its question.
  droidTest('sends every pick of a multiple-choice question, one answer for each question', async ({ askingDroidWorkspace, page, modelScript }) => {
    void askingDroidWorkspace
    await modelScript.queue(
      {
        toolCalls: [askUserQuestionToolCall(PROVIDER, 'ask-multi', [
          {
            question: 'Which colors do you like?',
            header: 'Colors',
            multiSelect: true,
            options: [
              { label: 'Blue', description: 'The color blue' },
              { label: 'Green', description: 'The color green' },
              { label: 'Red', description: 'The color red' },
            ],
          },
          {
            question: 'Which size do you want?',
            header: 'Size',
            options: [{ label: 'Small', description: 'The small size' }, { label: 'Large', description: 'The large size' }],
          },
        ])],
      },
      { text: 'The answers were recorded.' },
    )
    await sendMessage(page, modelScript.prompt('Ask me about colors and sizes.'))
    await modelScript.waitForSteps(1)

    await expect(banner(page)).toContainText('Which colors do you like?')
    // Pick against the order of the options. The answer follows the options.
    await banner(page).getByTestId('question-option-Red').click()
    await banner(page).getByTestId('question-option-Blue').click()
    // A multiple-choice question stays on its page, so the reader moves on by hand.
    await page.getByTestId('control-pagination').filter({ visible: true }).locator('button').nth(1).click()
    await expect(banner(page)).toContainText('Which size do you want?')
    await banner(page).getByTestId('question-option-Large').click()
    const submit = page.getByTestId('control-submit-btn').filter({ visible: true })
    await expect(submit).toBeEnabled()
    await submit.click()
    await expect(banner(page)).toHaveCount(0)

    const status = await modelScript.waitForSteps()
    await waitForAgentIdle(page)
    const followUp = status.requests.find(request => request.stepIndex === 1)
    const answer = nativeToolResult(followUp, nativeDroidCallId(followUp, 'AskUser', 'ask-multi'))
    // Droid writes "<index>. [question] <question>", then "[answer] <answer>", for each
    // answer in the order of the reply. The parts must appear in exactly that order.
    const parts = [
      '1. [question] Which colors do you like?',
      '[answer] Blue, Red',
      '2. [question] Which size do you want?',
      '[answer] Large',
    ]
    for (const part of parts)
      expect(answer).toContain(part)
    const positions = parts.map(part => answer.indexOf(part))
    expect(positions).toEqual([...positions].sort((a, b) => a - b))
    expect(answer).not.toContain('Green')
    await expect(savedControlAnswer(page)).toContainText('Which colors do you like?: Blue, Red')
    await expect(savedControlAnswer(page)).toContainText('Which size do you want?: Large')
  })
})
