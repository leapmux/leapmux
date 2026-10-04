import { existsSync, readFileSync } from 'node:fs'
import { join } from 'node:path'
import { expect } from '@playwright/test'
import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { nativeToolResult } from '../helpers/nativeToolResult'
import { writeToolCall } from '../helpers/providerToolCalls'
import { applyPermissionPreset, chooseSettingsOption, expectSettingsChip, openPlusMenu, sendMessage, waitForAgentIdle, waitForSettingsHydrated, waitForSettingsIdle } from '../helpers/ui'
import { ZCODE_E2E_SKIP_REASON, zcodeTest } from '../zcode-fixtures'
import { exerciseZCodeRemovalPermission } from './permissionScenario'

zcodeTest.skip(!!ZCODE_E2E_SKIP_REASON, ZCODE_E2E_SKIP_REASON || '')

zcodeTest('bypass-permissions-shortcut: the permission banner applies the selected bypass pill on allow', async ({ authenticatedZCodeWorkspace, page, modelScript, leapmuxServer }) => {
  const context = { page, modelScript, leapmuxServer, workspaceId: authenticatedZCodeWorkspace.workspaceId, provider: AgentProvider.ZCODE }
  await exerciseZCodeRemovalPermission(context, { bypass: true })
})

zcodeTest('offers only the bypass permission shortcut', async ({ authenticatedZCodeWorkspace, page }) => {
  void authenticatedZCodeWorkspace
  await waitForSettingsHydrated(page)
  const menu = await openPlusMenu(page)
  // ZCode declares no Smart preset, so only the bypass shortcut is drawn.
  await expect(menu.getByTestId('composer-smart-permissions')).toHaveCount(0)
  await applyPermissionPreset(page, 'bypass')
  await expectSettingsChip(page, 'Yolo')
})

zcodeTest('bypass-permissions-shortcut: plan mode refuses a native write that Yolo mode runs', async ({ authenticatedZCodeWorkspace, page, modelScript }) => {
  const workingDir = authenticatedZCodeWorkspace.workingDir
  if (!workingDir)
    throw new Error('the ZCode workspace has no working directory')
  const path = join(workingDir, 'zcode-mode-write.txt')

  await chooseSettingsOption(page, 'permissionMode-plan')
  await waitForSettingsIdle(page)
  await modelScript.queue(
    { toolCalls: [writeToolCall(AgentProvider.ZCODE, 'zcode-plan-write', { path, content: 'plan mutation\n' })] },
    { text: 'The Plan check ended.' },
  )
  await sendMessage(page, modelScript.prompt('Try the scripted write in Plan mode.'))
  const planned = await modelScript.waitForSteps(2)
  await waitForAgentIdle(page)
  expect(existsSync(path)).toBe(false)
  const planResult = nativeToolResult(planned.requests.find(request => request.stepIndex === 1), 'zcode-plan-write')
  expect(planResult).toMatch(/plan|not available|denied/i)
  await expect(page.locator('[data-testid="control-banner"]:visible')).toHaveCount(0)

  await chooseSettingsOption(page, 'permissionMode-yolo')
  await waitForSettingsIdle(page)
  await modelScript.queue(
    { toolCalls: [writeToolCall(AgentProvider.ZCODE, 'zcode-yolo-write', { path, content: 'yolo mutation\n' })] },
    { text: 'The Yolo check ended.' },
  )
  await sendMessage(page, modelScript.prompt('Run the scripted write in Yolo mode.'))
  const allowed = await modelScript.waitForSteps(4)
  await waitForAgentIdle(page)
  await expect(page.locator('[data-testid="control-banner"]:visible')).toHaveCount(0)
  expect(readFileSync(path, 'utf8')).toBe('yolo mutation\n')
  const result = nativeToolResult(allowed.requests.find(request => request.stepIndex === 3), 'zcode-yolo-write')
  expect(result).toContain('zcode-mode-write.txt')
})
