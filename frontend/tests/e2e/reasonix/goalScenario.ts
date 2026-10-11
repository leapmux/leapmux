import type { ManagedNativeScenarioContext } from '../helpers/nativeScenario'
import { expect } from '@playwright/test'
import { finishCleanup, withCleanup } from '../helpers/cleanup'
import { clearGoal, expandGoalsAndTodosSection, expectEmptyGoalCard, expectGoalObjective, expectGoalStatus, goalAction, openGoalMenu, scriptedObjective, setGoal, submitGoal } from '../helpers/goalsAndTodos'
import { interruptButton, openWorkspace } from '../helpers/ui'

/** Preserve a native goal through busy refusal, cancellation, clear, and browser reload. */
export async function exerciseReasonixGoalLifecycle(context: ManagedNativeScenarioContext): Promise<void> {
  const { page, modelScript } = context
  const objective = scriptedObjective(modelScript, 'Keep the native objective until the browser clears it.')
  const firstGate = `reasonix-goal-first-${crypto.randomUUID()}`
  const secondGate = `reasonix-goal-second-${crypto.randomUUID()}`
  const start = await modelScript.queue(
    { text: 'The first native goal turn must stay held until interruption.', gate: firstGate },
    { text: 'The second native goal turn must stay held until interruption.', gate: secondGate },
  )
  await withCleanup(async () => {
    // Reasonix runs a goal round at once, so the interrupt control proves that the goal turn started.
    const interrupt = interruptButton(page)
    const setRunningGoal = async () => {
      await setGoal(page, objective)
      await expect(interrupt).toBeVisible()
    }
    await setRunningGoal()
    await modelScript.waitForGate(firstGate)
    const first = await modelScript.requestAt(start)
    expect(first.body).toBeDefined()
    expect(JSON.stringify(first.body)).toContain(objective.text)
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
    await expect(interrupt).toBeVisible()
    await interrupt.click()
    await expect(interrupt).toHaveCount(0)
    await setRunningGoal()
    await modelScript.waitForGate(secondGate)
    const second = await modelScript.requestAt(start + 1)
    expect(second.body).toBeDefined()
    expect(JSON.stringify(second.body)).toContain(objective.text)
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
