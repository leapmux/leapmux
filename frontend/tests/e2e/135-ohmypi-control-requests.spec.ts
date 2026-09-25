import type { Page } from '@playwright/test'
import { AgentProvider } from '../../src/generated/proto/leapmux/v1/agent_pb'
import { askUserQuestionToolCall, bashToolCall } from './helpers/providerToolCalls'
import { messageContents, sendMessage, waitForAgentIdle } from './helpers/ui'
import { expect, OH_MY_PI_E2E_SKIP_REASON, ohMyPiTest } from './ohmypi-fixtures'

/**
 * 135 — Oh My Pi control requests.
 *
 * omp asks through its own `extension_ui_request` dialogs. In `write` mode it asks
 * before a command runs, with a `select` dialog whose options are Approve and Deny;
 * the banner draws them as the shared Allow and Deny. An `ask` call asks each
 * question through a chain of dialogs, which the worker bridges into one question
 * request.
 */
ohMyPiTest.skip(!!OH_MY_PI_E2E_SKIP_REASON, OH_MY_PI_E2E_SKIP_REASON || '')

/** The visible chat text, joined. */
async function chatText(page: Page): Promise<string> {
  return (await messageContents(page).allTextContents()).join(' ')
}

/**
 * The text of every tool result in one Chat Completions request, which is the
 * protocol that the E2E `models.yml` gives omp.
 */
function toolResultText(body: unknown): string {
  const messages = typeof body === 'object' && body !== null && Array.isArray((body as { messages?: unknown }).messages)
    ? (body as { messages: unknown[] }).messages
    : []
  return messages
    .filter(message => typeof message === 'object' && message !== null && (message as { role?: unknown }).role === 'tool')
    .map(message => JSON.stringify((message as { content?: unknown }).content ?? ''))
    .join('\n')
}

/** The control banner on screen. The chat renders each unmeasured row twice. */
function banner(page: Page) {
  return page.getByTestId('control-banner').filter({ visible: true })
}

ohMyPiTest.describe('Oh My Pi control requests', () => {
  ohMyPiTest('runs a command after the reader allows it', async ({ approvingOhMyPiWorkspace, page, modelScript }) => {
    void approvingOhMyPiWorkspace
    await modelScript.queue(
      { toolCalls: [bashToolCall(AgentProvider.OH_MY_PI, 'approve-call', 'echo "omp-$((40 + 2))"')] },
      { text: 'The command ran.' },
    )
    await sendMessage(page, modelScript.prompt('Run the arithmetic command.'))
    // omp asks before the call runs, so the second step waits on the banner.
    await modelScript.waitForSteps(1)

    // The banner states the command that omp asks about.
    await expect(banner(page)).toContainText('echo "omp-$((40 + 2))"')
    await page.getByTestId('control-allow-btn').filter({ visible: true }).click()

    await modelScript.waitForSteps()
    await waitForAgentIdle(page, 180_000)
    await expect(banner(page)).toHaveCount(0)
    // The command text states no `omp-42`, so only the command's own output can
    // put it on the page.
    await expect.poll(() => chatText(page)).toContain('omp-42')
  })

  ohMyPiTest('refuses a command that the reader denies', async ({ approvingOhMyPiWorkspace, page, modelScript }) => {
    void approvingOhMyPiWorkspace
    await modelScript.queue(
      { toolCalls: [bashToolCall(AgentProvider.OH_MY_PI, 'deny-call', 'echo "omp-$((50 + 5))"')] },
      { text: 'The command was refused.' },
    )
    await sendMessage(page, modelScript.prompt('Run the other arithmetic command.'))
    await modelScript.waitForSteps(1)

    await expect(banner(page)).toContainText('echo "omp-$((50 + 5))"')
    await page.getByTestId('control-deny-btn').filter({ visible: true }).click()

    await modelScript.waitForSteps()
    await waitForAgentIdle(page, 180_000)
    await expect(banner(page)).toHaveCount(0)
    // omp fails the call with its own words, and the command never runs.
    await expect.poll(() => chatText(page)).toContain('Tool call denied by user')
    expect(await chatText(page)).not.toContain('omp-55')
  })

  ohMyPiTest('delivers the answer to a question', async ({ authenticatedOhMyPiWorkspace, page, modelScript }) => {
    void authenticatedOhMyPiWorkspace
    await modelScript.queue(
      {
        toolCalls: [askUserQuestionToolCall(AgentProvider.OH_MY_PI, 'style-question', [{
          question: 'Choose a style',
          header: 'Style',
          options: [
            { label: 'Alpha', description: 'Use the first style.' },
            { label: 'Beta', description: 'Use the second style.' },
          ],
        }])],
      },
      { text: 'Recorded the style.' },
    )
    await sendMessage(page, modelScript.prompt('Ask me which style to use.'))
    await modelScript.waitForSteps(1)

    await expect(banner(page)).toContainText('Choose a style')
    await expect(banner(page).getByText('Use the second style.', { exact: true })).toBeVisible()
    await banner(page).getByTestId('question-option-Beta').click()
    await page.getByTestId('control-submit-btn').filter({ visible: true }).click()

    await modelScript.waitForSteps()
    await waitForAgentIdle(page, 180_000)
    await expect(banner(page)).toHaveCount(0)
    // The answer went back to omp through the dialog chain, and omp returned it to
    // the model: the tool result of the second request states the chosen label.
    // The request also replays the call's own arguments, which list every
    // option, so only the tool result proves the answer.
    const status = await modelScript.status()
    const followUp = status.requests.find(request => request.stepIndex === 1)
    expect(toolResultText(followUp?.body)).toContain('Beta')
    expect(toolResultText(followUp?.body)).not.toContain('Alpha')
    // The saved answer states the question and the chosen label, and exists only
    // after the answer. The question's own row lists every option before any
    // answer, so it cannot prove which one the reader chose.
    await expect(page.locator('[data-testid="control-response-text"]:visible')).toHaveText('Choose a style: Beta')
  })
})
