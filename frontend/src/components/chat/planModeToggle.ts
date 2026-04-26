/**
 * Pure decision logic for the Shift+Tab plan-mode toggle.
 *
 * Behavior — given the agent's current permission mode and the previously
 * remembered non-plan mode:
 *
 * - When the agent is in plan mode, toggle restores the previously
 *   remembered non-plan mode (e.g. Default, Accept Edits) — without updating
 *   the remembered value.
 * - When the agent is in any other mode, toggle switches to plan mode and
 *   remembers the *current* mode so a follow-up toggle returns to it.
 */

export interface PlanModeToggleInput {
  /** The agent's current permission mode (from agent state). */
  currentMode: string
  /** The plan-mode value for the active provider (provider-specific identifier). */
  planValue: string
  /** The most-recently-tracked non-plan mode (used as the toggle target). */
  previousNonPlanMode: string
}

export interface PlanModeToggleDecision {
  /** The mode to set on the agent. */
  nextMode: string
  /**
   * If non-undefined, the caller should update its tracked
   * `previousNonPlanMode` to this value. Returned only when toggling
   * INTO plan mode (so the next toggle restores the just-departed mode).
   */
  updatePreviousNonPlanMode?: string
}

export function decidePlanModeToggle(input: PlanModeToggleInput): PlanModeToggleDecision {
  if (input.currentMode === input.planValue) {
    // Currently in plan mode → restore previous mode. Don't overwrite
    // the remembered value: the user expects round-tripping.
    return { nextMode: input.previousNonPlanMode }
  }
  // Currently NOT in plan mode → enter plan mode and remember the
  // mode we're leaving so the next toggle returns to it.
  return {
    nextMode: input.planValue,
    updatePreviousNonPlanMode: input.currentMode,
  }
}
