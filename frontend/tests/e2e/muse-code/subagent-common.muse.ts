/** Native workflow-child scenarios with independently marked model requests. */
import type { ManagedNativeScenarioContext } from '../helpers/nativeScenario'
import { expect } from '@playwright/test'
import { readNativeInputQueue } from '../helpers/nativeInputQueueIdle'
import { selectedAgentTabId } from '../helpers/nativeScenario'
import { bashToolCall, codeExecutionToolCall, museSubmitResultToolCall } from '../helpers/providerToolCalls'
import { retryUntilPass } from '../helpers/retryUntilPass'
import { backgroundTaskRows, expandBackgroundTasksSection, openChildTabFromRow } from '../helpers/subagentRegistry'
import { assistantBubbles, sendMessage, tabById, waitForAgentIdle } from '../helpers/ui'

export const MUSE_CHILD_TASK = 'Inspect the native child transcript.'
export const MUSE_CHILD_PROGRESS = 'The native child started its own command.'
export const MUSE_CHILD_OUTPUT = 'MUSE_CHILD_OUTPUT_42'
export const MUSE_CHILD_LATER_OUTPUT = 'MUSE_CHILD_LATER_77'
export const MUSE_CHILD_RESULT = 'The native child completed its own work.'
export const MUSE_CHILD_GATE = 'muse-child-next-boundary'

/** Start one real child and return its actual registry link and native tab. */
export async function openMuseChild(native: ManagedNativeScenarioContext, held: boolean) {
  const { page, modelScript } = native
  const parentId = await selectedAgentTabId(page)
  const childInput = modelScript.prompt(MUSE_CHILD_TASK)
  await modelScript.rule(
    {
      name: 'the Muse child runs its own command',
      when: { body: ['Workflow child completion protocol', MUSE_CHILD_TASK] },
      once: true,
      respond: { text: MUSE_CHILD_PROGRESS, toolCalls: [bashToolCall(native.provider, 'muse-child-first', 'printf \'MUSE_CHILD_OUTPUT_%s\\n\' 42')] },
    },
    {
      name: 'the Muse child reaches its next command boundary',
      when: { body: ['Workflow child completion protocol', MUSE_CHILD_OUTPUT] },
      once: true,
      respond: { ...(held ? { gate: MUSE_CHILD_GATE } : {}), toolCalls: [bashToolCall(native.provider, 'muse-child-later', 'printf \'MUSE_CHILD_LATER_%s\\n\' 77')] },
    },
    {
      name: 'the Muse child submits its own result',
      when: { body: ['Workflow child completion protocol', MUSE_CHILD_LATER_OUTPUT] },
      once: true,
      respond: { text: MUSE_CHILD_RESULT, toolCalls: [museSubmitResultToolCall('muse-child-submit', 'The child result contract is complete.')] },
    },
  )
  const start = await modelScript.queue(
    { toolCalls: [codeExecutionToolCall(native.provider, 'muse-child-workflow', `export default async function workflow(host) { return await host.agent({ input: ${JSON.stringify(childInput)} }); }`)] },
    { text: 'The parent received the native child outcome.' },
  )
  await sendMessage(page, modelScript.prompt('Run the native child workflow.'))
  if (held) {
    await modelScript.waitForGate(MUSE_CHILD_GATE)
  }
  else {
    await modelScript.waitForSteps(start + 2)
    await waitForAgentIdle(page)
  }
  await expandBackgroundTasksSection(page)
  const row = backgroundTaskRows(page, { kind: 'subagent' }).filter({ hasText: MUSE_CHILD_TASK }).first()
  await expect(row).toHaveAttribute('data-status', held ? 'running' : 'succeeded')
  const childId = await openChildTabFromRow(page, row)
  if (held) {
    // The native model gate precedes the worker's asynchronous child-history read.
    await retryUntilPass(async () => {
      const queue = await readNativeInputQueue(native.leapmuxServer, childId)
      expect(queue.activeTurn && queue.activeTurnSteerable).toBe(true)
    })
  }
  const finish = async () => {
    await modelScript.releaseGateIfHeld(MUSE_CHILD_GATE)
    await modelScript.waitForSteps(start + 2)
    await tabById(page, parentId).click()
    await waitForAgentIdle(page)
    await tabById(page, childId).click()
    await waitForAgentIdle(page)
  }
  return { row, childId, parentId, finish }
}

export async function expectMuseChildResult(native: ManagedNativeScenarioContext, result = MUSE_CHILD_RESULT) {
  await expect(assistantBubbles(native.page).filter({ hasText: result })).toHaveCount(1)
}
