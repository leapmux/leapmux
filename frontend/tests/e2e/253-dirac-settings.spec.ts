import { DIRAC_E2E_SKIP_REASON, diracTest, expect, openDiracAgent } from './dirac-fixtures'
import { diracRespondToolCall } from './helpers/providerToolCalls'
import { openSettingsMenu, openWorkspace, sendMessage, waitForAgentIdle, waitForSettingsHydrated } from './helpers/ui'

diracTest.skip(!!DIRAC_E2E_SKIP_REASON, DIRAC_E2E_SKIP_REASON || '')

diracTest.describe('Dirac settings', () => {
  // Dirac's modes are `plan` and `act`, on the permission-mode axis. A new
  // session runs `act`.
  diracTest('the mode menu lists Plan and Act', async ({ page, authenticatedEmptyWorkspace, leapmuxServer }) => {
    await openDiracAgent(leapmuxServer, authenticatedEmptyWorkspace.workspaceId)
    await openWorkspace(page, authenticatedEmptyWorkspace.workspaceId)
    await waitForSettingsHydrated(page)

    const group = await openSettingsMenu(page, 'permissionMode')
    await expect(group.locator('[data-testid="permissionMode-act"] input[type="radio"]')).toBeChecked()
    await expect(group.locator('[data-testid="permissionMode-plan"] input[type="radio"]')).toBeVisible()
  })

  // Dirac's effort axis is its own `reasoning_effort` config option, not the
  // well-known `effort` id. The worker maps the env default onto it.
  diracTest('a turn completes through the respond tool', async ({ page, authenticatedEmptyWorkspace, leapmuxServer, modelScript }) => {
    await openDiracAgent(leapmuxServer, authenticatedEmptyWorkspace.workspaceId)
    await openWorkspace(page, authenticatedEmptyWorkspace.workspaceId)

    await modelScript.queue({ toolCalls: [diracRespondToolCall('dirac-respond', 'complete', 'Turn complete.')] })
    await sendMessage(page, modelScript.prompt('Finish the turn.'))
    await waitForAgentIdle(page, 120_000)
    await expect(page.getByText('Turn complete.').first()).toBeVisible()
  })
})
