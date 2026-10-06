import { expect, fastAgentTest } from '../fastagent-fixtures'
import { exerciseBasicChat } from '../helpers/nativeConversation'
import { assistantBubbles, sendMessage, waitForAgentIdle } from '../helpers/ui'
import { nativeContext } from './scenarios'

fastAgentTest.describe('Fast Agent Basic Chat', () => {
  fastAgentTest('send message and receive response', async ({ authenticatedFastAgentWorkspace, page, modelScript }) => {
    void authenticatedFastAgentWorkspace
    await modelScript.queue({ text: 'Hello from the mock model.' })
    await sendMessage(page, modelScript.prompt('Say hello.'))
    await waitForAgentIdle(page)
    await expect(assistantBubbles(page).filter({ hasText: 'Hello from the mock model.' }).first()).toBeVisible()
  })
})

fastAgentTest('ends the actual native turn and keeps its answer after reload', async ({ authenticatedFastAgentWorkspace, page, modelScript, leapmuxServer }) => {
  const context = await nativeContext({ page, modelScript, leapmuxServer, workspaceId: authenticatedFastAgentWorkspace.workspaceId })
  await exerciseBasicChat(context)
})
