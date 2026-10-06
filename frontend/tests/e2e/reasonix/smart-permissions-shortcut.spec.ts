import { expect } from '@playwright/test'
import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { exerciseRelatedTodo } from '../helpers/relatedTodoProof'
import { applyPermissionPreset, chooseSettingsOption, expectSettingsChip, openPlusMenu, sendMessage, waitForAgentIdle, waitForSettingsHydrated, waitForSettingsIdle } from '../helpers/ui'
import { expectMissingPermissionShortcut } from '../helpers/unsupportedConfiguration'
import { reasonixTest } from '../reasonix-fixtures'

reasonixTest('smart-permissions-shortcut: applies Reasonix session settings and preserves them after reload', async ({ authenticatedReasonixWorkspace, page, modelScript }) => {
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

reasonixTest('proves the native smart-permissions-shortcut limit after a real sidebar operation', async ({ authenticatedReasonixWorkspace, page, modelScript, leapmuxServer }) => {
  const context = { page, modelScript, leapmuxServer, workspaceId: authenticatedReasonixWorkspace.workspaceId, provider: AgentProvider.REASONIX }
  const relatedProof = () => exerciseRelatedTodo(context, { prepare: () => applyPermissionPreset(page, 'bypass') })
  await expectMissingPermissionShortcut(context, { preset: 'smart', relatedProof })
})
