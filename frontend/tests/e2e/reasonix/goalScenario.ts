import type { ManagedNativeScenarioContext } from '../helpers/nativeScenario'
import { expect } from '@playwright/test'
import { AgentGoalAction, WorkerAgentService } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { getTestChannel } from '../helpers/api'
import { finishCleanup, withCleanup } from '../helpers/cleanup'
import { clearGoal, expandGoalsAndTodosSection, expectEmptyGoalCard, expectGoalObjective, expectGoalStatus, goalAction, openGoalMenu, scriptedObjective, setGoal, submitGoal } from '../helpers/goalsAndTodos'
import { currentNativeAgent } from '../helpers/nativeScenario'
import { interruptButton, messageContents, openWorkspace, waitForAgentIdle } from '../helpers/ui'

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
    await expect(goalAction(page, 'pause')).toBeEnabled()
    await expect(goalAction(page, 'resume')).toBeDisabled()
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
  }, () => finishCleanup([
    modelScript.releaseGateIfHeld(firstGate),
    modelScript.releaseGateIfHeld(secondGate),
  ]))
}

/** Pause the retained objective, reload, and resume it without creating a new native goal. */
export async function exerciseReasonixGoalPauseResume(context: ManagedNativeScenarioContext): Promise<void> {
  const { page, modelScript } = context
  const before = await currentNativeAgent(context)
  const objective = scriptedObjective(modelScript, 'Retain this native goal across pause and resume.')
  const firstGate = `reasonix-pause-${crypto.randomUUID()}`
  const resumedGate = `reasonix-resume-${crypto.randomUUID()}`
  const start = await modelScript.queue(
    { text: 'Hold the first goal turn.', gate: firstGate },
    { text: 'Hold the resumed goal turn.', gate: resumedGate },
  )
  await withCleanup(async () => {
    await setGoal(page, objective)
    await modelScript.waitForGate(firstGate)
    await expectGoalStatus(page, 'active')
    await openGoalMenu(page)
    await goalAction(page, 'pause').click()
    await expectGoalStatus(page, 'paused')
    // Native Pause stops continuation at the next boundary, so release the current turn and let it finish.
    await expect(interruptButton(page)).toBeVisible()
    await modelScript.releaseGateIfHeld(firstGate)
    await waitForAgentIdle(page)
    await expectGoalStatus(page, 'paused')
    await page.reload()
    await openWorkspace(page, context.workspaceId)
    await expandGoalsAndTodosSection(page)
    await expectGoalObjective(page, objective)
    await expectGoalStatus(page, 'paused')
    await openGoalMenu(page)
    await goalAction(page, 'resume').click()
    await modelScript.waitForGate(resumedGate)
    await expectGoalStatus(page, 'active')
    await expectGoalObjective(page, objective)
    const resumed = await modelScript.requestAt(start + 1)
    expect(JSON.stringify(resumed.body)).toContain(objective.text)
    expect(JSON.stringify(resumed.body)).toContain('Continue the retained goal.')
    expect((await currentNativeAgent(context)).agentSessionId).toBe(before.agentSessionId)
    await openGoalMenu(page)
    await goalAction(page, 'pause').click()
    await expectGoalStatus(page, 'paused')
    await modelScript.releaseGateIfHeld(resumedGate)
    await waitForAgentIdle(page)
    await clearGoal(page)
    await expectEmptyGoalCard(page)
    await page.reload()
    await openWorkspace(page, context.workspaceId)
    await expandGoalsAndTodosSection(page)
    await expectEmptyGoalCard(page)
    expect((await modelScript.waitForSteps(start + 2)).unexpectedRequests).toEqual([])
  }, () => finishCleanup([
    modelScript.releaseGateIfHeld(firstGate),
    modelScript.releaseGateIfHeld(resumedGate),
  ]))
}

/** Recover the native goal that survives a model failure, including its retained objective. */
export async function exerciseReasonixGoalErrorResume(context: ManagedNativeScenarioContext): Promise<void> {
  const { page, modelScript } = context
  const before = await currentNativeAgent(context)
  const objective = scriptedObjective(modelScript, 'Retain the native goal after a model failure.')
  const error = { status: 400, code: 'invalid_request_error', message: 'NATIVE GOAL FAILURE' }
  const gate = `reasonix-goal-error-resume-${crypto.randomUUID()}`
  // Reasonix tries its alternate request form once, including for this native 400 response.
  const start = await modelScript.queue({ error }, { error }, { text: 'Hold the recovered native goal.', gate })
  await withCleanup(async () => {
    await submitGoal(page, objective.input)
    await modelScript.waitForSteps(start + 2)
    await waitForAgentIdle(page)
    await expectGoalObjective(page, objective)
    await expectGoalStatus(page, 'blocked')
    await expect(messageContents(page).filter({ hasText: error.message })).toHaveCount(1)
    await page.reload()
    await openWorkspace(page, context.workspaceId)
    await expandGoalsAndTodosSection(page)
    await expectGoalObjective(page, objective)
    await expectGoalStatus(page, 'blocked')
    const channel = await getTestChannel(context.leapmuxServer.hubUrl, context.leapmuxServer.adminToken)
    await channel.callWorker(context.leapmuxServer.workerId, WorkerAgentService.method.updateAgentGoal, { agentId: before.id, action: AgentGoalAction.RESUME })
    await modelScript.waitForGate(gate)
    await expectGoalStatus(page, 'active')
    await expectGoalObjective(page, objective)
    expect((await currentNativeAgent(context)).agentSessionId).toBe(before.agentSessionId)
    expect(JSON.stringify((await modelScript.requestAt(start + 2)).body)).toContain(objective.text)
    await openGoalMenu(page)
    await goalAction(page, 'pause').click()
    await expectGoalStatus(page, 'paused')
    await modelScript.releaseGateIfHeld(gate)
    await waitForAgentIdle(page)
    await clearGoal(page)
    await expectEmptyGoalCard(page)
    expect((await modelScript.waitForSteps(start + 3)).unexpectedRequests).toEqual([])
  }, async () => { await modelScript.releaseGateIfHeld(gate) })
}
