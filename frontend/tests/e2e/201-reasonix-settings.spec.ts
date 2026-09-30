import { existsSync } from 'node:fs'
import { join } from 'node:path'
import { AgentProvider } from '../../src/generated/proto/leapmux/v1/agent_pb'
import { nativeToolResult } from './helpers/nativeToolResult'
import { writeToolCall } from './helpers/providerToolCalls'
import { applyPermissionPreset, chooseSettingsOption, expectSettingsChip, openPlusMenu, sendMessage, waitForAgentIdle, waitForControlBanner, waitForSettingsHydrated, waitForSettingsIdle } from './helpers/ui'
import { expect, REASONIX_E2E_SKIP_REASON, reasonixTest } from './reasonix-fixtures'

reasonixTest.skip(!!REASONIX_E2E_SKIP_REASON, REASONIX_E2E_SKIP_REASON || '')

reasonixTest('applies Reasonix session settings and preserves them after reload', async ({ authenticatedReasonixWorkspace, page, modelScript }) => {
  void authenticatedReasonixWorkspace
  await waitForSettingsHydrated(page)

  await chooseSettingsOption(page, 'permissionMode-plan')
  await expectSettingsChip(page, 'Plan')
  await waitForSettingsIdle(page)

  await chooseSettingsOption(page, 'effort-low')
  await waitForSettingsIdle(page)
  await expectSettingsChip(page, 'Low')

  await modelScript.queue({ text: 'Reasonix answered at low effort.' })
  await sendMessage(page, modelScript.prompt('Reply once after the effort switch.'))
  const status = await modelScript.waitForSteps()
  await waitForAgentIdle(page)
  expect(status.requests.find(request => request.stepIndex === 0)?.body).toMatchObject({ reasoning_effort: 'low' })

  const menu = await openPlusMenu(page)
  await expect(menu.getByTestId('composer-smart-permissions')).toHaveCount(0)
  await expect(menu.getByTestId('composer-bypass-permissions')).toBeVisible()
  await page.keyboard.press('Escape')
  await applyPermissionPreset(page, 'bypass')
  await waitForSettingsIdle(page)

  await page.reload()
  await waitForSettingsHydrated(page)
  await expectSettingsChip(page, 'Plan')
  await expectSettingsChip(page, 'Low')

  await chooseSettingsOption(page, 'permissionMode-normal')
  await chooseSettingsOption(page, 'tool_approval-ask')
  await waitForSettingsIdle(page)
  await expectSettingsChip(page, 'Normal')
})

reasonixTest('refuses a native write in Plan mode and asks in Normal mode', async ({ authenticatedReasonixWorkspace, page, modelScript }) => {
  const workingDir = authenticatedReasonixWorkspace.workingDir
  if (!workingDir)
    throw new Error('the Reasonix workspace has no working directory')
  const planFile = join(workingDir, 'reasonix-plan-write.txt')
  const normalFile = join(workingDir, 'reasonix-normal-write.txt')

  await chooseSettingsOption(page, 'permissionMode-plan')
  await waitForSettingsIdle(page)
  await modelScript.queue(
    { toolCalls: [writeToolCall(AgentProvider.REASONIX, 'reasonix-plan-write', { path: planFile, content: 'plan mutation\n' })] },
    { text: 'The Plan check ended.' },
  )
  await sendMessage(page, modelScript.prompt('Try the scripted write in Plan mode.'))
  const planned = await modelScript.waitForSteps(2)
  await waitForAgentIdle(page)
  expect(existsSync(planFile)).toBe(false)
  const planResult = nativeToolResult(planned.requests.find(request => request.stepIndex === 1), 'reasonix-plan-write')
  expect(planResult).toContain('plan mode forbids workspace mutations')
  const exitBanner = await waitForControlBanner(page)
  await expect(exitBanner).toContainText('exit_plan_mode')
  await page.getByTestId('control-deny-btn').filter({ visible: true }).click()
  await expect(exitBanner).toHaveCount(0)

  await chooseSettingsOption(page, 'permissionMode-normal')
  await chooseSettingsOption(page, 'tool_approval-ask')
  await waitForSettingsIdle(page)
  await modelScript.queue(
    { toolCalls: [writeToolCall(AgentProvider.REASONIX, 'reasonix-normal-write', { path: normalFile, content: 'normal mutation\n' })] },
    { text: 'The Normal check ended.' },
  )
  await sendMessage(page, modelScript.prompt('Try the scripted write in Normal mode.'))
  await modelScript.waitForSteps(3)
  const banner = await waitForControlBanner(page)
  await expect(banner).toContainText('reasonix-normal-write.txt')
  await page.getByTestId('control-deny-btn').filter({ visible: true }).click()
  await modelScript.waitForSteps(4)
  await waitForAgentIdle(page)
  expect(existsSync(normalFile)).toBe(false)
})
