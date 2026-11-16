import type { MockModelRequestRecord } from '../helpers/mockModelScript'
import type { ManagedNativeScenarioContext } from '../helpers/nativeScenario'
import { expect } from '@playwright/test'
import { nativeModelLastUserText } from '../helpers/nativeScenario'
import { exerciseNativePlanWithEffort } from '../helpers/nativeSettings'
import { expectSettingsChip } from '../helpers/ui'

/** The reminder that OpenCode puts into the last user message of a Plan turn. */
export const OPENCODE_PLAN_REMINDER = '# Plan Mode - System Reminder'

/**
 * Restore the selected native axis while preserving the coupled Plan and effort settings, for OpenCode and for Kilo,
 * which builds on OpenCode. The two differ only in the Plan reminder, which each passes.
 */
export async function exercisePlanAndEffort(
  context: ManagedNativeScenarioContext,
  restore: 'mode' | 'effort',
  planReminder: string = OPENCODE_PLAN_REMINDER,
): Promise<void> {
  if (planReminder.trim() === '')
    throw new Error('The OpenCode family Plan check needs the Plan reminder of its provider.')
  const expectNativePlan = (request: MockModelRequestRecord) => {
    expect(request.protocol).toBe('openai-chat-completions')
    expect(request.body).toMatchObject({ reasoning_effort: 'low' })
    expect(nativeModelLastUserText(request)).toContain(planReminder)
  }
  await exerciseNativePlanWithEffort(context, {
    mode: { groupId: 'primaryAgent', value: 'plan' },
    effort: { groupId: 'effort', value: 'low' },
    restore,
    nativeBuildProof(request) {
      expect(nativeModelLastUserText(request)).not.toContain(planReminder)
    },
    nativePlanProof: expectNativePlan,
  })
  await expectSettingsChip(context.page, 'Plan')
  await expectSettingsChip(context.page, 'Low')
}
