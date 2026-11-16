import type { MockModelRequestRecord } from '../helpers/mockModelScript'
import type { ManagedNativeScenarioContext } from '../helpers/nativeScenario'
import { expect } from '@playwright/test'
import { nativeModelContextText } from '../helpers/nativeScenario'
import { exerciseNativePlanWithEffort } from '../helpers/nativeSettings'
import { expectSettingsChip } from '../helpers/ui'

/** The reminder that OpenCode puts into the last user message of a Plan turn. */
export const OPENCODE_PLAN_REMINDER = 'You are in Plan mode.'
const OPENCODE_PLAN_EXIT_REMINDER = 'You are NO LONGER in Plan mode.'

/** The last native reminder states the active mode, even when history keeps older reminders. */
export function openCodePlanIsActive(request: MockModelRequestRecord): boolean {
  const text = nativeModelContextText(request)
  return text.lastIndexOf(OPENCODE_PLAN_REMINDER) > text.lastIndexOf(OPENCODE_PLAN_EXIT_REMINDER)
}

/**
 * Restore the selected native axis while preserving the coupled Plan and effort settings.
 */
export async function exercisePlanAndEffort(
  context: ManagedNativeScenarioContext,
  restore: 'mode' | 'effort',
): Promise<void> {
  const expectNativePlan = (request: MockModelRequestRecord) => {
    expect(request.protocol).toBe('openai-chat-completions')
    expect(request.body).toMatchObject({ reasoning_effort: 'low' })
    expect(openCodePlanIsActive(request)).toBe(true)
  }
  await exerciseNativePlanWithEffort(context, {
    mode: { groupId: 'primaryAgent', value: 'plan' },
    effort: { groupId: 'effort', value: 'variant:low' },
    restore,
    nativeBuildProof(request) {
      expect(openCodePlanIsActive(request)).toBe(false)
    },
    nativePlanProof: expectNativePlan,
  })
  await expectSettingsChip(context.page, 'Plan')
  await expectSettingsChip(context.page, 'Low')
}
