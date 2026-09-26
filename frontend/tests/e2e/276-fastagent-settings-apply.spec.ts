import { expect, FAST_AGENT_E2E_SKIP_REASON, fastAgentTest, openFastAgentAgent } from './fastagent-fixtures'
import { FAST_AGENT_MOCK_MODEL } from './helpers/mockAgentEnvironment'
import { openSettingsMenu, openWorkspace, waitForSettingsHydrated } from './helpers/ui'

fastAgentTest.skip(!!FAST_AGENT_E2E_SKIP_REASON, FAST_AGENT_E2E_SKIP_REASON || '')

/**
 * 276 — Fast Agent settings apply.
 *
 * Fast Agent reports one `agent` mode and fixes its model at session creation
 * (matrix note 31; `set_config_option` raises `method_not_found`), so there is
 * no second value to switch to. The settings the session shows survive a
 * reload: the one mode stays chosen and the model group still lists the
 * pinned model.
 */
fastAgentTest.describe('Fast Agent settings apply', () => {
  fastAgentTest('keeps the chosen mode and the model listing after reload', async ({ page, authenticatedEmptyWorkspace, leapmuxServer }) => {
    await openFastAgentAgent(leapmuxServer, authenticatedEmptyWorkspace.workspaceId)
    await openWorkspace(page, authenticatedEmptyWorkspace.workspaceId)
    await waitForSettingsHydrated(page)

    const modeGroup = await openSettingsMenu(page, 'permissionMode')
    await expect(modeGroup.locator('[data-testid="permissionMode-agent"] input[type="radio"]')).toBeChecked()
    const modelGroup = await openSettingsMenu(page, 'model')
    await expect(modelGroup.locator(`[data-testid="model-${FAST_AGENT_MOCK_MODEL}"]`)).toBeVisible()

    await page.reload()
    await waitForSettingsHydrated(page)
    const reloadedModeGroup = await openSettingsMenu(page, 'permissionMode')
    await expect(reloadedModeGroup.locator('[data-testid="permissionMode-agent"] input[type="radio"]')).toBeChecked()
    const reloadedModelGroup = await openSettingsMenu(page, 'model')
    await expect(reloadedModelGroup.locator(`[data-testid="model-${FAST_AGENT_MOCK_MODEL}"]`)).toBeVisible()
  })
})
