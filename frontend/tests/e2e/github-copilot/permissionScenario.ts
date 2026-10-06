import type { Page } from '@playwright/test'
import type { MockModelRule } from '../helpers/mockModelScript'
import type { ManagedNativeScenarioContext } from '../helpers/nativeScenario'
import { expect } from '@playwright/test'
import { COPILOT_MODE, COPILOT_OPTION, COPILOT_PERMISSION_MODE } from '../../../src/generated/contracts/copilot-protocol'
import { exerciseNativeToolWrite } from '../helpers/nativePermission'
import { expectNativeOptionValue } from '../helpers/nativeScenario'
import { applyPermissionPreset, closeComposerMenus, expectSettingsOptionChosen, openSettingsMenu } from '../helpers/ui'

/**
 * Answer the permission judge of Assisted mode, so it consumes no scripted step.
 *
 * In Assisted mode, Copilot 1.0.87 sends one extra, non-streaming model request for
 * each call that needs approval. Its system prompt opens "You are Luna, a one-call
 * permission judge", and it asks for one line, `ALLOW: <reason>` or `DENY: <reason>`.
 * The runtime then raises its permission request with the verdict in
 * `promptRequest.assistedApproval.recommendation`, and the reader still answers it.
 * Without this rule the judge takes the next queued step, and the turn that follows
 * the approval finds the queue empty.
 */
export const COPILOT_PERMISSION_JUDGE_RULE: MockModelRule = {
  name: 'copilot-permission-judge',
  when: { system: 'one-call permission judge' },
  respond: { text: 'ALLOW: the scripted write is the requested work' },
}

/**
 * Switch the native permission mode through both shortcuts, and require the session mode that the switch leaves.
 * The fixture opens Copilot in Manual mode. Its explicit setting overrides the native new-session default.
 * Bypass selects Allow All, and Smart selects Assisted. Neither shortcut changes the Interactive session mode, and
 * the session still offers the Plan and Autopilot modes.
 */
export async function exerciseCopilotPresetSwitch(page: Page): Promise<void> {
  await expectSettingsOptionChosen(page, `permissionMode-${COPILOT_PERMISSION_MODE.Manual}`)
  await applyPermissionPreset(page, 'bypass')
  await expectSettingsOptionChosen(page, `permissionMode-${COPILOT_PERMISSION_MODE.AllowAll}`)
  await applyPermissionPreset(page, 'smart')
  await expectSettingsOptionChosen(page, `permissionMode-${COPILOT_PERMISSION_MODE.Assisted}`)
  await expectSettingsOptionChosen(page, `${COPILOT_OPTION.SessionMode}-${COPILOT_MODE.Interactive}`)
  const modes = await openSettingsMenu(page, COPILOT_OPTION.SessionMode)
  for (const mode of [COPILOT_MODE.Plan, COPILOT_MODE.Autopilot])
    await expect(modes.getByTestId(`${COPILOT_OPTION.SessionMode}-${mode}`), `the session offers the ${mode} mode`).toBeVisible()
  await closeComposerMenus(page)
}

/** Prove the selected native Copilot preset with actual file bytes and a matched tool result. */
export async function exerciseCopilotPermissionPreset(context: ManagedNativeScenarioContext, preset: 'smart' | 'bypass'): Promise<void> {
  if (preset === 'smart')
    await context.modelScript.rule(COPILOT_PERMISSION_JUDGE_RULE)
  await applyPermissionPreset(context.page, preset)
  await expectNativeOptionValue(context, 'permissionMode', preset === 'smart' ? COPILOT_PERMISSION_MODE.Assisted : COPILOT_PERMISSION_MODE.AllowAll)
  await exerciseNativeToolWrite(context, { permission: preset === 'smart' ? 'native' : 'absent' })
}
