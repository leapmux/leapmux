import { expect, FAST_AGENT_E2E_SKIP_REASON, fastAgentTest, openFastAgentAgent } from './fastagent-fixtures'
import { FAST_AGENT_MOCK_MODEL } from './helpers/mockAgentEnvironment'
import { closeComposerMenus, openPlusMenu, openSettingsMenu, openWorkspace, sendMessage, settingsGroupTrigger, waitForAgentIdle, waitForSettingsHydrated } from './helpers/ui'

fastAgentTest.skip(!!FAST_AGENT_E2E_SKIP_REASON, FAST_AGENT_E2E_SKIP_REASON || '')

/**
 * 276 — Fast Agent settings apply.
 *
 * Fast Agent reports one `agent` mode. Its model is a launch flag, and the ACP
 * session offers no model catalog or model setting. The mode survives a reload.
 */
fastAgentTest.describe('Fast Agent settings apply', () => {
  fastAgentTest('keeps the chosen agent mode after reload', async ({ page, authenticatedEmptyWorkspace, leapmuxServer }) => {
    await openFastAgentAgent(leapmuxServer, authenticatedEmptyWorkspace.workspaceId)
    await openWorkspace(page, authenticatedEmptyWorkspace.workspaceId)
    await waitForSettingsHydrated(page, 'permissionMode')

    const modeGroup = await openSettingsMenu(page, 'permissionMode')
    await expect(modeGroup.locator('[data-testid="permissionMode-agent"] input[type="radio"]')).toBeChecked()
    await page.reload()
    await waitForSettingsHydrated(page, 'permissionMode')
    const reloadedModeGroup = await openSettingsMenu(page, 'permissionMode')
    await expect(reloadedModeGroup.locator('[data-testid="permissionMode-agent"] input[type="radio"]')).toBeChecked()
  })

  fastAgentTest('omits a model setting while the launch model answers a turn', async ({ page, authenticatedEmptyWorkspace, leapmuxServer, modelScript }) => {
    await openFastAgentAgent(leapmuxServer, authenticatedEmptyWorkspace.workspaceId)
    await openWorkspace(page, authenticatedEmptyWorkspace.workspaceId)
    await waitForSettingsHydrated(page, 'permissionMode')
    await openPlusMenu(page)
    await expect(settingsGroupTrigger(page, 'model')).toHaveCount(0)
    await closeComposerMenus(page)
    await modelScript.queue({ text: 'The launch model answered.' })
    await sendMessage(page, modelScript.prompt('Reply once with the launch model.'))
    const status = await modelScript.waitForSteps()
    await waitForAgentIdle(page, 120_000)
    expect(status.requests.filter(request => request.stepIndex === 0)).toHaveLength(1)
    expect(status.requests.find(request => request.stepIndex === 0)?.body).toMatchObject({ model: FAST_AGENT_MOCK_MODEL })

    await openPlusMenu(page)
    await expect(settingsGroupTrigger(page, 'model')).toHaveCount(0)
    await closeComposerMenus(page)
  })
})
