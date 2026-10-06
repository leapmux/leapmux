import { expect } from '@playwright/test'

import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { askUserQuestionToolCall } from '../helpers/providerToolCalls'
import { controlBanner, sendMessage, waitForAgentIdle } from '../helpers/ui'

import { ohMyPiTest } from '../ohmypi-fixtures'

/**
 * A real native question tool opens the shared question controls. The selected answer must reach the native model.
 *
 * The Worker drives `omp --mode rpc-ui` through its JSON Lines protocol.
 *
 * Oh My Pi's ask tool sends a series of native dialogs. The Worker combines them into one question request.
 */
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

ohMyPiTest.describe('Oh My Pi control requests', () => {
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

    await expect(controlBanner(page)).toContainText('Choose a style')
    await expect(controlBanner(page).getByText('Use the second style.', { exact: true })).toBeVisible()
    await controlBanner(page).getByTestId('question-option-Beta').click()
    await page.getByTestId('control-submit-btn').filter({ visible: true }).click()

    await modelScript.waitForSteps()
    await waitForAgentIdle(page)
    await expect(controlBanner(page)).toHaveCount(0)
    // The dialog reply reaches Oh My Pi, which sends the selected label to the model.
    // Read that label from the second request's tool result. The request also contains the call arguments, which include every option.
    // Only the tool result proves the selected answer.
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
