import { expect, FAST_AGENT_E2E_SKIP_REASON, fastAgentTest, openFastAgentAgent } from './fastagent-fixtures'
import { openSettingsMenu, openWorkspace, waitForSettingsHydrated } from './helpers/ui'

fastAgentTest.skip(!!FAST_AGENT_E2E_SKIP_REASON, FAST_AGENT_E2E_SKIP_REASON || '')

fastAgentTest.describe('Fast Agent settings', () => {
  // fast-agent reports its one `agent` mode on the session and offers no
  // per-session model switch over ACP (`set_config_option` raises
  // `method_not_found`). The settings panel therefore shows the mode and the
  // model with no editable axes beyond them.
  fastAgentTest('the settings menu shows the agent mode', async ({ page, authenticatedEmptyWorkspace, leapmuxServer }) => {
    await openFastAgentAgent(leapmuxServer, authenticatedEmptyWorkspace.workspaceId)
    await openWorkspace(page, authenticatedEmptyWorkspace.workspaceId)
    await waitForSettingsHydrated(page)

    const group = await openSettingsMenu(page, 'permissionMode')
    await expect(group.locator('[data-testid="permissionMode-agent"] input[type="radio"]')).toBeChecked()
  })
})
