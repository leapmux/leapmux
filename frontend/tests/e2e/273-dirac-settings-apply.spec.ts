import { DIRAC_E2E_SKIP_REASON, diracTest, openDiracAgent } from './dirac-fixtures'
import { chooseSettingsOption, expectSettingsChip, openWorkspace, waitForSettingsHydrated, waitForSettingsIdle } from './helpers/ui'

diracTest.skip(!!DIRAC_E2E_SKIP_REASON, DIRAC_E2E_SKIP_REASON || '')

/**
 * 273 — Dirac settings apply.
 *
 * Dirac's mode axis carries Plan and Act; its effort axis is the provider's own
 * `reasoning_effort` config option. A switch applies for the next prompt, and
 * both axes survive a reload. The model axis is pinned by the e2e environment
 * to the one mock model, so it has no second value to switch to.
 */
diracTest.describe('Dirac settings apply', () => {
  diracTest('switches the mode and the effort, and keeps them after reload', async ({ page, authenticatedEmptyWorkspace, leapmuxServer }) => {
    await openDiracAgent(leapmuxServer, authenticatedEmptyWorkspace.workspaceId)
    await openWorkspace(page, authenticatedEmptyWorkspace.workspaceId)
    await waitForSettingsHydrated(page)
    await expectSettingsChip(page, 'Act')

    await chooseSettingsOption(page, 'permissionMode-plan')
    await waitForSettingsIdle(page)
    await expectSettingsChip(page, 'Plan')

    await chooseSettingsOption(page, 'reasoning_effort-high')
    await waitForSettingsIdle(page)
    await expectSettingsChip(page, 'High')

    await page.reload()
    await waitForSettingsHydrated(page)
    await expectSettingsChip(page, 'Plan')
    await expectSettingsChip(page, 'High')

    await chooseSettingsOption(page, 'permissionMode-act')
    await waitForSettingsIdle(page)
    await expectSettingsChip(page, 'Act')
  })
})
