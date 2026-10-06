import { expect } from '@playwright/test'
import { COPILOT_MODE, COPILOT_OPTION, COPILOT_PERMISSION_MODE } from '../../../src/generated/contracts/copilot-protocol'
import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { copilotTest } from '../copilot-fixtures'
import { applyPermissionPreset, openSettingsMenu } from '../helpers/ui'
import { exerciseCopilotPermissionPreset } from './permissionScenario'

copilotTest('bypass-permissions-shortcut: permission presets switch the native permission mode', async ({ authenticatedCopilotWorkspace, page }) => {
  void authenticatedCopilotWorkspace
  const checked = async (value: string) => {
    const group = await openSettingsMenu(page, 'permissionMode')
    return group.locator(`[data-testid="permissionMode-${value}"] input[type="radio"]`)
  }
  // The fixture opens Copilot in Manual mode. Its explicit setting overrides the native new-session default.
  await expect(await checked(COPILOT_PERMISSION_MODE.Manual)).toBeChecked()

  await applyPermissionPreset(page, 'bypass')
  await expect(await checked(COPILOT_PERMISSION_MODE.AllowAll)).toBeChecked()

  await applyPermissionPreset(page, 'smart')
  await expect(await checked(COPILOT_PERMISSION_MODE.Assisted)).toBeChecked()

  const modes = await openSettingsMenu(page, COPILOT_OPTION.SessionMode)
  await expect(modes.locator(`[data-testid="${COPILOT_OPTION.SessionMode}-${COPILOT_MODE.Interactive}"] input[type="radio"]`)).toBeChecked()
  for (const mode of [COPILOT_MODE.Plan, COPILOT_MODE.Autopilot])
    await expect(modes.locator(`[data-testid="${COPILOT_OPTION.SessionMode}-${mode}"]`)).toBeVisible()
})

copilotTest('runs a real native write through the bypass shortcut', async ({ authenticatedCopilotWorkspace, page, modelScript, leapmuxServer }) => {
  const context = { page, modelScript, leapmuxServer, workspaceId: authenticatedCopilotWorkspace.workspaceId, provider: AgentProvider.GITHUB_COPILOT }
  await exerciseCopilotPermissionPreset(context, 'bypass')
})
