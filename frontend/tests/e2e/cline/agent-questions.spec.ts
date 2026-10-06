import { expect } from '@playwright/test'

import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { clineTest } from '../cline-fixtures'
import { askUserQuestionToolCall } from '../helpers/providerToolCalls'
import { assistantBubbles, sendMessage, visibleControlBanner, waitForAgentIdle } from '../helpers/ui'

/**
 * A real native question tool opens the shared question controls. The selected answer must reach the native model.
 *
 * The Worker starts one private Cline hub for this agent. Cline's DeepSeek provider sends requests to the isolated mock.
 *
 * The Worker answers Cline's native question executor. Its reply must reach the same native call.
 */
const PROVIDER = AgentProvider.CLINE

/**
 * Read the native tool result from the tool message in the Chat Completions request.
 * The whole body also contains the earlier call arguments. Those arguments include every option label, regardless of the selected answer.
 */
function toolResult(body: unknown, toolCallId: string): string {
  const messages = (body as { messages?: { role?: string, tool_call_id?: string, content?: unknown }[] } | undefined)?.messages ?? []
  const results = messages.filter(message => message.role === 'tool' && message.tool_call_id === toolCallId)
  expect(results, `the request carries the result of ${toolCallId}`).toHaveLength(1)
  return JSON.stringify(results[0]!.content)
}

clineTest.describe('Cline control requests', () => {
  clineTest('answers a question with the option the reader picks', async ({ askingClineWorkspace, page, modelScript }) => {
    void askingClineWorkspace
    await modelScript.queue(
      {
        toolCalls: [askUserQuestionToolCall(PROVIDER, 'cline-question', [{
          question: 'Which database?',
          header: 'Database',
          options: [{ label: 'Postgres', description: 'Relational' }, { label: 'Redis', description: 'In-memory' }],
        }])],
      },
      { text: 'Redis it is.' },
    )
    await sendMessage(page, modelScript.prompt('Ask me for a database.'))
    await modelScript.waitForSteps(1)
    await expect(visibleControlBanner(page)).toContainText('Which database?')
    // The SECOND option: a result that states the first option, or no option at
    // all, fails the check below.
    await page.locator('[data-testid="question-option-Redis"]:visible').click()
    const submit = page.locator('[data-testid="control-submit-btn"]:visible')
    await expect(submit).toBeEnabled()
    await submit.click()
    await expect(visibleControlBanner(page)).toHaveCount(0)

    const status = await modelScript.waitForSteps()
    await waitForAgentIdle(page)
    // The worker answered Cline's question executor, and Cline gave the answer to the
    // model as the question's result.
    const followUp = status.requests.find(request => request.stepIndex === 1)
    const result = toolResult(followUp?.body, 'cline-question')
    expect(result).toContain('Redis')
    expect(result).not.toContain('Postgres')
    await expect(assistantBubbles(page).filter({ hasText: 'Redis it is.' })).toBeVisible()
  })
})
