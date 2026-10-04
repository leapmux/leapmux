import { expect } from '@playwright/test'
import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { GOOSE_E2E_SKIP_REASON, gooseTest } from '../goose-fixtures'
import { applyPermissionPreset, openPlusMenu, openSettingsMenu, waitForSettingsHydrated } from '../helpers/ui'
import { exerciseGoosePermissionRemoval } from './permissionScenario'

gooseTest.skip(!!GOOSE_E2E_SKIP_REASON, GOOSE_E2E_SKIP_REASON || '')

gooseTest('permission shortcuts switch Smart Approve and Auto', async ({ authenticatedGooseWorkspace, page }) => {
  void authenticatedGooseWorkspace
  await waitForSettingsHydrated(page)
  const menu = await openPlusMenu(page)
  // A new Goose session starts in Smart Approve. Its Smart shortcut is disabled until the mode changes.
  await expect(menu.getByTestId('composer-smart-permissions')).toBeDisabled()

  await applyPermissionPreset(page, 'bypass')
  let group = await openSettingsMenu(page, 'permissionMode')
  await expect(group.locator('[data-testid="permissionMode-auto"] input[type="radio"]')).toBeChecked()

  await applyPermissionPreset(page, 'smart')
  group = await openSettingsMenu(page, 'permissionMode')
  await expect(group.locator('[data-testid="permissionMode-smart_approve"] input[type="radio"]')).toBeChecked()
})

gooseTest('asks before a real removal after the Smart shortcut and executes it through Auto', async ({ authenticatedGooseWorkspace, page, modelScript, leapmuxServer }) => {
  const context = { page, modelScript, leapmuxServer, workspaceId: authenticatedGooseWorkspace.workspaceId, provider: AgentProvider.GOOSE }
  await exerciseGoosePermissionRemoval(context)
})
