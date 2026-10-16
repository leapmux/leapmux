import type { AgentControlRequest } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import type { NativePermissionOperationPlan } from './nativePermission'
import type { ManagedNativeScenarioContext } from './nativeScenario'
import { expect } from '@playwright/test'
import { ControlResponseState } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { watchAgentEvents } from './agentEventWatch'
import { createNativePermissionFileWrite } from './nativePermission'
import { currentNativeAgent, nativeTextStep } from './nativeScenario'
import { uniqueMarker } from './shellArguments'
import { applyPermissionPreset, expectNoControlBanner, sendMessage, waitForAgentIdle, waitForControlBanner } from './ui'

type MidTurnEvent = { kind: 'request', request: AgentControlRequest } | { kind: 'turnEnd' }

/** Prove that Bypass resolves the pending tool and covers the next tool in the same native turn. */
export async function exerciseMidTurnBypass(
  context: ManagedNativeScenarioContext,
  operations?: readonly [NativePermissionOperationPlan, NativePermissionOperationPlan],
): Promise<void> {
  const marker = uniqueMarker('MIDBYPASS')
  const createWrite = (index: number) => createNativePermissionFileWrite(context, {
    fileName: `${marker}-${index}.txt`,
    callId: `mid-bypass-${index}`,
    outputPrefix: `${marker}_${index}_`,
  })
  const [first, second] = operations ?? await Promise.all([createWrite(0), createWrite(1)])
  const owner = await currentNativeAgent(context)
  const requests = await watchAgentEvents<MidTurnEvent>(context.leapmuxServer, owner.id, {
    label: 'mid-turn Bypass requests',
    select: (event) => {
      if (event.event.case === 'controlRequest' && event.event.value.responseState === ControlResponseState.READY)
        return { kind: 'request', request: event.event.value }
      if (event.event.case === 'turnEnd' && !event.replay)
        return { kind: 'turnEnd' }
      return undefined
    },
  })
  const pendingInstances = () => [...new Set(requests.items().flatMap(event => event.kind === 'request' ? [JSON.stringify([event.request.requestId, event.request.claimToken])] : []))]
  const completedTurns = () => requests.items().filter(event => event.kind === 'turnEnd').length
  try {
    const start = await context.modelScript.queue(
      { toolCalls: [first.toolCall] },
      { toolCalls: [second.toolCall] },
      nativeTextStep(context, 'Both native tools ran after Bypass.'),
    )
    const turnsBefore = completedTurns()
    await sendMessage(context.page, context.modelScript.prompt('Run both scripted permission-controlled operations.'))
    await context.modelScript.waitForSteps(start + 1)
    await waitForControlBanner(context.page)
    await first.beforeDecision()
    await second.beforeDecision()
    await expect.poll(pendingInstances).toHaveLength(1)
    const initial = pendingInstances()
    await applyPermissionPreset(context.page, 'bypass')
    await context.modelScript.waitForSteps(start + 3)
    await waitForAgentIdle(context.page)
    // This stream must receive the turn end before it can prove that no later approval appeared.
    await expect.poll(completedTurns).toBeGreaterThan(turnsBefore)
    await first.nativeProof(await context.modelScript.requestAt(start + 1))
    await second.nativeProof(await context.modelScript.requestAt(start + 2))
    await expectNoControlBanner(context.page)
    expect(pendingInstances(), 'later native tools need no manual approval after Bypass').toEqual(initial)
  }
  finally {
    requests.cancel()
  }
}
