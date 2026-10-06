import type { Page } from '@playwright/test'
import type { ManagedNativeScenarioContext, NativeScenarioContext } from '../helpers/nativeScenario'
import { expect } from '@playwright/test'
import { AgentGoalStatus } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { finishCleanup, withCleanup } from '../helpers/cleanup'
import { clearGoal, expandGoalsAndTodosSection, expectEmptyGoalCard, expectGoalStatus, goalAction, goalsAndTodosSection, openGoalMenu, pauseResumeClearGoal, scriptedObjective, setGoal, submitGoal } from '../helpers/goalsAndTodos'
import { SCENARIO_MARKER } from '../helpers/mockModelScript'
import { currentNativeAgent } from '../helpers/nativeScenario'
import { readNativeSidebarSnapshot } from '../helpers/nativeSidebarSnapshot'
import { retryUntilPass } from '../helpers/retryUntilPass'
import { answerControl, controlBanner, expectNoControlBanner, openWorkspace, tabById, visibleOnly, waitForAgentIdle } from '../helpers/ui'
import { openProviderAgent } from '../helpers/workspace'
import { PI_AGENT } from './scenarios'

/**
 * Approve Pi's confirmation of a goal clear.
 * Pi asks before it clears a goal, so the goal card stays until the approval.
 */
async function approvePiGoalClear(page: Page): Promise<void> {
  await expect(controlBanner(page)).toContainText('Clear goal?')
  await answerControl(page, 'allow')
}

/**
 * Set, pause, resume, and clear a native Pi session goal. The objective and the paused status survive a reload.
 * Pi runs goal turns one after another while the goal is active. The first turn ends, and the fallback holds each
 * later turn at a gate, so the goal stays active until the browser pauses it.
 */
export async function exercisePiGoalLifecycle(context: NativeScenarioContext): Promise<void> {
  const { page, modelScript } = context
  const gate = 'pi-goal-lifecycle'
  const start = await modelScript.queue({ text: 'The first goal turn finished.' })
  await modelScript.fallback({ text: 'The next goal turn is held.', gate })
  const objective = scriptedObjective(modelScript, 'Keep the Pi session goal until the browser clears it.')
  await withCleanup(async () => {
    await setGoal(page, objective, async () => {
      await modelScript.waitForSteps(start + 1)
      await modelScript.waitForGate(gate)
    })
    await pauseResumeClearGoal(page, objective, { afterClear: () => approvePiGoalClear(page) })
  }, async () => {
    await modelScript.releaseGateIfHeld(gate)
  })
}

/** Control the native goal turns while preserving the goal panel and clear confirmation. */
export async function exercisePiGoalPanel(context: ManagedNativeScenarioContext): Promise<void> {
  const { page, modelScript, leapmuxServer } = context
  const objective = 'Keep this disposable goal active until the operator pauses or clears it. Do not call tools or change files.'
  const firstGate = `pi-panel-first-${crypto.randomUUID()}`
  const secondGate = `pi-panel-resumed-${crypto.randomUUID()}`
  const start = await modelScript.queue({ text: 'The first native goal turn ended.', gate: firstGate }, { text: 'The resumed native goal turn ended.', gate: secondGate })
  await withCleanup(async () => {
    const { provider } = PI_AGENT
    const { agentId } = await openProviderAgent(leapmuxServer, context.workspaceId, PI_AGENT, { directoryPrefix: 'renderer-pi-goal-' })
    await page.reload()
    await openWorkspace(page, context.workspaceId)
    const tab = visibleOnly(tabById(page, agentId)).first()
    await tab.click()
    await expect(tab).toHaveAttribute('aria-selected', 'true')
    const agent = await currentNativeAgent(context)
    expect(agent.id).toBe(agentId)
    expect(agent.agentProvider).toBe(provider)
    await expect(goalsAndTodosSection(page)).toBeVisible()
    await expandGoalsAndTodosSection(page)
    await expectEmptyGoalCard(page)
    // Wait until the Worker holds the goal in `status`, and return the snapshot that holds it.
    const workerGoal = (status: AgentGoalStatus) => retryUntilPass(async () => {
      const snapshot = await readNativeSidebarSnapshot(context, agentId)
      expect(snapshot.goal?.status, `the Worker holds the goal as ${AgentGoalStatus[status]}`).toBe(status)
      return snapshot
    })
    await submitGoal(page, modelScript.prompt(objective))
    const startedGoal = await workerGoal(AgentGoalStatus.ACTIVE)
    expect(startedGoal.goalLoaded).toBe(true)
    expect(startedGoal.goal?.objective).toContain(objective)
    expect(startedGoal.goal?.objective).toContain(`${SCENARIO_MARKER}${modelScript.id}`)
    expect(startedGoal.goal?.nativeId).toBeTruthy()
    await expectGoalStatus(page, 'active')
    await modelScript.waitForGate(firstGate)
    expect(JSON.stringify((await modelScript.requestAt(start)).body)).toContain(objective)
    await openGoalMenu(page)
    await goalAction(page, 'pause').click()
    await workerGoal(AgentGoalStatus.PAUSED)
    await expectGoalStatus(page, 'paused')
    await modelScript.releaseGateIfHeld(firstGate)
    await waitForAgentIdle(page)
    await page.reload()
    await openWorkspace(page, context.workspaceId)
    await expect(visibleOnly(tabById(page, agentId)).first()).toHaveAttribute('aria-selected', 'true')
    await expandGoalsAndTodosSection(page)
    await expectGoalStatus(page, 'paused')
    const restoredGoal = await readNativeSidebarSnapshot(context, agentId)
    expect(restoredGoal.goalLoaded).toBe(true)
    expect(restoredGoal.goal?.nativeId).toBe(startedGoal.goal?.nativeId)
    expect(restoredGoal.goal?.objective).toBe(startedGoal.goal?.objective)
    await openGoalMenu(page)
    await goalAction(page, 'resume').click()
    expect((await workerGoal(AgentGoalStatus.ACTIVE)).goal?.nativeId).toBe(startedGoal.goal?.nativeId)
    await expectGoalStatus(page, 'active')
    await modelScript.waitForGate(secondGate)
    expect(JSON.stringify((await modelScript.requestAt(start + 1)).body)).toContain(objective)
    await clearGoal(page)
    await approvePiGoalClear(page)
    await retryUntilPass(async () => {
      const snapshot = await readNativeSidebarSnapshot(context, agentId)
      expect({ loaded: snapshot.goalLoaded, goal: snapshot.goal }, 'the Worker loaded the goal state and holds no goal').toEqual({ loaded: true, goal: undefined })
    })
    await expectNoControlBanner(page)
    await expectEmptyGoalCard(page)
  }, () => finishCleanup([modelScript.releaseGateIfHeld(firstGate), modelScript.releaseGateIfHeld(secondGate)]))
  await waitForAgentIdle(page)
  expect((await modelScript.waitForSteps(start + 2)).unexpectedRequests).toEqual([])
}
