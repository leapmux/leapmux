import { expect } from '@playwright/test'
import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { chooseSettingsOption, expectSettingsChip, expectSettingsOptionChosen, openSettingsMenu, settingsBar, waitForSettingsIdle } from '../helpers/ui'
import { ZCODE_E2E_SKIP_REASON, zcodeTest } from '../zcode-fixtures'
import { exerciseZCodeMode } from './modeScenario'

zcodeTest.skip(!!ZCODE_E2E_SKIP_REASON, ZCODE_E2E_SKIP_REASON || '')

zcodeTest('the mode chip starts on Build and can switch to Plan and Yolo', async ({ authenticatedZCodeWorkspace, page, modelScript, leapmuxServer }) => {
  const context = { page, modelScript, leapmuxServer, workspaceId: authenticatedZCodeWorkspace.workspaceId, provider: AgentProvider.ZCODE }
  await expect(settingsBar(page)).toBeVisible()
  await expectSettingsChip(page, 'Build')
  await exerciseZCodeMode(context, 'build')
  await chooseSettingsOption(page, 'permissionMode-plan')
  await waitForSettingsIdle(page)
  await expectSettingsChip(page, 'Plan')
  await exerciseZCodeMode(context, 'plan')
  await chooseSettingsOption(page, 'permissionMode-yolo')
  await waitForSettingsIdle(page)
  await expectSettingsChip(page, 'Yolo')
  await exerciseZCodeMode(context, 'yolo')
  await chooseSettingsOption(page, 'permissionMode-build')
  await waitForSettingsIdle(page)
  await expectSettingsChip(page, 'Build')
  await exerciseZCodeMode(context, 'build')
})

zcodeTest('plan mode refuses a native write that Yolo mode runs', async ({ authenticatedZCodeWorkspace, page, modelScript, leapmuxServer }) => {
  const context = { page, modelScript, leapmuxServer, workspaceId: authenticatedZCodeWorkspace.workspaceId, provider: AgentProvider.ZCODE }
  for (const mode of ['plan', 'yolo'] as const) {
    await chooseSettingsOption(page, `permissionMode-${mode}`)
    await waitForSettingsIdle(page)
    await exerciseZCodeMode(context, mode)
    await page.reload()
    await expectSettingsOptionChosen(page, `permissionMode-${mode}`)
    await exerciseZCodeMode(context, mode)
  }
})

zcodeTest('auto is not offered, because the shipped app-server does not implement it', async ({ authenticatedZCodeWorkspace, page }) => {
  void authenticatedZCodeWorkspace
  const menu = await openSettingsMenu(page, 'permissionMode')
  await expect(menu.locator('[data-testid="permissionMode-auto"]')).toHaveCount(0)
  await expect(menu.locator('[data-testid="permissionMode-plan"]')).toBeVisible()
  await expect(menu.locator('[data-testid="permissionMode-build"]')).toBeVisible()
  await expect(menu.locator('[data-testid="permissionMode-edit"]')).toBeVisible()
  await expect(menu.locator('[data-testid="permissionMode-yolo"]')).toBeVisible()
  await page.keyboard.press('Escape')
})
