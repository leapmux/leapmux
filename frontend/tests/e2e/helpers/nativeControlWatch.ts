import type { AgentControlRequest } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import type { ManagedNativeScenarioContext } from './nativeScenario'
import { create, fromBinary, toBinary } from '@bufbuild/protobuf'
import { expect } from '@playwright/test'
import { WatchReplayMode } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { WatchEventsRequestSchema, WatchEventsResponseSchema, WatchMode } from '../../../src/generated/proto/leapmux/v1/workspace_pb'
import { getTestChannel } from './api'

export interface NativeControlFrame {
  requestId: string
  payload: Record<string, unknown>
  responseState: AgentControlRequest['responseState']
}

/** Collect actual native control frames for one acknowledged Worker subscription. */
export class NativeControlFrameCollector {
  readonly controls: NativeControlFrame[] = []
  subscribed = false
  error: Error | undefined

  constructor(private readonly agentId: string) {}

  accept(payload: Uint8Array): void {
    try {
      const response = fromBinary(WatchEventsResponseSchema, payload)
      if (response.event.case === 'updateAck') {
        if (response.event.value.rejectedAgents.length > 0)
          throw new Error('The Worker refused the native control subscription.')
        if (response.event.value.updateId === 1n)
          this.subscribed = true
        return
      }
      if (response.event.case !== 'agentEvent' || response.event.value.agentId !== this.agentId)
        return
      const event = response.event.value.event
      if (event.case !== 'controlRequest' && event.case !== 'controlResponseChanged')
        return
      if (event.case === 'controlResponseChanged' && event.value.payload.length === 0)
        return
      if (!event.value.requestId)
        throw new Error('The native control frame has no request ID.')
      const parsed: unknown = JSON.parse(new TextDecoder().decode(event.value.payload))
      if (typeof parsed !== 'object' || parsed === null || Array.isArray(parsed))
        throw new Error('The native control payload is not an object.')
      this.controls.push({ requestId: event.value.requestId, payload: parsed as Record<string, unknown>, responseState: event.value.responseState })
    }
    catch (error) {
      this.error = new Error('The Worker sent an invalid native control frame.', { cause: error })
    }
  }

  assertHealthy(): void {
    if (this.error)
      throw this.error
  }
}

/** Capture real provider controls before a native operation starts. */
export async function watchNativeControls(
  server: ManagedNativeScenarioContext['leapmuxServer'],
  agentId: string,
): Promise<{ controls: () => readonly NativeControlFrame[], cancel: () => void }> {
  if (!agentId)
    throw new Error('The native control subscription requires an agent ID.')
  const channel = await getTestChannel(server.hubUrl, server.adminToken)
  const channelId = await channel.getOrOpenChannel(server.workerId)
  const request = create(WatchEventsRequestSchema, {
    agents: [{ agentId, mode: WatchMode.FULL, replay: WatchReplayMode.LATEST, cursorSeq: 0n }],
    updateId: 1n,
  })
  const watch = channel.stream(channelId, 'WatchEvents', toBinary(WatchEventsRequestSchema, request))
  const collector = new NativeControlFrameCollector(agentId)
  watch.onMessage(frame => collector.accept(frame.payload))
  watch.onError(error => collector.error = error)
  watch.onEnd(() => collector.error = new Error('The native control subscription ended before its assertion.'))
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
    controls: () => {
      collector.assertHealthy()
      return collector.controls
    },
    cancel: () => watch.cancel(),
  }
}
