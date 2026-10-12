/**
 * Muse works a session goal in its own goal turns and parks it between them.
 *
 * The panel's pause and resume commands run through the native goal/pause and
 * goal/resume routes; each settled state parks the goal again, so the objective
 * survives the whole cycle and reads paused throughout, and the clear empties it.
 */
import { expect } from '@playwright/test'
import { clearGoal, expandGoalsAndTodosSection, expectEmptyGoalCard, expectGoalObjective, expectGoalStatus, goalAction, openGoalMenu, scriptedObjective, setGoal } from '../helpers/goalsAndTodos'
import { waitForAgentIdle } from '../helpers/ui'
import { museTest } from '../muse-fixtures'

museTest('session-goal-pause-and-resume: parks and resumes a native goal after reload', async ({ native }) => {
  const { page, modelScript } = native
  await modelScript.fallback({ text: 'The native goal turn completed.' })
  const objective = scriptedObjective(modelScript, 'Keep the Muse native goal through its pause and resume.')
  await setGoal(page, objective)
  await waitForAgentIdle(page)
  await expectGoalObjective(page, objective)
  await expectGoalStatus(page, 'paused')

  // A parked goal offers no pause (it is already paused); its resume runs a fresh
  // native goal turn and parks again once it settles.
  await openGoalMenu(page)
  await expect(goalAction(page, 'pause')).toBeDisabled()
  await goalAction(page, 'resume').click()
  await waitForAgentIdle(page)
  await expectGoalObjective(page, objective)
  await expectGoalStatus(page, 'paused')

  await page.reload()
  await expandGoalsAndTodosSection(page)
  await expectGoalObjective(page, objective)
  await expectGoalStatus(page, 'paused')

  await clearGoal(page)
  await expectEmptyGoalCard(page)
})
