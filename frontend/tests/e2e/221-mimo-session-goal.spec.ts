import type { Page } from '@playwright/test'
import { expandGoalsAndTodosSection, expectGoalStatus, goalAction, openGoalMenu } from './helpers/subagentRegistry'
import { assistantBubbles, waitForAgentIdle } from './helpers/ui'
import { expect, MIMO_E2E_SKIP_REASON, mimoTest } from './mimo-fixtures'

mimoTest.skip(!!MIMO_E2E_SKIP_REASON, MIMO_E2E_SKIP_REASON || '')

/**
 * The system text of MiMo's goal judge (`JUDGE_SYSTEM` in `src/session/goal.ts`).
 *
 * The judge is a separate model call before each stop while a goal is active. It
 * reads the transcript and answers one JSON verdict.
 */
const JUDGE_SYSTEM = 'You are evaluating a stop-condition hook in Mimo Code'

/** One verdict of the judge, as the JSON object MiMo parses. */
function verdict(ok: boolean, reason: string): string {
  return JSON.stringify({ ok, reason })
}

/**
 * Set the goal from the sidebar card.
 *
 * MiMo's goal command takes the condition as the prompt of the turn that it
 * starts, so the objective carries the scenario marker.
 */
async function setGoal(page: Page, objective: string): Promise<void> {
  await expandGoalsAndTodosSection(page)
  await goalAction(page, 'set').click()
  await page.locator('[data-testid="goal-editor"]:visible .ProseMirror').fill(objective)
  await page.locator('[data-testid="set-goal-submit"]:visible').click()
}

mimoTest.describe('MiMo Code session goal', () => {
  mimoTest('a goal that the judge finds met ends done, and the card clears', async ({ authenticatedMiMoWorkspace, page, modelScript }) => {
    void authenticatedMiMoWorkspace
    await modelScript.rule({
      name: 'the judge finds the condition met',
      when: { system: JUDGE_SYSTEM },
      respond: { text: verdict(true, 'The transcript says GOAL_DONE.') },
    })
    await modelScript.queue({ text: 'GOAL_DONE' })
    await setGoal(page, modelScript.prompt('Reply with the word GOAL_DONE.'))
    await modelScript.waitForSteps()
    await waitForAgentIdle(page, 120_000)

    await expect(page.locator('[data-testid="goal-objective"]:visible')).toContainText('Reply with the word GOAL_DONE.')
    await expectGoalStatus(page, 'done')
    // The model's own answer. The goal notice in the transcript states the objective,
    // which holds the word too, so only an agent bubble proves that the model answered.
    await expect(assistantBubbles(page).filter({ hasText: 'GOAL_DONE' })).toBeVisible()

    await openGoalMenu(page)
    await goalAction(page, 'clear').click()
    await expect(page.locator('[data-testid="goal-card-empty"]:visible')).toBeVisible()
  })

  // A verdict of "not met" makes MiMo run another pass of the loop, with the
  // judge's reason as the reminder. The second verdict ends the goal.
  mimoTest('an unmet verdict runs another pass before the goal ends', async ({ authenticatedMiMoWorkspace, page, modelScript }) => {
    void authenticatedMiMoWorkspace
    await modelScript.rule(
      {
        name: 'the judge first finds the condition unmet',
        when: { system: JUDGE_SYSTEM },
        respond: { text: verdict(false, 'The word SECOND_PASS is missing.') },
        once: true,
      },
      {
        name: 'the judge then finds the condition met',
        when: { system: JUDGE_SYSTEM },
        respond: { text: verdict(true, 'The transcript says SECOND_PASS.') },
      },
    )
    await modelScript.queue({ text: 'FIRST_PASS' }, { text: 'SECOND_PASS' })
    await setGoal(page, modelScript.prompt('Reply with the word SECOND_PASS.'))
    await modelScript.waitForSteps()
    await waitForAgentIdle(page, 120_000)

    await expectGoalStatus(page, 'done')
    await expect(assistantBubbles(page).filter({ hasText: 'SECOND_PASS' })).toBeVisible()
    const status = await modelScript.status()
    expect(status.ruleMatches['the judge first finds the condition unmet']).toBe(1)
    expect(status.ruleMatches['the judge then finds the condition met']).toBe(1)
  })
})
