import type { AgentChatMessage, AgentInfo } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import type { ModelScript } from './modelScriptFixture'
import type { NativeControlFrame } from './nativeControlWatch'
import type { NativeMessageSnapshot } from './nativeMessages'
import type { ManagedNativeScenarioContext } from './nativeScenario'
import { isDeepStrictEqual } from 'node:util'
import { expect } from '@playwright/test'
import { parsePersistedControlResponse } from '../../../src/components/chat/persistedControlResponse'
import { MarkType, MessageSource } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { parseMessageContent } from '../../../src/lib/messageParser'
import { readNativeMessageSnapshot } from './nativeMessages'

/**
 * One saved answer to a native control request, as the Worker stored it.
 *
 * The Worker writes this row after it delivers the answer. The provider spec reads the
 * native fields of `request` and `response`. This module reads no provider field.
 */
export interface NativeStoredControlDecision {
  /** The Worker row that holds the answer. */
  message: AgentChatMessage
  /** The Worker request ID that the answer belongs to. */
  requestId: string
  /** The token of the request instance that the answer belongs to. */
  claimToken: string
  /** The complete native request, which the Worker stores beside the answer. */
  request: Record<string, unknown>
  /** The native answer that the Worker delivered to the provider. */
  response: Record<string, unknown>
}

/**
 * Select the one native control request that a Worker watch observed.
 *
 * It returns the first frame of that request. It throws in each of these cases:
 *
 * - The watch observed no request.
 * - The watch observed a second request ID.
 * - The frames of the one request disagree about its payload. The Worker gives a
 *   changed payload a new instance, and that instance is not the request that the
 *   caller saw.
 */
export function onlyObservedNativeControl(frames: readonly NativeControlFrame[]): NativeControlFrame {
  const first = frames[0]
  if (!first)
    throw new Error('The Worker watch observed no native control request.')
  if (!first.requestId.trim())
    throw new Error('The observed native control request has no request ID.')
  for (const frame of frames) {
    if (frame.requestId !== first.requestId)
      throw new Error(`The Worker sent a second native control request ${frame.requestId} after ${first.requestId}.`)
    if (!isDeepStrictEqual(frame.payload, first.payload))
      throw new Error(`The native control request ${first.requestId} changed its payload.`)
  }
  return first
}

/**
 * Read the one saved answer to an observed request in the root native session.
 *
 * The answer must satisfy each of these conditions:
 *
 * - The Worker wrote it as a user control-response row.
 * - It belongs to the root scope of the snapshot's native session.
 * - It holds the native request, the native answer, and the claim token.
 * - It is the only row for the request ID.
 *
 * Any other row that states the request ID makes the read fail. A foreign or
 * malformed row is not skipped, because a skipped row would hide a defect.
 */
export function readNativeStoredControlDecision(snapshot: NativeMessageSnapshot, requestId: string): NativeStoredControlDecision {
  if (!snapshot.agentId.trim() || !snapshot.agentSessionId.trim() || !requestId.trim())
    throw new Error('The saved native decision read requires an agent ID, a native session ID, and an observed request ID.')
  const decisions: NativeStoredControlDecision[] = []
  for (const message of snapshot.messages) {
    const saved = parsePersistedControlResponse(parseMessageContent(message))
    if (!saved || saved.requestId !== requestId)
      continue
    if (message.source !== MessageSource.USER || message.markType !== MarkType.CONTROL_RESPONSE)
      throw new Error(`The row for native request ${requestId} is not a saved control response.`)
    if (message.agentSessionId !== snapshot.agentSessionId || message.depth !== 0 || message.parentSpanId !== '')
      throw new Error(`The saved decision for native request ${requestId} is outside the root scope of native session ${snapshot.agentSessionId}.`)
    if (!message.id.trim() || message.seq < 0n)
      throw new Error(`The saved decision for native request ${requestId} has no valid Worker row identity.`)
    if (!saved.claimToken.trim() || !saved.request || !saved.response)
      throw new Error(`The saved decision for native request ${requestId} lacks its native request, its native answer, or its claim token.`)
    decisions.push({ message, requestId: saved.requestId, claimToken: saved.claimToken, request: saved.request, response: saved.response })
  }
  const [decision, ...others] = decisions
  if (!decision || others.length > 0)
    throw new Error(`The Worker holds ${decisions.length} saved decisions for native request ${requestId}. Exactly one must exist.`)
  return decision
}

/** The frames that a Worker watch observed. `watchNativeControls` in `./nativeControlWatch.ts` returns one. */
export interface NativeControlObserver {
  controls: () => readonly NativeControlFrame[]
}

/** Wait until the watch observes a native control request, and return the one request that it observed. */
export async function waitForOneNativeControl(watch: NativeControlObserver): Promise<NativeControlFrame> {
  await expect.poll(() => watch.controls().length, { message: 'the Worker watch observes a native control request' }).toBeGreaterThan(0)
  return onlyObservedNativeControl(watch.controls())
}

/** The saved answer to an observed request, and the Worker snapshot that holds it. */
export interface ObservedNativeDecision {
  decision: NativeStoredControlDecision
  /** The snapshot, for a provider that also reads its native tool records. */
  snapshot: NativeMessageSnapshot
}

/**
 * Read the saved answer to the observed request after the browser answered it.
 *
 * It requires these facts before it returns the answer:
 * - The watch observed no second request, and the frames of the request kept one payload.
 * - The snapshot belongs to the native session of `agent`.
 * - The Worker stored the same native request that the browser answered.
 *
 * The provider spec then checks the provider-owned fields of `decision.request` and `decision.response`.
 */
export async function readObservedNativeDecision(
  context: Pick<ManagedNativeScenarioContext, 'leapmuxServer'>,
  agent: Pick<AgentInfo, 'id' | 'agentSessionId'>,
  watch: NativeControlObserver,
  observed: NativeControlFrame,
): Promise<ObservedNativeDecision> {
  // A second request fails inside `onlyObservedNativeControl`. The check fails when the first frame is another one.
  expect(onlyObservedNativeControl(watch.controls()), 'the watch still holds the observed request as its first frame').toBe(observed)
  const snapshot = await readNativeMessageSnapshot(context, agent.id)
  expect(snapshot.agentSessionId, 'the saved decision belongs to the native session of the agent').toBe(agent.agentSessionId)
  const decision = readNativeStoredControlDecision(snapshot, observed.requestId)
  expect(decision.request, 'the Worker stored the native request that the browser answered').toEqual(observed.payload)
  return { decision, snapshot }
}

/**
 * Require that the turn ended after `nextStep` ordered steps of the script: the agent requested those steps and no
 * more, and it sent no request that the script did not expect.
 * Call it after the turn ended, for example after `waitForAgentIdle`. A step index from `queue` keeps the count
 * correct after an earlier turn: a turn that queued one step at `start` ends after `start + 1`.
 */
export async function expectTurnEndedAfter(modelScript: Pick<ModelScript, 'status'>, nextStep: number): Promise<void> {
  if (!Number.isSafeInteger(nextStep) || nextStep < 1)
    throw new Error(`A turn ends after one or more ordered steps, not after ${nextStep}.`)
  const status = await modelScript.status()
  expect(status.unexpectedRequests, 'the agent sent no request that the script did not expect').toEqual([])
  expect(status.nextStep, `the agent requested ${nextStep} ordered steps and no more`).toBe(nextStep)
}
