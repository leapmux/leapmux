import type { Page } from '@playwright/test'
import type { ManagedNativeScenarioContext } from '../helpers/nativeScenario'
import { expect } from '@playwright/test'
import { sendNativeAnswer } from '../helpers/nativeConversation'
import { exerciseRestoredNativeOption } from '../helpers/nativeSettings'
import { applyPermissionPreset, chooseSettingsOption, expectNoControlBanner, expectSettingsChip, openPlusMenu, waitForAgentIdle, waitForControlBanner, waitForSettingsHydrated, waitForSettingsIdle } from '../helpers/ui'

/**
 * Reject the request that ends each Reasonix answer in Plan mode, and keep Plan mode.
 *
 * Reasonix 1.38 streams a Plan-mode answer as the plan. Then it asks to leave Plan mode
 * with an `exit_plan_mode` permission request, and the prompt ends only after the answer
 * (internal/control/turn_orchestrator.go executeApprovedPlan). The request asks after the
 * Bypass preset also, because Reasonix keeps a plan approval interactive in YOLO mode
 * (controller.go requestApprovalDecisionWithOptions). A rejection keeps Plan mode.
 *
 * Call it after sendNativeAnswer. That helper proves the answer visible while this
 * request still waits, so the reader sees the plan before the decision.
 */
async function rejectPlanExit(page: Page): Promise<void> {
  const banner = await waitForControlBanner(page)
  await expect(banner).toContainText('exit_plan_mode')
  await page.getByTestId('control-deny-btn').filter({ visible: true }).first().click()
  await expectNoControlBanner(page)
  await waitForAgentIdle(page)
}

/** Keep Reasonix's effort, mode, and approval settings through a native turn and reload. */
export async function exerciseReasonixSessionSettings(context: ManagedNativeScenarioContext): Promise<void> {
  const { page } = context
  await waitForSettingsHydrated(page)
  await chooseSettingsOption(page, 'permissionMode-plan')
  await expectSettingsChip(page, 'Plan')
  await waitForSettingsIdle(page)
  await chooseSettingsOption(page, 'effort-low')
  await waitForSettingsIdle(page)
  await expectSettingsChip(page, 'Low')
  const request = await sendNativeAnswer(context, 'Reply once after the effort switch.', 'Reasonix answered at low effort.')
  expect(request.body).toMatchObject({ reasoning_effort: 'low' })
  await rejectPlanExit(page)
  const menu = await openPlusMenu(page)
  await expect(menu.getByTestId('composer-smart-permissions')).toHaveCount(0)
  await expect(menu.getByTestId('composer-bypass-permissions')).toBeVisible()
  await page.keyboard.press('Escape')
  await applyPermissionPreset(page, 'bypass')
  await waitForSettingsIdle(page)
  await exerciseRestoredNativeOption(context, {
    groupId: 'effort',
    value: 'low',
    async nativeProof(restored) {
      expect(restored.body).toMatchObject({ reasoning_effort: 'low' })
      await rejectPlanExit(page)
    },
  })
  await expectSettingsChip(page, 'Plan')
  await expectSettingsChip(page, 'Low')
  await chooseSettingsOption(page, 'permissionMode-normal')
  await chooseSettingsOption(page, 'tool_approval-ask')
  await waitForSettingsIdle(page)
  await expectSettingsChip(page, 'Normal')
}
