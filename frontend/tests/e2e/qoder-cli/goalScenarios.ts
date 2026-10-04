import type { NativeScenarioContext } from '../helpers/nativeScenario'
import { expandGoalsAndTodosSection, expectGoalStatus, goalAction, openGoalMenu } from '../helpers/subagentRegistry'
import { expect } from '../qoder-fixtures'

/** Exercise the actual native control and retain every original assertion. */
export async function exerciseNativeGoalCycle(context: NativeScenarioContext): Promise<void> {
  const { page, modelScript } = context

  await expandGoalsAndTodosSection(page)
  await expect(page.locator('[data-testid="goal-card-empty"]:visible')).toBeVisible()
  await goalAction(page, 'set').click()
  const objective = 'Keep the Qoder goal until I clear it.'
  await page.locator('[data-testid="goal-editor"]:visible .ProseMirror').fill(objective)
  await page.locator('[data-testid="set-goal-submit"]:visible').click()
  await expect(page.locator('[data-testid="goal-objective"]:visible')).toContainText(objective)
  await expectGoalStatus(page, 'active')

  await openGoalMenu(page)
  await goalAction(page, 'pause').click()
  await expectGoalStatus(page, 'paused')
  await page.reload()
  await expandGoalsAndTodosSection(page)
  await expect(page.locator('[data-testid="goal-objective"]:visible')).toContainText(objective)
  await expectGoalStatus(page, 'paused')

  await openGoalMenu(page)
  await goalAction(page, 'resume').click()
  await expectGoalStatus(page, 'active')
  await openGoalMenu(page)
  await goalAction(page, 'clear').click()
  await expect(page.locator('[data-testid="goal-card-empty"]:visible')).toBeVisible()
  expect((await modelScript.status()).requests).toHaveLength(0)
}
