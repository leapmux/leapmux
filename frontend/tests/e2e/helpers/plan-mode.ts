import type { Page } from '@playwright/test'
import { sendMessage, waitForAgentIdle, waitForControlBanner } from './ui'

// ──────────────────────────────────────────────
// Plan mode helpers
// ──────────────────────────────────────────────
//
// Plan mode prompts are split into two steps so that each LLM invocation
// has a single, deterministic task.  Combining EnterPlanMode + Write +
// ExitPlanMode in one prompt caused flakiness because the LLM occasionally
// skipped ExitPlanMode.

const PLAN_BODY = 'This is a dummy plan for testing the coding agent plan mode UI. Never execute this plan.'

/**
 * Prompt for entering plan mode and writing a plan file.
 * Does NOT call ExitPlanMode — use {@link EXIT_PLAN_PROMPT} for that.
 */
export const ENTER_PLAN_PROMPT
  = `I am testing the coding agent plan mode UI. Please use EnterPlanMode tool to enter plan mode, then write a plan file whose title is "# Dummy plan" and whose body is "${PLAN_BODY}" Do not call ExitPlanMode yet.`

/**
 * Generate an enter-plan prompt with a unique plan title.
 * See {@link ENTER_PLAN_PROMPT} for the default version.
 */
export function enterPlanPrompt(testId: string): string {
  return `I am testing the coding agent plan mode UI. Please use EnterPlanMode tool to enter plan mode, then write a plan file whose title is "# Dummy plan ${testId}" and whose body is "${PLAN_BODY}" Do not call ExitPlanMode yet.`
}

/** Prompt for exiting plan mode. Produces an ExitPlanMode control request. */
export const EXIT_PLAN_PROMPT
  = 'Please use ExitPlanMode tool to exit plan mode. Do not do anything else.'

/**
 * Enter plan mode, write a plan file, and exit.
 * Returns the ExitPlanMode control-request banner so the caller can
 * approve or reject it.
 *
 * @param page — Playwright page
 * @param testId — Optional unique ID embedded in the plan title
 *   (e.g. "first" → title "Dummy plan first").
 */
export async function enterAndExitPlanMode(page: Page, testId?: string) {
  // Step 1: Enter plan mode and write the plan file.
  const prompt = testId ? enterPlanPrompt(testId) : ENTER_PLAN_PROMPT
  await sendMessage(page, prompt)
  await waitForAgentIdle(page)

  // Step 2: Exit plan mode (produces control_request banner).
  await sendMessage(page, EXIT_PLAN_PROMPT)
  return waitForControlBanner(page)
}

export { PLAN_BODY }
