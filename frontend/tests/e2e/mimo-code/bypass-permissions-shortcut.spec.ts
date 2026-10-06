import { existsSync, writeFileSync } from 'node:fs'
import { join } from 'node:path'
import { expect } from '@playwright/test'
import { MIMO_OPTION, MIMO_PERMISSION_POLICY } from '../../../src/generated/contracts/mimo-protocol'
import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { agentOpenOptions, agentSettings } from '../agentSettings'
import { openAgentViaAPI } from '../helpers/api'
import { bashToolCall } from '../helpers/providerToolCalls'
import { createTestDirectory } from '../helpers/runDirectory'
import { applyPermissionPreset, closeComposerMenus, openPlusMenu, openSettingsMenu, openWorkspace, sendMessage, waitForAgentIdle, waitForSettingsHydrated } from '../helpers/ui'
import { mimoTest } from '../mimo-fixtures'

mimoTest.describe('MiMo Code settings', () => {
  // MiMo controls native approvals through two runtime switches.
  // Bypass enables skip-all and auto-approve-delete. The second switch permits deletion without the native permission question.
  mimoTest('bypass runs a deletion without asking', async ({ page, authenticatedEmptyWorkspace, leapmuxServer, modelScript }) => {
    const directory = createTestDirectory('mimo-bypass-')
    const file = join(directory, 'doomed.txt')
    writeFileSync(file, 'delete me\n')
    await openAgentViaAPI(leapmuxServer.hubUrl, leapmuxServer.adminToken, leapmuxServer.workerId, authenticatedEmptyWorkspace.workspaceId, directory, {
      agentProvider: AgentProvider.MIMO_CODE,
      ...agentOpenOptions(agentSettings(AgentProvider.MIMO_CODE)),
    })
    await openWorkspace(page, authenticatedEmptyWorkspace.workspaceId)
    await waitForSettingsHydrated(page)

    const menu = await openPlusMenu(page)
    await expect(menu.getByTestId('composer-smart-permissions')).toHaveCount(0)
    await expect(menu.getByTestId('composer-bypass-permissions')).toBeVisible()
    await page.keyboard.press('Escape')
    await applyPermissionPreset(page, 'bypass')
    // The policy is not a status-bar axis, so its own group in the menu states it.
    const policies = await openSettingsMenu(page, MIMO_OPTION.PermissionPolicy)
    await expect(policies.getByTestId(`${MIMO_OPTION.PermissionPolicy}-${MIMO_PERMISSION_POLICY.Bypass}`)).toHaveAttribute('aria-checked', 'true')
    await closeComposerMenus(page)

    await modelScript.queue(
      { toolCalls: [bashToolCall(AgentProvider.MIMO_CODE, 'delete-call', 'rm -f doomed.txt')] },
      { text: 'DELETED_WITHOUT_ASKING' },
    )
    await sendMessage(page, modelScript.prompt('Delete doomed.txt.'))
    await modelScript.waitForSteps()
    await waitForAgentIdle(page)
    await expect(page.locator('[data-testid="control-banner"]')).toHaveCount(0)
    expect(existsSync(file)).toBe(false)
  })
})
