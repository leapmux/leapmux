import type { Page } from '@playwright/test'
import type { ModelScript } from '../helpers/modelScriptFixture'
import { expect } from '@playwright/test'
import { SCENARIO_MARKER } from '../helpers/mockModelScript'
import { expandGoalsAndTodosSection, expectGoalStatus, goalAction, openGoalMenu } from '../helpers/subagentRegistry'
import { zcodeTest } from '../zcode-fixtures'

function scriptedObjective(modelScript: ModelScript, text: string): GoalObjective {
  return { input: modelScript.prompt(text), text, marker: `${SCENARIO_MARKER}${modelScript.id}` }
}

async function setGoal(page: Page, objective: GoalObjective, afterSubmit?: () => Promise<void>): Promise<void> {
  await expandGoalsAndTodosSection(page)
  await goalAction(page, 'set').click()
  await page.locator('[data-testid="goal-editor"]:visible .ProseMirror').fill(objective.input)
  await page.locator('[data-testid="set-goal-submit"]:visible').click()
  await afterSubmit?.()
  await expectObjective(page, objective)
  await expectGoalStatus(page, 'active')
}

async function pauseResumeClearGoal(page: Page, objective: GoalObjective, options: { clearApproval?: boolean } = {}): Promise<void> {
  await openGoalMenu(page)
  await goalAction(page, 'pause').click()
  await expectGoalStatus(page, 'paused')

  await page.reload()
  await expandGoalsAndTodosSection(page)
  await expectObjective(page, objective)
  await expectGoalStatus(page, 'paused')

  await openGoalMenu(page)
  await goalAction(page, 'resume').click()
  await expectGoalStatus(page, 'active')

  await openGoalMenu(page)
  await goalAction(page, 'clear').click()
  if (options.clearApproval) {
    const approval = page.getByTestId('control-banner').filter({ visible: true })
    await expect(approval).toContainText('Clear goal?')
    await page.getByRole('button', { name: 'Approve', exact: true }).click()
  }
  await expect(page.locator('[data-testid="goal-card-empty"]:visible')).toBeVisible()
}

interface GoalObjective {
  input: string
  text: string
  marker: string
}

async function expectObjective(page: Page, objective: GoalObjective): Promise<void> {
  const displayed = page.locator('[data-testid="goal-objective"]:visible')
  await expect(displayed).toContainText(objective.text)
  await expect(displayed).toContainText(objective.marker)
}

zcodeTest('sets, pauses, resumes, and clears the native goal', async ({ authenticatedZCodeWorkspace, page, modelScript }) => {
  void authenticatedZCodeWorkspace
  await modelScript.fallback({ text: 'Goal turn complete.' })
  const objective = scriptedObjective(modelScript, 'Keep the ZCode session goal until the browser clears it.')
  await setGoal(page, objective)
  await pauseResumeClearGoal(page, objective)
})
