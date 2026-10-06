import type { MockModelPattern, MockModelRule } from './mockModelScript'
import type { ManagedNativeScenarioContext } from './nativeScenario'
import { existsSync } from 'node:fs'
import { join } from 'node:path'
import { expect } from '@playwright/test'
import { withCleanup } from './cleanup'
import { currentNativeAgent } from './nativeScenario'
import { writeToolCall } from './providerToolCalls'
import { uniqueMarker } from './shellArguments'
import { expandGoalsAndTodosSection, expectGoalStatus, goalAction, openGoalMenu } from './subagentRegistry'
import { applyPermissionPreset } from './ui'

/**
 * When the native pause takes effect on a goal round that runs.
 *
 * - `at-once`: the provider pauses the goal and cancels the running round.
 *   The round ends its model request and never finishes its tool call.
 * - `after-the-round`: the provider queues the pause, finishes the running
 *   round, and pauses before the next one.
 */
export type NativeGoalPauseTiming = 'at-once' | 'after-the-round'

/** The goal of one pause scenario, as the support rules need it. */
export interface NativeGoalScenario {
  /** Text that each request of this goal holds: the objective quotes it. */
  marker: string
}

/**
 * Pause a running native goal through the goal card, then prove that the native goal resumes its own model work.
 *
 * The first goal round holds its model answer at a gate, so the pause reaches a goal whose round runs.
 * That round writes a progress file. The rounds after it answer with text only.
 */
export async function exerciseNativeGoalPauseAndResume(
  context: ManagedNativeScenarioContext,
  options: {
    pauseTiming: NativeGoalPauseTiming
    /** Rules for the model calls of the provider's own goal machinery, such as a planner. They precede the round rules. */
    supportRules?: (scenario: NativeGoalScenario) => MockModelRule[]
    /**
     * A pattern that the system prompt of a goal round states and that a housekeeping request lacks.
     * A housekeeping request, such as a session title, can quote the goal and so carry its marker.
     * Without this pattern, the gated rule answers such a request. The real round then runs with no gate
     * and can end the goal before the pause.
     */
    roundSystem?: MockModelPattern
    pausedProof: () => Promise<void>
  },
): Promise<void> {
  await applyPermissionPreset(context.page, 'bypass')
  const agent = await currentNativeAgent(context)
  const marker = uniqueMarker('NATIVEGOAL')
  const progressFile = join(agent.workingDir, 'native-goal-progress.txt')
  const gate = `native-goal-first-${marker}`
  const rule = `native-goal-following-${marker}`
  const roundWhen = { body: marker, ...(options.roundSystem === undefined ? {} : { system: options.roundSystem }) }
  await withCleanup(async () => {
    await context.modelScript.rule(...(options.supportRules?.({ marker }) ?? []), {
      name: `native-goal-first-${marker}`,
      when: roundWhen,
      once: true,
      respond: { gate, toolCalls: [writeToolCall(context.provider, 'native-goal-progress', { path: progressFile, content: 'The first native goal iteration changed this file.\n' })] },
    }, {
      name: rule,
      when: roundWhen,
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
    // The goal command reaches the agent at once. A command that waited in the
    // input queue for an idle agent would never reach a goal that keeps running.
    await expect(context.page.locator('[data-testid="agent-input-queue"]:visible').filter({ hasText: '/goal' })).toHaveCount(0)
    if (options.pauseTiming === 'at-once') {
      // The round still holds its model answer, so only the pause can end it.
      await expectGoalStatus(context.page, 'paused')
      // The pause cancels the round, and the cancelled model request leaves the gate. A release now
      // fails with "no waiting request". The empty gate proves that the pause ended the round.
      await expect.poll(async () => (await context.modelScript.status()).pendingGates).not.toContain(gate)
    }
    else {
      // The pause waits behind the round that holds its model answer.
      await expectGoalStatus(context.page, 'active')
      await context.modelScript.releaseGate(gate)
      await expectGoalStatus(context.page, 'paused')
      expect(existsSync(progressFile), 'the round before the pause finished its tool call').toBe(true)
    }
    await options.pausedProof()
    const before = (await context.modelScript.status()).ruleMatches[rule] ?? 0
    await openGoalMenu(context.page)
    await goalAction(context.page, 'resume').click()
    await expect.poll(async () => (await context.modelScript.status()).ruleMatches[rule] ?? 0).toBeGreaterThan(before)
    if (options.pauseTiming === 'at-once') {
      // The resumed rounds answer with text only, so the file stays absent only
      // if the cancelled round never ran its tool call.
      expect(existsSync(progressFile), 'the pause cancelled the running round before its tool call').toBe(false)
    }
    await expectGoalStatus(context.page, 'paused')
    await openGoalMenu(context.page)
    await goalAction(context.page, 'clear').click()
    await expect(context.page.locator('[data-testid="goal-card-empty"]:visible')).toBeVisible()
  }, async () => {
    await context.modelScript.releaseGateIfHeld(gate)
  })
}
