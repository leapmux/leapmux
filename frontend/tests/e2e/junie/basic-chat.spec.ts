import { exerciseBasicChat } from '../helpers/nativeConversation'
import { junieAnswerToolCall } from '../helpers/providerToolCalls'
import { assistantBubbles, sendMessage, waitForAgentIdle } from '../helpers/ui'
import { expect, JUNIE_E2E_SKIP_REASON, junieTest } from '../junie-fixtures'
import { nativeContext } from './scenarios'

junieTest.describe('Junie basic chat', () => {
  junieTest.skip(!!JUNIE_E2E_SKIP_REASON, JUNIE_E2E_SKIP_REASON || '')

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
    await waitForAgentIdle(page)
    await expect(assistantBubbles(page).filter({ hasText: 'Hello from the mock model.' }).first()).toBeVisible()
  })
})

junieTest('ends the actual native turn and keeps its answer after reload', async ({ authenticatedJunieWorkspace, page, modelScript, leapmuxServer }) => {
  const context = await nativeContext({ page, modelScript, leapmuxServer, workspaceId: authenticatedJunieWorkspace.workspaceId })
  await exerciseBasicChat(context)
})
