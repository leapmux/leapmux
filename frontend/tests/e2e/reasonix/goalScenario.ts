import type { ManagedNativeScenarioContext } from '../helpers/nativeScenario'
import { expect } from '@playwright/test'
import { finishCleanup, withCleanup } from '../helpers/cleanup'
import { expandGoalsAndTodosSection, expectGoalStatus, goalAction, openGoalMenu } from '../helpers/subagentRegistry'
import { openWorkspace } from '../helpers/ui'

/** Preserve a native goal through busy refusal, cancellation, clear, and browser reload. */
export async function exerciseReasonixGoalLifecycle(context: ManagedNativeScenarioContext): Promise<void> {
  const { page, modelScript } = context
  const objective = 'Keep the native objective until the browser clears it.'
  const nativeObjective = modelScript.prompt(objective)
  const firstGate = `reasonix-goal-first-${crypto.randomUUID()}`
  const secondGate = `reasonix-goal-second-${crypto.randomUUID()}`
  const start = (await modelScript.status()).stepCount
  await withCleanup(async () => {
    await modelScript.queue(
      { text: 'The first native goal turn must stay held until interruption.', gate: firstGate },
      { text: 'The second native goal turn must stay held until interruption.', gate: secondGate },
    )
    await expandGoalsAndTodosSection(page)
    const setGoal = async () => {
      await goalAction(page, 'set').click()
      await page.locator('[data-testid="goal-editor"]:visible .ProseMirror').fill(nativeObjective)
      await page.locator('[data-testid="set-goal-submit"]:visible').click()
      await expect(page.locator('[data-testid="goal-objective"]:visible')).toContainText(objective)
      await expectGoalStatus(page, 'active')
      await expect(page.getByTestId('interrupt-button').filter({ visible: true })).toBeVisible()
    }
    await setGoal()
    const first = await modelScript.waitForGate(firstGate)
    expect(first.requests.find(request => request.stepIndex === start)?.body).toBeDefined()
    expect(JSON.stringify(first.requests.find(request => request.stepIndex === start)?.body)).toContain(objective)
    await openGoalMenu(page)
    await goalAction(page, 'set').click()
    await page.locator('[data-testid="goal-editor"]:visible .ProseMirror').fill('Retain this replacement after the refusal.')
    await page.locator('[data-testid="set-goal-submit"]:visible').click()
    await expect(page.getByText('agent is already running a turn', { exact: false })).toBeVisible()
    const refusedDialog = page.getByTestId('set-goal-dialog')
    await expect(refusedDialog).toBeVisible()
    await expect(refusedDialog.locator('.ProseMirror')).toContainText('Retain this replacement after the refusal.')
    await refusedDialog.getByRole('button', { name: 'Cancel', exact: true }).click()
    await expect(page.locator('[data-testid="goal-objective"]:visible')).toContainText(objective)
    await page.reload()
    await openWorkspace(page, context.workspaceId)
    await expandGoalsAndTodosSection(page)
    await expect(page.locator('[data-testid="goal-objective"]:visible')).toContainText(objective)
    await openGoalMenu(page)
    await expect(goalAction(page, 'pause')).toHaveCount(0)
    await expect(goalAction(page, 'resume')).toHaveCount(0)
    await goalAction(page, 'clear').click()
    await expect(page.locator('[data-testid="goal-card-empty"]:visible')).toBeVisible()
    const interrupt = page.getByTestId('interrupt-button').filter({ visible: true })
    await expect(interrupt).toBeVisible()
    await interrupt.click()
    await expect(interrupt).toHaveCount(0)
    await setGoal()
    const second = await modelScript.waitForGate(secondGate)
    expect(second.requests.find(request => request.stepIndex === start + 1)?.body).toBeDefined()
    expect(JSON.stringify(second.requests.find(request => request.stepIndex === start + 1)?.body)).toContain(objective)
    await interrupt.click()
    await expect(interrupt).toHaveCount(0)
    await expectGoalStatus(page, 'blocked')
    await openGoalMenu(page)
    await goalAction(page, 'clear').click()
    await expect(page.locator('[data-testid="goal-card-empty"]:visible')).toBeVisible()
    await page.reload()
    await openWorkspace(page, context.workspaceId)
    await expandGoalsAndTodosSection(page)
    await expect(page.locator('[data-testid="goal-card-empty"]:visible')).toBeVisible()
    const status = await modelScript.waitForSteps(start + 2)
    expect(status.unexpectedRequests).toEqual([])
  }, () => finishCleanup([modelScript.releaseGateIfHeld(firstGate), modelScript.releaseGateIfHeld(secondGate)]))
}
