import type { Page } from '@playwright/test'
import type { ModelScript } from '../helpers/modelScriptFixture'
import type { ManagedNativeScenarioContext } from '../helpers/nativeScenario'
import { expect } from '@playwright/test'
import { AgentGoalStatus, AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { agentOpenOptions, agentSettings } from '../agentSettings'
import { openAgentViaAPI } from '../helpers/api'
import { finishCleanup, withCleanup } from '../helpers/cleanup'
import { SCENARIO_MARKER } from '../helpers/mockModelScript'
import { currentNativeAgent } from '../helpers/nativeScenario'
import { readNativeSidebarSnapshot } from '../helpers/nativeSidebarSnapshot'
import { createTestDirectory } from '../helpers/runDirectory'
import { expandGoalsAndTodosSection, expectGoalStatus, goalAction, openGoalMenu } from '../helpers/subagentRegistry'
import { openWorkspace, tabById, visibleOnly, waitForAgentIdle } from '../helpers/ui'

export function scriptedObjective(modelScript: ModelScript, text: string): GoalObjective {
  return { input: modelScript.prompt(text), text, marker: `${SCENARIO_MARKER}${modelScript.id}` }
}

export async function setGoal(page: Page, objective: GoalObjective, afterSubmit?: () => Promise<void>): Promise<void> {
  await expandGoalsAndTodosSection(page)
  await goalAction(page, 'set').click()
  await page.locator('[data-testid="goal-editor"]:visible .ProseMirror').fill(objective.input)
  await page.locator('[data-testid="set-goal-submit"]:visible').click()
  await afterSubmit?.()
  await expectObjective(page, objective)
  await expectGoalStatus(page, 'active')
}

export async function pauseResumeClearGoal(page: Page, objective: GoalObjective, options: { clearApproval?: boolean } = {}): Promise<void> {
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

/** Control the native goal turns while preserving the goal panel and clear confirmation. */
export async function exercisePiGoalPanel(context: ManagedNativeScenarioContext): Promise<void> {
  const { page, modelScript, leapmuxServer } = context
  const objective = 'Keep this disposable goal active until the operator pauses or clears it. Do not call tools or change files.'
  const firstGate = `pi-panel-first-${crypto.randomUUID()}`
  const secondGate = `pi-panel-resumed-${crypto.randomUUID()}`
  const start = (await modelScript.status()).stepCount
  await withCleanup(async () => {
    await modelScript.queue({ text: 'The first native goal turn ended.', gate: firstGate }, { text: 'The resumed native goal turn ended.', gate: secondGate })

    const provider = AgentProvider.PI
    const agentId = await openAgentViaAPI(leapmuxServer.hubUrl, leapmuxServer.adminToken, leapmuxServer.workerId, context.workspaceId, createTestDirectory('renderer-pi-goal-'), {
      agentProvider: provider,
      ...agentOpenOptions(agentSettings(provider)),
    })
    await page.reload()
    await openWorkspace(page, context.workspaceId)
    const tab = visibleOnly(tabById(page, agentId)).first()
    await tab.click()
    await expect(tab).toHaveAttribute('aria-selected', 'true')
    const agent = await currentNativeAgent(context)
    expect(agent.id).toBe(agentId)
    expect(agent.agentProvider).toBe(provider)
    await expect(page.locator('[data-testid="section-header-todos"]:visible')).toBeVisible()
    await expandGoalsAndTodosSection(page)
    await expect(page.locator('[data-testid="goal-card-empty"]:visible')).toBeVisible()
    await goalAction(page, 'set').click()
    await page.locator('[data-testid="goal-editor"]:visible .ProseMirror').fill(modelScript.prompt(objective))
    await page.locator('[data-testid="set-goal-submit"]:visible').click()
    await expect.poll(async () => (await readNativeSidebarSnapshot(context, agentId)).goal?.status).toBe(AgentGoalStatus.ACTIVE)
    const startedGoal = await readNativeSidebarSnapshot(context, agentId)
    expect(startedGoal.goalLoaded).toBe(true)
    expect(startedGoal.goal?.objective).toContain(objective)
    expect(startedGoal.goal?.objective).toContain(`${SCENARIO_MARKER}${modelScript.id}`)
    expect(startedGoal.goal?.nativeId).toBeTruthy()
    await expectGoalStatus(page, 'active')
    const first = await modelScript.waitForGate(firstGate)
    expect(JSON.stringify(first.requests.find(request => request.stepIndex === start)?.body)).toContain(objective)
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
    const resumed = await modelScript.waitForGate(secondGate)
    expect(JSON.stringify(resumed.requests.find(request => request.stepIndex === start + 1)?.body)).toContain(objective)
    await openGoalMenu(page)
    await goalAction(page, 'clear').click()
    const banner = page.getByTestId('control-banner').filter({ visible: true })
    await expect(banner).toContainText('Clear goal?')
    await page.getByTestId('control-allow-btn').filter({ visible: true }).click()
    await expect.poll(async () => {
      const snapshot = await readNativeSidebarSnapshot(context, agentId)
      return snapshot.goalLoaded && snapshot.goal === undefined
    }).toBe(true)
    await expect(banner).toHaveCount(0)
    await expect(page.locator('[data-testid="goal-card-empty"]:visible')).toBeVisible()
  }, () => finishCleanup([modelScript.releaseGateIfHeld(firstGate), modelScript.releaseGateIfHeld(secondGate)]))
  await waitForAgentIdle(page)
  expect((await modelScript.waitForSteps(start + 2)).unexpectedRequests).toEqual([])
}
