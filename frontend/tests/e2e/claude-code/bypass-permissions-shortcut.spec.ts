import { expect } from '@playwright/test'
import { CLAUDE_MODE } from '../../../src/generated/contracts/claude-protocol'
import { claudeTest } from '../claude-fixtures'
import { exerciseBypassPermissions } from '../helpers/nativeBypassPermissions'
import { nativeOptionValue } from '../helpers/nativeScenario'
import { enterPlanMode, exitPlanMode } from '../helpers/plan-mode'
import { answerPlanReview, expectNoControlBanner, expectSettingsChip, settingsBar, waitForSettingsIdle } from '../helpers/ui'
import { CLAUDE_AGENT } from './scenarios'

claudeTest.describe('plan mode - bypass permissions', () => {
  claudeTest('bypass permissions from ExitPlanMode banner', async ({ page, authenticatedWorkspace, modelScript }) => {
    const trigger = settingsBar(page)
    await expect(trigger).toBeVisible()
    await expectSettingsChip(page, 'Default')

    // Step 1: Enter plan mode
    await enterPlanMode({ page, modelScript, provider: CLAUDE_AGENT.provider })

    // Verify dropdown switches to Plan Mode (EnterPlanMode is auto-approved)
    await expectSettingsChip(page, 'Plan Mode')

    // Step 2: Exit plan mode (produces control_request banner)
    const banner = await exitPlanMode({ page, modelScript, provider: CLAUDE_AGENT.provider })
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

    await expect(page.getByTestId('plan-approve-btn').filter({ visible: true })).toBeEnabled()
    await answerPlanReview(page, 'approve')

    // Verify control banner disappears (plan was approved)
    await expectNoControlBanner(page)

    // Verify permission mode changed to Bypass Permissions
    await waitForSettingsIdle(page)
    await expectSettingsChip(page, 'Bypass Permissions')
  })
})

claudeTest('executes a protected native Bypass command and keeps the applied mode after reload', async ({ native }) => {
  await exerciseBypassPermissions(native, {
    settingsProof: (agent) => {
      expect(nativeOptionValue(agent, 'permissionMode')).toBe(CLAUDE_MODE.BypassPermissions)
    },
  })
})
