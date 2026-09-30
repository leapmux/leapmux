import type { Page } from '@playwright/test'
import type { ServerInfo } from './fixtures'
import type { ModelScript } from './helpers/modelScriptFixture'
import { ListAgentInputQueueRequestSchema, ListAgentInputQueueResponseSchema } from '../../src/generated/proto/leapmux/v1/agent_pb'
import { expect } from './fixtures'
import { getTestChannel } from './helpers/api'
import { SCENARIO_MARKER } from './helpers/mockModelScript'
import { expandGoalsAndTodosSection, expectGoalStatus, goalAction, openGoalMenu } from './helpers/subagentRegistry'
import { KILO_E2E_SKIP_REASON, kiloTest } from './kilo-fixtures'
import { PI_E2E_SKIP_REASON, piTest } from './pi-fixtures'
import { ZCODE_E2E_SKIP_REASON, zcodeTest } from './zcode-fixtures'

interface GoalObjective {
  input: string
  text: string
  marker: string
}

// Kilo's ACP runUntilIdle waits this long for a slash command that emits no
// matching idle or assistant event. /goal pause emits only a goal notice.
const KILO_ACP_IDLE_FALLBACK_MS = 60_000

function scriptedObjective(modelScript: ModelScript, text: string): GoalObjective {
  return { input: modelScript.prompt(text), text, marker: `${SCENARIO_MARKER}${modelScript.id}` }
}

async function expectObjective(page: Page, objective: GoalObjective): Promise<void> {
  const displayed = page.locator('[data-testid="goal-objective"]:visible')
  await expect(displayed).toContainText(objective.text)
  await expect(displayed).toContainText(objective.marker)
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

async function deliverQueuedKiloGoalCommand(page: Page, command: string): Promise<void> {
  const queue = page.locator('[data-testid="agent-input-queue"]:visible')
  await expect(queue).toContainText(command)
  const interrupt = page.locator('[data-testid="interrupt-button"]:visible')
  if (await interrupt.isVisible()) {
    try {
      await interrupt.click()
    }
    catch (error) {
      // The native turn can end between the visibility check and the click.
      if (await interrupt.isVisible())
        throw error
    }
  }
  await page.locator('[data-testid="queue-pause-button"]:visible').click()
  await expect(queue).toHaveCount(0)
}

async function waitForKiloPromptEnd(page: Page, server: ServerInfo): Promise<void> {
  const agentId = await page.locator('[data-testid="tab"][data-tab-type="agent"]').first().getAttribute('data-tab-id') ?? ''
  const channel = await getTestChannel(server.hubUrl, server.adminToken)
  await expect.poll(async () => {
    const response = await channel.callWorker(
      server.workerId,
      'ListAgentInputQueue',
      ListAgentInputQueueRequestSchema,
      ListAgentInputQueueResponseSchema,
      { agentId },
    )
    return response.snapshot?.activeTurn ?? false
  }, { timeout: KILO_ACP_IDLE_FALLBACK_MS * 2 }).toBe(false)
}

async function exerciseKiloGoal(page: Page, modelScript: ModelScript, objective: GoalObjective, server: ServerInfo): Promise<void> {
  const gate = 'kilo-resumed-goal'
  await setGoal(page, objective)
  await modelScript.waitForSteps(1)
  await openGoalMenu(page)
  await goalAction(page, 'pause').click()
  await expectGoalStatus(page, 'paused')

  await page.reload()
  await expandGoalsAndTodosSection(page)
  await expectObjective(page, objective)
  await expectGoalStatus(page, 'paused')

  try {
    await waitForKiloPromptEnd(page, server)
    await openGoalMenu(page)
    await goalAction(page, 'resume').click()
    await modelScript.waitForGate(gate)
    await expectGoalStatus(page, 'active')

    await page.locator('[data-testid="queue-pause-button"]:visible').click()
    await openGoalMenu(page)
    await goalAction(page, 'clear').click()
    await deliverQueuedKiloGoalCommand(page, '/goal clear')
    await expect(page.locator('[data-testid="goal-card-empty"]:visible')).toBeVisible()
  }
  finally {
    if ((await modelScript.status()).pendingGates.includes(gate))
      await modelScript.releaseGate(gate)
  }
}

kiloTest.describe('Kilo session goal', () => {
  kiloTest.skip(!!KILO_E2E_SKIP_REASON, KILO_E2E_SKIP_REASON || '')
  kiloTest.setTimeout(KILO_ACP_IDLE_FALLBACK_MS * 4)
  kiloTest('sets, pauses, resumes, and clears the native goal', async ({ authenticatedKiloWorkspace, page, modelScript, leapmuxServer }) => {
    void authenticatedKiloWorkspace
    await modelScript.queue(
      { text: 'The first Kilo goal turn finished.' },
      { text: 'The resumed Kilo goal turn is held.', gate: 'kilo-resumed-goal' },
    )
    const objective = scriptedObjective(modelScript, 'Keep the Kilo session goal until the browser clears it.')
    await exerciseKiloGoal(page, modelScript, objective, leapmuxServer)
  })
})

piTest.describe('Pi session goal', () => {
  piTest.skip(!!PI_E2E_SKIP_REASON, PI_E2E_SKIP_REASON || '')
  piTest('pauses and resumes a native goal after reload', async ({ authenticatedPiWorkspace, page, modelScript }) => {
    void authenticatedPiWorkspace
    const gate = 'pi-goal-lifecycle'
    await modelScript.queue({ text: 'The first goal turn finished.' })
    await modelScript.fallback({ text: 'The next goal turn is held.', gate })
    const objective = scriptedObjective(modelScript, 'Keep the Pi session goal until the browser clears it.')
    try {
      await setGoal(page, objective, async () => {
        await modelScript.waitForSteps(1)
        await modelScript.waitForGate(gate)
      })
      await pauseResumeClearGoal(page, objective, { clearApproval: true })
    }
    finally {
      if ((await modelScript.status()).pendingGates.includes(gate))
        await modelScript.releaseGate(gate)
    }
  })
})

zcodeTest.describe('ZCode session goal', () => {
  zcodeTest.skip(!!ZCODE_E2E_SKIP_REASON, ZCODE_E2E_SKIP_REASON || '')
  zcodeTest('sets, pauses, resumes, and clears the native goal', async ({ authenticatedZCodeWorkspace, page, modelScript }) => {
    void authenticatedZCodeWorkspace
    await modelScript.fallback({ text: 'Goal turn complete.' })
    const objective = scriptedObjective(modelScript, 'Keep the ZCode session goal until the browser clears it.')
    await setGoal(page, objective)
    await pauseResumeClearGoal(page, objective)
  })
})
