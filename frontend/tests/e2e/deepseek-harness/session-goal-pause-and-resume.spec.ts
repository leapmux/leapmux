import { expect } from '@playwright/test'
import { deepseekHarnessTest } from '../deepseek-harness-fixtures'
import { clearGoal, expandGoalsAndTodosSection, expectEmptyGoalCard, expectGoalStatus, goalAction, openGoalMenu, scriptedObjective, setGoal } from '../helpers/goalsAndTodos'
import { ruleRequest } from '../helpers/mockModelScript'
import { uniqueMarker } from '../helpers/shellArguments'
import { waitForAgentIdle } from '../helpers/ui'
import { captureDeepseekHarnessGoalOwner, withDeepseekHarnessGoalCleanup } from './goalCleanupRuntime'
import { deepseekHarnessModelContextText } from './modelContextText'

deepseekHarnessTest('pauses the native goal driver and resumes its own second model round after reload', async ({ native }) => {
  const { page, modelScript } = native
  const owner = await captureDeepseekHarnessGoalOwner(native)
  const marker = uniqueMarker('DEEPSEEKGOAL')
  const firstGate = 'deepseek-goal-first'
  const secondGate = 'deepseek-goal-second'
  await withDeepseekHarnessGoalCleanup(native, owner, [firstGate, secondGate], async () => {
    await modelScript.rule(
      { name: 'the first actual native goal round', when: { user: marker }, once: true, respond: { text: 'The first native goal round completed.', gate: firstGate } },
      { name: 'the second actual native goal round', when: { user: marker }, once: true, respond: { text: 'The second native goal round completed.', gate: secondGate } },
    )
    await setGoal(page, scriptedObjective(modelScript, `Keep ${marker} until the operator clears it.`), async () => {
      await modelScript.waitForGate(firstGate)
    })
    await openGoalMenu(page)
    await goalAction(page, 'pause').click()
    await expectGoalStatus(page, 'paused')
    await modelScript.releaseGateIfHeld(firstGate)
    await waitForAgentIdle(page)
    await page.reload()
    await expandGoalsAndTodosSection(page)
    await expectGoalStatus(page, 'paused')
    await openGoalMenu(page)
    await goalAction(page, 'resume').click()
    await modelScript.waitForGate(secondGate)
    await expectGoalStatus(page, 'active')
    const status = await modelScript.status()
    const resumed = ruleRequest(status, 'the second actual native goal round')
    expect(deepseekHarnessModelContextText(resumed)).toContain(marker)
    expect(deepseekHarnessModelContextText(resumed)).toContain('Round: 2/')
    expect(status.ruleMatches['the first actual native goal round']).toBe(1)
    expect(status.ruleMatches['the second actual native goal round']).toBe(1)
    await openGoalMenu(page)
    await goalAction(page, 'pause').click()
    await expectGoalStatus(page, 'paused')
    await modelScript.releaseGateIfHeld(secondGate)
    await waitForAgentIdle(page)
    await clearGoal(page)
    await expectEmptyGoalCard(page)
  })
})
