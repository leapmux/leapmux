import type { ManagedNativeScenarioContext } from '../helpers/nativeScenario'
import { expect } from '@playwright/test'
import { finishCleanup, withCleanup } from '../helpers/cleanup'
import { clearGoal, expandGoalsAndTodosSection, expectEmptyGoalCard, expectGoalObjective, expectGoalStatus, goalAction, openGoalMenu, submitGoal } from '../helpers/goalsAndTodos'
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
    // Reasonix runs a goal round at once, so the interrupt control proves that the goal turn started.
    const setRunningGoal = async () => {
      await submitGoal(page, nativeObjective)
      await expectGoalObjective(page, objective)
      await expectGoalStatus(page, 'active')
      await expect(page.getByTestId('interrupt-button').filter({ visible: true })).toBeVisible()
    }
    await setRunningGoal()
    const first = await modelScript.waitForGate(firstGate)
    expect(first.requests.find(request => request.stepIndex === start)?.body).toBeDefined()
    expect(JSON.stringify(first.requests.find(request => request.stepIndex === start)?.body)).toContain(objective)
    await openGoalMenu(page)
    await submitGoal(page, 'Retain this replacement after the refusal.')
    await expect(page.getByText('agent is already running a turn', { exact: false })).toBeVisible()
    const refusedDialog = page.getByTestId('set-goal-dialog')
    await expect(refusedDialog).toBeVisible()
    await expect(refusedDialog.locator('.ProseMirror')).toContainText('Retain this replacement after the refusal.')
    await refusedDialog.getByRole('button', { name: 'Cancel', exact: true }).click()
    await expectGoalObjective(page, objective)
    await page.reload()
    await openWorkspace(page, context.workspaceId)
    await expandGoalsAndTodosSection(page)
    await expectGoalObjective(page, objective)
    await openGoalMenu(page)
    await expect(goalAction(page, 'pause')).toHaveCount(0)
    await expect(goalAction(page, 'resume')).toHaveCount(0)
    await goalAction(page, 'clear').click()
    await expectEmptyGoalCard(page)
    const interrupt = page.getByTestId('interrupt-button').filter({ visible: true })
    await expect(interrupt).toBeVisible()
    await interrupt.click()
    await expect(interrupt).toHaveCount(0)
    await setRunningGoal()
    const second = await modelScript.waitForGate(secondGate)
    expect(second.requests.find(request => request.stepIndex === start + 1)?.body).toBeDefined()
    expect(JSON.stringify(second.requests.find(request => request.stepIndex === start + 1)?.body)).toContain(objective)
    await interrupt.click()
    await expect(interrupt).toHaveCount(0)
    await expectGoalStatus(page, 'blocked')
    await clearGoal(page)
    await expectEmptyGoalCard(page)
    await page.reload()
    await openWorkspace(page, context.workspaceId)
    await expandGoalsAndTodosSection(page)
    await expectEmptyGoalCard(page)
    const status = await modelScript.waitForSteps(start + 2)
    expect(status.unexpectedRequests).toEqual([])
  }, () => finishCleanup([modelScript.releaseGateIfHeld(firstGate), modelScript.releaseGateIfHeld(secondGate)]))
}
