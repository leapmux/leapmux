import { expect } from '@playwright/test'
import { clearGoal, expectEmptyGoalCard, expectGoalObjective, expectGoalStatus, scriptedObjective, submitGoal } from '../helpers/goalsAndTodos'
import { assistantBubbles, waitForAgentIdle } from '../helpers/ui'
import { mimoTest } from '../mimo-fixtures'

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

mimoTest.describe('MiMo Code session goal', () => {
  mimoTest('a goal that the judge finds met ends done, and the card clears', async ({ native }) => {
    const { page, modelScript } = native
    await modelScript.rule({
      name: 'the judge finds the condition met',
      when: { system: JUDGE_SYSTEM },
      respond: { text: verdict(true, 'The transcript says GOAL_DONE.') },
    })
    await modelScript.queue({ text: 'GOAL_DONE' })
    const objective = scriptedObjective(modelScript, 'Reply with the word GOAL_DONE.')
    await submitGoal(page, objective.input)
    await modelScript.waitForSteps()
    await waitForAgentIdle(page)

    await expectGoalObjective(page, objective)
    await expectGoalStatus(page, 'done')
    // The model's own answer. The goal notice in the transcript states the objective,
    // which holds the word too, so only an agent bubble proves that the model answered.
    await expect(assistantBubbles(page).filter({ hasText: 'GOAL_DONE' })).toBeVisible()

    await clearGoal(page)
    await expectEmptyGoalCard(page)
  })

  // A verdict of "not met" makes MiMo run another pass of the loop, with the
  // judge's reason as the reminder. The second verdict ends the goal.
  mimoTest('an unmet verdict runs another pass before the goal ends', async ({ native }) => {
    const { page, modelScript } = native
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
    await submitGoal(page, modelScript.prompt('Reply with the word SECOND_PASS.'))
    await modelScript.waitForSteps()
    await waitForAgentIdle(page)

    await expectGoalStatus(page, 'done')
    await expect(assistantBubbles(page).filter({ hasText: 'SECOND_PASS' })).toBeVisible()
    const status = await modelScript.status()
    expect(status.ruleMatches['the judge first finds the condition unmet']).toBe(1)
    expect(status.ruleMatches['the judge then finds the condition met']).toBe(1)
  })
})
