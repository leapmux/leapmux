import type { ManagedNativeScenarioContext } from '../helpers/nativeScenario'
import { expect } from '@playwright/test'
import { COPILOT_MODE, COPILOT_OPTION } from '../../../src/generated/contracts/copilot-protocol'
import { nativeModelToolNames } from '../helpers/nativeScenario'
import { exerciseNativePlanWithEffort } from '../helpers/nativeSettings'
import { expectSettingsChip } from '../helpers/ui'

/** The tool that Copilot offers to the model only in Plan mode. */
const EXIT_PLAN_TOOL = 'exit_plan_mode'

/**
 * Keep Copilot's Plan mode and low effort together after a turn and a reload, and restore one of the two axes.
 * A turn before the change offers no Plan tool, so the Plan tool of the later turns proves the selected mode.
 */
export async function exerciseCopilotPlanAndEffort(context: ManagedNativeScenarioContext, restore: 'mode' | 'effort'): Promise<void> {
  await exerciseNativePlanWithEffort(context, {
    mode: { groupId: COPILOT_OPTION.SessionMode, value: COPILOT_MODE.Plan },
    effort: { groupId: 'effort', value: 'low' },
    restore,
    nativeBuildProof: (request) => {
      expect(request.protocol).toBe('openai-chat-completions')
      expect(nativeModelToolNames(request)).not.toContain(EXIT_PLAN_TOOL)
    },
    nativePlanProof: (request) => {
      expect(request.protocol).toBe('openai-chat-completions')
      expect(request.body).toMatchObject({ reasoning_effort: 'low' })
      expect(nativeModelToolNames(request)).toContain(EXIT_PLAN_TOOL)
    },
  })
  await expectSettingsChip(context.page, 'Low')
}
