import type { MockModelRule } from '../helpers/mockModelScript'
import type { ManagedNativeScenarioContext } from '../helpers/nativeScenario'
import { expect } from '@playwright/test'
import { COPILOT_PERMISSION_MODE } from '../../../src/generated/contracts/copilot-protocol'
import { exerciseNativeToolWrite } from '../helpers/nativePermission'
import { currentNativeAgent } from '../helpers/nativeScenario'
import { applyPermissionPreset } from '../helpers/ui'

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

/** Prove the selected native Copilot preset with actual file bytes and a matched tool result. */
export async function exerciseCopilotPermissionPreset(context: ManagedNativeScenarioContext, preset: 'smart' | 'bypass'): Promise<void> {
  if (preset === 'smart')
    await context.modelScript.rule(COPILOT_PERMISSION_JUDGE_RULE)
  await applyPermissionPreset(context.page, preset)
  const agent = await currentNativeAgent(context)
  expect(agent.optionGroups.find(group => group.id === 'permissionMode')?.currentValue)
    .toBe(preset === 'smart' ? COPILOT_PERMISSION_MODE.Assisted : COPILOT_PERMISSION_MODE.AllowAll)
  await exerciseNativeToolWrite(context, { permission: preset === 'smart' ? 'native' : 'absent' })
}
