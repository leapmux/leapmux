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
  await sendPlanStep(page, prompt, async () => {
    // The agent entering plan mode is what moves the permission mode, so the
    // mode chip is the app's own confirmation that step 1 landed. Without it a
    // skipped EnterPlanMode was invisible here and surfaced two prompts later
    // as "control-banner not found", 120s after the fact.
    //
    // `count()` guards the read: `textContent()` on an absent element rejects,
    // and the `catch` below would turn that into a permanent `false` — the
    // predicate would then never confirm, whatever the agent did.
    const chip = page.locator('[data-testid="composer-mode-trigger"]')
    if (await chip.count() === 0)
      return false
    const text = await chip.textContent().catch(() => null)
    return text?.includes('Plan Mode') ?? false
  }, 'the agent did not enter plan mode')

  // Step 2: Exit plan mode (produces control_request banner).
  await sendPlanStep(page, EXIT_PLAN_PROMPT, async () => {
    return page.locator('[data-testid="control-banner"]').isVisible().catch(() => false)
  }, 'the agent did not call ExitPlanMode')
  return waitForControlBanner(page)
}

/** How many times to re-prompt a plan step the agent did not carry out. */
const PLAN_STEP_ATTEMPTS = 3

/**
 * How long to wait for a plan step's effect before re-prompting.
 *
 * A deliberately SHORT budget, not a redundant override of the global expect
 * timeout: the point is to give up early enough to re-prompt within the test's
 * own budget. A plan turn that is going to work finishes in seconds; one where
 * the model skipped the tool call never finishes at all, and waiting the global
 * 120s for it would leave no room to try again.
 */
const PLAN_STEP_SETTLE_MS = 15_000

/**
 * Send `prompt` and wait for `landed()`, re-prompting if the agent did not do
 * what was asked.
 *
 * These prompts drive real tool calls, and the model occasionally skips one --
 * the reason the enter and exit prompts were split in the first place. Splitting
 * reduced it; it did not remove it, and 033/050/051 all died on the same missing
 * banner in a single run. Retrying the PRECONDITION is legitimate here because
 * none of these specs is about the model's obedience: they assert what the UI
 * does once the control request exists. A step that never lands after several
 * attempts still fails, with a message naming which one.
 */
async function sendPlanStep(
  page: Page,
  prompt: string,
  landed: () => Promise<boolean>,
  failure: string,
): Promise<void> {
  for (let attempt = 1; attempt <= PLAN_STEP_ATTEMPTS; attempt++) {
    await sendMessage(page, prompt)
    await waitForAgentIdle(page)
    const deadline = Date.now() + PLAN_STEP_SETTLE_MS
    while (Date.now() < deadline) {
      if (await landed())
        return
      await page.waitForTimeout(250)
    }
  }
  throw new Error(`${failure} after ${PLAN_STEP_ATTEMPTS} attempts`)
}

export { PLAN_BODY }
