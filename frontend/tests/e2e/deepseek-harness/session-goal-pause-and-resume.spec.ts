import { randomUUID } from 'node:crypto'
import { expect } from '@playwright/test'
import { deepseekHarnessTest } from '../deepseek-harness-fixtures'
import { expandGoalsAndTodosSection, expectGoalStatus, goalAction, openGoalMenu } from '../helpers/subagentRegistry'
import { waitForAgentIdle } from '../helpers/ui'
import { captureDeepseekHarnessGoalOwner, withDeepseekHarnessGoalCleanup } from './goalCleanupRuntime'
import { deepseekHarnessModelContextText } from './modelContextText'
import { nativeContext } from './scenarios'

deepseekHarnessTest('pauses the native goal driver and resumes its own second model round after reload', async ({ deepseekHarnessWorkspace, page, modelScript, leapmuxServer }) => {
  const context = nativeContext({ page, modelScript, leapmuxServer, workspaceId: deepseekHarnessWorkspace.workspaceId })
  const owner = await captureDeepseekHarnessGoalOwner(context)
  const marker = `DEEPSEEKGOAL${randomUUID().replaceAll('-', '')}`
  const firstGate = 'deepseek-goal-first'
  const secondGate = 'deepseek-goal-second'
  await withDeepseekHarnessGoalCleanup(context, owner, [firstGate, secondGate], async () => {
    await modelScript.rule(
      { name: 'the first actual native goal round', when: { user: marker }, once: true, respond: { text: 'The first native goal round completed.', gate: firstGate } },
      { name: 'the second actual native goal round', when: { user: marker }, once: true, respond: { text: 'The second native goal round completed.', gate: secondGate } },
    )
    await expandGoalsAndTodosSection(page)
    await goalAction(page, 'set').click()
    await page.locator('[data-testid="goal-editor"]:visible .ProseMirror').fill(modelScript.prompt(`Keep ${marker} until the operator clears it.`))
    await page.locator('[data-testid="set-goal-submit"]:visible').click()
    await modelScript.waitForGate(firstGate)
    await expectGoalStatus(page, 'active')
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
    const resumed = status.requests.find(request => request.rule === 'the second actual native goal round')
    if (!resumed)
      throw new Error('The native goal resume started no second model round.')
    expect(deepseekHarnessModelContextText(resumed)).toContain(marker)
    expect(deepseekHarnessModelContextText(resumed)).toContain('Round: 2/')
    expect(status.ruleMatches['the first actual native goal round']).toBe(1)
    expect(status.ruleMatches['the second actual native goal round']).toBe(1)
    await openGoalMenu(page)
    await goalAction(page, 'pause').click()
    await expectGoalStatus(page, 'paused')
    await modelScript.releaseGateIfHeld(secondGate)
    await waitForAgentIdle(page)
    await openGoalMenu(page)
    await goalAction(page, 'clear').click()
    await expect(page.locator('[data-testid="goal-card-empty"]:visible')).toBeVisible()
  })
})
