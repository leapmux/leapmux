import { expect } from '@playwright/test'
import { deepseekHarnessTest } from '../deepseek-harness-fixtures'
import { clearGoal, expandGoalsAndTodosSection, expectEmptyGoalCard, expectGoalStatus, submitGoal } from '../helpers/goalsAndTodos'
import { sendNativeAnswer } from '../helpers/nativeConversation'
import { waitForAgentIdle } from '../helpers/ui'
import { captureDeepseekHarnessGoalOwner, withDeepseekHarnessGoalCleanup } from './goalCleanupRuntime'
import { deepseekHarnessModelContextText } from './modelContextText'
import { nativeContext } from './scenarios'

deepseekHarnessTest('keeps the literal clear objective and clears the actual native goal without interpreting it as a command', async ({ authenticatedDeepseekHarnessWorkspace, page, modelScript, leapmuxServer }) => {
  const context = await nativeContext({ page, modelScript, leapmuxServer, workspaceId: authenticatedDeepseekHarnessWorkspace.workspaceId })
  await sendNativeAnswer(context, 'Prepare the session for a native goal.', 'The native session is ready for its goal.')
  const gate = 'deepseek-reserved-objective'
  const owner = await captureDeepseekHarnessGoalOwner(context)
  await withDeepseekHarnessGoalCleanup(context, owner, [gate], async () => {
    await modelScript.rule({ name: 'the exact reserved objective round', when: { user: 'Objective: "clear"' }, once: true, respond: { text: 'The literal clear objective reached the native goal driver.', gate } })
    await submitGoal(page, 'clear')
    await modelScript.waitForGate(gate)
    await expect(page.locator('[data-testid="goal-objective"]:visible')).toHaveText('clear')
    await expectGoalStatus(page, 'active')
    const status = await modelScript.status()
    const goal = status.requests.find(request => request.rule === 'the exact reserved objective round')
    if (!goal)
      throw new Error('The literal objective reached no actual native goal round.')
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
