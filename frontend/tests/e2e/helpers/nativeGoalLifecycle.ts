import type { ManagedNativeScenarioContext } from './nativeScenario'
import { randomUUID } from 'node:crypto'
import { join } from 'node:path'
import { expect } from '@playwright/test'
import { withCleanup } from './cleanup'
import { currentNativeAgent } from './nativeScenario'
import { writeToolCall } from './providerToolCalls'
import { expandGoalsAndTodosSection, expectGoalStatus, goalAction, openGoalMenu } from './subagentRegistry'
import { applyPermissionPreset } from './ui'

/** Deliver a real queued pause command, then prove the native goal resumes its own model work. */
export async function exerciseNativeGoalPauseAndResume(
  context: ManagedNativeScenarioContext,
  options: { pausedProof: () => Promise<void> },
): Promise<void> {
  await applyPermissionPreset(context.page, 'bypass')
  const agent = await currentNativeAgent(context)
  const marker = `NATIVEGOAL${randomUUID().replaceAll('-', '')}`
  const gate = `native-goal-first-${marker}`
  const rule = `native-goal-following-${marker}`
  await withCleanup(async () => {
    await context.modelScript.rule({
      name: `native-goal-first-${marker}`,
      when: { body: marker },
      once: true,
      respond: { gate, toolCalls: [writeToolCall(context.provider, 'native-goal-progress', { path: join(agent.workingDir, 'native-goal-progress.txt'), content: 'The first native goal iteration changed this file.\n' })] },
    }, {
      name: rule,
      when: { body: marker },
      respond: { text: 'The current goal needs another iteration.' },
    })
    await expandGoalsAndTodosSection(context.page)
    await goalAction(context.page, 'set').click()
    await context.page.locator('[data-testid="goal-editor"]:visible .ProseMirror').fill(context.modelScript.prompt(`Keep ${marker} until the operator clears it.`))
    await context.page.locator('[data-testid="set-goal-submit"]:visible').click()
    await context.modelScript.waitForGate(gate)
    await expectGoalStatus(context.page, 'active')
    await openGoalMenu(context.page)
    await goalAction(context.page, 'pause').click()
    await expect(context.page.locator('[data-testid="agent-input-queue"]:visible')).toContainText('/goal pause')
    await context.modelScript.releaseGate(gate)
    await expectGoalStatus(context.page, 'paused')
    await options.pausedProof()
    const before = (await context.modelScript.status()).ruleMatches[rule] ?? 0
    await openGoalMenu(context.page)
    await goalAction(context.page, 'resume').click()
    await expect.poll(async () => (await context.modelScript.status()).ruleMatches[rule] ?? 0).toBeGreaterThan(before)
    await expectGoalStatus(context.page, 'paused')
    await openGoalMenu(context.page)
    await goalAction(context.page, 'clear').click()
    await expect(context.page.locator('[data-testid="goal-card-empty"]:visible')).toBeVisible()
  }, async () => {
    await context.modelScript.releaseGateIfHeld(gate)
  })
}
