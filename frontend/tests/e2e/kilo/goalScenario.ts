import type { Page } from '@playwright/test'
import type { ServerInfo } from '../fixtures'
import type { ModelScript } from '../helpers/modelScriptFixture'
import { expect } from '@playwright/test'
import { withCleanup } from '../helpers/cleanup'
import { SCENARIO_MARKER } from '../helpers/mockModelScript'
import { waitForNativeInputQueueIdle } from '../helpers/nativeInputQueueIdle'
import { expandGoalsAndTodosSection, expectGoalStatus, goalAction, openGoalMenu } from '../helpers/subagentRegistry'

export const KILO_ACP_IDLE_FALLBACK_MS = 60_000

export function scriptedObjective(modelScript: ModelScript, text: string): GoalObjective {
  return { input: modelScript.prompt(text), text, marker: `${SCENARIO_MARKER}${modelScript.id}` }
}

export async function exerciseKiloGoal(page: Page, modelScript: ModelScript, objective: GoalObjective, server: ServerInfo): Promise<void> {
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

  await withCleanup(async () => {
    await waitForKiloPromptEnd(page, server, modelScript)
    await openGoalMenu(page)
    await goalAction(page, 'resume').click()
    await modelScript.waitForGate(gate)
    await expectGoalStatus(page, 'active')

    await page.locator('[data-testid="queue-pause-button"]:visible').click()
    await openGoalMenu(page)
    await goalAction(page, 'clear').click()
    await deliverQueuedKiloGoalCommand(page, '/goal clear')
    await expect(page.locator('[data-testid="goal-card-empty"]:visible')).toBeVisible()
  }, async () => {
    await modelScript.releaseGateIfHeld(gate)
  })
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

async function setGoal(page: Page, objective: GoalObjective, afterSubmit?: () => Promise<void>): Promise<void> {
  await expandGoalsAndTodosSection(page)
  await goalAction(page, 'set').click()
  await page.locator('[data-testid="goal-editor"]:visible .ProseMirror').fill(objective.input)
  await page.locator('[data-testid="set-goal-submit"]:visible').click()
  await afterSubmit?.()
  await expectObjective(page, objective)
  await expectGoalStatus(page, 'active')
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

// Kilo returns an unpublished response ID for Pause and Clear. Its ACP waiter normally uses its fixed 60-second fallback.
// Require the actual Worker turn end before Resume. The paused goal card does not establish native completion.
export async function waitForKiloPromptEnd(page: Page, server: ServerInfo, modelScript: Pick<ModelScript, 'testDeadline'>): Promise<void> {
  const agentId = await page.locator('[data-testid="tab"][data-tab-type="agent"][aria-selected="true"]:visible').first().getAttribute('data-tab-id')
  if (!agentId)
    throw new Error('The selected Kilo tab has no agent ID.')
  await waitForNativeInputQueueIdle(server, agentId, modelScript.testDeadline)
}
