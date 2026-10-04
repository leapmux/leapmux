import { expect } from '@playwright/test'
import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { claudeTest as test } from '../claude-fixtures'
import { exerciseSmartPermissions } from '../helpers/nativeBypassPermissions'
import { currentNativeAgent } from '../helpers/nativeScenario'
import { applyPermissionPreset, chooseSettingsOption, expectSettingsChip, openPlusMenu, permissionModeOffered, waitForSettingsHydrated, waitForSettingsIdle } from '../helpers/ui'

test.describe('Agent Settings', () => {
  test('permission shortcuts use the Claude modes that the session offers', async ({ authenticatedClaudeWorkspace, page }) => {
    void authenticatedClaudeWorkspace
    await waitForSettingsHydrated(page)
    // Smart selects Auto Mode. The native startup result determines whether the session offers that mode.
    // Check either catalog outcome. The separate native case requires Auto support and its actual command result.
    const autoOffered = await permissionModeOffered(page, 'auto')

    const menu = await openPlusMenu(page)
    const smart = menu.getByTestId('composer-smart-permissions')
    await expect(menu.getByTestId('composer-bypass-permissions')).toBeVisible()
    if (autoOffered)
      await expect(smart).toBeVisible()
    else
      await expect(smart).toHaveCount(0)

    await applyPermissionPreset(page, 'bypass')
    await expectSettingsChip(page, 'Bypass Permissions')

    if (autoOffered) {
      await applyPermissionPreset(page, 'smart')
      await expectSettingsChip(page, 'Auto Mode')
    }
  })
})

test('executes the native Smart command with confirmed Auto mode before and after reload', async ({ authenticatedClaudeWorkspace, page, leapmuxServer, modelScript }) => {
  const context = { page, modelScript, leapmuxServer, provider: AgentProvider.CLAUDE_CODE, workspaceId: authenticatedClaudeWorkspace.workspaceId }
  const before = await currentNativeAgent(context)
  expect(before.optionGroups.find(group => group.id === 'permissionMode')?.options.map(option => option.id)).toContain('auto')
  await chooseSettingsOption(page, 'permissionMode-default')
  await waitForSettingsIdle(page)
  expect((await currentNativeAgent(context)).optionGroups.find(group => group.id === 'permissionMode')?.currentValue).toBe('default')
  await exerciseSmartPermissions(context, {
    settingsProof: (agent) => {
      expect(agent.optionGroups.find(group => group.id === 'permissionMode')?.currentValue).toBe('auto')
    },
  })
})
