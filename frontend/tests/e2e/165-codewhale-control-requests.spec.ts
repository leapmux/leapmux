import type { Page } from '@playwright/test'
import type { ModelScript } from './helpers/modelScriptFixture'
import type { QuestionRequest } from './helpers/providerToolCalls'
import { AgentProvider } from '../../src/generated/proto/leapmux/v1/agent_pb'
import { CODEWHALE_E2E_SKIP_REASON, codewhaleTest, codewhaleToolMessages, expect, expectCodewhalePosture } from './codewhale-fixtures'
import { askUserQuestionToolCall, bashToolCall } from './helpers/providerToolCalls'
import { assistantBubbles, sendMessage, waitForAgentIdle, waitForControlBanner } from './helpers/ui'

codewhaleTest.skip(!!CODEWHALE_E2E_SKIP_REASON, CODEWHALE_E2E_SKIP_REASON || '')

const CODEWHALE = AgentProvider.CODEWHALE

/**
 * The content of the last tool result that the model read, as JSON text.
 *
 * Read off the `tool` message of the last Chat Completions request. The whole
 * body is not enough: the earlier tool call repeats its own arguments there,
 * so an option label is in the body whatever the reader chose.
 */
async function lastToolResult(script: ModelScript): Promise<string> {
  const { requests } = await script.status()
  const body = requests.at(-1)?.body as { messages?: { role?: string, content?: unknown }[] } | undefined
  const results = (body?.messages ?? []).filter(message => message.role === 'tool')
  expect(results, 'the last request carries a tool result').not.toHaveLength(0)
  return JSON.stringify(results.at(-1)!.content)
}

codewhaleTest.describe('Codewhale approvals', () => {
  // The Ask posture asks before a command that writes. A read-only command runs
  // without a banner, which is why each command below creates a file.
  //
  // Each command that must RUN prints a number that its own text does not state,
  // such as `approved-42` from `approved-$((40 + 2))`. The row's header shows the
  // command, so a marker that the command text holds matches the row whether or
  // not the command ran.
  codewhaleTest('runs a command that the reader allows', async ({ authenticatedCodewhaleWorkspace, page, modelScript }) => {
    void authenticatedCodewhaleWorkspace
    await modelScript.queue(
      { toolCalls: [bashToolCall(CODEWHALE, 'allow-call', 'touch approved.txt && echo "approved-$((40 + 2))"')] },
      { text: 'I created approved.txt.' },
    )
    await sendMessage(page, modelScript.prompt('Create approved.txt.'))
    await modelScript.waitForSteps(1)

    // The approval states no arguments of its own. The banner draws the command
    // from the call that the runtime reported before it asked.
    const banner = await waitForControlBanner(page)
    await expect(banner).toContainText('touch approved.txt')
    await page.getByTestId('control-allow-btn').click()
    await expect(page.locator('[data-testid="control-banner"]')).not.toBeVisible()

    await modelScript.waitForSteps()
    await waitForAgentIdle(page)
    await expect(codewhaleToolMessages(page).filter({ hasText: 'approved-42' }).first()).toBeVisible()
    await expect(assistantBubbles(page).filter({ hasText: 'I created approved.txt.' })).toBeVisible()
  })

  codewhaleTest('refuses a command that the reader denies', async ({ authenticatedCodewhaleWorkspace, page, modelScript }) => {
    void authenticatedCodewhaleWorkspace
    await modelScript.queue(
      { toolCalls: [bashToolCall(CODEWHALE, 'deny-call', 'touch denied.txt')] },
      { text: 'The command was not approved.' },
    )
    await sendMessage(page, modelScript.prompt('Create denied.txt.'))
    await modelScript.waitForSteps(1)

    const banner = await waitForControlBanner(page)
    await expect(banner).toContainText('touch denied.txt')
    await page.getByTestId('control-deny-btn').click()
    await expect(page.locator('[data-testid="control-banner"]')).not.toBeVisible()

    await modelScript.waitForSteps()
    await waitForAgentIdle(page)
    // The runtime fails the call, and the model reads why.
    expect(await lastToolResult(modelScript)).toContain('denied by user')
    await expect(assistantBubbles(page).filter({ hasText: 'The command was not approved.' })).toBeVisible()
  })

  codewhaleTest('applies the bypass pill when the reader allows', async ({ authenticatedCodewhaleWorkspace, page, modelScript }) => {
    void authenticatedCodewhaleWorkspace
    await modelScript.queue(
      { toolCalls: [bashToolCall(CODEWHALE, 'bypass-call', 'touch bypass.txt && echo "bypass-$((40 + 2))"')] },
      { text: 'I created bypass.txt.' },
    )
    await sendMessage(page, modelScript.prompt('Create bypass.txt.'))
    await modelScript.waitForSteps(1)

    await waitForControlBanner(page)
    const pills = page.getByRole('radiogroup', { name: 'Permissions' })
    await expect(pills.getByRole('radio', { name: 'Unchanged' })).toBeChecked()
    const bypass = pills.getByRole('radio', { name: 'Bypass' })
    await bypass.click()
    await expect(bypass).toBeChecked()
    await page.getByTestId('control-allow-btn').click()
    await expect(page.locator('[data-testid="control-banner"]')).not.toBeVisible()
    await modelScript.waitForSteps()
    await waitForAgentIdle(page)
    await expect(codewhaleToolMessages(page).filter({ hasText: 'bypass-42' }).first()).toBeVisible()
    // The same posture that the composer menu's bypass shortcut lands on.
    await expectCodewhalePosture(page, 'full_access')

    // Full Access runs a command that writes without asking.
    await modelScript.queue(
      { toolCalls: [bashToolCall(CODEWHALE, 'unasked-call', 'touch unasked.txt && echo "unasked-$((40 + 2))"')] },
      { text: 'I created unasked.txt.' },
    )
    await sendMessage(page, modelScript.prompt('Create unasked.txt.'))
    await modelScript.waitForSteps()
    await waitForAgentIdle(page)
    await expect(codewhaleToolMessages(page).filter({ hasText: 'unasked-42' }).first()).toBeVisible()
    await expect(page.locator('[data-testid="control-banner"]')).toHaveCount(0)
  })
})

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
    const result = await lastToolResult(modelScript)
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
    const result = await lastToolResult(modelScript)
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

    // The runtime has no decline route, so the worker answers each question
    // with the reader's reason as free text, and the model reads it as the
    // answer. The Stop button states its own reason.
    await modelScript.waitForSteps()
    await waitForAgentIdle(page)
    const result = await lastToolResult(modelScript)
    expect(result).toContain('question_1')
    expect(result).toContain('Other')
    expect(result).toContain('User stopped')
    expect(result).not.toContain('Tea')
  })
})
