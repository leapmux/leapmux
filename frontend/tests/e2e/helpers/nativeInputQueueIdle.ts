import type { AgentInputQueueSnapshot } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import type { ChannelManager } from '../../../src/lib/channel'
import type { ServerInfo } from '../fixtures'
import { create, fromBinary, toBinary } from '@bufbuild/protobuf'
import { WatchReplayMode } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { WatchEventsRequestSchema, WatchEventsResponseSchema, WatchMode } from '../../../src/generated/proto/leapmux/v1/workspace_pb'
import { getTestChannel } from './api'
import { withCleanup } from './cleanup'
import { STEP_WAIT_REPORT_MARGIN_MS } from './modelScriptFixture'

const WATCH_UPDATE_ID = 1n
const MAX_TIMER_DELAY_MS = 2_147_483_647

/** Read authoritative input queue state for one acknowledged Worker subscription. */
export class NativeInputQueueIdleCollector {
  subscribed = false
  private latestSnapshot: AgentInputQueueSnapshot | undefined

  constructor(private readonly agentId: string) {
    if (!agentId.trim())
      throw new Error('The input queue subscription requires an agent ID.')
  }

  get idleSnapshot(): AgentInputQueueSnapshot | undefined {
    return this.subscribed && this.latestSnapshot?.activeTurn === false ? this.latestSnapshot : undefined
  }

  accept(payload: Uint8Array): void {
    const response = fromBinary(WatchEventsResponseSchema, payload)
    if (response.event.case === 'updateAck') {
      const acknowledgement = response.event.value
      if (acknowledgement.updateId !== WATCH_UPDATE_ID)
        return
      if (acknowledgement.rejectedAgents.length > 0)
        throw new Error('The Worker refused the input queue subscription.')
      this.subscribed = true
      return
    }
    if (response.event.case !== 'agentEvent' || response.event.value.agentId !== this.agentId)
      return
    const event = response.event.value.event
    if (event.case !== 'inputQueueChanged')
      return
    const snapshot = event.value.snapshot
    if (!snapshot)
      throw new Error('The Worker input queue event has no queue snapshot.')
    if (snapshot.agentId !== this.agentId)
      throw new Error('The Worker queue snapshot identifies a different agent.')
    if (this.latestSnapshot && snapshot.revision < this.latestSnapshot.revision)
      return
    // Derived item capabilities can change without a queue revision. Only the turn flag decides completion.
    if (this.latestSnapshot && snapshot.revision === this.latestSnapshot.revision && snapshot.activeTurn !== this.latestSnapshot.activeTurn)
      throw new Error('The Worker queue snapshot changed at the same revision.')
    this.latestSnapshot = snapshot
  }
}

/** Wait for acknowledged Worker state, with the existing whole-test deadline as a failure limit. */
export async function waitForNativeInputQueueIdle(
  server: Pick<ServerInfo, 'hubUrl' | 'adminToken' | 'workerId'>,
  agentId: string,
  testDeadline: () => number | undefined,
): Promise<AgentInputQueueSnapshot> {
  const collector = new NativeInputQueueIdleCollector(agentId)
  const deadline = testDeadline()
  if (deadline === undefined || !Number.isSafeInteger(deadline))
    throw new Error('The input queue wait requires the current whole-test deadline.')
  const failureDeadline = deadline - STEP_WAIT_REPORT_MARGIN_MS
  if (failureDeadline <= Date.now())
    throw new Error('The whole-test deadline leaves no time for the input queue wait.')

  let watch: ReturnType<ChannelManager['stream']> | undefined
  let timer: ReturnType<typeof setTimeout> | undefined
  let completed = false
  const completion = new Promise<AgentInputQueueSnapshot>((resolve, reject) => {
    const fail = (error: unknown): void => {
      if (completed)
        return
      completed = true
      reject(error)
    }
    const scheduleDeadline = (): void => {
      if (completed)
        return
      const remaining = failureDeadline - Date.now()
      if (remaining <= 0) {
        fail(new Error('The Worker input queue did not complete before the whole-test deadline.'))
        return
      }
      // Node caps timer delays. Recheck the same deadline when that cap expires.
      timer = setTimeout(scheduleDeadline, Math.min(remaining, MAX_TIMER_DELAY_MS))
    }
    const open = async (): Promise<void> => {
      const channel = await getTestChannel(server.hubUrl, server.adminToken)
      if (completed)
        return
      const channelId = await channel.getOrOpenChannel(server.workerId)
      if (completed)
        return
      const request = create(WatchEventsRequestSchema, {
        agents: [{ agentId, mode: WatchMode.FULL, replay: WatchReplayMode.LATEST, cursorSeq: 0n }],
        updateId: WATCH_UPDATE_ID,
      })
      watch = channel.stream(channelId, 'WatchEvents', toBinary(WatchEventsRequestSchema, request))
      watch.onMessage((frame) => {
        if (completed)
          return
        try {
          collector.accept(frame.payload)
          const snapshot = collector.idleSnapshot
          if (snapshot) {
            completed = true
            resolve(snapshot)
          }
        }
        catch (error) {
          fail(error)
        }
      })
      watch.onError(fail)
      watch.onEnd(() => fail(new Error('The Worker input queue subscription ended before completion.')))
    }
    scheduleDeadline()
    void open().catch(fail)
  })
  return withCleanup(() => completion, async () => {
    completed = true
    clearTimeout(timer)
    watch?.cancel()
  })
}
