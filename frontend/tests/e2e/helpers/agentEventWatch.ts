/**
 * Subscribe to the Worker's live events for one agent.
 * Send one WatchEvents request and require its exact registered FULL replay identity.
 * Keep the events that the selector chooses.
 * Cancel the stream if subscription fails.
 * Preserve a refusal or stream failure for the test's next assertion.
 */
import type { AgentEvent } from '../../../src/generated/proto/leapmux/v1/workspace_pb'
import type { ServerInfo } from '../fixtures'
import { create, fromBinary, toBinary } from '@bufbuild/protobuf'
import { expect } from '@playwright/test'
import { WatchReplayMode } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { WatchEventsRequestSchema, WatchEventsResponseSchema, WatchMode } from '../../../src/generated/proto/leapmux/v1/workspace_pb'
import { getTestChannel } from './api'

/** The Worker that a watch subscribes on. */
export type AgentWatchServer = Pick<ServerInfo, 'hubUrl' | 'adminToken' | 'workerId'>

/** The update ID of the one subscription that each watch sends. */
export const AGENT_WATCH_UPDATE_ID = 1n

/** Encode the request that subscribes to the live events of one agent. */
export function agentWatchRequest(agentId: string): Uint8Array {
  if (!agentId.trim())
    throw new Error('An agent event watch requires an agent ID.')
  return toBinary(WatchEventsRequestSchema, create(WatchEventsRequestSchema, {
    agents: [{ agentId, mode: WatchMode.FULL, replay: WatchReplayMode.LATEST, cursorSeq: 0n, replayId: AGENT_WATCH_UPDATE_ID }],
    updateId: AGENT_WATCH_UPDATE_ID,
  }))
}

/**
 * What one `WatchEvents` frame means to a watch of one agent.
 *
 * - `acknowledged`: the Worker registered the requested FULL replay identity.
 * - `event`: an event of the watched agent.
 * - `ignored`: any other frame, such as an acknowledgement of another update or
 *   an event of another agent.
 */
export type AgentWatchFrame
  = | { kind: 'acknowledged' }
    | { kind: 'event', event: AgentEvent }
    | { kind: 'ignored' }

/**
 * Decode one `WatchEvents` frame for a watch of `agentId`.
 *
 * Reject these failures:
 * - Malformed bytes.
 * - A refused subscription.
 * - An incorrect registered identity.
 * Include the watch label in each error message.
 */
export function readAgentWatchFrame(payload: Uint8Array, agentId: string, label: string): AgentWatchFrame {
  let response
  try {
    response = fromBinary(WatchEventsResponseSchema, payload)
  }
  catch (error) {
    throw new Error(`The Worker sent an invalid ${label} frame.`, { cause: error })
  }
  if (response.event.case === 'updateAck') {
    const acknowledgement = response.event.value
    if (acknowledgement.updateId !== AGENT_WATCH_UPDATE_ID)
      return { kind: 'ignored' }
    if (acknowledgement.rejectedAgents.length > 0)
      throw new Error(`The Worker refused the ${label}: ${JSON.stringify(acknowledgement.rejectedAgents)}`)
    const registered = acknowledgement.agentStates.filter(state => state.agentId === agentId)
    if (registered.length !== 1 || registered[0]?.mode !== WatchMode.FULL || registered[0]?.replayId !== AGENT_WATCH_UPDATE_ID)
      throw new Error(`The Worker did not register the ${label} with its requested FULL replay identity.`)
    return { kind: 'acknowledged' }
  }
  if (response.event.case !== 'agentEvent' || response.event.value.agentId !== agentId)
    return { kind: 'ignored' }
  const event = response.event.value
  if (event.replay && (event.replayAgentId !== agentId || event.replayId !== AGENT_WATCH_UPDATE_ID))
    return { kind: 'ignored' }
  return { kind: 'event', event }
}

/**
 * Return the value that the watch keeps, or undefined to skip the event.
 * A thrown error becomes the watch's failure.
 */
export type AgentEventSelector<T> = (event: AgentEvent) => T | undefined

/**
 * Retain the acknowledgement and selected items for one watch.
 * Retain its first failure also.
 * The stream callback passes each frame to accept, which never throws.
 * assertHealthy delivers the failure to the test.
 */
export class AgentEventCollector<T> {
  readonly items: T[] = []
  subscribed = false
  private failure: Error | undefined

  constructor(private readonly agentId: string, private readonly label: string, private readonly select: AgentEventSelector<T>) {
    if (!agentId.trim())
      throw new Error(`The ${label} requires an agent ID.`)
  }

  get error(): Error | undefined {
    return this.failure
  }

  accept(payload: Uint8Array): void {
    if (this.failure)
      return
    try {
      const frame = readAgentWatchFrame(payload, this.agentId, this.label)
      if (frame.kind === 'acknowledged') {
        this.subscribed = true
        return
      }
      if (frame.kind !== 'event')
        return
      const item = this.select(frame.event)
      if (item !== undefined)
        this.items.push(item)
    }
    catch (error) {
      this.fail(error)
    }
  }

  /** Keep the first failure. A later error must not replace its cause. */
  fail(error: unknown): void {
    this.failure ??= error instanceof Error ? error : new Error(String(error))
  }

  assertHealthy(): void {
    if (this.failure)
      throw this.failure
  }
}

export interface AgentEventWatch<T> {
  /** The selected items in arrival order. Throws the failure of the watch. */
  items: () => readonly T[]
  cancel: () => void
}

/**
 * Subscribe to one agent's live events.
 * Resolve after the Worker confirms the exact registered identity.
 * Cancel the stream if subscription fails before that confirmation.
 */
export async function watchAgentEvents<T>(
  server: AgentWatchServer,
  agentId: string,
  options: { label: string, select: AgentEventSelector<T> },
): Promise<AgentEventWatch<T>> {
  const collector = new AgentEventCollector(agentId, options.label, options.select)
  const request = agentWatchRequest(agentId)
  const channel = await getTestChannel(server.hubUrl, server.adminToken)
  const channelId = await channel.getOrOpenChannel(server.workerId)
  const watch = channel.stream(channelId, 'WatchEvents', request)
  watch.onMessage(frame => collector.accept(frame.payload))
  watch.onError(error => collector.fail(error))
  watch.onEnd(() => collector.fail(new Error(`The ${options.label} ended before its assertion.`)))
  try {
    // A thrown read ends expect.poll immediately, so a refusal retains its own error message.
    await expect.poll(() => {
      collector.assertHealthy()
      return collector.subscribed
    }).toBe(true)
  }
  catch (error) {
    watch.cancel()
    throw error
  }
  return {
    items: () => {
      collector.assertHealthy()
      return collector.items
    },
    cancel: () => watch.cancel(),
  }
}
