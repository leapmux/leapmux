import { create, fromBinary, toBinary } from '@bufbuild/protobuf'
import { expect } from '@playwright/test'
import { SESSION_INFO_KEY } from '../../../src/generated/contracts/session-info'
import { NOTIFICATION_TYPE } from '../../../src/generated/contracts/worker-vocab'
import { WatchReplayMode } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { WatchEventsRequestSchema, WatchEventsResponseSchema, WatchMode } from '../../../src/generated/proto/leapmux/v1/workspace_pb'
import { decompressContentToString } from '../../../src/lib/decompress'
import { isObject, pickObject, pickString } from '../../../src/lib/jsonPick'
import { getTestChannel } from './api'

interface WorkerConnection {
  hubUrl: string
  adminToken: string
  workerId: string
}

/** Collect only live context-usage updates for one agent. */
export class ContextUsageFrameCollector {
  readonly readings: Record<string, unknown>[] = []
  subscribed = false
  error: Error | undefined

  constructor(private readonly agentId: string) {}

  accept(payload: Uint8Array): void {
    let response
    try {
      response = fromBinary(WatchEventsResponseSchema, payload)
    }
    catch (error) {
      this.error = new Error('The Worker sent an invalid usage watch frame.', { cause: error })
      return
    }
    if (response.event.case === 'updateAck') {
      const ack = response.event.value
      if (ack.rejectedAgents.length > 0)
        this.error = new Error(`The Worker refused the usage watch: ${JSON.stringify(ack.rejectedAgents)}`)
      else if (ack.updateId === 1n)
        this.subscribed = true
      return
    }
    if (response.event.case !== 'agentEvent')
      return
    const event = response.event.value
    if (event.agentId !== this.agentId || event.replay || event.event.case !== 'agentMessage')
      return
    const message = event.event.value
    if (message.seq !== -1n)
      return
    let raw: string | null
    try {
      raw = decompressContentToString(message.content, message.contentCompression)
    }
    catch (error) {
      this.error = new Error('The Worker sent invalid compressed usage content.', { cause: error })
      return
    }
    if (!raw)
      return
    let parsed: unknown
    try {
      parsed = JSON.parse(raw)
    }
    catch {
      return
    }
    if (!isObject(parsed) || pickString(parsed, 'type') !== NOTIFICATION_TYPE.AgentSessionInfo)
      return
    const context = pickObject(pickObject(parsed, 'info'), SESSION_INFO_KEY.ContextUsage)
    if (context)
      this.readings.push(context)
  }

  assertHealthy(): void {
    if (this.error)
      throw this.error
  }
}

/** Watch the native usage map that the Worker publishes for one agent. */
export async function watchAgentContextUsage(server: WorkerConnection, agentId: string): Promise<{ readings: () => readonly Record<string, unknown>[], cancel: () => void }> {
  if (agentId === '')
    throw new Error('The usage watch needs an agent ID.')
  const channel = await getTestChannel(server.hubUrl, server.adminToken)
  const channelId = await channel.getOrOpenChannel(server.workerId)
  const request = create(WatchEventsRequestSchema, {
    agents: [{ agentId, mode: WatchMode.FULL, replay: WatchReplayMode.LATEST, cursorSeq: 0n }],
    updateId: 1n,
  })
  const watch = channel.stream(channelId, 'WatchEvents', toBinary(WatchEventsRequestSchema, request))
  const collector = new ContextUsageFrameCollector(agentId)
  watch.onMessage(frame => collector.accept(frame.payload))
  watch.onError((error) => {
    collector.error = error
  })
  watch.onEnd(() => {
    collector.error = new Error('The usage watch ended before the assertion.')
  })
  try {
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
    readings: () => {
      collector.assertHealthy()
      return collector.readings
    },
    cancel: () => watch.cancel(),
  }
}
