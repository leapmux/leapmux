import type { ManagedNativeScenarioContext } from '../helpers/nativeScenario'
import type { NativeModeSelectionProof } from '../helpers/nativeSettings'
import { expect } from '@playwright/test'
import { COPILOT_MODE, COPILOT_OPTION, COPILOT_TOOL } from '../../../src/generated/contracts/copilot-protocol'
import { nativeModelToolNames } from '../helpers/nativeScenario'
import { exerciseNativePlanWithEffort } from '../helpers/nativeSettings'
import { expectSettingsChip } from '../helpers/ui'
import { isCopilotPlanTurn } from './modeSignals'

/**
 * Keep Copilot's Plan mode and low effort together after a turn and a reload, and restore one of the two axes.
 * A turn before the change offers no Plan tool, so the Plan tool of the later turns proves the selected mode.
 * The Plan prefix of each user turn proves the mode that Copilot applied to that turn.
 */
export async function exerciseCopilotPlanAndEffort(
  context: ManagedNativeScenarioContext,
  restore: 'mode' | 'effort',
  afterModeSelected?: NativeModeSelectionProof,
): Promise<void> {
  await exerciseNativePlanWithEffort(context, {
    mode: { groupId: COPILOT_OPTION.SessionMode, value: COPILOT_MODE.Plan },
    effort: { groupId: 'effort', value: 'low' },
    restore,
    ...(afterModeSelected
      ? { afterModeSelected }
      : {}),
    nativeBuildProof: (request) => {
      expect(request.protocol).toBe('openai-chat-completions')
      expect(nativeModelToolNames(request)).not.toContain(COPILOT_TOOL.ExitPlanMode)
      expect(isCopilotPlanTurn(request), 'Copilot handles the build turn in Agent mode').toBe(false)
    },
    nativePlanProof: (request) => {
      expect(request.protocol).toBe('openai-chat-completions')
      expect(request.body).toMatchObject({ reasoning_effort: 'low' })
      expect(nativeModelToolNames(request)).toContain(COPILOT_TOOL.ExitPlanMode)
      expect(isCopilotPlanTurn(request), 'Copilot handles the turn in Plan mode').toBe(true)
    },
  })
  await expectSettingsChip(context.page, 'Low')
}
