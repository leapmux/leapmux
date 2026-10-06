import type { Page } from '@playwright/test'
import type { ModelScript } from '../helpers/modelScriptFixture'
import type { ManagedNativeScenarioContext } from '../helpers/nativeScenario'
import { expect } from '@playwright/test'
import { withCleanup } from '../helpers/cleanup'
import { clearGoal, expandGoalsAndTodosSection, expectEmptyGoalCard, expectGoalObjective, expectGoalStatus, goalAction, openGoalMenu, scriptedObjective, setGoal } from '../helpers/goalsAndTodos'
import { waitForNativeInputQueueIdle } from '../helpers/nativeInputQueueIdle'
import { selectedAgentTabId } from '../helpers/nativeScenario'
import { inputQueue, queuePauseButton } from '../helpers/ui'

/** Kilo's fixed ACP idle fallback, which a Pause and a Clear wait for. A goal spec sets its timeout from it. */
export const KILO_ACP_IDLE_FALLBACK_MS = 60_000

/** The gate that holds the resumed goal turn until the scenario clears the goal. */
const RESUMED_GOAL_GATE = 'kilo-resumed-goal'

/**
 * Set, pause, resume, and clear a native Kilo session goal.
 * The objective and the paused status survive a reload. Kilo receives the Clear as a queued `/goal clear` command.
 */
export async function exerciseKiloGoal(context: ManagedNativeScenarioContext): Promise<void> {
  const { page, modelScript } = context
  const start = await modelScript.queue(
    { text: 'The first Kilo goal turn finished.' },
    { text: 'The resumed Kilo goal turn is held.', gate: RESUMED_GOAL_GATE },
  )
  const objective = scriptedObjective(modelScript, 'Keep the Kilo session goal until the browser clears it.')
  await setGoal(page, objective)
  await modelScript.waitForSteps(start + 1)
  await openGoalMenu(page)
  await goalAction(page, 'pause').click()
  await expectGoalStatus(page, 'paused')

  await page.reload()
  await expandGoalsAndTodosSection(page)
  await expectGoalObjective(page, objective)
  await expectGoalStatus(page, 'paused')

  await withCleanup(async () => {
    await waitForKiloPromptEnd(context)
    await openGoalMenu(page)
    await goalAction(page, 'resume').click()
    await modelScript.waitForGate(RESUMED_GOAL_GATE)
    await expectGoalStatus(page, 'active')

    await queuePauseButton(page).click()
    await clearGoal(page)
    await deliverQueuedKiloGoalCommand(page, '/goal clear')
    await expectEmptyGoalCard(page)
  }, async () => {
    await modelScript.releaseGateIfHeld(RESUMED_GOAL_GATE)
  })
}

async function deliverQueuedKiloGoalCommand(page: Page, command: string): Promise<void> {
  const queue = inputQueue(page)
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
  await queuePauseButton(page).click()
  await expect(queue).toHaveCount(0)
}

// Kilo returns an unpublished response ID for Pause and Clear. Its ACP waiter normally uses its fixed 60-second fallback.
// Require the actual Worker turn end before Resume. The paused goal card does not establish native completion.
export async function waitForKiloPromptEnd(context: Pick<ManagedNativeScenarioContext, 'page' | 'leapmuxServer'> & { modelScript: Pick<ModelScript, 'testDeadline'> }): Promise<void> {
  const agentId = await selectedAgentTabId(context.page)
  await waitForNativeInputQueueIdle(context.leapmuxServer, agentId, context.modelScript.testDeadline)
}
