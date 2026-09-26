import { junieAnswerToolCall } from './helpers/providerToolCalls'
import { assistantBubbles, sendMessage, waitForAgentIdle } from './helpers/ui'
import { expect, JUNIE_E2E_SKIP_REASON, junieTest } from './junie-fixtures'

junieTest.skip(!!JUNIE_E2E_SKIP_REASON, JUNIE_E2E_SKIP_REASON || '')

/**
 * Junie answers THREE model calls per task: a capability filter, a task-name
 * summarizer and the main agent. The first two are housekeeping turns, so they
 * ride `rule` and never take the answer the test scripted for the main turn.
 * The main agent REJECTS a text-only reply and retries six times, so its
 * answers carry a tool call: `answer` is the one that states the answer text.
 */
junieTest.describe('Junie Basic Chat', () => {
  junieTest('send message and receive response', async ({ authenticatedJunieWorkspace, page, modelScript }) => {
    void authenticatedJunieWorkspace
    await modelScript.rule(
      {
        name: 'junie-capability-filter',
        when: { system: 'capability filter agent' },
        respond: { text: '' },
      },
      {
        name: 'junie-task-name',
        when: { system: 'task description summarizer' },
        respond: { text: 'Greeting task' },
      },
    )
    await modelScript.queue({ toolCalls: [junieAnswerToolCall('junie-answer', 'Hello from the mock model.')] })
    await sendMessage(page, modelScript.prompt('Say hello.'))
    await waitForAgentIdle(page, 120_000)
    await expect(assistantBubbles(page).filter({ hasText: 'Hello from the mock model.' }).first()).toBeVisible()
  })
})
