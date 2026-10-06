/**
 * The Worker's live event subscription for one agent, which several proofs watch.
 *
 * Each watch sends one `WatchEvents` request, waits for the Worker to acknowledge
 * it, and keeps the events that its selector picks. This module owns the steps
 * that every watch shares: the request, the acknowledgement, a refusal, a stream
 * error, a stream end, and the cancel on failure.
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
    agents: [{ agentId, mode: WatchMode.FULL, replay: WatchReplayMode.LATEST, cursorSeq: 0n }],
    updateId: AGENT_WATCH_UPDATE_ID,
  }))
}

/**
 * What one `WatchEvents` frame means to a watch of one agent.
 *
 * - `acknowledged`: the Worker accepted the subscription.
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
 * Throws when the bytes are not a frame, and when the Worker refuses the
 * subscription. `label` names the watch in those messages.
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
    return { kind: 'acknowledged' }
  }
  if (response.event.case !== 'agentEvent' || response.event.value.agentId !== agentId)
    return { kind: 'ignored' }
  return { kind: 'event', event: response.event.value }
}

/**
 * Pick the value that a watch keeps from one event of its agent, or undefined to
 * skip the event. A thrown error fails the watch with that error.
 */
export type AgentEventSelector<T> = (event: AgentEvent) => T | undefined

/**
 * The state of one watch: the acknowledgement, the selected items, and the first
 * failure. A stream callback hands each frame to `accept`, which never throws, so
 * the failure reaches the test through `assertHealthy`.
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

  /** Record a failure. The first one wins, because a later stream end only follows it. */
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
 * Subscribe to the live events of one agent, and resolve once the Worker
 * acknowledges the subscription. A refusal, a stream error, or a stream end
 * before the acknowledgement rejects, and cancels the stream.
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
    // A thrown read ends `expect.poll` at once, so a refusal fails here with its own message.
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
