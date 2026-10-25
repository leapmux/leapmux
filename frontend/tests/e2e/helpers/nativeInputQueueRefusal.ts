import type { QueuedAgentInput } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import type { ManagedNativeScenarioContext } from './nativeScenario'
import { expect } from '@playwright/test'
import { AgentInputState } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { readNativeInputQueue } from './nativeInputQueueIdle'
import { currentNativeAgent } from './nativeScenario'
import { retryUntilPass } from './retryUntilPass'
import { inputQueue } from './ui'

/**
 * Require that the Worker refused the one queued input of the current agent, for a reason that contains `reason`.
 *
 * The queue is paused, no turn runs, the one item failed, and both the item and its row in the queue state the
 * reason. A test reads this refusal before it asserts that no model request carries the input, because the durable
 * refusal is what makes that absence final. It returns the failed item, so the test can retry or delete it.
 */
export async function expectNativeInputRefusal(
  native: Pick<ManagedNativeScenarioContext, 'page' | 'leapmuxServer'>,
  reason: string,
): Promise<QueuedAgentInput> {
  if (reason.trim() === '')
    throw new Error('A refusal check needs the reason that the Worker states.')
  const agent = await currentNativeAgent(native)
  const item = await retryUntilPass(async () => {
    const snapshot = await readNativeInputQueue(native.leapmuxServer, agent.id)
    expect(snapshot.paused).toBe(true)
    expect(snapshot.activeTurn).toBe(false)
    expect(snapshot.items).toHaveLength(1)
    const failed = snapshot.items[0]
    if (!failed)
      throw new Error('The refused input has no queued input item.')
    expect(failed.state).toBe(AgentInputState.FAILED)
    expect(failed.error).toContain(reason)
    return failed
  })
  await expect(inputQueue(native.page).getByTestId(`queued-input-${item.id}`)).toContainText(reason)
  return item
}
