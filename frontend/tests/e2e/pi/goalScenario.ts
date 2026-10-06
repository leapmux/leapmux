import type { Page } from '@playwright/test'
import type { ManagedNativeScenarioContext, NativeScenarioContext } from '../helpers/nativeScenario'
import { expect } from '@playwright/test'
import { AgentGoalStatus, AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { agentOpenOptions } from '../agentSettings'
import { openAgentViaAPI } from '../helpers/api'
import { finishCleanup, withCleanup } from '../helpers/cleanup'
import { clearGoal, expandGoalsAndTodosSection, expectEmptyGoalCard, expectGoalStatus, goalAction, goalsAndTodosSection, openGoalMenu, pauseResumeClearGoal, scriptedObjective, setGoal, submitGoal } from '../helpers/goalsAndTodos'
import { SCENARIO_MARKER } from '../helpers/mockModelScript'
import { currentNativeAgent } from '../helpers/nativeScenario'
import { readNativeSidebarSnapshot } from '../helpers/nativeSidebarSnapshot'
import { createTestDirectory } from '../helpers/runDirectory'
import { controlActions, controlBanner, expectNoControlBanner, openWorkspace, tabById, visibleOnly, waitForAgentIdle } from '../helpers/ui'

/**
 * Approve Pi's confirmation of a goal clear.
 * Pi asks before it clears a goal, so the goal card stays until the approval.
 */
async function approvePiGoalClear(page: Page): Promise<void> {
  await expect(controlBanner(page)).toContainText('Clear goal?')
  await controlActions(page).getByRole('button', { name: 'Approve', exact: true }).click()
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
    const provider = AgentProvider.PI
    const agentId = await openAgentViaAPI(leapmuxServer.hubUrl, leapmuxServer.adminToken, leapmuxServer.workerId, context.workspaceId, createTestDirectory('renderer-pi-goal-'), agentOpenOptions(provider))
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
    await submitGoal(page, modelScript.prompt(objective))
    await expect.poll(async () => (await readNativeSidebarSnapshot(context, agentId)).goal?.status).toBe(AgentGoalStatus.ACTIVE)
    const startedGoal = await readNativeSidebarSnapshot(context, agentId)
    expect(startedGoal.goalLoaded).toBe(true)
    expect(startedGoal.goal?.objective).toContain(objective)
    expect(startedGoal.goal?.objective).toContain(`${SCENARIO_MARKER}${modelScript.id}`)
    expect(startedGoal.goal?.nativeId).toBeTruthy()
    await expectGoalStatus(page, 'active')
    await modelScript.waitForGate(firstGate)
    expect(JSON.stringify((await modelScript.requestAt(start)).body)).toContain(objective)
    await openGoalMenu(page)
    await goalAction(page, 'pause').click()
    await expect.poll(async () => (await readNativeSidebarSnapshot(context, agentId)).goal?.status).toBe(AgentGoalStatus.PAUSED)
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
    await expect.poll(async () => (await readNativeSidebarSnapshot(context, agentId)).goal?.status).toBe(AgentGoalStatus.ACTIVE)
    expect((await readNativeSidebarSnapshot(context, agentId)).goal?.nativeId).toBe(startedGoal.goal?.nativeId)
    await expectGoalStatus(page, 'active')
    await modelScript.waitForGate(secondGate)
    expect(JSON.stringify((await modelScript.requestAt(start + 1)).body)).toContain(objective)
    await clearGoal(page)
    await approvePiGoalClear(page)
    await expect.poll(async () => {
      const snapshot = await readNativeSidebarSnapshot(context, agentId)
      return snapshot.goalLoaded && snapshot.goal === undefined
    }).toBe(true)
    await expectNoControlBanner(page)
    await expectEmptyGoalCard(page)
  }, () => finishCleanup([modelScript.releaseGateIfHeld(firstGate), modelScript.releaseGateIfHeld(secondGate)]))
  await waitForAgentIdle(page)
  expect((await modelScript.waitForSteps(start + 2)).unexpectedRequests).toEqual([])
}
