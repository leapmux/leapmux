import { expect, fastAgentTest, openFastAgentAgent } from '../fastagent-fixtures'
import { FAST_AGENT_MOCK_MODEL } from '../helpers/mockAgentEnvironment'
import { closeComposerMenus, openPlusMenu, openWorkspace, sendMessage, settingsGroupTrigger, waitForAgentIdle, waitForSettingsHydrated } from '../helpers/ui'
import { expectMissingOptionGroup } from '../helpers/unsupportedConfiguration'
import { nativeContext } from './scenarios'

fastAgentTest.describe('Fast Agent settings apply', () => {
  fastAgentTest('omits a model setting while the launch model answers a turn', async ({ page, authenticatedEmptyWorkspace, leapmuxServer, modelScript }) => {
    await openFastAgentAgent(leapmuxServer, authenticatedEmptyWorkspace.workspaceId)
    await openWorkspace(page, authenticatedEmptyWorkspace.workspaceId)
    await waitForSettingsHydrated(page, 'permissionMode')
    await openPlusMenu(page)
    await expect(settingsGroupTrigger(page, 'model')).toHaveCount(0)
    await closeComposerMenus(page)
    const context = await nativeContext({ page, modelScript, leapmuxServer, workspaceId: authenticatedEmptyWorkspace.workspaceId })
    await expectMissingOptionGroup(context, {
      groupId: 'model',
      relatedProof: async () => {
        await modelScript.queue({ text: 'The launch model answered.' })
        await sendMessage(page, modelScript.prompt('Reply once with the launch model.'))
        const status = await modelScript.waitForSteps()
        await waitForAgentIdle(page)
        expect(status.requests.filter(request => request.stepIndex === 0)).toHaveLength(1)
        expect(status.requests.find(request => request.stepIndex === 0)?.body).toMatchObject({ model: FAST_AGENT_MOCK_MODEL })
      },
    })

    await openPlusMenu(page)
    await expect(settingsGroupTrigger(page, 'model')).toHaveCount(0)
    await closeComposerMenus(page)
  })
})
