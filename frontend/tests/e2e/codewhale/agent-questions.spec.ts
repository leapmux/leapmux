import type { Locator } from '@playwright/test'
import type { NativeScenarioContext } from '../helpers/nativeScenario'
import type { QuestionRequest } from '../helpers/providerToolCalls'
import { expect } from '@playwright/test'
import { codewhaleTest } from '../codewhale-fixtures'
import { nativeToolResultAt } from '../helpers/nativeToolExecution'
import { askUserQuestionToolCall } from '../helpers/providerToolCalls'
import { assistantBubbles, controlButton, expectNoControlBanner, questionPagination, sendMessage, waitForAgentIdle, waitForControlBanner } from '../helpers/ui'

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

/**
 * Script a question, send the turn that asks it, and return the step index of the answer that follows the reply.
 *
 * `request_user_input` is a deferred tool: the runtime answers the first call
 * with its schema and runs nothing. The second call raises the banner, and the
 * third step is the model's answer after it reads the reply. The shared question
 * turn scripts one question call, so this turn stays here.
 */
async function askQuestions(context: NativeScenarioContext, questions: QuestionRequest[], answer: string): Promise<number> {
  const start = await context.modelScript.queue(
    { toolCalls: [askUserQuestionToolCall(context.provider, 'load-question', questions)] },
    { toolCalls: [askUserQuestionToolCall(context.provider, 'ask-question', questions)] },
    { text: answer },
  )
  await sendMessage(context.page, context.modelScript.prompt('Ask me about my drink.'))
  await context.modelScript.waitForSteps(start + 2)
  return start + 2
}

/** Pick the option `label` inside the banner. */
async function clickOption(banner: Locator, label: string) {
  const option = banner.getByTestId(`question-option-${label}`)
  await expect(option).toBeVisible()
  await option.click()
}

codewhaleTest.describe('Codewhale questions', () => {
  codewhaleTest('sends the chosen option as the answer', async ({ native }) => {
    const { page, modelScript } = native
    const answerStep = await askQuestions(native, [DRINK_Q], 'You prefer tea.')

    const banner = await waitForControlBanner(page)
    await expect(banner.getByText('Do you prefer tea or coffee?')).toBeVisible()
    await clickOption(banner, 'Tea')
    const submit = controlButton(page, 'submit')
    await expect(submit).toBeEnabled()
    await submit.click()
    await expectNoControlBanner(page)

    await modelScript.waitForSteps(answerStep + 1)
    await waitForAgentIdle(page)
    const result = await nativeToolResultAt(modelScript, answerStep, 'ask-question')
    expect(result).toContain('Tea')
    expect(result).not.toContain('Coffee')
    await expect(assistantBubbles(page).filter({ hasText: 'You prefer tea.' })).toBeVisible()
  })

  codewhaleTest('answers two questions, one page each', async ({ native }) => {
    const { page, modelScript } = native
    const answerStep = await askQuestions(native, [DRINK_Q, SIZE_Q], 'A large coffee.')

    const banner = await waitForControlBanner(page)
    await expect(questionPagination(page).locator('button')).toHaveCount(2)
    await expect(banner.getByText('Do you prefer tea or coffee?')).toBeVisible()
    await clickOption(banner, 'Coffee')
    await expect(banner.getByText('Which cup size?')).toBeVisible()
    await clickOption(banner, 'Large')
    const submit = controlButton(page, 'submit')
    await expect(submit).toBeEnabled()
    await submit.click()

    await modelScript.waitForSteps(answerStep + 1)
    await waitForAgentIdle(page)
    const result = await nativeToolResultAt(modelScript, answerStep, 'ask-question')
    expect(result).toContain('Coffee')
    expect(result).toContain('Large')
    expect(result).not.toContain('Small')
    await expect(assistantBubbles(page).filter({ hasText: 'A large coffee.' })).toBeVisible()
  })

  codewhaleTest('tells the model that the reader declined', async ({ native }) => {
    const { page, modelScript } = native
    const answerStep = await askQuestions(native, [DRINK_Q], 'You declined to answer.')

    await waitForControlBanner(page)
    await controlButton(page, 'stop').click()
    await expectNoControlBanner(page)

    // The runtime offers no decline route. The Worker sends the reader's reason as free text for each question.
    // The model reads that text as the answer. The Stop button supplies its own reason.
    await modelScript.waitForSteps(answerStep + 1)
    await waitForAgentIdle(page)
    const result = await nativeToolResultAt(modelScript, answerStep, 'ask-question')
    expect(result).toContain('question_1')
    expect(result).toContain('Other')
    expect(result).toContain('User stopped')
    expect(result).not.toContain('Tea')
  })
})
