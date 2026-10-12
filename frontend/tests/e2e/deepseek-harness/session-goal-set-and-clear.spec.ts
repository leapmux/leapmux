import { expect } from '@playwright/test'
import { deepseekHarnessTest } from '../deepseek-harness-fixtures'
import { clearGoal, expandGoalsAndTodosSection, expectEmptyGoalCard, expectGoalStatus, goalObjective, submitGoal } from '../helpers/goalsAndTodos'
import { ruleRequest } from '../helpers/mockModelScript'
import { sendNativeAnswer } from '../helpers/nativeConversation'
import { waitForAgentIdle } from '../helpers/ui'
import { captureDeepseekHarnessGoalOwner, withDeepseekHarnessGoalCleanup } from './goalCleanupRuntime'
import { deepseekHarnessModelContextText } from './modelContextText'

deepseekHarnessTest('keeps the literal clear objective and clears the actual native goal without interpreting it as a command', async ({ native }) => {
  const { page, modelScript } = native
  await sendNativeAnswer(native, 'Prepare the session for a native goal.', 'The native session is ready for its goal.')
  const gate = 'deepseek-reserved-objective'
  const owner = await captureDeepseekHarnessGoalOwner(native)
  await withDeepseekHarnessGoalCleanup(native, owner, [gate], async () => {
    await modelScript.rule({ name: 'the exact reserved objective round', when: { user: 'Objective: "clear"' }, once: true, respond: { text: 'The literal clear objective reached the native goal driver.', gate } })
    await submitGoal(page, 'clear')
    await modelScript.waitForGate(gate)
    // The objective is exactly the reserved word, so the check requires the whole text, not a part of it.
    await expect(goalObjective(page)).toHaveText('clear')
    await expectGoalStatus(page, 'active')
    const goal = ruleRequest(await modelScript.status(), 'the exact reserved objective round')
    expect(deepseekHarnessModelContextText(goal)).toContain('Objective: "clear"')
    await clearGoal(page)
    await expectEmptyGoalCard(page)
    await modelScript.releaseGateIfHeld(gate)
    await waitForAgentIdle(page)
    await page.reload()
    await expandGoalsAndTodosSection(page)
    await expectEmptyGoalCard(page)
  })
})
