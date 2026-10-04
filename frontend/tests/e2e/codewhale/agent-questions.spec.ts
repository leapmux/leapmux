import type { Page } from '@playwright/test'
import type { ModelScript } from '../helpers/modelScriptFixture'
import type { QuestionRequest } from '../helpers/providerToolCalls'
import { expect } from '@playwright/test'
import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { CODEWHALE_E2E_SKIP_REASON, codewhaleTest } from '../codewhale-fixtures'
import { nativeToolResultAt } from '../helpers/nativeToolExecution'
import { askUserQuestionToolCall } from '../helpers/providerToolCalls'
import { assistantBubbles, sendMessage, waitForAgentIdle, waitForControlBanner } from '../helpers/ui'

codewhaleTest.skip(!!CODEWHALE_E2E_SKIP_REASON, CODEWHALE_E2E_SKIP_REASON || '')

const CODEWHALE = AgentProvider.CODEWHALE

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
 * Script a question and send the turn that asks it.
 *
 * `request_user_input` is a deferred tool: the runtime answers the first call
 * with its schema and runs nothing. The second call raises the banner, and the
 * third step is the model's answer after it reads the reply.
 */
async function askQuestions(page: Page, script: ModelScript, questions: QuestionRequest[], answer: string): Promise<void> {
  await script.queue(
    { toolCalls: [askUserQuestionToolCall(CODEWHALE, 'load-question', questions)] },
    { toolCalls: [askUserQuestionToolCall(CODEWHALE, 'ask-question', questions)] },
    { text: answer },
  )
  await sendMessage(page, script.prompt('Ask me about my drink.'))
  await script.waitForSteps(2)
}

async function clickOption(page: Page, label: string) {
  const option = page.locator(`[data-testid="question-option-${label}"]`)
  await expect(option).toBeVisible()
  await option.click()
}

codewhaleTest.describe('Codewhale questions', () => {
  codewhaleTest('sends the chosen option as the answer', async ({ authenticatedCodewhaleWorkspace, page, modelScript }) => {
    void authenticatedCodewhaleWorkspace
    await askQuestions(page, modelScript, [DRINK_Q], 'You prefer tea.')

    const banner = await waitForControlBanner(page)
    await expect(banner.getByText('Do you prefer tea or coffee?')).toBeVisible()
    await clickOption(page, 'Tea')
    const submit = page.locator('[data-testid="control-submit-btn"]')
    await expect(submit).toBeEnabled()
    await submit.click()
    await expect(page.locator('[data-testid="control-banner"]')).not.toBeVisible()

    await modelScript.waitForSteps()
    await waitForAgentIdle(page)
    const result = await nativeToolResultAt(modelScript, 2, 'ask-question')
    expect(result).toContain('Tea')
    expect(result).not.toContain('Coffee')
    await expect(assistantBubbles(page).filter({ hasText: 'You prefer tea.' })).toBeVisible()
  })

  codewhaleTest('answers two questions, one page each', async ({ authenticatedCodewhaleWorkspace, page, modelScript }) => {
    void authenticatedCodewhaleWorkspace
    await askQuestions(page, modelScript, [DRINK_Q, SIZE_Q], 'A large coffee.')

    const banner = await waitForControlBanner(page)
    await expect(page.locator('[data-testid="control-pagination"] button')).toHaveCount(2)
    await expect(banner.getByText('Do you prefer tea or coffee?')).toBeVisible()
    await clickOption(page, 'Coffee')
    await expect(banner.getByText('Which cup size?')).toBeVisible()
    await clickOption(page, 'Large')
    const submit = page.locator('[data-testid="control-submit-btn"]')
    await expect(submit).toBeEnabled()
    await submit.click()

    await modelScript.waitForSteps()
    await waitForAgentIdle(page)
    const result = await nativeToolResultAt(modelScript, 2, 'ask-question')
    expect(result).toContain('Coffee')
    expect(result).toContain('Large')
    expect(result).not.toContain('Small')
    await expect(assistantBubbles(page).filter({ hasText: 'A large coffee.' })).toBeVisible()
  })

  codewhaleTest('tells the model that the reader declined', async ({ authenticatedCodewhaleWorkspace, page, modelScript }) => {
    void authenticatedCodewhaleWorkspace
    await askQuestions(page, modelScript, [DRINK_Q], 'You declined to answer.')

    await waitForControlBanner(page)
    await page.locator('[data-testid="control-stop-btn"]').click()
    await expect(page.locator('[data-testid="control-banner"]')).not.toBeVisible()

    // The runtime offers no decline route. The Worker sends the reader's reason as free text for each question.
    // The model reads that text as the answer. The Stop button supplies its own reason.
    await modelScript.waitForSteps()
    await waitForAgentIdle(page)
    const result = await nativeToolResultAt(modelScript, 2, 'ask-question')
    expect(result).toContain('question_1')
    expect(result).toContain('Other')
    expect(result).toContain('User stopped')
    expect(result).not.toContain('Tea')
  })
})
