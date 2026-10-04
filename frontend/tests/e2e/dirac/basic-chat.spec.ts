import { DIRAC_E2E_SKIP_REASON, diracTest, expect, openDiracAgent } from '../dirac-fixtures'
import { exerciseBasicChat } from '../helpers/nativeConversation'
import { diracRespondToolCall } from '../helpers/providerToolCalls'
import { assistantBubbles, openWorkspace, sendMessage, waitForAgentIdle } from '../helpers/ui'
import { nativeContext } from './scenarios'

diracTest.describe('Dirac Basic Chat', () => {
  diracTest.skip(!!DIRAC_E2E_SKIP_REASON, DIRAC_E2E_SKIP_REASON || '')

  diracTest('send message and receive response', async ({ authenticatedDiracWorkspace, page, modelScript }) => {
    void authenticatedDiracWorkspace
    await modelScript.queue({ toolCalls: [diracRespondToolCall('dirac-respond', 'complete', 'Hello from the mock model.')] })
    await sendMessage(page, modelScript.prompt('Say hello.'))
    await waitForAgentIdle(page, 120_000)
    await expect(assistantBubbles(page).filter({ hasText: 'Hello from the mock model.' }).first()).toBeVisible()
  })
})

diracTest.describe('Dirac settings', () => {
  diracTest.skip(!!DIRAC_E2E_SKIP_REASON, DIRAC_E2E_SKIP_REASON || '')

  diracTest('a turn completes through the respond tool', async ({ page, authenticatedEmptyWorkspace, leapmuxServer, modelScript }) => {
    await openDiracAgent(leapmuxServer, authenticatedEmptyWorkspace.workspaceId)
    await openWorkspace(page, authenticatedEmptyWorkspace.workspaceId)

    await modelScript.queue({ toolCalls: [diracRespondToolCall('dirac-respond', 'complete', 'Turn complete.')] })
    await sendMessage(page, modelScript.prompt('Finish the turn.'))
    await waitForAgentIdle(page, 120_000)
    await expect(page.getByText('Turn complete.').first()).toBeVisible()
  })
})

diracTest('ends the actual native turn and keeps its answer after reload', async ({ authenticatedDiracWorkspace, page, modelScript, leapmuxServer }) => {
  const context = await nativeContext({ page, modelScript, leapmuxServer, workspaceId: authenticatedDiracWorkspace.workspaceId })
  await exerciseBasicChat(context)
})
