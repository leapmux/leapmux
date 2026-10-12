/**
 * The Goals sidebar sets and clears Muse's native session goal.
 *
 * The Worker sends Muse's own goal/set and goal/clear commands, and Muse runs a native
 * goal turn for each: the objective reaches the model, and Muse reports the goal state
 * that the panel shows.
 */
import { expect } from '@playwright/test'
import { clearGoal, expandGoalsAndTodosSection, expectEmptyGoalCard, expectGoalObjective, expectGoalStatus, scriptedObjective, setGoal } from '../helpers/goalsAndTodos'
import { assistantBubbles, sendMessage, waitForAgentIdle } from '../helpers/ui'
import { museTest } from '../muse-fixtures'

/**
 * Muse works a goal in its own goal turns and parks it between them, so a
 * settled session reads its goal as paused while the objective stays.
 */
museTest('sets a native goal through the panel and clears it after reload', async ({ native }) => {
  const { page, modelScript } = native
  // Muse runs a native goal turn for the set and the clear. The fallback answers both.
  await modelScript.fallback({ text: 'The native goal turn completed.' })
  const objective = scriptedObjective(modelScript, 'Keep the Muse native goal until the browser clears it.')
  await setGoal(page, objective)
  await waitForAgentIdle(page)
  await expectGoalObjective(page, objective)
  await expectGoalStatus(page, 'paused')

  // The goal turn is a native model request that carries the objective.
  await expect.poll(async () => {
    const status = await modelScript.status()
    return status.requests.some(request => (JSON.stringify(request.body) ?? '').includes(objective.text))
  }).toBe(true)

  await page.reload()
  await expandGoalsAndTodosSection(page)
  await expectGoalObjective(page, objective)
  await expectGoalStatus(page, 'paused')

  await clearGoal(page)
  await expectEmptyGoalCard(page)
  // The session keeps answering turns after the goal cleared.
  const after = await modelScript.queue({ text: 'The session continues without a goal.' })
  await sendMessage(page, modelScript.prompt('Confirm the session still answers.'))
  await modelScript.waitForSteps(after + 1)
  await waitForAgentIdle(page)
  await expect(assistantBubbles(page).filter({ hasText: 'The session continues without a goal.' })).toBeVisible()
})
