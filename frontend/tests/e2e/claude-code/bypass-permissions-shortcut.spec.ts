import { expect } from '@playwright/test'
import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { claudeTest } from '../claude-fixtures'
import { test } from '../fixtures'
import { exerciseBypassPermissions } from '../helpers/nativeBypassPermissions'
import { enterPlanMode, exitPlanMode } from '../helpers/plan-mode'
import { expectSettingsChip, settingsBar, waitForSettingsIdle } from '../helpers/ui'

test.describe('plan mode - bypass permissions', () => {
  test('bypass permissions from ExitPlanMode banner', async ({ page, authenticatedWorkspace, modelScript }) => {
    const trigger = settingsBar(page)
    await expect(trigger).toBeVisible()
    await expectSettingsChip(page, 'Default')

    // Step 1: Enter plan mode
    await enterPlanMode(page, modelScript)

    // Verify dropdown switches to Plan Mode (EnterPlanMode is auto-approved)
    await expectSettingsChip(page, 'Plan Mode')

    // Step 2: Exit plan mode (produces control_request banner)
    const banner = await exitPlanMode(page, modelScript)
    await expect(banner.getByText('Plan Ready for Review')).toBeVisible()

    // Verify the switch and the permission pills are visible, with Smart selected.
    const clearContextSwitch = page.locator('[data-testid="plan-clear-context-checkbox"] input[type="checkbox"]')
    await expect(clearContextSwitch).toBeVisible()
    await expect(clearContextSwitch).not.toBeChecked()

    const permissionPill = page.getByRole('radiogroup', { name: 'Permissions' })
    const bypassRadio = permissionPill.getByRole('radio', { name: 'Bypass' })
    await expect(permissionPill.getByRole('radio', { name: 'Unchanged' })).toBeVisible()
    // A plan approval opens on Smart, which Claude offers.
    await expect(permissionPill.getByRole('radio', { name: 'Smart' })).toBeChecked()
    await expect(permissionPill.getByRole('radio', { name: 'Unchanged' })).not.toBeChecked()
    await expect(bypassRadio).toBeVisible()
    await expect(bypassRadio).not.toBeChecked()

    // Select bypass permissions, then approve.
    await bypassRadio.click()
    await expect(bypassRadio).toBeChecked()

    const approveBtn = page.locator('[data-testid="plan-approve-btn"]')
    await expect(approveBtn).toBeEnabled()
    await approveBtn.click()

    // Verify control banner disappears (plan was approved)
    await expect(page.locator('[data-testid="control-banner"]')).not.toBeVisible()

    // Verify permission mode changed to Bypass Permissions
    await waitForSettingsIdle(page)
    await expectSettingsChip(page, 'Bypass Permissions')
  })
})

claudeTest('executes a protected native Bypass command and keeps the applied mode after reload', async ({ authenticatedClaudeWorkspace, page, leapmuxServer, modelScript }) => {
  await exerciseBypassPermissions({ page, modelScript, leapmuxServer, provider: AgentProvider.CLAUDE_CODE, workspaceId: authenticatedClaudeWorkspace.workspaceId }, {
    settingsProof: (agent) => {
      expect(agent.optionGroups.find(group => group.id === 'permissionMode')?.currentValue).toBe('bypassPermissions')
    },
  })
})
