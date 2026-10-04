import type { MockModelRequestRecord } from '../helpers/mockModelScript'
import type { ManagedNativeScenarioContext } from '../helpers/nativeScenario'
import { expect } from '@playwright/test'
import { nativeModelLastUserText } from '../helpers/nativeScenario'
import { exerciseNativePlanWithEffort } from '../helpers/nativeSettings'
import { expectSettingsChip } from '../helpers/ui'

/** Prove kilo's actual Plan instruction and selected native effort. */
function expectNativePlan(request: MockModelRequestRecord): void {
  expect(request.protocol).toBe('openai-chat-completions')
  expect(request.body).toMatchObject({ reasoning_effort: 'low' })
  expect(nativeModelLastUserText(request)).toContain('# Native Plan Mode')
}

/** Restore the selected native axis while preserving the coupled Plan and effort settings. */
export async function exercisePlanAndEffort(context: ManagedNativeScenarioContext, restore: 'mode' | 'effort'): Promise<void> {
  await exerciseNativePlanWithEffort(context, {
    mode: { groupId: 'primaryAgent', value: 'plan' },
    effort: { groupId: 'effort', value: 'low' },
    restore,
    nativeBuildProof(request) {
      expect(nativeModelLastUserText(request)).not.toContain('# Native Plan Mode')
    },
    nativePlanProof: expectNativePlan,
  })
  await expectSettingsChip(context.page, 'Plan')
  await expectSettingsChip(context.page, 'Low')
}
